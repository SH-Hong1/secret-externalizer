package report_test

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

var now = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func TestBuild_RedactedKinds(t *testing.T) {
	in := []report.Redaction{
		{Kind: finding.KindCard, File: "b.txt", Bundles: 2, Locations: 3},
		{Kind: finding.KindPhone, File: "z.txt", Bundles: 5, Locations: 5},
		{Kind: finding.KindCard, File: "a.txt", Bundles: 1, Locations: 1},
		{Kind: finding.KindCard, File: "a.txt", Untracked: true, Bundles: 1, Locations: 1},
	}
	r := report.Build(report.Data{Redactions: in}, now)

	wantKinds := []report.KindCount{
		{Kind: finding.KindPhone, Bundles: 5, Locations: 5, Files: 1},
		{Kind: finding.KindCard, Bundles: 4, Locations: 5, Files: 2},
	}
	if diff := cmp.Diff(wantKinds, r.RedactedKinds); diff != "" {
		t.Errorf("Build(...).RedactedKinds mismatch (-want +got):\n%s", diff)
	}
	wantRows := []report.Redaction{in[1], in[2], in[3], in[0]}
	if diff := cmp.Diff(wantRows, r.Redacted.Rows); diff != "" {
		t.Errorf("Build(...).Redacted.Rows mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_Caps(t *testing.T) {
	var d report.Data
	lines := make([]report.Line, 0, report.MaxRows+5)
	for i := range report.MaxRows + 5 {
		lines = append(lines, report.Line{Bundle: triage.BundleID(i + 1), File: fmt.Sprintf("f%05d", i)})
	}
	d.Structure = report.List[report.Line]{Rows: lines, Total: report.MaxRows + 5}
	d.Remaining = report.List[report.Line]{Rows: lines[:3], Total: report.MaxRows + 700}
	d.FalsePositives = report.List[report.Line]{Rows: lines[:2], Total: 2}
	places := make([]report.Place, 0, report.MaxPlaces+3)
	for i := range report.MaxPlaces + 3 {
		places = append(places, report.Place{File: fmt.Sprintf("f%03d", i), Line: 1})
	}
	entries := make([]report.Entry, 0, report.MaxRows+1)
	for i := range report.MaxRows + 1 {
		entries = append(entries, report.Entry{
			Bundle: triage.BundleID(i + 1), Name: fmt.Sprintf("ext.a.b%03d", i), Places: places, PlaceTotal: 4000,
		})
	}
	d.Externalized = report.List[report.Entry]{Rows: entries, Total: report.MaxRows + 1}
	d.Events = make([]report.Event, report.MaxRows)
	d.EventTotal = report.MaxRows + 1400
	r := report.Build(d, now)

	if got := len(r.Structure.Rows); got != report.MaxRows || r.Structure.Total != report.MaxRows+5 || r.Structure.More() != 5 {
		t.Errorf("Structure = %d rows of %d, More() %d, want %d of %d, 5",
			got, r.Structure.Total, r.Structure.More(), report.MaxRows, report.MaxRows+5)
	}
	if got := len(r.Remaining.Rows); got != 3 || r.Remaining.More() != report.MaxRows+697 {
		t.Errorf("Remaining = %d rows, More() %d, want 3, %d", got, r.Remaining.More(), report.MaxRows+697)
	}
	if got := len(r.FalsePositives.Rows); got != 2 || r.FalsePositives.More() != 0 {
		t.Errorf("FalsePositives = %d rows, More() %d, want 2, 0", got, r.FalsePositives.More())
	}
	if got := len(r.Externalized.Rows); got != report.MaxRows || r.Externalized.More() != 1 {
		t.Errorf("Externalized = %d rows, More() %d, want %d, 1", got, r.Externalized.More(), report.MaxRows)
	}
	e := r.Externalized.Rows[0]
	if len(e.Places) != report.MaxPlaces || e.PlaceTotal != 4000 {
		t.Errorf("entry places = %d of %d, want %d of 4000", len(e.Places), e.PlaceTotal, report.MaxPlaces)
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
	newData := func() report.Data {
		return report.Data{
			Project: project.Project{Excludes: []string{"vendor"}},
			Scan: scan.Scan{
				ToolVersions:  map[string]string{"betterleaks": "1"},
				SkippedFiles:  []string{"big.bin"},
				ScannerErrors: map[string]int{"opengrep": 1},
			},
			Structure:      report.List[report.Line]{Rows: []report.Line{{Bundle: 1, File: "a"}}, Total: 1},
			Remaining:      report.List[report.Line]{Rows: []report.Line{{Bundle: 2, File: "b"}}, Total: 1},
			FalsePositives: report.List[report.Line]{Rows: []report.Line{{Bundle: 3, File: "c"}}, Total: 1},
			Externalized: report.List[report.Entry]{Rows: []report.Entry{{
				Bundle: 1, Name: "ext.a.b", Places: []report.Place{{File: "a", Line: 1}}, PlaceTotal: 1,
			}}, Total: 1},
			Redactions: []report.Redaction{{Kind: finding.KindPhone, File: "p", Bundles: 1, Locations: 1}},
			Events:     []report.Event{{Actor: audit.Actor{Name: "x"}, Detail: map[string]string{"verdict": "hold"}}},
			EventTotal: 1,
		}
	}
	d := newData()
	r := report.Build(d, now)
	want := report.Build(newData(), now)

	d.Project.Excludes[0] = "changed"
	d.Scan.ToolVersions["betterleaks"] = "changed"
	d.Scan.SkippedFiles[0] = "changed"
	d.Scan.ScannerErrors["opengrep"] = 99
	d.Structure.Rows[0].File = "changed"
	d.Remaining.Rows[0].File = "changed"
	d.FalsePositives.Rows[0].File = "changed"
	d.Externalized.Rows[0].Name = "ext.changed.name"
	d.Externalized.Rows[0].Places[0].File = "changed"
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
