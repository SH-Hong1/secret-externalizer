package db_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// linesOf returns the lines of the three lists of what the scan found.
func linesOf(d report.Data) []report.Line {
	return slices.Concat(d.Structure.Rows, d.Remaining.Rows, d.FalsePositives.Rows)
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
			if d.Previous != 0 || d.Totals != (report.Totals{}) || len(linesOf(d)) != 0 || len(d.Externalized.Rows) != 0 ||
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
	for _, l := range linesOf(d) {
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
		got := lineTotals(linesOf(d))
		got.Externalized, got.Redacted = d.Totals.Externalized, d.Totals.Redacted
		if diff := cmp.Diff(d.Totals, got); diff != "" {
			t.Errorf("Report(%d) lines added up by verdict differ from Totals (-totals +lines):\n%s", id, diff)
		}
	}
}

// listTotals adds up the triage list of a scan by verdict in the units of
// report.Totals: a line of customer data counts each bundle it groups.
func listTotals(entries []triage.Entry) report.Totals {
	var got report.Totals
	for _, e := range entries {
		n := report.Count{Bundles: e.Bundles, Locations: e.Count}
		var c *report.Count
		switch e.Verdict {
		case triage.VerdictFalsePositive:
			c = &got.FalsePositive
		case triage.VerdictHold:
			c = &got.Hold
		case "":
			c = &got.Undecided
		case triage.VerdictExternalize, triage.VerdictRedact:
			c = &got.Pending
		}
		c.Bundles += n.Bundles
		c.Locations += n.Locations
		got.Found.Bundles += n.Bundles
		got.Found.Locations += n.Locations
	}
	return got
}

func TestReport_ConfirmationTotalsMatchTriageList(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	many := phones("logs/many.log", 5)
	first := saveAt(t, s, pid, minute(0),
		append([]finding.Finding{totalsPwA, totalsToken, totalsHost, totalsHold, totalsUndec}, many...)...)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"},
		bundleOf(t, s, pid, fpA, "conf/a.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "api.main.token"},
		bundleOf(t, s, pid, fpE, "conf/c.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, bundlesOf(t, s, pid, many[:2])...)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, pid, fpB, "conf/a.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater},
		bundleOf(t, s, pid, fpG, "conf/d.yml"))

	// The run applies the password and both redactions and skips the
	// token, so the token is still pending and one redacted phone number
	// is found again.
	run := runAt(t, s, pid, first, apply.StateSucceeded, minute(60), minute(61))
	skipped := itemOf(t, s, pid, totalsToken, "api.main.token", apply.StatusSkipped)
	skipped.Reason = apply.Reason("file-changed")
	insertItems(t, s, run,
		itemOf(t, s, pid, totalsPwA, "db.main.password", apply.StatusApplied),
		itemOf(t, s, pid, many[0], "", apply.StatusApplied),
		itemOf(t, s, pid, many[1], "", apply.StatusApplied),
		skipped)
	confirmation := save(t, s, pid, append([]finding.Finding{totalsToken, totalsHost, totalsHold, totalsUndec}, many[1:]...)...)
	if err := s.SetScan(ctx, confirmation, scan.StatusSucceeded, *minute(61), run); err != nil {
		t.Fatalf("SetScan(%d) error = %v", confirmation, err)
	}

	r := readReport(t, s, confirmation)
	if r.Latest != confirmation {
		t.Fatalf("Report(%d).Latest = %d, want the confirmation scan to be the latest", confirmation, r.Latest)
	}
	page, err := s.Entries(ctx, pid, confirmation, triage.Filter{Status: triage.StatusAll, Page: 1})
	if err != nil {
		t.Fatalf("Entries(%d, all) error = %v", confirmation, err)
	}
	if page.Pages != 1 {
		t.Fatalf("Entries(%d, all) has %d pages, want 1", confirmation, page.Pages)
	}

	got := listTotals(page.Entries)
	got.Externalized, got.Redacted = r.Totals.Externalized, r.Totals.Redacted
	if diff := cmp.Diff(r.Totals, got); diff != "" {
		t.Errorf("Report(%d).Totals differ from its triage list added up by bundle (-report +list):\n%s", confirmation, diff)
	}

	// The tabs count lines: the three undecided phone numbers of one file
	// are one line, and the redacted one found again is another.
	wantTabs := map[triage.Verdict]int{
		"": 2, triage.VerdictExternalize: 1, triage.VerdictRedact: 1, triage.VerdictFalsePositive: 1, triage.VerdictHold: 1,
	}
	if diff := cmp.Diff(wantTabs, page.Counts); diff != "" {
		t.Errorf("Entries(%d).Counts differ (-want +got):\n%s", confirmation, diff)
	}
	wantReport := report.Totals{
		Found: report.Count{Bundles: 8, Locations: 8}, FalsePositive: report.Count{Bundles: 1, Locations: 1},
		Hold: report.Count{Bundles: 1, Locations: 1}, Undecided: report.Count{Bundles: 4, Locations: 4},
		Pending:      report.Count{Bundles: 2, Locations: 2},
		Externalized: report.Count{Bundles: 1, Locations: 1}, Redacted: report.Count{Bundles: 2, Locations: 2},
	}
	if diff := cmp.Diff(wantReport, r.Totals); diff != "" {
		t.Errorf("Report(%d).Totals differ (-want +got):\n%s", confirmation, diff)
	}
}

func TestReport_OneSnapshot(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, second := totalsProject(t, s, "shop")
	before := readReport(t, s, second)

	paused, resume, traced := db.NewTestStorePaused(t, s)
	type result struct {
		d   report.Data
		err error
	}
	done := make(chan result, 1)
	go func() {
		d, err := traced.Report(ctx, second)
		done <- result{d, err}
	}()

	// Report has read the scan row and has the rest of its statements still
	// to run. The write commits a change to the totals, the lines, the
	// events and the latest scan.
	select {
	case <-paused:
	case r := <-done:
		t.Fatalf("Report(%d) = %v before it paused after its first SELECT", second, r.err)
	}
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive},
		bundleOf(t, s, pid, totalsUndec.Fingerprint, totalsUndec.File))
	saveAt(t, s, pid, minute(180), totalsHost)
	resume()

	r := <-done
	if r.err != nil {
		t.Fatalf("Report(%d) error = %v", second, r.err)
	}
	if diff := cmp.Diff(before, r.d); diff != "" {
		t.Errorf("Report(%d) with a write committed after its first read differs from the report before the write (-before +during):\n%s",
			second, diff)
	}
	if after := readReport(t, s, second); cmp.Equal(before, after) {
		t.Errorf("Report(%d) after the write = the report before it, want the write to show", second)
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
	if diff := cmp.Diff(want, d.Remaining.Rows, cmp.FilterPath(func(p cmp.Path) bool {
		return p.Last().String() == ".DecidedAt"
	}, cmp.Ignore())); diff != "" {
		t.Errorf("Report(%d).Remaining.Rows for 37 phone numbers with one decision (-want +got):\n%s", sid, diff)
	}
	if len(d.Remaining.Rows) == 1 && d.Remaining.Rows[0].DecidedAt.IsZero() {
		t.Errorf("Report(%d).Remaining.Rows[0].DecidedAt is zero, want the time of the decision", sid)
	}

	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact, Note: "a different note"}, ids[5])
	d = readReport(t, s, sid)
	got := map[string]int{}
	for _, l := range linesOf(d) {
		if l.Bundle != 0 {
			t.Errorf("Report(%d) customer-data line %+v has bundle %d, want 0", sid, l, l.Bundle)
		}
		got[l.Note] = l.Bundles
	}
	if diff := cmp.Diff(map[string]int{"remove from logs": 36, "a different note": 1}, got); diff != "" {
		t.Errorf("Report(%d) bundles by note after one note changed (-want +got):\n%s", sid, diff)
	}
}

func TestReport_CustomerDataSplitByApplied(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	found := phones("logs/app.log", 4)
	first := saveAt(t, s, pid, minute(0), found...)
	ids := bundlesOf(t, s, pid, found)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, ids...)

	// A run before the second scan redacts the first phone number, which
	// the second scan finds again; a run after it redacts the second.
	before := runAt(t, s, pid, first, apply.StateSucceeded, minute(10), minute(11))
	insertItems(t, s, before, itemOf(t, s, pid, found[0], "", apply.StatusApplied))
	second := saveAt(t, s, pid, minute(20), found...)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater}, ids...)
	after := runAt(t, s, pid, second, apply.StateSucceeded, minute(30), minute(31))
	insertItems(t, s, after, itemOf(t, s, pid, found[1], "", apply.StatusApplied))

	d := readReport(t, s, second)
	got := map[report.Applied]int{}
	for _, l := range linesOf(d) {
		got[l.Applied] += l.Bundles
	}
	want := map[report.Applied]int{report.AppliedBefore: 1, report.AppliedAfter: 1, "": 2}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Report(%d) bundles of one customer-data line by Applied (-want +got):\n%s", second, diff)
	}
	if n := len(linesOf(d)); n != 3 {
		t.Errorf("Report(%d) has %d lines, want 3: one for each applied state", second, n)
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
	// neither time, and no scan can be requested while it waits, so it is
	// after every scan.
	again := runAt(t, s, pid, second, apply.StateQueued, nil, nil)
	insertItems(t, s, again, itemOf(t, s, pid, queued, "db.other.password", apply.StatusApplied))

	d := readReport(t, s, second)
	applies := map[triage.BundleID]report.Applied{}
	for _, l := range linesOf(d) {
		applies[l.Bundle] = l.Applied
	}
	want := map[triage.BundleID]report.Applied{
		pwBundle:                               report.AppliedBefore,
		bundleOf(t, s, pid, fpE, "conf/c.yml"): "",
		bundleOf(t, s, pid, fpG, "conf/d.yml"): report.AppliedAfter,
	}
	if diff := cmp.Diff(want, applies); diff != "" {
		t.Errorf("Report(second) Applied by bundle (-want +got):\n%s", diff)
	}
	wantEntries := report.List[report.Entry]{Total: 1, Rows: []report.Entry{{
		Bundle: pwBundle, Name: "db.main.password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
		StorePath: applied.StorePath, PlaceTotal: 2, Reappeared: true,
		Places: []report.Place{{File: "conf/a.yml", Line: 3, Run: run}, {File: "conf/a.yml", Line: 3, Run: closed}},
	}}}
	if diff := cmp.Diff(wantEntries, d.Externalized); diff != "" {
		t.Errorf("Report(second).Externalized (-want +got):\n%s", diff)
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
	want := report.List[report.Entry]{Total: 1, Rows: []report.Entry{{
		Bundle: pw, Name: "db.main.password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
		StorePath: bao.StorePath(int64(pid), "ext.db.main.password"), PlaceTotal: 1,
		Places: []report.Place{{File: "conf/a.yml", Line: 3, Run: run}},
	}}}
	if diff := cmp.Diff(want, d.Externalized); diff != "" {
		t.Errorf("Report(%d).Externalized after the bundle was deleted (-want +got):\n%s", second, diff)
	}
	var verdict *report.Event
	for i := range d.Events {
		if d.Events[i].Action == audit.ActionVerdict {
			verdict = &d.Events[i]
		}
	}
	if verdict == nil || verdict.Bundles != 1 || verdict.Category != "" || verdict.Kind != "" || verdict.File != "" {
		t.Errorf("Report(%d).Events = %+v, want the verdict event of the deleted bundle for 1 bundle with no category, kind or file",
			second, d.Events)
	}
}

// TestReport_HoldsNoDerivedValue fills every part of the report from rows
// that carry a preview, a fingerprint and a context line, and checks that
// none of the three reaches the report.
func TestReport_HoldsNoDerivedValue(t *testing.T) {
	const (
		preview = "dev-zzpreviewzz****"
		context = "zzcontextlinezz"
	)
	fpSecret := strings.Repeat("ab12", 16)
	fpPhone := strings.Repeat("cd34", 16)
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	secret := fnd(fpSecret, "conf/a.yml", finding.CategoryStorage, finding.KindPassword, 3)
	secret.Preview = preview
	secret.Locations[0].Context, secret.Locations[0].ContextFirstLine = []string{context}, 3
	phone := fnd(fpPhone, "logs/x.log", finding.CategoryCustomerData, finding.KindPhone, 7)
	phone.Preview = preview
	first := saveAt(t, s, pid, minute(0), secret, phone)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"},
		bundleOf(t, s, pid, fpSecret, "conf/a.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, bundleOf(t, s, pid, fpPhone, "logs/x.log"))
	run := runAt(t, s, pid, first, apply.StateSucceeded, minute(10), minute(11))
	insertItems(t, s, run,
		itemOf(t, s, pid, secret, "db.main.password", apply.StatusApplied),
		itemOf(t, s, pid, phone, "", apply.StatusApplied))
	second := saveAt(t, s, pid, minute(20), secret, phone)

	d := readReport(t, s, second)
	if len(linesOf(d)) == 0 || len(d.Externalized.Rows) == 0 || len(d.Redactions) == 0 || len(d.Events) == 0 {
		t.Fatalf("Report(%d) = %+v, want every part filled", second, d)
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal(Report(%d)) error = %v", second, err)
	}
	for name, text := range map[string]string{"%+v": fmt.Sprintf("%+v", d), "json": string(encoded)} {
		for _, leak := range []string{"zzpreviewzz", fpSecret, fpPhone, context} {
			if strings.Contains(text, leak) {
				t.Errorf("Report(%d) as %s contains %q, want no preview, fingerprint or context line", second, name, leak)
			}
		}
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
	places := 0
	for _, e := range got.Externalized.Rows {
		places += e.PlaceTotal
	}
	if got.Externalized.Total != 2 || places != 3 || len(got.Redactions) != 1 || len(linesOf(got)) != 3 || got.EventTotal != 5 {
		t.Errorf("Report(%d) = %d entries in %d places, %d redactions, %d lines, %d event lines, want 2, 3, 1, 3, 5",
			second, got.Externalized.Total, places, len(got.Redactions), len(linesOf(got)), got.EventTotal)
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
	start := fnd(fpH, "bin/start", finding.CategoryStorage, finding.KindPassword, 8)
	start.Shell = true
	sid := saveAt(t, s, pid, minute(0), jsp, runSh, stopSh, yml, jspPhone, start)

	d := readReport(t, s, sid)
	type flags struct {
		Files                     int
		Frontend, Shell, Customer bool
	}
	got := map[string]flags{}
	for _, l := range linesOf(d) {
		got[l.File] = flags{l.Files, l.Frontend, l.Shell, l.Category == finding.CategoryCustomerData}
	}
	want := map[string]flags{
		"web/login.jsp": {1, true, false, false},
		"bin/run.sh":    {2, false, true, false},
		"conf/a.yml":    {1, false, false, false},
		"web/list.jsp":  {1, false, false, true},
		"bin/start":     {1, false, true, false},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Report(%d) lines by first file (-want +got):\n%s", sid, diff)
	}
}

// filesOf returns the first files of lines in their order.
func filesOf(lines []report.Line) []string {
	var files []string
	for _, l := range lines {
		files = append(files, l.File)
	}
	return files
}

func TestReport_Sections(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	pw := func(n int, file string) finding.Finding {
		return fnd(fmt.Sprintf("%064x", 0x5ec000+n), file, finding.CategoryStorage, finding.KindPassword, n)
	}
	var (
		upperJSP    = pw(1, "web/A.JSP")
		js          = pw(2, "web/app.js")
		jsx         = pw(3, "web/app.jsx")
		jspDir      = pw(4, "old.jsp/readme")
		htm         = pw(5, "web/index.htm")
		fpJSP       = pw(6, "web/fp.jsp")
		structure   = pw(7, "conf/structure.yml")
		later       = pw(8, "conf/later.yml")
		named       = pw(9, "conf/named.yml")
		fpPlain     = pw(10, "conf/fp.yml")
		upper       = pw(11, "Zeta.yml")
		mixedPlain  = pw(12, "conf/mixed.yml")
		mixedJSPF   = pw(12, "web/mixed.jspf")
		jspPhone    = fnd(fpC, "web/list.jsp", finding.CategoryCustomerData, finding.KindPhone, 7)
		heldPhone   = fnd(fpD, "logs/held.log", finding.CategoryCustomerData, finding.KindPhone, 2)
		fpPhone     = fnd(fpH, "logs/fp.log", finding.CategoryCustomerData, finding.KindPhone, 4)
		allFindings = []finding.Finding{
			upperJSP, js, jsx, jspDir, htm, fpJSP, structure, later, named, fpPlain, upper, mixedPlain, mixedJSPF,
			jspPhone, heldPhone, fpPhone,
		}
	)
	sid := saveAt(t, s, pid, minute(0), allFindings...)
	b := func(f finding.Finding) triage.BundleID { return bundleOf(t, s, pid, f.Fingerprint, f.File) }
	// A frontend finding starts in a bundle of its own; moving it puts a
	// frontend member into the bundle of the plain file.
	if _, err := s.MoveFinding(t.Context(), pid, findingID(t, s, pid, mixedJSPF.Fingerprint, mixedJSPF.File),
		b(mixedPlain), tester); err != nil {
		t.Fatalf("MoveFinding(%s) error = %v", mixedJSPF.File, err)
	}
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, b(fpJSP), b(fpPlain), b(fpPhone))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldStructure}, b(structure), b(heldPhone))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater}, b(later))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, b(named))

	d := readReport(t, s, sid)
	for _, tc := range []struct {
		name string
		got  report.List[report.Line]
		want []string
	}{
		// Files sort by byte value, so an upper-case name comes first.
		{"Structure", d.Structure, []string{
			"conf/mixed.yml", "conf/structure.yml", "logs/held.log", "web/A.JSP", "web/app.js", "web/index.htm",
		}},
		{"Remaining", d.Remaining, []string{
			"Zeta.yml", "conf/later.yml", "conf/named.yml", "old.jsp/readme", "web/app.jsx", "web/list.jsp",
		}},
		{"FalsePositives", d.FalsePositives, []string{"conf/fp.yml", "logs/fp.log", "web/fp.jsp"}},
	} {
		if diff := cmp.Diff(tc.want, filesOf(tc.got.Rows)); diff != "" {
			t.Errorf("Report(%d).%s first files (-want +got):\n%s", sid, tc.name, diff)
		}
		if tc.got.Total != len(tc.want) {
			t.Errorf("Report(%d).%s.Total = %d, want %d", sid, tc.name, tc.got.Total, len(tc.want))
		}
	}
	for _, l := range linesOf(d) {
		if want := l.Bundle != 0 && (triage.Frontend(l.File) || l.File == "conf/mixed.yml"); l.Frontend != want {
			t.Errorf("Report(%d) line of %s Frontend = %t, want %t", sid, l.File, l.Frontend, want)
		}
	}
}

func TestReport_SectionsCut(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	const more = 5
	var found []finding.Finding
	for i := range report.MaxRows + more {
		// Stored in the reverse of the order the report lists them in.
		file := fmt.Sprintf("conf/f%04d.yml", report.MaxRows+more-i)
		found = append(found, fnd(fmt.Sprintf("%064x", 0xc07000+i), file, finding.CategoryStorage, finding.KindPassword, 1))
	}
	jsp := fnd(fpA, "web/login.jsp", finding.CategoryStorage, finding.KindPassword, 3)
	sid := saveAt(t, s, pid, minute(0), append(found, jsp, totalsPhone)...)

	d := readReport(t, s, sid)
	if len(d.Remaining.Rows) != report.MaxRows || d.Remaining.Total != report.MaxRows+more+1 {
		t.Fatalf("Report(%d).Remaining = %d rows of %d, want %d of %d", sid, len(d.Remaining.Rows), d.Remaining.Total,
			report.MaxRows, report.MaxRows+more+1)
	}
	if first, last := d.Remaining.Rows[0].File, d.Remaining.Rows[report.MaxRows-1].File; first != "conf/f0001.yml" ||
		last != fmt.Sprintf("conf/f%04d.yml", report.MaxRows) {
		t.Errorf("Report(%d).Remaining rows run from %s to %s, want the first %d files in order", sid, first, last, report.MaxRows)
	}
	if len(d.Structure.Rows) != 1 || d.Structure.Total != 1 || d.FalsePositives.Total != 0 {
		t.Errorf("Report(%d) Structure = %d rows of %d, FalsePositives.Total = %d, want 1 of 1 and 0",
			sid, len(d.Structure.Rows), d.Structure.Total, d.FalsePositives.Total)
	}
	if got := d.Totals.Found.Bundles; got != report.MaxRows+more+2 {
		t.Errorf("Report(%d).Totals.Found.Bundles = %d, want %d", sid, got, report.MaxRows+more+2)
	}
}

func TestReport_Externalized(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	gone := fnd(fpH, "conf/gone.yml", finding.CategoryPartnerSession, finding.KindToken, 9)
	later := totalsHold
	first := saveAt(t, s, pid, minute(0), totalsPwA, gone, later)
	run := runAt(t, s, pid, first, apply.StateSucceeded, minute(10), minute(11))
	place := func(f finding.Finding, name, file string, line int, mod func(*apply.Item)) apply.Item {
		it := itemOf(t, s, pid, f, "db.main.password", apply.StatusApplied)
		it.Name, it.File, it.Line, it.StartCol = name, file, line, line+100
		if mod != nil {
			mod(&it)
		}
		return it
	}
	storePath := bao.StorePath(int64(pid), "ext.db.main.password")
	insertItems(t, s, run,
		place(totalsPwA, "ext.db.main.password", "b.xml", 31, nil),
		place(totalsPwA, "EXT_DB_MAIN_PASSWORD", "deploy.sh", 3, nil),
		place(totalsPwA, "ext.db.main.password", "a.yml", 14, nil),
		place(totalsPwA, "ext.db.main.password", "k/key.pem", 0, func(i *apply.Item) { i.Action = apply.ActionRemoveFile }),
		// The first place by file has another kind; most places say password.
		place(totalsPwA, "ext.db.main.password", "Conn.java", 8, func(i *apply.Item) {
			i.CodeChange, i.Category, i.Kind = true, finding.CategoryOther, finding.KindOther
		}),
		place(gone, "ext.api.main.token", "conf/gone.yml", 9, nil),
		place(later, "ext.db.other.password", "conf/d.yml", 6, nil))
	second := saveAt(t, s, pid, minute(20), totalsPwA, later)
	// The value the scan found again was applied once more after the scan.
	after := runAt(t, s, pid, second, apply.StateSucceeded, minute(30), minute(31))
	insertItems(t, s, after, place(later, "ext.db.other.password", "conf/d.yml", 6, nil))

	pw, token, other := bundleOf(t, s, pid, fpA, "conf/a.yml"), bundleOf(t, s, pid, fpH, "conf/gone.yml"),
		bundleOf(t, s, pid, fpG, "conf/d.yml")
	want := report.List[report.Entry]{Total: 4, Rows: []report.Entry{
		{
			Bundle: token, Name: "ext.api.main.token", Category: finding.CategoryPartnerSession, Kind: finding.KindToken,
			StorePath: storePath, PlaceTotal: 1,
			Places: []report.Place{{File: "conf/gone.yml", Line: 9, Run: run}},
		},
		{
			Bundle: pw, Name: "ext.db.main.password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
			StorePath: storePath, PlaceTotal: 4, Reappeared: true, CodeChange: true,
			Places: []report.Place{
				{File: "Conn.java", Line: 8, Run: run},
				{File: "a.yml", Line: 14, Run: run},
				{File: "b.xml", Line: 31, Run: run},
				{File: "k/key.pem", Removed: true, Run: run},
			},
		},
		// The shell form of a name follows its config form.
		{
			Bundle: pw, Name: "EXT_DB_MAIN_PASSWORD", Category: finding.CategoryStorage, Kind: finding.KindPassword,
			StorePath: storePath, PlaceTotal: 1, Reappeared: true,
			Places: []report.Place{{File: "deploy.sh", Line: 3, Run: run}},
		},
		{
			Bundle: other, Name: "ext.db.other.password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
			StorePath: storePath, PlaceTotal: 1,
			Places: []report.Place{{File: "conf/d.yml", Line: 6, Run: run}},
		},
	}}
	if diff := cmp.Diff(want, readReport(t, s, second).Externalized); diff != "" {
		t.Errorf("Report(%d).Externalized (-want +got):\n%s", second, diff)
	}
}

func TestReport_ExternalizedCut(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	first := saveAt(t, s, pid, minute(0), totalsPwA)
	run := runAt(t, s, pid, first, apply.StateSucceeded, minute(10), minute(11))
	const moreEntries, morePlaces = 2, 3
	var items []apply.Item
	for i := range report.MaxRows + moreEntries {
		it := itemOf(t, s, pid, totalsPwA, "db.main.password", apply.StatusApplied)
		// Stored in the reverse of the order the report lists them in.
		it.Name, it.StartCol = fmt.Sprintf("ext.db.n%04d.password", report.MaxRows+moreEntries-i), i+1
		items = append(items, it)
	}
	for i := range report.MaxPlaces + morePlaces {
		it := itemOf(t, s, pid, totalsPwA, "db.main.password", apply.StatusApplied)
		it.Name, it.File, it.StartCol = "ext.db.n0001.password", fmt.Sprintf("conf/f%04d.yml", report.MaxPlaces+morePlaces-i), 1000+i
		it.CodeChange = i == 0
		items = append(items, it)
	}
	insertItems(t, s, run, items...)
	second := saveAt(t, s, pid, minute(20), totalsHost)

	got := readReport(t, s, second).Externalized
	if len(got.Rows) != report.MaxRows || got.Total != report.MaxRows+moreEntries {
		t.Fatalf("Report(%d).Externalized = %d rows of %d, want %d of %d", second, len(got.Rows), got.Total,
			report.MaxRows, report.MaxRows+moreEntries)
	}
	if first, last := got.Rows[0].Name, got.Rows[report.MaxRows-1].Name; first != "ext.db.n0001.password" ||
		last != fmt.Sprintf("ext.db.n%04d.password", report.MaxRows) {
		t.Errorf("Report(%d).Externalized rows run from %s to %s, want the first %d names in order", second, first, last, report.MaxRows)
	}
	// The entry holds the place of the first loop too, in conf/a.yml.
	e := got.Rows[0]
	if len(e.Places) != report.MaxPlaces || e.PlaceTotal != report.MaxPlaces+morePlaces+1 {
		t.Fatalf("Report(%d) first entry = %d places of %d, want %d of %d", second, len(e.Places), e.PlaceTotal,
			report.MaxPlaces, report.MaxPlaces+morePlaces+1)
	}
	if first, last := e.Places[0].File, e.Places[report.MaxPlaces-1].File; first != "conf/a.yml" ||
		last != fmt.Sprintf("conf/f%04d.yml", report.MaxPlaces-1) {
		t.Errorf("Report(%d) first entry places run from %s to %s, want the first %d files in order", second, first, last, report.MaxPlaces)
	}
	// The place in code is one the entry does not list.
	if !e.CodeChange {
		t.Errorf("Report(%d) first entry CodeChange = false, want true for a place in code past the cut", second)
	}
}
