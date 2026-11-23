package dashboard_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

var categories = []finding.Category{
	finding.CategoryPartnerSession, finding.CategoryStorage, finding.CategorySSH,
	finding.CategoryCustomerData, finding.CategoryOther,
}

func TestParsePeriod(t *testing.T) {
	tests := []struct {
		in      string
		want    dashboard.Period
		wantErr error
	}{
		{"", dashboard.PeriodAll, nil},
		{"all", dashboard.PeriodAll, nil},
		{"7d", dashboard.PeriodWeek, nil},
		{"30d", dashboard.PeriodMonth, nil},
		{"90d", dashboard.PeriodQuarter, nil},
		{"1y", "", dashboard.ErrUnknownPeriod},
		{"7D", "", dashboard.ErrUnknownPeriod},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := dashboard.ParsePeriod(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParsePeriod(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParsePeriod(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPeriod_Since(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		p    dashboard.Period
		want time.Time
	}{
		{dashboard.PeriodWeek, time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)},
		{dashboard.PeriodMonth, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)},
		{dashboard.PeriodQuarter, time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)},
		{dashboard.PeriodAll, time.Time{}},
	}
	for _, tt := range tests {
		if got := tt.p.Since(now); !got.Equal(tt.want) {
			t.Errorf("Period(%q).Since(%v) = %v, want %v", tt.p, now, got, tt.want)
		}
	}
}

func totals(undecided, hold, pending int, ext, red report.Count) report.Totals {
	return report.Totals{
		Undecided:    report.Count{Bundles: undecided},
		Hold:         report.Count{Bundles: hold},
		Pending:      report.Count{Bundles: pending},
		Externalized: ext,
		Redacted:     red,
	}
}

func state(id project.ID, name string, basis, previous scan.ID, tot, prev report.Totals) dashboard.ProjectState {
	return dashboard.ProjectState{
		Project:        project.Project{ID: id, Name: name},
		Last:           scan.Scan{ID: basis, ProjectID: id},
		Basis:          basis,
		Previous:       previous,
		Totals:         tot,
		PreviousTotals: prev,
	}
}

func sumData() dashboard.Data {
	return dashboard.Data{Projects: []dashboard.ProjectState{
		state(1, "a", 5, 4,
			totals(3, 1, 2, report.Count{Bundles: 4, Locations: 9}, report.Count{Bundles: 2, Locations: 2}),
			totals(6, 2, 2, report.Count{}, report.Count{})),
		state(2, "b", 7, 0, totals(1, 0, 0, report.Count{}, report.Count{}), report.Totals{}),
	}}
}

func TestBuild_Sums(t *testing.T) {
	d := sumData()
	d.Projects[0].Totals.FalsePositive = report.Count{Bundles: 1, Locations: 1}

	all := dashboard.Build(d, dashboard.Filter{})
	if diff := cmp.Diff(dashboard.Split{Undecided: 4, Hold: 1, Pending: 2}, all.Remaining); diff != "" {
		t.Errorf("Build all Remaining mismatch (-want +got):\n%s", diff)
	}
	if all.Delta != -4 || !all.HasDelta {
		t.Errorf("Build all Delta = %d, %v, want -4, true", all.Delta, all.HasDelta)
	}
	if diff := cmp.Diff(report.Count{Bundles: 4, Locations: 9}, all.Externalized); diff != "" {
		t.Errorf("Build all Externalized mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(report.Count{Bundles: 2, Locations: 2}, all.Redacted); diff != "" {
		t.Errorf("Build all Redacted mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(report.Count{Bundles: 1, Locations: 1}, all.FalsePositive); diff != "" {
		t.Errorf("Build all FalsePositive mismatch (-want +got):\n%s", diff)
	}
	if all.Scanned != 2 || len(all.Rows) != 2 {
		t.Errorf("Build all Scanned, rows = %d, %d, want 2, 2", all.Scanned, len(all.Rows))
	}
	if all.Rows[0].Rate != 50 || all.Rows[1].Rate != 0 {
		t.Errorf("Build all rates = %d, %d, want 50, 0", all.Rows[0].Rate, all.Rows[1].Rate)
	}

	b := dashboard.Build(d, dashboard.Filter{Project: 2})
	if diff := cmp.Diff(dashboard.Split{Undecided: 1}, b.Remaining); diff != "" {
		t.Errorf("Build project 2 Remaining mismatch (-want +got):\n%s", diff)
	}
	if b.HasDelta || b.Delta != 0 {
		t.Errorf("Build project 2 Delta = %d, %v, want 0, false", b.Delta, b.HasDelta)
	}
	if b.Project != 2 || b.Scanned != 1 || len(b.Rows) != 2 {
		t.Errorf("Build project 2 Project, Scanned, rows = %d, %d, %d, want 2, 1, 2", b.Project, b.Scanned, len(b.Rows))
	}
}

func TestBuild_Rate(t *testing.T) {
	tests := []struct {
		name string
		st   dashboard.ProjectState
		want int
	}{
		{"nothing", state(1, "a", 1, 0, report.Totals{}, report.Totals{}), -1},
		{"one of three", state(1, "a", 1, 0,
			totals(2, 0, 0, report.Count{Bundles: 1}, report.Count{}), report.Totals{}), 33},
		{"all done", state(1, "a", 1, 0,
			totals(0, 0, 0, report.Count{Bundles: 3}, report.Count{Bundles: 2}), report.Totals{}), 100},
		{"never scanned", state(1, "a", 0, 0, report.Totals{}, report.Totals{}), -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dashboard.Build(dashboard.Data{Projects: []dashboard.ProjectState{tt.st}}, dashboard.Filter{})
			if got.Rows[0].Rate != tt.want {
				t.Errorf("Build Rate = %d, want %d", got.Rows[0].Rate, tt.want)
			}
			if wantScanned := tt.st.Basis != 0; got.Rows[0].Scanned != wantScanned {
				t.Errorf("Build Scanned = %v, want %v", got.Rows[0].Scanned, wantScanned)
			}
		})
	}
}

func point(p project.ID, id scan.ID, per map[finding.Category]dashboard.Split) dashboard.Point {
	return dashboard.Point{Project: p, Scan: id, At: time.Date(2026, 11, int(id), 0, 0, 0, 0, time.UTC), Remaining: per}
}

// one puts n undecided bundles in category c.
func one(c finding.Category, n int) map[finding.Category]dashboard.Split {
	return map[finding.Category]dashboard.Split{c: {Undecided: n}}
}

func trendData() dashboard.Data {
	other := finding.CategoryOther
	return dashboard.Data{
		Projects: []dashboard.ProjectState{
			state(1, "a", 4, 0, totals(1, 1, 1, report.Count{}, report.Count{}), report.Totals{}),
			state(2, "b", 2, 0, totals(5, 0, 0, report.Count{}, report.Count{}), report.Totals{}),
		},
		Points: []dashboard.Point{
			point(1, 1, one(other, 10)),
			point(2, 2, one(finding.CategorySSH, 5)),
			point(1, 3, one(other, 8)),
			point(1, 4, map[finding.Category]dashboard.Split{other: {Undecided: 1, Hold: 1, Pending: 1}}),
		},
		PointTotal: 4,
	}
}

func TestBuild_Trend(t *testing.T) {
	got := dashboard.Build(trendData(), dashboard.Filter{})
	var totalsGot []int
	for _, s := range got.Trend {
		totalsGot = append(totalsGot, s.Total)
	}
	if diff := cmp.Diff([]int{10, 15, 13, 8}, totalsGot); diff != "" {
		t.Errorf("Build Trend totals mismatch (-want +got):\n%s", diff)
	}
	last := got.Trend[2]
	want := map[finding.Category]int{
		finding.CategoryPartnerSession: 0, finding.CategoryStorage: 0, finding.CategorySSH: 5,
		finding.CategoryCustomerData: 0, finding.CategoryOther: 8,
	}
	if diff := cmp.Diff(want, last.Remaining); diff != "" {
		t.Errorf("Build Trend[2].Remaining mismatch (-want +got):\n%s", diff)
	}
	if got.Trend[0].Name != "a" || got.Trend[1].Name != "b" || got.Trend[1].Project != 2 || got.Trend[1].Scan != 2 {
		t.Errorf("Build Trend names = %q, %q, want a, b", got.Trend[0].Name, got.Trend[1].Name)
	}
}

func TestBuild_Baseline(t *testing.T) {
	d := trendData()
	d.Points[1].Baseline = true
	d.PointTotal = 3
	got := dashboard.Build(d, dashboard.Filter{})
	var totalsGot []int
	for _, s := range got.Trend {
		totalsGot = append(totalsGot, s.Total)
	}
	if diff := cmp.Diff([]int{15, 13, 8}, totalsGot); diff != "" {
		t.Errorf("Build Trend totals mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_LastStepIsRemaining(t *testing.T) {
	got := dashboard.Build(trendData(), dashboard.Filter{})
	if last := got.Trend[len(got.Trend)-1].Total; last != got.Remaining.Total() {
		t.Errorf("Build last Step.Total = %d, Remaining.Total() = %d, want equal", last, got.Remaining.Total())
	}
	sum := 0
	for _, s := range got.Categories {
		sum += s.Total()
	}
	if sum != got.Remaining.Total() {
		t.Errorf("Build Categories total = %d, Remaining.Total() = %d, want equal", sum, got.Remaining.Total())
	}
	if len(got.Categories) != len(categories) {
		t.Errorf("Build Categories has %d keys, want %d", len(got.Categories), len(categories))
	}
}

func TestBuild_CategoriesFollowTheGivenOrder(t *testing.T) {
	other := finding.CategoryOther
	later := point(1, 2, one(other, 7))
	earlier := point(1, 9, one(other, 3))
	// The later-given point has the smaller scan ID and the later time.
	earlier.At = later.At.Add(-time.Hour)
	d := dashboard.Data{Points: []dashboard.Point{earlier, later}, PointTotal: 2}
	if got := dashboard.Build(d, dashboard.Filter{}); got.Categories[other].Undecided != 7 {
		t.Errorf("Build Categories[other] = %+v, want the later-given point (7 undecided)", got.Categories[other])
	}

	base := point(1, 1, one(other, 4))
	base.Baseline = true
	next := point(1, 5, one(other, 2))
	d = dashboard.Data{Points: []dashboard.Point{base, next}, PointTotal: 1}
	if got := dashboard.Build(d, dashboard.Filter{}); got.Categories[other].Undecided != 2 {
		t.Errorf("Build Categories[other] = %+v, want the point after the baseline (2 undecided)", got.Categories[other])
	}
}

func TestBuild_TrendMore(t *testing.T) {
	d := dashboard.Data{PointTotal: 45}
	for i := range dashboard.MaxPoints {
		d.Points = append(d.Points, point(1, scan.ID(i+1), one(finding.CategoryOther, 1)))
	}
	got := dashboard.Build(d, dashboard.Filter{})
	if got.TrendMore != 15 {
		t.Errorf("Build TrendMore = %d, want 15", got.TrendMore)
	}
	d.PointTotal = 3
	if got := dashboard.Build(d, dashboard.Filter{}); got.TrendMore != 0 {
		t.Errorf("Build TrendMore = %d, want 0", got.TrendMore)
	}
}

func TestBuild_Rules(t *testing.T) {
	var d dashboard.Data
	for i := range 12 {
		d.Rules = append(d.Rules, dashboard.Rule{Tool: "t", Rule: string(rune('a' + i)), Bundles: 2, FalsePositive: 12 - i})
	}
	got := dashboard.Build(d, dashboard.Filter{})
	if diff := cmp.Diff(d.Rules[:dashboard.MaxRules], got.Rules); diff != "" {
		t.Errorf("Build Rules mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_Empty(t *testing.T) {
	got := dashboard.Build(dashboard.Data{}, dashboard.Filter{})
	if len(got.Trend) != 0 || len(got.Rows) != 0 {
		t.Errorf("Build empty Trend, Rows = %d, %d, want 0, 0", len(got.Trend), len(got.Rows))
	}
	for _, c := range categories {
		s, ok := got.Categories[c]
		if !ok || s != (dashboard.Split{}) {
			t.Errorf("Build empty Categories[%q] = %v, %v, want zero, true", c, s, ok)
		}
	}
}

func TestBuild_KeepsNoInput(t *testing.T) {
	d := trendData()
	d.Rules = []dashboard.Rule{{Tool: "t", Rule: "r", Bundles: 1, FalsePositive: 1}}
	d.Projects[0].Project.Excludes = []string{"x"}
	got := dashboard.Build(d, dashboard.Filter{})
	want := dashboard.Build(trendData(), dashboard.Filter{})
	want.Rules = []dashboard.Rule{{Tool: "t", Rule: "r", Bundles: 1, FalsePositive: 1}}
	want.Rows[0].Project.Excludes = []string{"x"}

	d.Points[0].Remaining[finding.CategoryOther] = dashboard.Split{Undecided: 99}
	d.Points[3].Remaining[finding.CategoryOther] = dashboard.Split{Undecided: 99}
	d.Rules[0].Rule = "changed"
	d.Projects[0].Project.Excludes[0] = "changed"
	d.Points = d.Points[:1]

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Build result changed after the input was changed (-want +got):\n%s", diff)
	}
}

func TestTypes_HoldNoValueField(t *testing.T) {
	banned := []string{"Preview", "Fingerprint", "Context", "Value"}
	pkg := reflect.TypeFor[dashboard.Data]().PkgPath()
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
				if typ.PkgPath() == pkg && slices.Contains(banned, f.Name) {
					t.Errorf("%s has field %s, which may hold a value", typ, f.Name)
				}
				walk(f.Type)
			}
		}
	}
	walk(reflect.TypeFor[dashboard.Data]())
	walk(reflect.TypeFor[dashboard.Dashboard]())
}
