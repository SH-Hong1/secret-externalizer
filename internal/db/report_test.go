package db_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// More fake fingerprints for the report tests.
var (
	fpE = strings.Repeat("e", 64)
	fpG = strings.Repeat("9", 64)
	fpH = strings.Repeat("8", 64)
)

// reportBase is the time the report tests count from. Every scan request
// and apply run time is set from it, never from the database clock.
var reportBase = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

// minute returns the time n minutes after reportBase.
func minute(n int) *time.Time {
	t := reportBase.Add(time.Duration(n) * time.Minute)
	return &t
}

func readReport(t *testing.T, s *db.Store, id scan.ID) report.Data {
	t.Helper()
	d, err := s.Report(t.Context(), id)
	if err != nil {
		t.Fatalf("Report(%d) error = %v", id, err)
	}
	return d
}

// saveAt stores findings as a succeeded scan of the project requested at
// when.
func saveAt(t *testing.T, s *db.Store, pid project.ID, when *time.Time, findings ...finding.Finding) scan.ID {
	t.Helper()
	id := save(t, s, pid, findings...)
	if err := s.SetScan(t.Context(), id, scan.StatusSucceeded, *when, 0); err != nil {
		t.Fatalf("SetScan(%d) error = %v", id, err)
	}
	return id
}

// runAt stores an apply run of the project for scan sid in state, with the
// given start and finish times, nil for none.
func runAt(t *testing.T, s *db.Store, pid project.ID, sid scan.ID, state apply.State, started, finished *time.Time) apply.ID {
	t.Helper()
	id := applyRunIn(t, s, pid, sid, state)
	if err := s.SetApplyRunTimes(t.Context(), id, started, finished); err != nil {
		t.Fatalf("SetApplyRunTimes(%d) error = %v", id, err)
	}
	return id
}

// itemOf returns the item of an apply that changes the first location of
// f: externalize under name for a secret, redact for customer data.
func itemOf(t *testing.T, s *db.Store, pid project.ID, f finding.Finding, name string, status apply.Status) apply.Item {
	t.Helper()
	it := apply.Item{
		BundleID: bundleOf(t, s, pid, f.Fingerprint, f.File), FindingID: findingID(t, s, pid, f.Fingerprint, f.File),
		File: f.File, StartLine: f.Locations[0].StartLine, StartCol: 1, Line: f.Locations[0].StartLine,
		Action: apply.ActionReplace, Category: f.Category, Kind: f.Kind, Name: name, Tracked: true, Status: status,
	}
	if f.Category == finding.CategoryCustomerData {
		it.Action, it.Name = apply.ActionRedact, ""
	} else {
		it.StorePath = bao.StorePath(int64(pid), "ext."+name)
		it.StoreVersion = 1
	}
	return it
}

func insertItems(t *testing.T, s *db.Store, run apply.ID, items ...apply.Item) {
	t.Helper()
	for _, it := range items {
		if err := s.InsertApplyItem(t.Context(), run, it); err != nil {
			t.Fatalf("InsertApplyItem(%d, %+v) error = %v", run, it, err)
		}
	}
}

// The findings of totalsProject.
var (
	totalsPwA   = fnd(fpA, "conf/a.yml", finding.CategoryStorage, finding.KindPassword, 3)
	totalsPwB   = fnd(fpA, "conf/b.xml", finding.CategoryStorage, finding.KindPassword, 5)
	totalsToken = fnd(fpE, "conf/c.yml", finding.CategoryPartnerSession, finding.KindToken, 4)
	totalsPhone = fnd(fpC, "logs/x.log", finding.CategoryCustomerData, finding.KindPhone, 7)
	totalsHost  = fnd(fpB, "conf/a.yml", finding.CategoryStorage, finding.KindHost, 2)
	totalsHold  = fnd(fpG, "conf/d.yml", finding.CategoryStorage, finding.KindPassword, 6)
	totalsUndec = fnd(fpH, "conf/e.yml", finding.CategoryOther, finding.KindToken, 8)
)

// totalsProject stores a scan that finds six bundles (externalize two,
// redact one, a false positive, a hold and an undecided one), an apply run
// after it that applies the two externalized bundles in three places and
// the redaction in one and skips one place, and a second scan after the run
// that finds only the false positive, the hold and the undecided bundle.
func totalsProject(t *testing.T, s *db.Store, name string) (pid project.ID, first, second scan.ID) {
	t.Helper()
	pid = newProject(t, s, name)
	first = saveAt(t, s, pid, minute(0),
		totalsPwA, totalsPwB, totalsToken, totalsPhone, totalsHost, totalsHold, totalsUndec)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"},
		bundleOf(t, s, pid, fpA, "conf/a.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "api.main.token"},
		bundleOf(t, s, pid, fpE, "conf/c.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, bundleOf(t, s, pid, fpC, "logs/x.log"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, pid, fpB, "conf/a.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater, Note: "ask the owner"},
		bundleOf(t, s, pid, fpG, "conf/d.yml"))

	run := runAt(t, s, pid, first, apply.StateSucceeded, minute(60), minute(61))
	skipped := itemOf(t, s, pid, totalsPwB, "db.main.password", apply.StatusSkipped)
	skipped.StartCol, skipped.Reason = 9, apply.Reason("file-changed")
	insertItems(t, s, run,
		itemOf(t, s, pid, totalsPwA, "db.main.password", apply.StatusApplied),
		itemOf(t, s, pid, totalsPwB, "db.main.password", apply.StatusApplied),
		itemOf(t, s, pid, totalsToken, "api.main.token", apply.StatusApplied),
		itemOf(t, s, pid, totalsPhone, "", apply.StatusApplied),
		skipped)

	second = saveAt(t, s, pid, minute(120), totalsHost, totalsHold, totalsUndec)
	return pid, first, second
}

func TestReport_NotFound(t *testing.T) {
	s := newMigratedStore(t)
	if _, err := s.Report(t.Context(), 999999); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Report(999999) error = %v, want ErrNotFound", err)
	}
}

func TestReport_NotSucceeded(t *testing.T) {
	for _, tc := range []struct{ status, job string }{
		{"queued", "queued"}, {"running", "running"}, {"failed", "failed"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			s := newMigratedStore(t)
			pid := newProject(t, s, "shop")
			ok := saveAt(t, s, pid, minute(0), totalsPwA, totalsPhone)
			id, err := s.InsertRawScanJob(t.Context(), pid, tc.status, tc.job)
			if err != nil {
				t.Fatalf("InsertRawScanJob(%s) error = %v", tc.status, err)
			}
			d := readReport(t, s, id)
			if d.Project.ID != pid || d.Scan.ID != id || string(d.Scan.Status) != tc.status || d.Latest != ok {
				t.Errorf("Report(%s scan) = project %d, scan %d %s, latest %d; want project %d, scan %d, latest %d",
					tc.status, d.Project.ID, d.Scan.ID, d.Scan.Status, d.Latest, pid, id, ok)
			}
			if d.Previous != 0 || d.Totals != (report.Totals{}) || len(d.Lines) != 0 || len(d.Changes) != 0 ||
				len(d.Redactions) != 0 || len(d.Events) != 0 || d.EventTotal != 0 {
				t.Errorf("Report(%s scan) = %+v, want only the project, the scan and the latest scan", tc.status, d)
			}
		})
	}
}

func TestReport_Totals(t *testing.T) {
	s := newMigratedStore(t)
	_, first, second := totalsProject(t, s, "shop")

	d := readReport(t, s, second)
	want := report.Totals{
		Found:         report.Count{Bundles: 3, Locations: 3},
		FalsePositive: report.Count{Bundles: 1, Locations: 1},
		Hold:          report.Count{Bundles: 1, Locations: 1},
		Undecided:     report.Count{Bundles: 1, Locations: 1},
		Externalized:  report.Count{Bundles: 2, Locations: 3},
		Redacted:      report.Count{Bundles: 1, Locations: 1},
	}
	if diff := cmp.Diff(want, d.Totals); diff != "" {
		t.Errorf("Report(second).Totals (-want +got):\n%s", diff)
	}
	if d.Latest != second || d.Previous != first {
		t.Errorf("Report(second) latest, previous = %d, %d, want %d, %d", d.Latest, d.Previous, second, first)
	}
	wantPrev := report.Totals{
		Found:         report.Count{Bundles: 6, Locations: 7},
		FalsePositive: report.Count{Bundles: 1, Locations: 1},
		Hold:          report.Count{Bundles: 1, Locations: 1},
		Undecided:     report.Count{Bundles: 1, Locations: 1},
		Pending:       report.Count{Bundles: 3, Locations: 4},
	}
	if diff := cmp.Diff(wantPrev, d.PreviousTotals); diff != "" {
		t.Errorf("Report(second).PreviousTotals (-want +got):\n%s", diff)
	}

	d = readReport(t, s, first)
	if diff := cmp.Diff(wantPrev, d.Totals); diff != "" {
		t.Errorf("Report(first).Totals (-want +got):\n%s", diff)
	}
	if d.Latest != second || d.Previous != 0 || d.PreviousTotals != (report.Totals{}) {
		t.Errorf("Report(first) latest, previous, previous totals = %d, %d, %+v, want %d, 0, zero",
			d.Latest, d.Previous, d.PreviousTotals, second)
	}
	for _, l := range d.Lines {
		want := report.Applied("")
		if l.Verdict == triage.VerdictExternalize || l.Verdict == triage.VerdictRedact {
			want = report.AppliedAfter
		}
		if l.Applied != want {
			t.Errorf("Report(first) line %+v Applied = %q, want %q", l, l.Applied, want)
		}
	}
}

// lineTotals adds up lines by verdict as Totals counts them.
func lineTotals(lines []report.Line) report.Totals {
	var got report.Totals
	add := func(c *report.Count, l report.Line) {
		c.Bundles += l.Bundles
		c.Locations += l.Locations
	}
	for _, l := range lines {
		add(&got.Found, l)
		switch l.Verdict {
		case triage.VerdictFalsePositive:
			add(&got.FalsePositive, l)
		case triage.VerdictHold:
			add(&got.Hold, l)
		case "":
			add(&got.Undecided, l)
		case triage.VerdictExternalize, triage.VerdictRedact:
			add(&got.Pending, l)
		}
	}
	return got
}

func TestReport_TotalsMatchLines(t *testing.T) {
	s := newMigratedStore(t)
	pid, first, second := totalsProject(t, s, "shop")
	// More customer data, grouped into lines of several bundles.
	var phones []finding.Finding
	for i := range 5 {
		phones = append(phones, fnd(fmt.Sprintf("%064x", i+1), "logs/many.log", finding.CategoryCustomerData,
			finding.KindPhone, 10+i))
	}
	third := saveAt(t, s, pid, minute(180), append(phones, totalsHost, totalsHold, totalsUndec)...)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact},
		bundleOf(t, s, pid, phones[0].Fingerprint, "logs/many.log"), bundleOf(t, s, pid, phones[1].Fingerprint, "logs/many.log"))

	for _, id := range []scan.ID{first, second, third} {
		d := readReport(t, s, id)
		got := lineTotals(d.Lines)
		got.Externalized, got.Redacted = d.Totals.Externalized, d.Totals.Redacted
		if diff := cmp.Diff(d.Totals, got); diff != "" {
			t.Errorf("Report(%d) lines added up by verdict differ from Totals (-totals +lines):\n%s", id, diff)
		}
	}
}

// phones returns n phone numbers in file, each a value of its own.
func phones(file string, n int) []finding.Finding {
	out := make([]finding.Finding, 0, n)
	for i := range n {
		out = append(out, fnd(fmt.Sprintf("%064x", i+1), file, finding.CategoryCustomerData, finding.KindPhone, i+1))
	}
	return out
}

func bundlesOf(t *testing.T, s *db.Store, pid project.ID, fs []finding.Finding) []triage.BundleID {
	t.Helper()
	ids := make([]triage.BundleID, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, bundleOf(t, s, pid, f.Fingerprint, f.File))
	}
	return ids
}

func TestReport_CustomerDataGrouped(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	found := phones("logs/app.log", 37)
	sid := saveAt(t, s, pid, minute(0), found...)
	ids := bundlesOf(t, s, pid, found)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact, Note: "remove from logs"}, ids...)

	d := readReport(t, s, sid)
	want := []report.Line{{
		Category: finding.CategoryCustomerData, Kind: finding.KindPhone, Verdict: triage.VerdictRedact,
		Note: "remove from logs", File: "logs/app.log", FirstLine: 1, Files: 1, Bundles: 37, Locations: 37,
	}}
	if diff := cmp.Diff(want, d.Lines, cmp.FilterPath(func(p cmp.Path) bool {
		return p.Last().String() == ".DecidedAt"
	}, cmp.Ignore())); diff != "" {
		t.Errorf("Report(%d).Lines for 37 phone numbers with one decision (-want +got):\n%s", sid, diff)
	}
	if len(d.Lines) == 1 && d.Lines[0].DecidedAt.IsZero() {
		t.Errorf("Report(%d).Lines[0].DecidedAt is zero, want the time of the decision", sid)
	}

	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact, Note: "a different note"}, ids[5])
	d = readReport(t, s, sid)
	got := map[string]int{}
	for _, l := range d.Lines {
		if l.Bundle != 0 {
			t.Errorf("Report(%d) customer-data line %+v has bundle %d, want 0", sid, l, l.Bundle)
		}
		got[l.Note] = l.Bundles
	}
	if diff := cmp.Diff(map[string]int{"remove from logs": 36, "a different note": 1}, got); diff != "" {
		t.Errorf("Report(%d) bundles by note after one note changed (-want +got):\n%s", sid, diff)
	}
}

func TestReport_AppliedAsOf(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	pw := totalsPwA
	other := totalsToken
	queued := totalsHold
	first := saveAt(t, s, pid, minute(0), pw, other, queued)
	pwBundle := bundleOf(t, s, pid, fpA, "conf/a.yml")
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, pwBundle)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "api.main.token"},
		bundleOf(t, s, pid, fpE, "conf/c.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.other.password"},
		bundleOf(t, s, pid, fpG, "conf/d.yml"))

	// A run that started before the second scan: one place applied, the
	// others in every status but applied.
	run := runAt(t, s, pid, first, apply.StateSucceeded, minute(10), minute(11))
	applied := itemOf(t, s, pid, pw, "db.main.password", apply.StatusApplied)
	insertItems(t, s, run, applied)
	for i, st := range []apply.Status{apply.StatusSkipped, apply.StatusFailed, apply.StatusWriting, apply.StatusPlanned} {
		it := itemOf(t, s, pid, other, "api.main.token", st)
		it.StartCol = 10 + i
		insertItems(t, s, run, it)
	}
	// A run that closed without a start time before the second scan.
	closed := runAt(t, s, pid, first, apply.StateFailed, nil, minute(20))
	closedItem := itemOf(t, s, pid, pw, "db.main.password", apply.StatusApplied)
	closedItem.StartCol = 2
	insertItems(t, s, closed, closedItem)

	second := saveAt(t, s, pid, minute(30), pw, other, queued)

	// A run released after its first attempt and queued again: it has
	// neither time, so it is neither before nor after a scan.
	again := runAt(t, s, pid, second, apply.StateQueued, nil, nil)
	insertItems(t, s, again, itemOf(t, s, pid, queued, "db.other.password", apply.StatusApplied))

	d := readReport(t, s, second)
	applies := map[triage.BundleID]report.Applied{}
	for _, l := range d.Lines {
		applies[l.Bundle] = l.Applied
	}
	want := map[triage.BundleID]report.Applied{
		pwBundle:                               report.AppliedBefore,
		bundleOf(t, s, pid, fpE, "conf/c.yml"): "",
		bundleOf(t, s, pid, fpG, "conf/d.yml"): "",
	}
	if diff := cmp.Diff(want, applies); diff != "" {
		t.Errorf("Report(second) Applied by bundle (-want +got):\n%s", diff)
	}
	applied.Status, closedItem.Status = apply.StatusApplied, apply.StatusApplied
	wantChanges := []report.Change{{Run: run, Item: applied}, {Run: closed, Item: closedItem}}
	if diff := cmp.Diff(wantChanges, d.Changes); diff != "" {
		t.Errorf("Report(second).Changes (-want +got):\n%s", diff)
	}
	if d.Totals.Externalized != (report.Count{Bundles: 1, Locations: 2}) {
		t.Errorf("Report(second).Totals.Externalized = %+v, want 1 bundle in 2 places", d.Totals.Externalized)
	}

	t.Run("the run a scan confirms", func(t *testing.T) {
		// A run closed without a start time requests its confirmation scan
		// in the transaction that closes it, so both have the same time.
		rescanRun := runAt(t, s, pid, second, apply.StateFailed, nil, minute(40))
		it := itemOf(t, s, pid, other, "api.main.token", apply.StatusApplied)
		it.StartCol = 3
		insertItems(t, s, rescanRun, it)
		if err := s.MoveApplyRun(ctx, again, apply.StateFailed); err != nil {
			t.Fatalf("MoveApplyRun(%d, failed) error = %v", again, err)
		}
		third := save(t, s, pid, pw, other)
		if err := s.SetScan(ctx, third, scan.StatusSucceeded, *minute(40), rescanRun); err != nil {
			t.Fatalf("SetScan(%d) error = %v", third, err)
		}
		d := readReport(t, s, third)
		if d.Totals.Externalized != (report.Count{Bundles: 2, Locations: 3}) {
			t.Errorf("Report(confirmation scan).Totals.Externalized = %+v, want 2 bundles in 3 places",
				d.Totals.Externalized)
		}
	})
}

func TestReport_ItemOfMissingBundle(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	first := saveAt(t, s, pid, minute(0), totalsPwA, totalsHost)
	pw := bundleOf(t, s, pid, fpA, "conf/a.yml")
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, pw)
	run := runAt(t, s, pid, first, apply.StateSucceeded, minute(10), minute(11))
	it := itemOf(t, s, pid, totalsPwA, "db.main.password", apply.StatusApplied)
	insertItems(t, s, run, it)
	second := saveAt(t, s, pid, minute(20), totalsHost)
	if err := s.DeleteBundleRows(t.Context(), pw); err != nil {
		t.Fatalf("DeleteBundleRows(%d) error = %v", pw, err)
	}

	d := readReport(t, s, second)
	if diff := cmp.Diff([]report.Change{{Run: run, Item: it}}, d.Changes); diff != "" {
		t.Errorf("Report(%d).Changes after the bundle was deleted (-want +got):\n%s", second, diff)
	}
	if len(d.Changes) == 1 && (d.Changes[0].Item.Name == "" || d.Changes[0].Item.StorePath == "") {
		t.Errorf("Report(%d).Changes[0] = %+v, want its name and store path", second, d.Changes[0])
	}
}

func TestReport_Events(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	found := phones("logs/app.log", 37)
	sid := saveAt(t, s, pid, minute(0), append(found, totalsPwA)...)
	ids := bundlesOf(t, s, pid, found)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, ids...)
	pw := bundleOf(t, s, pid, fpA, "conf/a.yml")
	if err := s.RecordReveal(ctx, pid, pw, tester); err != nil {
		t.Fatalf("RecordReveal(%d) error = %v", pw, err)
	}
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, pw)

	d := readReport(t, s, sid)
	if d.EventTotal != 2 || len(d.Events) != 2 {
		t.Fatalf("Report(%d) events = %d of %d, want 2 of 2: %+v", sid, len(d.Events), d.EventTotal, d.Events)
	}
	newest, group := d.Events[0], d.Events[1]
	if newest.Action != audit.ActionVerdict || newest.Bundles != 1 ||
		newest.Detail[audit.DetailVerdict] != string(triage.VerdictFalsePositive) || newest.Kind != finding.KindPassword {
		t.Errorf("Report(%d).Events[0] = %+v, want the false-positive verdict on the password", sid, newest)
	}
	if group.Action != audit.ActionVerdict || group.Bundles != 37 || group.Actor != tester ||
		!cmp.Equal(group.Detail, map[string]string{audit.DetailVerdict: string(triage.VerdictRedact)}) ||
		group.Category != finding.CategoryCustomerData || group.Kind != finding.KindPhone || group.File != "logs/app.log" {
		t.Errorf("Report(%d).Events[1] = %+v, want one line for the 37 redactions", sid, group)
	}
	if !newest.At.After(group.At) {
		t.Errorf("Report(%d) events at %v, %v, want newest first", sid, newest.At, group.At)
	}

	// Older than any decision above, which the database clock dates.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	extra := make([]audit.Event, 0, report.MaxRows+5)
	for i := range report.MaxRows + 5 {
		extra = append(extra, audit.Event{
			At: old.Add(-time.Duration(i) * time.Minute), Actor: tester, Action: audit.ActionMove,
			ProjectID: pid, BundleID: pw, Detail: map[string]string{audit.DetailTo: fmt.Sprint(i)},
		})
	}
	if err := s.InsertAuditEvents(ctx, extra...); err != nil {
		t.Fatalf("InsertAuditEvents(%d events) error = %v", len(extra), err)
	}
	d = readReport(t, s, sid)
	if len(d.Events) != report.MaxRows || d.EventTotal != report.MaxRows+7 {
		t.Errorf("Report(%d) events = %d of %d, want %d of %d", sid, len(d.Events), d.EventTotal,
			report.MaxRows, report.MaxRows+7)
	}
	if len(d.Events) > 0 && d.Events[0].Detail[audit.DetailVerdict] != string(triage.VerdictFalsePositive) {
		t.Errorf("Report(%d).Events[0] = %+v, want the newest decision first", sid, d.Events[0])
	}
}

func TestReport_OtherProject(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	_, _, second := totalsProject(t, s, "shop")
	want := readReport(t, s, second)

	otherPid, otherFirst, _ := totalsProject(t, s, "other")
	run := runAt(t, s, otherPid, otherFirst, apply.StateSucceeded, minute(1), minute(2))
	insertItems(t, s, run, itemOf(t, s, otherPid, totalsHold, "db.other.password", apply.StatusApplied))
	if err := s.InsertAuditEvents(ctx, audit.Event{
		At: *minute(1), Actor: tester, Action: audit.ActionApply, ProjectID: otherPid,
		Detail: map[string]string{audit.DetailApply: "1"},
	}); err != nil {
		t.Fatalf("InsertAuditEvents() error = %v", err)
	}

	got := readReport(t, s, second)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Report(%d) changed when another project got scans, applies and events (-before +after):\n%s",
			second, diff)
	}
	if len(got.Changes) != 3 || len(got.Redactions) != 1 || len(got.Lines) != 3 || got.EventTotal != 5 {
		t.Errorf("Report(%d) = %d changes, %d redactions, %d lines, %d event lines, want 3, 1, 3, 5",
			second, len(got.Changes), len(got.Redactions), len(got.Lines), got.EventTotal)
	}
}

func TestReport_FrontendAndShell(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	jsp := fnd(fpA, "web/login.jsp", finding.CategoryStorage, finding.KindPassword, 3)
	runSh := fnd(fpE, "bin/run.sh", finding.CategoryStorage, finding.KindPassword, 4)
	stopSh := fnd(fpE, "bin/stop.sh", finding.CategoryStorage, finding.KindPassword, 5)
	yml := fnd(fpG, "conf/a.yml", finding.CategoryStorage, finding.KindPassword, 6)
	jspPhone := fnd(fpC, "web/list.jsp", finding.CategoryCustomerData, finding.KindPhone, 7)
	sid := saveAt(t, s, pid, minute(0), jsp, runSh, stopSh, yml, jspPhone)

	d := readReport(t, s, sid)
	type flags struct {
		Files                     int
		Frontend, Shell, Customer bool
	}
	got := map[string]flags{}
	for _, l := range d.Lines {
		got[l.File] = flags{l.Files, l.Frontend, l.Shell, l.Category == finding.CategoryCustomerData}
	}
	want := map[string]flags{
		"web/login.jsp": {1, true, false, false},
		"bin/run.sh":    {2, false, true, false},
		"conf/a.yml":    {1, false, false, false},
		"web/list.jsp":  {1, false, false, true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Report(%d) lines by first file (-want +got):\n%s", sid, diff)
	}
}
