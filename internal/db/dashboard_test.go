package db_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

func readDashboard(t *testing.T, s *db.Store, f dashboard.Filter) dashboard.Data {
	t.Helper()
	d, err := s.Dashboard(t.Context(), f)
	if err != nil {
		t.Fatalf("Dashboard(%+v) error = %v", f, err)
	}
	return d
}

// fpN returns the nth fake fingerprint of the dashboard tests.
func fpN(n int) string { return fmt.Sprintf("%064x", 0xdb000+n) }

// doneAt stores findings as a succeeded scan of the project requested and
// finished n minutes after reportBase.
func doneAt(t *testing.T, s *db.Store, pid project.ID, n int, findings ...finding.Finding) scan.ID {
	t.Helper()
	id := saveAt(t, s, pid, minute(n), findings...)
	if err := s.SetScanFinished(t.Context(), id, minute(n)); err != nil {
		t.Fatalf("SetScanFinished(%d) error = %v", id, err)
	}
	return id
}

// detectedBy returns f with every location found by the given detections.
func detectedBy(f finding.Finding, ds ...finding.Detection) finding.Finding {
	locs := make([]finding.Location, 0, len(f.Locations))
	for _, l := range f.Locations {
		l.Detections = ds
		locs = append(locs, l)
	}
	f.Locations = locs
	return f
}

func pointsOf(d dashboard.Data, sid scan.ID) []dashboard.Point {
	var out []dashboard.Point
	for _, p := range d.Points {
		if p.Scan == sid {
			out = append(out, p)
		}
	}
	return out
}

func sumOfSplits(m map[finding.Category]dashboard.Split) dashboard.Split {
	var sum dashboard.Split
	for _, v := range m {
		sum.Undecided += v.Undecided
		sum.Hold += v.Hold
		sum.Pending += v.Pending
	}
	return sum
}

// pointKey is what the tests compare of a point: everything but its time,
// which they compare as minutes after reportBase.
type pointKey struct {
	Project  project.ID
	Scan     scan.ID
	Minute   int
	Baseline bool
}

func keysOf(points []dashboard.Point) []pointKey {
	out := make([]pointKey, 0, len(points))
	for _, p := range points {
		out = append(out, pointKey{p.Project, p.Scan, int(p.At.Sub(reportBase) / time.Minute), p.Baseline})
	}
	return out
}

func TestDashboard_Empty(t *testing.T) {
	s := newMigratedStore(t)
	if diff := cmp.Diff(dashboard.Data{}, readDashboard(t, s, dashboard.Filter{})); diff != "" {
		t.Errorf("Dashboard({}) with no project (-want +got):\n%s", diff)
	}

	pid := newProject(t, s, "shop")
	for _, f := range []dashboard.Filter{{}, {Project: pid}, {Since: *minute(0)}} {
		d := readDashboard(t, s, f)
		if len(d.Projects) != 1 {
			t.Fatalf("Dashboard(%+v).Projects = %+v, want one project", f, d.Projects)
		}
		p := d.Projects[0]
		if p.Project.ID != pid || p.Project.Name != "shop" || p.Last.ID != 0 || p.Basis != 0 || p.Previous != 0 ||
			p.Totals != (dashboard.ProjectState{}).Totals {
			t.Errorf("Dashboard(%+v).Projects[0] = %+v, want project %d with no scan", f, p, pid)
		}
		if d.Points != nil || d.PointTotal != 0 || d.Rules != nil {
			t.Errorf("Dashboard(%+v) = %d points of %d, rules %+v, want none", f, len(d.Points), d.PointTotal, d.Rules)
		}
	}
}

func TestDashboard_UnknownProject(t *testing.T) {
	s := newMigratedStore(t)
	newProject(t, s, "shop")
	if _, err := s.Dashboard(t.Context(), dashboard.Filter{Project: 999999}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Dashboard(project 999999) error = %v, want ErrNotFound", err)
	}
}

func TestDashboard_TotalsMatchReport(t *testing.T) {
	s := newMigratedStore(t)
	pid, first, second := totalsProject(t, s, "shop")

	d := readDashboard(t, s, dashboard.Filter{})
	if len(d.Projects) != 1 {
		t.Fatalf("Dashboard({}).Projects = %+v, want one project", d.Projects)
	}
	p := d.Projects[0]
	if p.Project.ID != pid || p.Last.ID != second || p.Basis != second || p.Previous != first {
		t.Errorf("Dashboard({}).Projects[0] project, last, basis, previous = %d, %d, %d, %d, want %d, %d, %d, %d",
			p.Project.ID, p.Last.ID, p.Basis, p.Previous, pid, second, second, first)
	}
	r := readReport(t, s, second)
	if diff := cmp.Diff(r.Totals, p.Totals); diff != "" {
		t.Errorf("Dashboard({}).Projects[0].Totals differ from Report(%d).Totals (-report +dashboard):\n%s", second, diff)
	}
	if diff := cmp.Diff(r.PreviousTotals, p.PreviousTotals); diff != "" {
		t.Errorf("Dashboard({}).Projects[0].PreviousTotals differ from Report(%d).PreviousTotals (-report +dashboard):\n%s",
			second, diff)
	}
}

func TestDashboard_PointsMatchTotals(t *testing.T) {
	s := newMigratedStore(t)
	_, first, second := totalsProject(t, s, "shop")

	d := readDashboard(t, s, dashboard.Filter{})
	for _, sid := range []scan.ID{first, second} {
		points := pointsOf(d, sid)
		if len(points) != 1 {
			t.Errorf("Dashboard({}) has %d points of scan %d, want 1", len(points), sid)
			continue
		}
		totals := readReport(t, s, sid).Totals
		want := dashboard.Split{
			Undecided: totals.Undecided.Bundles, Hold: totals.Hold.Bundles, Pending: totals.Pending.Bundles,
		}
		if got := sumOfSplits(points[0].Remaining); got != want {
			t.Errorf("Dashboard({}) point of scan %d adds up to %+v, want %+v as Report(%d).Totals", sid, got, want, sid)
		}
	}
}

func TestDashboard_Points(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	falsePositive := fnd(fpN(1), "conf/fp.yml", finding.CategoryStorage, finding.KindPassword, 1)
	hold := fnd(fpN(2), "conf/hold.yml", finding.CategoryStorage, finding.KindPassword, 2)
	// One bundle of two members in two categories; the first one stored
	// has the lower ID.
	undecidedLow := fnd(fpN(3), "conf/u1.yml", finding.CategoryStorage, finding.KindPassword, 3)
	undecidedHigh := fnd(fpN(3), "conf/u1.properties", finding.CategoryOther, finding.KindPassword, 3)
	undecided := fnd(fpN(4), "conf/u2.yml", finding.CategorySSH, finding.KindPassword, 4)
	externalize := fnd(fpN(5), "conf/e.yml", finding.CategoryPartnerSession, finding.KindToken, 5)
	redact := fnd(fpN(6), "logs/c.log", finding.CategoryCustomerData, finding.KindPhone, 6)

	first := doneAt(t, s, pid, 0, falsePositive, hold, undecidedLow, undecidedHigh, undecided, externalize, redact)
	if lo, hi := findingID(t, s, pid, fpN(3), "conf/u1.yml"), findingID(t, s, pid, fpN(3), "conf/u1.properties"); lo >= hi ||
		bundleOf(t, s, pid, fpN(3), "conf/u1.yml") != bundleOf(t, s, pid, fpN(3), "conf/u1.properties") {
		t.Fatalf("findings %d, %d of fingerprint 3, want one bundle with the yml member first", lo, hi)
	}
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, pid, fpN(1), "conf/fp.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater},
		bundleOf(t, s, pid, fpN(2), "conf/hold.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "api.main.token"},
		bundleOf(t, s, pid, fpN(5), "conf/e.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, bundleOf(t, s, pid, fpN(6), "logs/c.log"))

	second := doneAt(t, s, pid, 10, undecidedHigh, undecided)
	if _, err := s.InsertRawScanJob(ctx, pid, "failed", "failed"); err != nil {
		t.Fatalf("InsertRawScanJob(failed) error = %v", err)
	}
	empty := doneAt(t, s, pid, 20)
	if _, err := s.InsertRawScanJob(ctx, pid, "queued", "queued"); err != nil {
		t.Fatalf("InsertRawScanJob(queued) error = %v", err)
	}

	d := readDashboard(t, s, dashboard.Filter{})
	want := []dashboard.Point{
		{Project: pid, Scan: first, At: *minute(0), Remaining: map[finding.Category]dashboard.Split{
			finding.CategoryStorage:        {Undecided: 1, Hold: 1},
			finding.CategorySSH:            {Undecided: 1},
			finding.CategoryPartnerSession: {Pending: 1},
			finding.CategoryCustomerData:   {Pending: 1},
		}},
		{Project: pid, Scan: second, At: *minute(10), Remaining: map[finding.Category]dashboard.Split{
			finding.CategoryOther: {Undecided: 1},
			finding.CategorySSH:   {Undecided: 1},
		}},
		{Project: pid, Scan: empty, At: *minute(20), Remaining: map[finding.Category]dashboard.Split{}},
	}
	if diff := cmp.Diff(want, d.Points); diff != "" {
		t.Errorf("Dashboard({}).Points (-want +got):\n%s", diff)
	}
	if d.PointTotal != 3 {
		t.Errorf("Dashboard({}).PointTotal = %d, want 3", d.PointTotal)
	}
	if got := sumOfSplits(want[0].Remaining).Total(); got != 5 {
		t.Errorf("first point adds up to %d, want 5", got)
	}
}

func TestDashboard_Period(t *testing.T) {
	s := newMigratedStore(t)
	a := newProject(t, s, "a-shop")
	b := newProject(t, s, "b-shop")
	doneAt(t, s, a, 0)
	bOnly := doneAt(t, s, b, 5)
	aBefore := doneAt(t, s, a, 10)
	aAfter := doneAt(t, s, a, 30)

	d := readDashboard(t, s, dashboard.Filter{Since: *minute(20)})
	want := []pointKey{{b, bOnly, 5, true}, {a, aBefore, 10, true}, {a, aAfter, 30, false}}
	if diff := cmp.Diff(want, keysOf(d.Points)); diff != "" {
		t.Errorf("Dashboard(since minute 20).Points (-want +got):\n%s", diff)
	}
	if d.PointTotal != 1 {
		t.Errorf("Dashboard(since minute 20).PointTotal = %d, want 1", d.PointTotal)
	}

	d = readDashboard(t, s, dashboard.Filter{Since: *minute(40)})
	want = []pointKey{{b, bOnly, 5, true}, {a, aAfter, 30, true}}
	if diff := cmp.Diff(want, keysOf(d.Points)); diff != "" {
		t.Errorf("Dashboard(since minute 40).Points (-want +got):\n%s", diff)
	}
	if d.PointTotal != 0 {
		t.Errorf("Dashboard(since minute 40).PointTotal = %d, want 0", d.PointTotal)
	}
}

func TestDashboard_EveryScannedProjectHasPoint(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()

	// Succeeded scans with no finish time: they count from their request.
	unfinished := newProject(t, s, "unfinished")
	for _, n := range []int{0, 50} {
		id := saveAt(t, s, unfinished, minute(n))
		if err := s.SetScanFinished(ctx, id, nil); err != nil {
			t.Fatalf("SetScanFinished(%d, nil) error = %v", id, err)
		}
	}
	// Two scans that finished at the same time.
	tied := newProject(t, s, "tied")
	doneAt(t, s, tied, 10)
	doneAt(t, s, tied, 20)
	doneAt(t, s, tied, 20)
	failed := newProject(t, s, "failed")
	if _, err := s.InsertRawScanJob(ctx, failed, "failed", "failed"); err != nil {
		t.Fatalf("InsertRawScanJob(failed) error = %v", err)
	}
	never := newProject(t, s, "never")

	for _, pf := range []project.ID{0, unfinished, tied, failed, never} {
		for _, since := range []time.Time{{}, *minute(15), *minute(100)} {
			f := dashboard.Filter{Project: pf, Since: since}
			d := readDashboard(t, s, f)
			if len(d.Projects) != 4 {
				t.Errorf("Dashboard(%+v).Projects has %d projects, want 4", f, len(d.Projects))
			}
			for _, p := range d.Projects {
				inFilter := pf == 0 || pf == p.Project.ID
				var basis, other int
				for _, pt := range d.Points {
					switch {
					case pt.Project != p.Project.ID:
					case pt.Scan == p.Basis:
						basis++
					default:
						other++
					}
				}
				want := 0
				if inFilter && p.Basis != 0 {
					want = 1
				}
				if basis != want {
					t.Errorf("Dashboard(%+v) has %d points of the basis %d of project %q, want %d",
						f, basis, p.Basis, p.Project.Name, want)
				}
				if !inFilter && other != 0 {
					t.Errorf("Dashboard(%+v) has %d points of project %q outside the filter, want 0",
						f, other, p.Project.Name)
				}
			}
		}
	}
}

func TestDashboard_Cap(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	ids := make([]scan.ID, 0, 32)
	for n := range 32 {
		ids = append(ids, doneAt(t, s, pid, n))
	}

	d := readDashboard(t, s, dashboard.Filter{})
	want := []pointKey{{pid, ids[1], 1, true}}
	for n := 2; n < 32; n++ {
		want = append(want, pointKey{pid, ids[n], n, false})
	}
	if diff := cmp.Diff(want, keysOf(d.Points)); diff != "" {
		t.Errorf("Dashboard({}).Points of 32 scans (-want +got):\n%s", diff)
	}
	if d.PointTotal != 32 {
		t.Errorf("Dashboard({}).PointTotal = %d, want 32", d.PointTotal)
	}
}

func TestDashboard_ProjectFilter(t *testing.T) {
	s := newMigratedStore(t)
	a := newProject(t, s, "a-shop")
	b := newProject(t, s, "b-shop")
	for _, tc := range []struct {
		pid  project.ID
		rule string
	}{{a, "a-rule"}, {b, "b-rule"}} {
		doneAt(t, s, tc.pid, 0, detectedBy(fnd(fpN(1), "conf/a.yml", finding.CategoryStorage, finding.KindPassword, 1),
			finding.Detection{Tool: "opengrep", Rule: tc.rule}))
		decide(t, s, tc.pid, triage.Decision{Verdict: triage.VerdictFalsePositive},
			bundleOf(t, s, tc.pid, fpN(1), "conf/a.yml"))
	}

	d := readDashboard(t, s, dashboard.Filter{Project: a})
	if len(d.Projects) != 2 {
		t.Errorf("Dashboard(project a).Projects has %d projects, want 2", len(d.Projects))
	}
	if len(d.Points) != 1 || d.Points[0].Project != a {
		t.Errorf("Dashboard(project a).Points = %+v, want one point of project %d", d.Points, a)
	}
	want := []dashboard.Rule{{Tool: "opengrep", Rule: "a-rule", Bundles: 1, FalsePositive: 1}}
	if diff := cmp.Diff(want, d.Rules); diff != "" {
		t.Errorf("Dashboard(project a).Rules (-want +got):\n%s", diff)
	}
}

func TestDashboard_Rules(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	r1 := finding.Detection{Tool: "opengrep", Rule: "r1"}
	r2 := finding.Detection{Tool: "betterleaks", Rule: "r2"}
	r3 := finding.Detection{Tool: "opengrep", Rule: "r3"}

	// Only the earlier scan finds this false positive.
	old := detectedBy(fnd(fpN(9), "conf/old.yml", finding.CategoryStorage, finding.KindToken, 1), r1)
	doneAt(t, s, pid, 0, old)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, pid, fpN(9), "conf/old.yml"))

	// x1 has two locations that r1 found; r1 and r2 found the place of x2.
	x1 := detectedBy(fnd(fpN(1), "conf/x1.yml", finding.CategoryStorage, finding.KindToken, 1), r1)
	second := x1.Locations[0]
	second.StartLine, second.EndLine = 7, 7
	x1.Locations = append(x1.Locations, second)
	x2 := detectedBy(fnd(fpN(2), "conf/x2.yml", finding.CategoryStorage, finding.KindToken, 1), r1, r2)
	x3 := detectedBy(fnd(fpN(3), "conf/x3.yml", finding.CategoryStorage, finding.KindToken, 1), r1)
	z1 := detectedBy(fnd(fpN(4), "conf/z1.yml", finding.CategoryOther, finding.KindToken, 1), r3)
	z2 := detectedBy(fnd(fpN(5), "conf/z2.yml", finding.CategoryOther, finding.KindToken, 1), r3)
	doneAt(t, s, pid, 10, x1, x2, x3, z1, z2)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive},
		bundleOf(t, s, pid, fpN(1), "conf/x1.yml"), bundleOf(t, s, pid, fpN(2), "conf/x2.yml"))

	d := readDashboard(t, s, dashboard.Filter{})
	want := []dashboard.Rule{
		{Tool: "opengrep", Rule: "r1", Bundles: 3, FalsePositive: 2},
		{Tool: "betterleaks", Rule: "r2", Bundles: 1, FalsePositive: 1},
	}
	if diff := cmp.Diff(want, d.Rules); diff != "" {
		t.Errorf("Dashboard({}).Rules (-want +got):\n%s", diff)
	}
}
