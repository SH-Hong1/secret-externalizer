package report_test

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

var now = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func bundleIDs(lines []report.Line) []triage.BundleID {
	var ids []triage.BundleID
	for _, l := range lines {
		ids = append(ids, l.Bundle)
	}
	return ids
}

func TestBuild_Partition(t *testing.T) {
	lines := []report.Line{
		{Bundle: 1, File: "a/x.jsp", FirstLine: 3, Verdict: triage.VerdictFalsePositive, Frontend: true},
		{Bundle: 2, File: "a/y.java", FirstLine: 3, Verdict: triage.VerdictFalsePositive},
		{Bundle: 3, File: "b/x.jsp", FirstLine: 1, Frontend: true},
		{Bundle: 4, File: "b/y.java", FirstLine: 1, Verdict: triage.VerdictHold, Reason: triage.HoldStructure},
		{Category: finding.CategoryCustomerData, Kind: finding.KindPhone, File: "c/x.jsp", FirstLine: 9, Verdict: triage.VerdictRedact, Bundles: 2},
		{Bundle: 5, File: "d.java", Verdict: triage.VerdictHold, Reason: triage.HoldLater},
		{Bundle: 6, File: "e.java"},
		{Bundle: 7, File: "f.java", Verdict: triage.VerdictExternalize},
	}
	r := report.Build(report.Data{Lines: lines}, now)

	tests := []struct {
		name string
		got  report.List[report.Line]
		want []triage.BundleID
	}{
		{"false positives", r.FalsePositives, []triage.BundleID{1, 2}},
		{"structure", r.Structure, []triage.BundleID{3, 4}},
		{"remaining", r.Remaining, []triage.BundleID{0, 5, 6, 7}},
	}
	sum := 0
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, bundleIDs(tt.got.Rows)); diff != "" {
				t.Errorf("Build(...).%s bundles mismatch (-want +got):\n%s", tt.name, diff)
			}
			if tt.got.Total != len(tt.want) {
				t.Errorf("Build(...).%s.Total = %d, want %d", tt.name, tt.got.Total, len(tt.want))
			}
		})
		sum += tt.got.Total
	}
	if sum != len(lines) {
		t.Errorf("sum of Totals = %d, want %d", sum, len(lines))
	}
}

func TestBuild_Externalized(t *testing.T) {
	item := func(name, file string, line int, mod func(*apply.Item)) report.Change {
		it := apply.Item{
			BundleID: 7, Name: name, File: file, Line: line, Action: apply.ActionReplace,
			Category: finding.CategoryOther, Kind: finding.KindPassword,
			StorePath: "projects/1/db/main/password",
		}
		if mod != nil {
			mod(&it)
		}
		return report.Change{Run: 3, Item: it}
	}
	changes := []report.Change{
		item("EXT_DB_MAIN_PASSWORD", "deploy.sh", 3, nil),
		item("ext.db.main.password", "b.xml", 31, nil),
		item("ext.db.main.password", "a.yml", 14, nil),
		item("ext.db.main.password", "k/key.pem", 0, func(i *apply.Item) { i.Action = apply.ActionRemoveFile }),
		item("ext.db.main.password", "Conn.java", 8, func(i *apply.Item) { i.CodeChange = true }),
	}
	lines := []report.Line{{Bundle: 7, File: "a.yml", Applied: report.AppliedBefore}}
	r := report.Build(report.Data{Changes: changes, Lines: lines}, now)

	want := []report.Entry{
		{
			Bundle: 7, Name: "EXT_DB_MAIN_PASSWORD", Category: finding.CategoryOther, Kind: finding.KindPassword,
			StorePath: "projects/1/db/main/password", PlaceTotal: 1, Reappeared: true,
			Places: []report.Place{{File: "deploy.sh", Line: 3, Run: 3}},
		},
		{
			Bundle: 7, Name: "ext.db.main.password", Category: finding.CategoryOther, Kind: finding.KindPassword,
			StorePath: "projects/1/db/main/password", PlaceTotal: 4, Reappeared: true, CodeChange: true,
			Places: []report.Place{
				{File: "Conn.java", Line: 8, Run: 3},
				{File: "a.yml", Line: 14, Run: 3},
				{File: "b.xml", Line: 31, Run: 3},
				{File: "k/key.pem", Removed: true, Run: 3},
			},
		},
	}
	// Names sort by byte value, so the upper-case name comes first.
	if diff := cmp.Diff(want, r.Externalized.Rows); diff != "" {
		t.Errorf("Build(...).Externalized.Rows mismatch (-want +got):\n%s", diff)
	}

	r = report.Build(report.Data{Changes: changes}, now)
	for _, e := range r.Externalized.Rows {
		if e.Reappeared {
			t.Errorf("Build(...) entry %s Reappeared = true without a line, want false", e.Name)
		}
	}
	r = report.Build(report.Data{Changes: changes, Lines: []report.Line{{Bundle: 7, Applied: report.AppliedAfter}}}, now)
	if r.Externalized.Rows[0].Reappeared {
		t.Errorf("Build(...) Reappeared = true for an apply after the scan, want false")
	}
}

func TestBuild_RedactedKinds(t *testing.T) {
	in := []report.Redaction{
		{Kind: finding.KindCard, File: "b.txt", Bundles: 2, Locations: 3},
		{Kind: finding.KindPhone, File: "z.txt", Bundles: 5, Locations: 5},
		{Kind: finding.KindCard, File: "a.txt", Bundles: 1, Locations: 1},
	}
	r := report.Build(report.Data{Redactions: in}, now)

	wantKinds := []report.KindCount{
		{Kind: finding.KindPhone, Bundles: 5, Locations: 5, Files: 1},
		{Kind: finding.KindCard, Bundles: 3, Locations: 4, Files: 2},
	}
	if diff := cmp.Diff(wantKinds, r.RedactedKinds); diff != "" {
		t.Errorf("Build(...).RedactedKinds mismatch (-want +got):\n%s", diff)
	}
	wantRows := []report.Redaction{in[1], in[2], in[0]}
	if diff := cmp.Diff(wantRows, r.Redacted.Rows); diff != "" {
		t.Errorf("Build(...).Redacted.Rows mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_Caps(t *testing.T) {
	var d report.Data
	for i := range report.MaxRows + 5 {
		d.Lines = append(d.Lines, report.Line{Bundle: triage.BundleID(i + 1), File: fmt.Sprintf("f%05d", i)})
	}
	for i := range report.MaxPlaces + 3 {
		d.Changes = append(d.Changes, report.Change{Item: apply.Item{BundleID: 1, Name: "ext.a.b", File: fmt.Sprintf("f%03d", i), Line: 1}})
	}
	d.Events = make([]report.Event, report.MaxRows)
	d.EventTotal = 2400
	r := report.Build(d, now)

	if got := len(r.Remaining.Rows); got != report.MaxRows {
		t.Errorf("len(Remaining.Rows) = %d, want %d", got, report.MaxRows)
	}
	if r.Remaining.Total != 1005 || r.Remaining.More() != 5 {
		t.Errorf("Remaining Total, More() = %d, %d, want 1005, 5", r.Remaining.Total, r.Remaining.More())
	}
	e := r.Externalized.Rows[0]
	if len(e.Places) != report.MaxPlaces || e.PlaceTotal != 53 {
		t.Errorf("entry places = %d of %d, want %d of 53", len(e.Places), e.PlaceTotal, report.MaxPlaces)
	}
	if r.History.More() != 1400 {
		t.Errorf("History.More() = %d, want 1400", r.History.More())
	}
}

func TestBuild_Delta(t *testing.T) {
	cur := report.Totals{Found: report.Count{Bundles: 5, Locations: 9}, Hold: report.Count{Bundles: 1, Locations: 1}}
	prev := report.Totals{Found: report.Count{Bundles: 8, Locations: 10}, Hold: report.Count{Bundles: 1, Locations: 1}}

	r := report.Build(report.Data{Totals: cur, PreviousTotals: prev}, now)
	if diff := cmp.Diff(report.Totals{}, r.Delta); diff != "" {
		t.Errorf("Delta without a previous scan mismatch (-want +got):\n%s", diff)
	}

	r = report.Build(report.Data{Totals: cur, PreviousTotals: prev, Previous: 4}, now)
	want := report.Totals{Found: report.Count{Bundles: -3, Locations: -1}}
	if diff := cmp.Diff(want, r.Delta); diff != "" {
		t.Errorf("Delta mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_Empty(t *testing.T) {
	r := report.Build(report.Data{}, now)
	totals := []int{
		r.Externalized.Total, r.Redacted.Total, r.Structure.Total,
		r.Remaining.Total, r.FalsePositives.Total, r.History.Total,
	}
	for i, n := range totals {
		if n != 0 {
			t.Errorf("Build(Data{}) list %d Total = %d, want 0", i, n)
		}
	}
	if len(r.RedactedKinds) != 0 {
		t.Errorf("Build(Data{}).RedactedKinds = %v, want none", r.RedactedKinds)
	}
}

func TestBuild_KeepsNoInput(t *testing.T) {
	d := report.Data{
		Lines:      []report.Line{{Bundle: 1, File: "a"}},
		Changes:    []report.Change{{Item: apply.Item{BundleID: 1, Name: "ext.a.b", File: "a", Line: 1}}},
		Redactions: []report.Redaction{{Kind: finding.KindPhone, File: "p", Bundles: 1, Locations: 1}},
		Events:     []report.Event{{Actor: audit.Actor{Name: "x"}, Detail: map[string]string{"verdict": "hold"}}},
		EventTotal: 1,
	}
	r := report.Build(d, now)
	want := report.Build(d, now)

	d.Lines[0].File = "changed"
	d.Changes[0].Item.File = "changed"
	d.Changes[0].Item.Name = "ext.changed.name"
	d.Redactions[0].File = "changed"
	d.Events[0].Detail["verdict"] = "changed"
	d.Events[0].Actor.Name = "changed"

	if diff := cmp.Diff(want, r); diff != "" {
		t.Errorf("Report changed after the input did (-want +got):\n%s", diff)
	}
}

func TestTypes_HoldNoValueField(t *testing.T) {
	banned := []string{"Preview", "Fingerprint", "Context", "Value"}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		//nolint:exhaustive // only the kinds that can hold a struct matter
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(typ.Elem())
		case reflect.Map:
			walk(typ.Key())
			walk(typ.Elem())
		case reflect.Struct:
			for f := range typ.Fields() {
				if typ.PkgPath() == reflect.TypeFor[report.Data]().PkgPath() && slices.Contains(banned, f.Name) {
					t.Errorf("%s has field %s, which may hold a value", typ, f.Name)
				}
				walk(f.Type)
			}
		}
	}
	walk(reflect.TypeFor[report.Data]())
	walk(reflect.TypeFor[report.Report]())
}
