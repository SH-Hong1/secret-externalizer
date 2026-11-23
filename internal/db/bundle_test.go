package db_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

var fpD = strings.Repeat("d", 64)

// tester is the actor of every decision in these tests.
var tester = audit.Actor{Subject: "fake-subject", Name: "Fake Tester"}

// fnd returns a finding of the value fp in file with one location at line.
func fnd(fp, file string, c finding.Category, k finding.Kind, line int) finding.Finding {
	f := finding.Finding{
		Fingerprint: fp, File: file, Category: c, Kind: k, Preview: "****",
		HasOriginal: c != finding.CategoryCustomerData,
		Locations: []finding.Location{{
			StartLine: line, StartCol: 1, EndLine: line, EndCol: 4, FileHash: fileHash,
			Detections: []finding.Detection{{Tool: "opengrep", Rule: "fake-rule"}},
		}},
	}
	if c == finding.CategoryCustomerData {
		f.Preview = "010-****-****"
	}
	return f
}

func password(file string) finding.Finding {
	return fnd(fpA, file, finding.CategoryStorage, finding.KindPassword, 3)
}

func host(file string) finding.Finding {
	return fnd(fpB, file, finding.CategoryStorage, finding.KindHost, 2)
}

func phone(file string) finding.Finding {
	return fnd(fpC, file, finding.CategoryCustomerData, finding.KindPhone, 7)
}

// mixed is the scan result of the bundling rules: one password in three
// files, one of them a JSP, one host in two files, and one phone number in
// two logs.
func mixed() []finding.Finding {
	return []finding.Finding{
		password("conf/a.yml"), password("conf/b.xml"), password("web/c.jsp"),
		host("conf/a.yml"), host("conf/b.xml"),
		phone("logs/x.log"), phone("logs/y.log"),
	}
}

// save stores findings as the result of a new scan of the project.
func save(t *testing.T, s *db.Store, pid project.ID, findings ...finding.Finding) scan.ID {
	t.Helper()
	id, j := claimedScan(t, s, pid)
	if err := s.SaveResult(t.Context(), j.ID, "worker-a", id, result(findings...)); err != nil {
		t.Fatalf("SaveResult(scan %d) error = %v", id, err)
	}
	return id
}

func bundleOf(t *testing.T, s *db.Store, pid project.ID, fp, file string) triage.BundleID {
	t.Helper()
	return bundleOfKey(t, s, pid, fp, file, "")
}

func bundleOfKey(t *testing.T, s *db.Store, pid project.ID, fp, file, key string) triage.BundleID {
	t.Helper()
	id, err := s.BundleOfKey(t.Context(), pid, fp, file, key)
	if err != nil {
		t.Fatalf("BundleOfKey(%d, %.8s, %q, %q) error = %v", pid, fp, file, key, err)
	}
	return id
}

func findingID(t *testing.T, s *db.Store, pid project.ID, fp, file string) finding.ID {
	t.Helper()
	return findingIDOfKey(t, s, pid, fp, file, "")
}

func findingIDOfKey(t *testing.T, s *db.Store, pid project.ID, fp, file, key string) finding.ID {
	t.Helper()
	row, err := s.FindingRowOfKey(t.Context(), pid, fp, file, key)
	if err != nil {
		t.Fatalf("FindingRowOfKey(%d, %.8s, %q, %q) error = %v", pid, fp, file, key, err)
	}
	return finding.ID(row.ID)
}

// keyed returns f with the configuration key key.
func keyed(f finding.Finding, key string) finding.Finding {
	f.Key = key
	return f
}

func decide(t *testing.T, s *db.Store, pid project.ID, d triage.Decision, ids ...triage.BundleID) {
	t.Helper()
	if err := s.Decide(t.Context(), pid, ids, d, tester); err != nil {
		t.Fatalf("Decide(%d, %v, %+v) error = %v", pid, ids, d, err)
	}
}

func readBundle(t *testing.T, s *db.Store, pid project.ID, sid scan.ID, id triage.BundleID) triage.Bundle {
	t.Helper()
	b, err := s.Bundle(t.Context(), pid, sid, id)
	if err != nil {
		t.Fatalf("Bundle(%d, %d, %d) error = %v", pid, sid, id, err)
	}
	return b
}

func memberFiles(b triage.Bundle) []string {
	var out []string
	for _, m := range b.Members {
		out = append(out, m.File)
	}
	return out
}

// assertMixedBundles checks the bundles of the findings of mixed.
func assertMixedBundles(t *testing.T, s *db.Store, pid project.ID) {
	t.Helper()
	pwA := bundleOf(t, s, pid, fpA, "conf/a.yml")
	pwB := bundleOf(t, s, pid, fpA, "conf/b.xml")
	pwC := bundleOf(t, s, pid, fpA, "web/c.jsp")
	if pwA != pwB || pwA == pwC {
		t.Errorf("password bundles a.yml, b.xml, c.jsp = %d, %d, %d, want a.yml and b.xml shared and c.jsp apart", pwA, pwB, pwC)
	}
	if hA, hB := bundleOf(t, s, pid, fpB, "conf/a.yml"), bundleOf(t, s, pid, fpB, "conf/b.xml"); hA == hB {
		t.Errorf("host bundles a.yml, b.xml = %d, %d, want one each", hA, hB)
	}
	if pX, pY := bundleOf(t, s, pid, fpC, "logs/x.log"), bundleOf(t, s, pid, fpC, "logs/y.log"); pX == pY {
		t.Errorf("phone bundles x.log, y.log = %d, %d, want one each", pX, pY)
	}
	if n, err := s.Count(t.Context(), "bundle"); err != nil || n != 6 {
		t.Errorf("rows in bundle = %d, %v, want 6", n, err)
	}
}

func TestSaveResultAssignsBundles(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	save(t, s, pid, mixed()...)
	assertMixedBundles(t, s, pid)
}

func TestRescanKeepsDecisions(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	save(t, s, pid, mixed()...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")
	want := triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password", Note: "move to vault"}
	decide(t, s, pid, want, shared)

	second := save(t, s, pid, append(mixed(), password("conf/d.properties"))...)
	b := readBundle(t, s, pid, second, shared)
	if b.Decision != want {
		t.Errorf("Bundle(%d).Decision after a rescan = %+v, want %+v", shared, b.Decision, want)
	}
	if got := bundleOf(t, s, pid, fpA, "conf/d.properties"); got != shared {
		t.Errorf("bundle of the password in a new file = %d, want the shared bundle %d", got, shared)
	}

	moved, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "conf/b.xml"), 0, tester)
	if err != nil {
		t.Fatalf("MoveFinding(b.xml, new) error = %v", err)
	}
	third := save(t, s, pid, append(mixed(), password("conf/d.properties"))...)
	if got := bundleOf(t, s, pid, fpA, "conf/b.xml"); got != moved {
		t.Errorf("bundle of b.xml after a rescan = %d, want the bundle it was moved to %d", got, moved)
	}
	b = readBundle(t, s, pid, third, shared)
	if diff := cmp.Diff([]string{"conf/a.yml", "conf/d.properties"}, memberFiles(b)); diff != "" {
		t.Errorf("members of the shared bundle after the rescan (-want +got):\n%s", diff)
	}
}

func TestDecide(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	other := newProject(t, s, "other")
	sid := save(t, s, pid, mixed()...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")
	hostA := bundleOf(t, s, pid, fpB, "conf/a.yml")
	hostB := bundleOf(t, s, pid, fpB, "conf/b.xml")
	phoneX := bundleOf(t, s, pid, fpC, "logs/x.log")

	t.Run("stores and records", func(t *testing.T) {
		d := triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm, Note: "ask the owner"}
		decide(t, s, pid, d, hostA)
		b := readBundle(t, s, pid, sid, hostA)
		if b.Decision != d || b.DecidedBy != tester.Subject || b.DecidedAt.IsZero() {
			t.Errorf("Bundle(%d) = %+v, want decision %+v by %q with a time", hostA, b, d, tester.Subject)
		}
		e, err := s.LastAuditEvent(ctx)
		if err != nil {
			t.Fatalf("LastAuditEvent() error = %v", err)
		}
		wantDetail := map[string]string{"verdict": "hold", "reason": "needs-confirmation", "note": "ask the owner"}
		if e.Action != audit.ActionVerdict || e.Actor != tester || e.ProjectID != pid || e.BundleID != hostA ||
			e.FindingID != 0 || !cmp.Equal(e.Detail, wantDetail) {
			t.Errorf("audit event after Decide = %+v, want a verdict on bundle %d with detail %v", e, hostA, wantDetail)
		}
		if n, err := s.Count(ctx, "audit_event"); err != nil || n != 1 {
			t.Errorf("rows in audit_event = %d, %v, want 1", n, err)
		}
	})

	assertRefused := func(t *testing.T, err, want error, ids ...triage.BundleID) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Errorf("Decide() error = %v, want %v", err, want)
		}
		for _, id := range ids {
			if b := readBundle(t, s, pid, sid, id); b.Decision != (triage.Decision{}) {
				t.Errorf("Bundle(%d).Decision after a refused Decide = %+v, want none", id, b.Decision)
			}
		}
		if n, err := s.Count(ctx, "audit_event"); err != nil || n != 1 {
			t.Errorf("rows in audit_event after a refused Decide = %d, %v, want 1", n, err)
		}
	}
	fp := triage.Decision{Verdict: triage.VerdictFalsePositive}

	t.Run("other project", func(t *testing.T) {
		err := s.Decide(ctx, other, []triage.BundleID{shared}, fp, tester)
		assertRefused(t, err, db.ErrNotFound, shared)
	})
	t.Run("one id missing", func(t *testing.T) {
		err := s.Decide(ctx, pid, []triage.BundleID{shared, hostB, 999999}, fp, tester)
		assertRefused(t, err, db.ErrNotFound, shared, hostB)
	})
	t.Run("not allowed", func(t *testing.T) {
		err := s.Decide(ctx, pid, []triage.BundleID{phoneX},
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "customer.phone"}, tester)
		assertRefused(t, err, triage.ErrNotAllowed, phoneX)
	})
	t.Run("duplicate name", func(t *testing.T) {
		name := triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.host"}
		decide(t, s, pid, name, hostB)
		err := s.Decide(ctx, pid, []triage.BundleID{shared}, name, tester)
		if !errors.Is(err, db.ErrDuplicate) {
			t.Errorf("Decide() with a name another bundle uses error = %v, want ErrDuplicate", err)
		}
		if b := readBundle(t, s, pid, sid, shared); b.Decision != (triage.Decision{}) {
			t.Errorf("Bundle(%d).Decision after a duplicate name = %+v, want none", shared, b.Decision)
		}
	})
	t.Run("clear", func(t *testing.T) {
		decide(t, s, pid, triage.Decision{}, hostB)
		b := readBundle(t, s, pid, sid, hostB)
		if b.Decision != (triage.Decision{}) || b.DecidedBy != "" || !b.DecidedAt.IsZero() {
			t.Errorf("Bundle(%d) after clearing = %+v, want no decision", hostB, b)
		}
		// The name is free again.
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.host"}, hostA)
	})
}

func TestMoveFinding(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	other := newProject(t, s, "other")
	sid := save(t, s, pid, append(mixed(), fnd(fpD, "conf/a.yml", finding.CategoryStorage, finding.KindToken, 9))...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")
	hostA := bundleOf(t, s, pid, fpB, "conf/a.yml")
	hostB := bundleOf(t, s, pid, fpB, "conf/b.xml")
	token := bundleOf(t, s, pid, fpD, "conf/a.yml")

	t.Run("to a new bundle", func(t *testing.T) {
		id := findingID(t, s, pid, fpA, "conf/b.xml")
		got, err := s.MoveFinding(ctx, pid, id, 0, tester)
		if err != nil {
			t.Fatalf("MoveFinding(%d, new) error = %v", id, err)
		}
		if got == 0 || got == shared {
			t.Fatalf("MoveFinding(%d, new) = %d, want a new bundle", id, got)
		}
		if b := readBundle(t, s, pid, sid, got); !cmp.Equal(memberFiles(b), []string{"conf/b.xml"}) || b.Fingerprint != fpA {
			t.Errorf("Bundle(%d) after the move = %+v, want b.xml alone", got, b)
		}
		e, err := s.LastAuditEvent(ctx)
		if err != nil {
			t.Fatalf("LastAuditEvent() error = %v", err)
		}
		if e.Action != audit.ActionMove || e.BundleID != shared || e.FindingID != id ||
			!cmp.Equal(e.Detail, map[string]string{"to": fmt.Sprint(got)}) {
			t.Errorf("audit event after MoveFinding = %+v, want a move of %d from %d to %d", e, id, shared, got)
		}
	})
	otherHost := func() triage.BundleID {
		save(t, s, other, host("conf/a.yml"))
		return bundleOf(t, s, other, fpB, "conf/a.yml")
	}()

	// refused moves the finding of pid with fp in file to the bundle to as
	// project in and checks that it fails with want and changes nothing.
	refused := func(t *testing.T, in project.ID, fp, file string, to triage.BundleID, want error) {
		t.Helper()
		before := bundleOf(t, s, pid, fp, file)
		events, err := s.Count(ctx, "audit_event")
		if err != nil {
			t.Fatalf("counting audit_event: %v", err)
		}
		bundles, err := s.Count(ctx, "bundle")
		if err != nil {
			t.Fatalf("counting bundle: %v", err)
		}
		_, err = s.MoveFinding(ctx, in, findingID(t, s, pid, fp, file), to, tester)
		if !errors.Is(err, want) {
			t.Errorf("MoveFinding(%.8s in %s, %d) error = %v, want %v", fp, file, to, err, want)
		}
		if got := bundleOf(t, s, pid, fp, file); got != before {
			t.Errorf("bundle of %s after a refused move = %d, want %d", file, got, before)
		}
		if n, err := s.Count(ctx, "audit_event"); err != nil || n != events {
			t.Errorf("rows in audit_event after a refused move = %d, %v, want %d", n, err, events)
		}
		if n, err := s.Count(ctx, "bundle"); err != nil || n != bundles {
			t.Errorf("rows in bundle after a refused move = %d, %v, want %d", n, err, bundles)
		}
	}

	t.Run("different fingerprint", func(t *testing.T) {
		refused(t, pid, fpB, "conf/a.yml", token, triage.ErrNotAllowed)
	})
	t.Run("other project", func(t *testing.T) {
		refused(t, other, fpB, "conf/a.yml", hostB, db.ErrNotFound)
	})
	t.Run("bundle of another project", func(t *testing.T) {
		refused(t, pid, fpB, "conf/a.yml", otherHost, db.ErrNotFound)
	})
	t.Run("only member to a new bundle", func(t *testing.T) {
		d := triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.host"}
		decide(t, s, pid, d, hostA)
		events, err := s.Count(ctx, "audit_event")
		if err != nil {
			t.Fatalf("counting audit_event: %v", err)
		}
		got, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpB, "conf/a.yml"), 0, tester)
		if err != nil || got != hostA {
			t.Fatalf("MoveFinding(only member of %d, new) = %d, %v, want %d unchanged", hostA, got, err, hostA)
		}
		if b := readBundle(t, s, pid, sid, hostA); b.Decision != d {
			t.Errorf("Bundle(%d).Decision after a move that changes nothing = %+v, want %+v", hostA, b.Decision, d)
		}
		if n, err := s.Count(ctx, "audit_event"); err != nil || n != events {
			t.Errorf("rows in audit_event after a move that changes nothing = %d, %v, want %d", n, err, events)
		}
	})
	t.Run("last member", func(t *testing.T) {
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, hostB)
		bundles, err := s.Count(ctx, "bundle")
		if err != nil {
			t.Fatalf("counting bundle: %v", err)
		}
		got, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpB, "conf/a.yml"), hostB, tester)
		if err != nil || got != hostB {
			t.Fatalf("MoveFinding(host a.yml, %d) = %d, %v, want %d", hostB, got, err, hostB)
		}
		if n, err := s.Count(ctx, "bundle"); err != nil || n != bundles-1 {
			t.Errorf("rows in bundle after the last member left = %d, %v, want %d", n, err, bundles-1)
		}
		if _, err := s.Bundle(ctx, pid, sid, hostA); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("Bundle(%d) after its last member left error = %v, want ErrNotFound", hostA, err)
		}
		b := readBundle(t, s, pid, sid, hostB)
		if !cmp.Equal(memberFiles(b), []string{"conf/a.yml", "conf/b.xml"}) || b.Decision.Verdict != triage.VerdictFalsePositive {
			t.Errorf("Bundle(%d) after the move = %+v, want both hosts as false positives", hostB, b)
		}
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.host"}, token)
	})
	t.Run("frontend into externalized", func(t *testing.T) {
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, shared)
		refused(t, pid, fpA, "web/c.jsp", shared, triage.ErrNotAllowed)
	})
	t.Run("customer data into customer data of the same value", func(t *testing.T) {
		refused(t, pid, fpC, "logs/x.log", bundleOf(t, s, pid, fpC, "logs/y.log"), triage.ErrNotAllowed)
	})
}

func TestDecideUsesLatestScan(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	save(t, s, pid, mixed()...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")
	hostB := bundleOf(t, s, pid, fpB, "conf/b.xml")
	if _, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "web/c.jsp"), shared, tester); err != nil {
		t.Fatalf("MoveFinding(c.jsp, shared) error = %v", err)
	}

	// The JSP value and the host in b.xml are gone from the code.
	var rest []finding.Finding
	for _, f := range mixed() {
		if f.File != "web/c.jsp" && (f.Fingerprint != fpB || f.File != "conf/b.xml") {
			rest = append(rest, f)
		}
	}
	latest := save(t, s, pid, rest...)

	b := readBundle(t, s, pid, latest, shared)
	if !cmp.Equal(memberFiles(b), []string{"conf/a.yml", "conf/b.xml"}) || b.Allowed()[0] != triage.VerdictExternalize {
		t.Fatalf("Bundle(%d) in the latest scan = %+v allowing %v, want a.yml and b.xml allowing externalize", shared, b, b.Allowed())
	}
	d := triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}
	if err := s.Decide(ctx, pid, []triage.BundleID{shared}, d, tester); err != nil {
		t.Errorf("Decide(externalize) on a bundle whose JSP member is gone error = %v, want nil", err)
	}
	if err := s.Decide(ctx, pid, []triage.BundleID{hostB}, triage.Decision{Verdict: triage.VerdictFalsePositive}, tester); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Decide() on a bundle the latest scan did not find error = %v, want ErrNotFound", err)
	}
}

func TestEntries(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	var findings []finding.Finding
	for i := range 120 {
		findings = append(findings, fnd(fmt.Sprintf("%064x", i+1), "conf/app.yml", finding.CategoryStorage, finding.KindPassword, i+1))
	}
	for i := range 37 {
		findings = append(findings, fnd(fmt.Sprintf("%064x", 1000+i), "logs/app.log", finding.CategoryCustomerData, finding.KindPhone, i+1))
	}
	sid := save(t, s, pid, findings...)
	storage := triage.Filter{Category: finding.CategoryStorage, Status: triage.StatusAll}

	entries := func(t *testing.T, f triage.Filter) triage.Page {
		t.Helper()
		p, err := s.Entries(ctx, pid, sid, f)
		if err != nil {
			t.Fatalf("Entries(%+v) error = %v", f, err)
		}
		return p
	}

	t.Run("pages", func(t *testing.T) {
		f := storage
		f.Page = 3
		p := entries(t, f)
		if len(p.Entries) != 20 || p.Pages != 3 || p.Page != 3 || p.Total != 120 {
			t.Errorf("Entries(page 3) = %d entries, page %d of %d, total %d, want 20, 3 of 3, 120", len(p.Entries), p.Page, p.Pages, p.Total)
		}
		if p.Entries[0].FirstLine != 101 || p.Entries[0].Bundle == 0 || p.Entries[0].Count != 1 {
			t.Errorf("Entries(page 3)[0] = %+v, want the bundle at line 101", p.Entries[0])
		}
		f.Page = 9
		if p := entries(t, f); p.Page != 3 || len(p.Entries) != 20 {
			t.Errorf("Entries(page 9) = page %d with %d entries, want page 3 with 20", p.Page, len(p.Entries))
		}
		f.Page = 0
		if p := entries(t, f); p.Page != 1 || len(p.Entries) != triage.PageSize {
			t.Errorf("Entries(page 0) = page %d with %d entries, want page 1 with %d", p.Page, len(p.Entries), triage.PageSize)
		}
	})

	customer := triage.Filter{Category: finding.CategoryCustomerData, Status: triage.StatusAll, Page: 1}
	t.Run("customer data is one line per file and kind", func(t *testing.T) {
		p := entries(t, customer)
		want := []triage.Entry{{
			Group:    triage.GroupKey{File: "logs/app.log", Kind: finding.KindPhone},
			Category: finding.CategoryCustomerData, Kind: finding.KindPhone, Preview: "010-****-****",
			File: "logs/app.log", FirstLine: 1, Count: 37, Bundles: 37,
		}}
		if diff := cmp.Diff(want, p.Entries); diff != "" {
			t.Errorf("Entries(customer data) (-want +got):\n%s", diff)
		}
	})

	t.Run("a verdict splits the line", func(t *testing.T) {
		g, err := s.CustomerGroup(ctx, pid, sid, triage.GroupKey{File: "logs/app.log", Kind: finding.KindPhone})
		if err != nil {
			t.Fatalf("CustomerGroup() error = %v", err)
		}
		if len(g.Bundles) != 37 || g.Count != 37 || len(g.Lines) != 37 || g.Lines[0] != 1 || g.Lines[36] != 37 {
			t.Fatalf("CustomerGroup() = %+v, want 37 bundles on lines 1 to 37", g)
		}
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, g.Bundles[:5]...)
		p := entries(t, customer)
		var counts []int
		for _, e := range p.Entries {
			counts = append(counts, e.Count)
		}
		// The false positives are the bundles with the lowest IDs, on lines 1
		// to 5, so their line comes first.
		if diff := cmp.Diff([]int{5, 32}, counts); diff != "" {
			t.Errorf("counts of the customer data lines after 5 false positives (-want +got):\n%s", diff)
		}
		if p.Counts[""] != 152 || p.Counts[triage.VerdictFalsePositive] != 5 {
			t.Errorf("Entries().Counts = %v, want 152 undecided and 5 false positives", p.Counts)
		}
	})

	t.Run("filters", func(t *testing.T) {
		p := entries(t, triage.Filter{Status: string(triage.VerdictFalsePositive), Page: 1})
		if len(p.Entries) != 1 || p.Entries[0].Group.Verdict != triage.VerdictFalsePositive || p.Entries[0].Count != 5 {
			t.Errorf("Entries(false positives) = %+v, want the one line of 5", p.Entries)
		}
		p = entries(t, triage.Filter{Status: triage.StatusUndecided, Page: 1})
		if p.Total != 121 {
			t.Errorf("Entries(undecided).Total = %d, want 121", p.Total)
		}
		p = entries(t, triage.Filter{Category: finding.CategorySSH, Status: triage.StatusAll, Page: 1})
		if p.Total != 0 || p.Pages != 1 || p.Page != 1 {
			t.Errorf("Entries(ssh) = total %d, page %d of %d, want 0, 1 of 1", p.Total, p.Page, p.Pages)
		}
	})

	t.Run("values gone from the scan are left out", func(t *testing.T) {
		next := save(t, s, pid, findings[:3]...)
		p, err := s.Entries(ctx, pid, next, triage.Filter{Status: triage.StatusAll, Page: 1})
		if err != nil {
			t.Fatalf("Entries(next scan) error = %v", err)
		}
		if p.Total != 3 || p.Counts[""] != 3 {
			t.Errorf("Entries(next scan) = total %d, counts %v, want the 3 values it found", p.Total, p.Counts)
		}
		gone := bundleOf(t, s, pid, findings[5].Fingerprint, "conf/app.yml")
		if _, err := s.Bundle(ctx, pid, next, gone); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("Bundle(%d) in a scan without it error = %v, want ErrNotFound", gone, err)
		}
	})

	t.Run("unknown status", func(t *testing.T) {
		if _, err := s.Entries(ctx, pid, sid, triage.Filter{Status: "maybe"}); err == nil {
			t.Error("Entries(status maybe) error = nil, want an error")
		}
	})
}

func TestVerdictCounts(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	var findings []finding.Finding
	for i := range 4 {
		findings = append(findings, fnd(fmt.Sprintf("%064x", i+1), "conf/app.yml", finding.CategoryStorage, finding.KindPassword, i+1))
	}
	for i := range 3 {
		findings = append(findings, fnd(fmt.Sprintf("%064x", 1000+i), "logs/app.log", finding.CategoryCustomerData, finding.KindPhone, i+1))
	}
	first := save(t, s, pid, findings...)
	decide(t, s, pid, externalizeDB, bundleOf(t, s, pid, findings[0].Fingerprint, "conf/app.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, pid, findings[1].Fingerprint, "conf/app.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, pid, findings[5].Fingerprint, "logs/app.log"))
	// The second scan finds only the first two values, so the others do not count.
	second := save(t, s, pid, findings[:2]...)

	for _, tt := range []struct {
		name string
		sid  scan.ID
		want map[triage.Verdict]int
	}{
		{"first scan", first, map[triage.Verdict]int{
			"": 4, triage.VerdictExternalize: 1, triage.VerdictRedact: 0, triage.VerdictFalsePositive: 2, triage.VerdictHold: 0,
		}},
		{"second scan", second, map[triage.Verdict]int{
			"": 0, triage.VerdictExternalize: 1, triage.VerdictRedact: 0, triage.VerdictFalsePositive: 1, triage.VerdictHold: 0,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.VerdictCounts(ctx, pid, tt.sid)
			if err != nil {
				t.Fatalf("VerdictCounts(%d, %d) error = %v", pid, tt.sid, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("VerdictCounts(%d, %d) (-want +got):\n%s", pid, tt.sid, diff)
			}
			p, err := s.Entries(ctx, pid, tt.sid, triage.Filter{Status: triage.StatusAll, Page: 1})
			if err != nil {
				t.Fatalf("Entries(%d, %d) error = %v", pid, tt.sid, err)
			}
			if diff := cmp.Diff(p.Counts, got); diff != "" {
				t.Errorf("VerdictCounts differs from Entries().Counts (-entries +counts):\n%s", diff)
			}
		})
	}
}

func TestBundle(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	sid := save(t, s, pid, mixed()...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")
	b := readBundle(t, s, pid, sid, shared)
	first := findingID(t, s, pid, fpA, "conf/a.yml")
	want := triage.Bundle{
		ID: shared, ProjectID: pid, Fingerprint: fpA, Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "****",
		Members: []triage.Member{
			{FindingID: first, File: "conf/a.yml", HasOriginal: true, Locations: password("conf/a.yml").Locations},
			{FindingID: findingID(t, s, pid, fpA, "conf/b.xml"), File: "conf/b.xml", HasOriginal: true, Locations: password("conf/b.xml").Locations},
		},
	}
	if diff := cmp.Diff(want, b); diff != "" {
		t.Errorf("Bundle(%d) (-want +got):\n%s", shared, diff)
	}
	if _, err := s.Bundle(t.Context(), newProject(t, s, "other"), sid, shared); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Bundle() in another project error = %v, want ErrNotFound", err)
	}

	findings, err := s.FindingsByScan(t.Context(), sid)
	if err != nil {
		t.Fatalf("FindingsByScan(%d) error = %v", sid, err)
	}
	for _, f := range findings {
		if f.File == "conf/a.yml" && f.Fingerprint == fpA && f.ID != first {
			t.Errorf("FindingsByScan(%d) ID of the a.yml password = %d, want %d", sid, f.ID, first)
		}
	}
}

func TestUsedNames(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	save(t, s, pid, mixed()...)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, bundleOf(t, s, pid, fpA, "conf/a.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.host"}, bundleOf(t, s, pid, fpB, "conf/a.yml"))
	got, err := s.UsedNames(t.Context(), pid)
	if err != nil {
		t.Fatalf("UsedNames(%d) error = %v", pid, err)
	}
	if diff := cmp.Diff([]triage.Name{"db.main.host", "db.main.password"}, got); diff != "" {
		t.Errorf("UsedNames(%d) (-want +got):\n%s", pid, diff)
	}
}

func TestLatestScan(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	if _, err := s.LatestScan(ctx, pid); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("LatestScan() with no scan error = %v, want ErrNotFound", err)
	}
	first := save(t, s, pid)
	second := save(t, s, pid)
	if _, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"}); err != nil {
		t.Fatalf("RequestScan() error = %v", err)
	}
	got, err := s.LatestScan(ctx, pid)
	if err != nil || got.ID != second {
		t.Errorf("LatestScan() = %d, %v, want %d (not %d or the queued one)", got.ID, err, second, first)
	}
}

func TestPastDecision(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	var (
		pids    []project.ID
		bundles []triage.BundleID
	)
	for _, name := range []string{"one", "two", "three"} {
		pid := newProject(t, s, name)
		save(t, s, pid, password("conf/a.yml"))
		pids = append(pids, pid)
		bundles = append(bundles, bundleOf(t, s, pid, fpA, "conf/a.yml"))
	}

	if _, ok, err := s.PastDecision(ctx, fpA, bundles[2]); err != nil || ok {
		t.Errorf("PastDecision() with no verdict = %v, %v, want false", ok, err)
	}
	first := triage.Decision{Verdict: triage.VerdictFalsePositive}
	decide(t, s, pids[0], first, bundles[0])
	latest := triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater}
	decide(t, s, pids[1], latest, bundles[1])

	got, ok, err := s.PastDecision(ctx, fpA, bundles[2])
	if err != nil || !ok || got != latest {
		t.Errorf("PastDecision(%d) = %+v, %v, %v, want %+v", bundles[2], got, ok, err, latest)
	}
	if got, ok, err := s.PastDecision(ctx, fpA, bundles[1]); err != nil || !ok || got != first {
		t.Errorf("PastDecision(except %d) = %+v, %v, %v, want %+v", bundles[1], got, ok, err, first)
	}
	if _, ok, err := s.PastDecision(ctx, fpB, 0); err != nil || ok {
		t.Errorf("PastDecision(another value) = %v, %v, want false", ok, err)
	}
}

func TestMigration0005Backfill(t *testing.T) {
	s := db.NewTestStore(t)
	ctx := t.Context()
	if err := s.MigrateThrough(ctx, 4); err != nil {
		t.Fatalf("MigrateThrough(4) error = %v", err)
	}
	pid := newProject(t, s, "shop")
	sid, err := s.InsertRawScan(ctx, pid)
	if err != nil {
		t.Fatalf("InsertRawScan() error = %v", err)
	}
	for i, f := range mixed() {
		if err := s.InsertRawFinding(ctx, pid, int64(sid), f, i+1); err != nil {
			t.Fatalf("InsertRawFinding(%s) error = %v", f.File, err)
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() over 0004 error = %v", err)
	}
	assertMixedBundles(t, s, pid)
}

func TestProjectSummaries(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	alpha := newProject(t, s, "alpha")
	bravo := newProject(t, s, "bravo")
	charlie := newProject(t, s, "charlie")

	save(t, s, bravo, mixed()...)
	decide(t, s, bravo, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, bravo, fpB, "conf/a.yml"))
	failed, j := claimedScan(t, s, bravo)
	if err := s.FailScan(ctx, j.ID, "worker-a", failed, "fake failure"); err != nil {
		t.Fatalf("FailScan(%d) error = %v", failed, err)
	}

	save(t, s, charlie, mixed()...)
	save(t, s, charlie, password("conf/a.yml"))
	queued, err := s.RequestScan(ctx, charlie, audit.Actor{Subject: "fake-subject"})
	if err != nil {
		t.Fatalf("RequestScan(%d) error = %v", charlie, err)
	}

	got, err := s.ProjectSummaries(ctx)
	if err != nil {
		t.Fatalf("ProjectSummaries() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ProjectSummaries() = %d summaries, want 3", len(got))
	}
	for i, sum := range got {
		if sum.LastScanAt.IsZero() != (sum.LastScanID == 0) {
			t.Errorf("ProjectSummaries()[%d] scan %d at %v, want a time exactly when there is a scan", i, sum.LastScanID, sum.LastScanAt)
		}
	}
	type row struct {
		ID         project.ID
		Name       string
		LastScanID int64
		Status     string
		HasResult  bool
		Undecided  int
		Total      int
	}
	var rows []row
	for _, sum := range got {
		rows = append(rows, row{sum.Project.ID, sum.Project.Name, sum.LastScanID, sum.LastScanStatus, sum.HasResult, sum.Undecided, sum.Total})
	}
	want := []row{
		{alpha, "alpha", 0, "", false, 0, 0},
		{bravo, "bravo", int64(failed), "failed", true, 5, 6},
		{charlie, "charlie", int64(queued), "queued", true, 1, 1},
	}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("ProjectSummaries() (-want +got):\n%s", diff)
	}
}

func TestSameValueBundles(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	other := newProject(t, s, "other")
	sid := save(t, s, pid, mixed()...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")
	jsp := bundleOf(t, s, pid, fpA, "web/c.jsp")
	hostA := bundleOf(t, s, pid, fpB, "conf/a.yml")
	hostB := bundleOf(t, s, pid, fpB, "conf/b.xml")
	phoneX := bundleOf(t, s, pid, fpC, "logs/x.log")
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.host"}, hostB)

	tests := []struct {
		name string
		in   project.ID
		id   triage.BundleID
		want []triage.Entry
	}{
		{"shared password", pid, shared, []triage.Entry{{
			Bundle: jsp, Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "****",
			File: "web/c.jsp", FirstLine: 3, Count: 1, Bundles: 1,
		}}},
		{"jsp password", pid, jsp, []triage.Entry{{
			Bundle: shared, Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "****",
			File: "conf/a.yml", FirstLine: 3, Count: 2, Bundles: 1,
		}}},
		{"host", pid, hostA, []triage.Entry{{
			Bundle: hostB, Name: "db.main.host", Verdict: triage.VerdictExternalize,
			Category: finding.CategoryStorage, Kind: finding.KindHost, Preview: "****",
			File: "conf/b.xml", FirstLine: 2, Count: 1, Bundles: 1,
		}}},
		{"customer data", pid, phoneX, nil},
		{"bundle of another project", other, shared, nil},
		{"unknown bundle", pid, 999999, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.SameValueBundles(ctx, tt.in, sid, tt.id)
			if err != nil {
				t.Fatalf("SameValueBundles(%d, %d, %d) error = %v", tt.in, sid, tt.id, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("SameValueBundles(%d, %d, %d) (-want +got):\n%s", tt.in, sid, tt.id, diff)
			}
		})
	}

	later := save(t, s, pid, password("conf/a.yml"), password("conf/b.xml"))
	got, err := s.SameValueBundles(ctx, pid, later, shared)
	if err != nil {
		t.Fatalf("SameValueBundles(%d, %d, %d) error = %v", pid, later, shared, err)
	}
	if len(got) != 0 {
		t.Errorf("SameValueBundles(%d, %d, %d) = %+v, want none: the scan did not find the JSP", pid, later, shared, got)
	}
}

func TestApplyExcludesDecisions(t *testing.T) {
	for _, state := range []apply.State{apply.StateChecking, apply.StateQueued, apply.StateRunning} {
		t.Run("refused while "+string(state), func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			pid := newProject(t, s, "shop")
			sid := save(t, s, pid, mixed()...)
			pw := bundleOf(t, s, pid, fpA, "conf/a.yml")
			moving := findingID(t, s, pid, fpA, "conf/b.xml")
			applyRunIn(t, s, pid, sid, state)

			if err := s.Decide(ctx, pid, []triage.BundleID{pw}, externalizeDB, tester); !errors.Is(err, db.ErrApplyActive) {
				t.Errorf("Decide(%d) while an apply is %s error = %v, want ErrApplyActive", pw, state, err)
			}
			if _, err := s.MoveFinding(ctx, pid, moving, 0, tester); !errors.Is(err, db.ErrApplyActive) {
				t.Errorf("MoveFinding(%d) while an apply is %s error = %v, want ErrApplyActive", moving, state, err)
			}
			if b := readBundle(t, s, pid, sid, pw); b.Decision != (triage.Decision{}) || len(b.Members) != 2 {
				t.Errorf("Bundle(%d) after refused calls = %+v with %d members, want undecided with 2", pw, b.Decision, len(b.Members))
			}
			if got := bundleOf(t, s, pid, fpA, "conf/b.xml"); got != pw {
				t.Errorf("bundle of finding %d after a refused move = %d, want %d", moving, got, pw)
			}
			if n, err := s.Count(ctx, "audit_event"); err != nil || n != 0 {
				t.Errorf("rows in audit_event = %d, %v, want 0", n, err)
			}
		})
	}
	for _, state := range []apply.State{apply.StateReady, apply.StateSucceeded, apply.StateFailed} {
		t.Run("allowed when "+string(state), func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			pid := newProject(t, s, "shop")
			sid := save(t, s, pid, mixed()...)
			pw := bundleOf(t, s, pid, fpA, "conf/a.yml")
			applyRunIn(t, s, pid, sid, state)
			if err := s.Decide(ctx, pid, []triage.BundleID{pw}, externalizeDB, tester); err != nil {
				t.Errorf("Decide(%d) when an apply is %s error = %v, want nil", pw, state, err)
			}
			if _, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "conf/b.xml"), 0, tester); err != nil {
				t.Errorf("MoveFinding() when an apply is %s error = %v, want nil", state, err)
			}
		})
	}
}

func TestDecide_AppliedBundleIsFixed(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, ph := decidedProject(t, s, "shop")
	const storePath = "secret/data/projects/1/ext.db.main.password"
	for id, path := range map[triage.BundleID]string{pw: storePath, ph: ""} {
		if err := s.MarkApplied(ctx, id, path); err != nil {
			t.Fatalf("MarkApplied(%d) error = %v", id, err)
		}
	}
	if b := readBundle(t, s, pid, sid, pw); b.AppliedAt.IsZero() || b.StorePath != storePath {
		t.Errorf("Bundle(%d) applied at %v, store path %q, want a time and %q", pw, b.AppliedAt, b.StorePath, storePath)
	}

	refused := map[string]triage.Decision{
		"verdict": {Verdict: triage.VerdictFalsePositive},
		"name":    {Verdict: triage.VerdictExternalize, Name: "db.main.pass"},
		"cleared": {},
	}
	for name, d := range refused {
		if err := s.Decide(ctx, pid, []triage.BundleID{pw}, d, tester); !errors.Is(err, triage.ErrNotAllowed) {
			t.Errorf("Decide(applied bundle, %s %+v) error = %v, want ErrNotAllowed", name, d, err)
		}
	}
	if b := readBundle(t, s, pid, sid, pw); b.Decision != externalizeDB {
		t.Errorf("Bundle(%d) decision after refused changes = %+v, want %+v", pw, b.Decision, externalizeDB)
	}

	noted := externalizeDB
	noted.Note = "dev-only note"
	if err := s.Decide(ctx, pid, []triage.BundleID{pw}, noted, tester); err != nil {
		t.Errorf("Decide(applied bundle, same verdict and name with a note) error = %v, want nil", err)
	}
	if err := s.Decide(ctx, pid, []triage.BundleID{ph}, triage.Decision{Verdict: triage.VerdictFalsePositive}, tester); err != nil {
		t.Errorf("Decide(applied redaction, false positive) error = %v, want nil", err)
	}
}

func TestMoveFinding_KeepsAppliedBundle(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, _ := decidedProject(t, s, "shop")
	if err := s.MarkApplied(ctx, pw, "secret/data/projects/1/ext.db.main.password"); err != nil {
		t.Fatalf("MarkApplied(%d) error = %v", pw, err)
	}
	// The apply replaced the value in a.yml and b.xml; a later scan finds it
	// again in a new file, which joins the applied shared bundle.
	save(t, s, pid, password("conf/d.properties"), password("web/c.jsp"))
	if got := bundleOf(t, s, pid, fpA, "conf/d.properties"); got != pw {
		t.Fatalf("bundle of the new d.properties = %d, want the applied bundle %d", got, pw)
	}
	jsp := bundleOf(t, s, pid, fpA, "web/c.jsp")

	if _, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "conf/a.yml"), 0, tester); !errors.Is(err, triage.ErrNotAllowed) {
		t.Errorf("MoveFinding(a.yml, new bundle) of an applied location error = %v, want ErrNotAllowed", err)
	}
	if _, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "conf/b.xml"), jsp, tester); !errors.Is(err, triage.ErrNotAllowed) {
		t.Errorf("MoveFinding(b.xml, bundle %d) of an applied location error = %v, want ErrNotAllowed", jsp, err)
	}

	moved, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "conf/d.properties"), 0, tester)
	if err != nil {
		t.Fatalf("MoveFinding(d.properties, new bundle) found by the latest scan error = %v, want nil", err)
	}
	if moved == pw || bundleOf(t, s, pid, fpA, "conf/d.properties") != moved {
		t.Errorf("MoveFinding(d.properties, new bundle) = %d, want a new bundle holding it", moved)
	}
	b := readBundle(t, s, pid, sid, pw)
	if b.Decision != externalizeDB || b.AppliedAt.IsZero() || b.StorePath != "secret/data/projects/1/ext.db.main.password" {
		t.Errorf("Bundle(%d) after the move = %+v applied %v at %q, want %+v, applied, the same store path",
			pw, b.Decision, b.AppliedAt, b.StorePath, externalizeDB)
	}
	for _, file := range []string{"conf/a.yml", "conf/b.xml"} {
		if got := bundleOf(t, s, pid, fpA, file); got != pw {
			t.Errorf("bundle of %s after the move = %d, want %d", file, got, pw)
		}
	}
}

// TestMoveFinding_KeepsAppliedPlaces moves members of an applied bundle
// after the apply and before any new scan, while the latest scan still lists
// the places the apply changed as current.
func TestMoveFinding_KeepsAppliedPlaces(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, _ := decidedProject(t, s, "shop")
	sid := save(t, s, pid, append(mixed(), password("conf/d.properties"))...)
	if got := bundleOf(t, s, pid, fpA, "conf/d.properties"); got != pw {
		t.Fatalf("bundle of d.properties = %d, want the shared bundle %d", got, pw)
	}
	id, j := claimApply(t, s, pid)
	writeFile(t, s, id, "conf/a.yml", hashOf("1"), replaceItem(t, s, pid, pw, "conf/a.yml"))
	beginFile(t, s, id, "conf/b.xml", hashOf("2"), replaceItem(t, s, pid, pw, "conf/b.xml"))
	if err := s.FailApply(ctx, j.ID, "worker-a", id, "apply could not finish; see the worker log", nil, nil); err != nil {
		t.Fatalf("FailApply(%d) error = %v", id, err)
	}
	if b := readBundle(t, s, pid, sid, pw); b.AppliedAt.IsZero() || len(b.Members) != 3 {
		t.Fatalf("Bundle(%d) after the apply applied at %v with %d members, want applied with 3", pw, b.AppliedAt, len(b.Members))
	}
	bundles, err := s.Count(ctx, "bundle")
	if err != nil {
		t.Fatalf("Count(bundle) error = %v", err)
	}
	events, err := s.Count(ctx, "audit_event")
	if err != nil {
		t.Fatalf("Count(audit_event) error = %v", err)
	}

	// a.yml holds an applied item and b.xml a writing one.
	for _, file := range []string{"conf/a.yml", "conf/b.xml"} {
		if _, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, file), 0, tester); !errors.Is(err, triage.ErrNotAllowed) {
			t.Errorf("MoveFinding(%s, new bundle) of an applied place error = %v, want ErrNotAllowed", file, err)
		}
	}
	if b := readBundle(t, s, pid, sid, pw); len(b.Members) != 3 || b.Decision != externalizeDB {
		t.Errorf("Bundle(%d) after refused moves = %+v with %d members, want %+v with 3", pw, b.Decision, len(b.Members), externalizeDB)
	}
	if n, err := s.Count(ctx, "bundle"); err != nil || n != bundles {
		t.Errorf("rows in bundle after refused moves = %d, %v, want %d", n, err, bundles)
	}
	if n, err := s.Count(ctx, "audit_event"); err != nil || n != events {
		t.Errorf("rows in audit_event after refused moves = %d, %v, want %d", n, err, events)
	}

	moved, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "conf/d.properties"), 0, tester)
	if err != nil {
		t.Fatalf("MoveFinding(d.properties, new bundle) with no applied place error = %v, want nil", err)
	}
	if moved == pw || bundleOf(t, s, pid, fpA, "conf/d.properties") != moved {
		t.Errorf("MoveFinding(d.properties, new bundle) = %d, want a new bundle holding it", moved)
	}
	for _, file := range []string{"conf/a.yml", "conf/b.xml"} {
		if got := bundleOf(t, s, pid, fpA, file); got != pw {
			t.Errorf("bundle of %s after the moves = %d, want %d", file, got, pw)
		}
	}
}

// TestMoveFinding_SettledItemsDoNotBlock moves findings whose apply items
// are failed, interrupted or skipped: no apply changed those places, so they
// move as any other finding does.
func TestMoveFinding_SettledItemsDoNotBlock(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, _ := decidedProject(t, s, "shop")
	// e.ini stays in the bundle, so none of the moved findings is its last.
	save(t, s, pid, append(mixed(), password("conf/d.properties"), password("conf/e.ini"))...)
	id, j := claimApply(t, s, pid)
	interrupted := replaceItem(t, s, pid, pw, "conf/a.yml")
	beginFile(t, s, id, "conf/a.yml", hashOf("1"), interrupted)
	for _, f := range openFiles(t, s, pid) {
		if err := s.ResolveFile(ctx, f, false); err != nil {
			t.Fatalf("ResolveFile(%q, not done) error = %v", f.File, err)
		}
	}
	failed := replaceItem(t, s, pid, pw, "conf/b.xml")
	failed.Status, failed.Reason = apply.StatusFailed, apply.ReasonStoreFailed
	skipped := replaceItem(t, s, pid, pw, "conf/d.properties")
	skipped.Status, skipped.Reason = apply.StatusSkipped, apply.ReasonFileChanged
	if err := s.FailApply(ctx, j.ID, "worker-a", id, apply.FailureSecretStore, nil, []apply.Item{failed, skipped}); err != nil {
		t.Fatalf("FailApply(%d) error = %v", id, err)
	}
	statuses := map[string]apply.Reason{}
	for _, it := range applyItems(t, s, id) {
		statuses[it.File] = it.Reason
	}
	want := map[string]apply.Reason{
		"conf/a.yml": apply.ReasonInterrupted, "conf/b.xml": apply.ReasonStoreFailed, "conf/d.properties": apply.ReasonFileChanged,
	}
	if diff := cmp.Diff(want, statuses); diff != "" {
		t.Fatalf("ApplyItems(%d) reasons (-want +got):\n%s", id, diff)
	}

	for _, file := range []string{"conf/a.yml", "conf/b.xml", "conf/d.properties"} {
		moved, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, file), 0, tester)
		if err != nil {
			t.Errorf("MoveFinding(%s, new bundle) with a %s item error = %v, want nil", file, want[file], err)
			continue
		}
		if moved == pw || bundleOf(t, s, pid, fpA, file) != moved {
			t.Errorf("MoveFinding(%s, new bundle) = %d, want a new bundle holding it", file, moved)
		}
	}
}

func TestMoveFinding_KeepsLastFindingOfAppliedBundle(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	sid := save(t, s, pid, password("conf/a.yml"), password("web/c.jsp"))
	pw := bundleOf(t, s, pid, fpA, "conf/a.yml")
	decide(t, s, pid, externalizeDB, pw)
	if err := s.MarkApplied(ctx, pw, "secret/data/projects/1/ext.db.main.password"); err != nil {
		t.Fatalf("MarkApplied(%d) error = %v", pw, err)
	}
	jsp := bundleOf(t, s, pid, fpA, "web/c.jsp")
	if _, err := s.MoveFinding(ctx, pid, findingID(t, s, pid, fpA, "conf/a.yml"), jsp, tester); !errors.Is(err, triage.ErrNotAllowed) {
		t.Errorf("MoveFinding(a.yml, bundle %d) of the last finding of an applied bundle error = %v, want ErrNotAllowed", jsp, err)
	}
	if b := readBundle(t, s, pid, sid, pw); b.Decision != externalizeDB || len(b.Members) != 1 {
		t.Errorf("Bundle(%d) after the refused move = %+v with %d members, want %+v with 1", pw, b.Decision, len(b.Members), externalizeDB)
	}
}

// keysFile is the file the keyed passwords of these tests are in.
const keysFile = "conf/c.properties"

// pwKey returns the password of fpA in keysFile under key, at line 3.
func pwKey(key string) finding.Finding {
	return keyed(password(keysFile), key)
}

func TestSaveResult_KeysInOneFileStartApart(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	save(t, s, pid,
		pwKey("local.db.password"), pwKey("dev.db.password"), pwKey("live.db.password"),
		keyed(password("conf/other.properties"), "db.password"))

	local := bundleOfKey(t, s, pid, fpA, keysFile, "local.db.password")
	dev := bundleOfKey(t, s, pid, fpA, keysFile, "dev.db.password")
	live := bundleOfKey(t, s, pid, fpA, keysFile, "live.db.password")
	other := bundleOfKey(t, s, pid, fpA, "conf/other.properties", "db.password")
	if local != other {
		t.Errorf("bundles of local and other.properties = %d, %d, want one shared bundle", local, other)
	}
	if dev == local || live == local || dev == live {
		t.Errorf("bundles of local, dev, live = %d, %d, %d, want three different bundles", local, dev, live)
	}
	if n, err := s.Count(ctx, "bundle"); err != nil || n != 3 {
		t.Errorf("rows in bundle = %d, %v, want 3", n, err)
	}
}

func TestSaveResult_NewKeyNeverInheritsDecision(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	save(t, s, pid, pwKey("local.db.password"), pwKey("dev.db.password"))
	local := bundleOfKey(t, s, pid, fpA, keysFile, "local.db.password")
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.local.main.password"}, local)

	alpha := keyed(fnd(fpA, keysFile, finding.CategoryStorage, finding.KindPassword, 1), "alpha.db.password")
	second := save(t, s, pid, alpha, pwKey("local.db.password"), pwKey("dev.db.password"))
	got := bundleOfKey(t, s, pid, fpA, keysFile, "alpha.db.password")
	if got == local {
		t.Fatalf("bundle of alpha.db.password = %d, want a bundle other than local's", got)
	}
	if b := readBundle(t, s, pid, second, got); b.Decision != (triage.Decision{}) {
		t.Errorf("Bundle(%d).Decision of alpha.db.password = %+v, want none", got, b.Decision)
	}
}

func TestSaveResult_NewKeyAfterMemberGone(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	first := save(t, s, pid, pwKey("local.db.password"))
	local := bundleOfKey(t, s, pid, fpA, keysFile, "local.db.password")
	want := triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.local.main.password"}
	decide(t, s, pid, want, local)

	second := save(t, s, pid, pwKey("renamed.db.password"))
	renamed := bundleOfKey(t, s, pid, fpA, keysFile, "renamed.db.password")
	if renamed == local {
		t.Fatalf("bundle of renamed.db.password = %d, want a new bundle", renamed)
	}
	if b := readBundle(t, s, pid, second, renamed); b.Decision != (triage.Decision{}) {
		t.Errorf("Bundle(%d).Decision of renamed.db.password = %+v, want none", renamed, b.Decision)
	}
	if got := bundleOfKey(t, s, pid, fpA, keysFile, "local.db.password"); got != local {
		t.Errorf("bundle of local.db.password = %d, want %d", got, local)
	}
	if b := readBundle(t, s, pid, first, local); b.Decision != want {
		t.Errorf("Bundle(%d).Decision of local.db.password = %+v, want %+v", local, b.Decision, want)
	}
}

func TestSaveResult_KeylessAfterKeyed(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	keys := []string{"local.db.password", "dev.db.password", "live.db.password"}
	names := []triage.Name{"db.local.main.password", "db.dev.main.password", "db.prod.main.password"}
	keyedScan := func() []finding.Finding {
		var out []finding.Finding
		for _, k := range keys {
			out = append(out, pwKey(k))
		}
		return out
	}
	first := save(t, s, pid, keyedScan()...)
	type state struct {
		ID       finding.ID
		Bundle   triage.BundleID
		Decision triage.Decision
	}
	read := func(sid scan.ID) []state {
		var out []state
		for _, k := range keys {
			b := bundleOfKey(t, s, pid, fpA, keysFile, k)
			out = append(out, state{
				ID: findingIDOfKey(t, s, pid, fpA, keysFile, k), Bundle: b,
				Decision: readBundle(t, s, pid, sid, b).Decision,
			})
		}
		return out
	}
	for i, k := range keys {
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: names[i]},
			bundleOfKey(t, s, pid, fpA, keysFile, k))
	}
	before := read(first)

	keyless := password(keysFile)
	for _, line := range []int{4, 5} {
		keyless.Locations = append(keyless.Locations, fnd(fpA, keysFile, finding.CategoryStorage, finding.KindPassword, line).Locations...)
	}
	second := save(t, s, pid, keyless)
	b := bundleOf(t, s, pid, fpA, keysFile)
	for _, st := range before {
		if b == st.Bundle {
			t.Errorf("bundle of the keyless finding = %d, want a new bundle", b)
		}
	}
	if got := readBundle(t, s, pid, second, b); got.Decision != (triage.Decision{}) {
		t.Errorf("Bundle(%d).Decision of the keyless finding = %+v, want none", b, got.Decision)
	}
	keylessID := findingID(t, s, pid, fpA, keysFile)

	third := save(t, s, pid, keyedScan()...)
	if diff := cmp.Diff(before, read(third)); diff != "" {
		t.Errorf("keyed findings after a keyless scan (-want +got):\n%s", diff)
	}
	if got := findingID(t, s, pid, fpA, keysFile); got != keylessID {
		t.Errorf("keyless finding after the third scan = %d, want %d with no key", got, keylessID)
	}
}

// assertLegacyAdopts stores legacy, a finding with no key, decides its bundle
// with d, and rescans its file with keys local, dev and live. It checks that
// local takes over the finding with its bundle and decision and that dev and
// live start in undecided bundles of their own.
func assertLegacyAdopts(t *testing.T, legacy finding.Finding, d triage.Decision) {
	t.Helper()
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	save(t, s, pid, legacy)
	fp, file := legacy.Fingerprint, legacy.File
	id := findingID(t, s, pid, fp, file)
	bundle := bundleOf(t, s, pid, fp, file)
	decide(t, s, pid, d, bundle)

	second := save(t, s, pid, keyed(legacy, "local.db.password"), keyed(legacy, "dev.db.password"), keyed(legacy, "live.db.password"))
	if got := findingIDOfKey(t, s, pid, fp, file, "local.db.password"); got != id {
		t.Errorf("finding of local.db.password = %d, want the legacy finding %d", got, id)
	}
	if got := bundleOfKey(t, s, pid, fp, file, "local.db.password"); got != bundle {
		t.Errorf("bundle of local.db.password = %d, want the legacy bundle %d", got, bundle)
	}
	if b := readBundle(t, s, pid, second, bundle); b.Decision != d {
		t.Errorf("Bundle(%d).Decision after the keyed scan = %+v, want %+v", bundle, b.Decision, d)
	}
	for _, k := range []string{"dev.db.password", "live.db.password"} {
		b := bundleOfKey(t, s, pid, fp, file, k)
		if b == bundle {
			t.Errorf("bundle of %s = %d, want a new bundle", k, b)
			continue
		}
		if got := readBundle(t, s, pid, second, b); got.Decision != (triage.Decision{}) {
			t.Errorf("Bundle(%d).Decision of %s = %+v, want none", b, k, got.Decision)
		}
	}
	if _, err := s.FindingRow(ctx, pid, fp, file); err == nil {
		t.Errorf("FindingRow(%s with no key) error = nil, want no such finding", file)
	}
	if n, err := s.Count(ctx, "finding"); err != nil || n != 3 {
		t.Errorf("rows in finding = %d, %v, want 3", n, err)
	}
}

func TestSaveResult_LegacyFindingAdoptsFirstKey(t *testing.T) {
	assertLegacyAdopts(t, password(keysFile),
		triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"})
}

func TestSaveResult_LegacyOwnBundleAdopts(t *testing.T) {
	assertLegacyAdopts(t, host(keysFile),
		triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.host"})
}

func TestBundle_MembersCarryKey(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	sid := save(t, s, pid, pwKey("live.db.password"), password("conf/other.properties"))
	b := bundleOfKey(t, s, pid, fpA, keysFile, "live.db.password")
	decide(t, s, pid, externalizeDB, b)

	keys := func(bundle triage.Bundle) map[string]string {
		out := map[string]string{}
		for _, m := range bundle.Members {
			out[m.File] = m.Key
		}
		return out
	}
	want := map[string]string{keysFile: "live.db.password", "conf/other.properties": ""}

	if diff := cmp.Diff(want, keys(readBundle(t, s, pid, sid, b))); diff != "" {
		t.Errorf("Bundle(%d) member keys (-want +got):\n%s", b, diff)
	}
	id := requestCheck(t, s, pid)
	in := applyInput(t, s, id)
	if len(in.Bundles) != 1 {
		t.Fatalf("ApplyInput(%d) has %d bundles, want 1", id, len(in.Bundles))
	}
	if diff := cmp.Diff(want, keys(in.Bundles[0])); diff != "" {
		t.Errorf("ApplyInput(%d) member keys (-want +got):\n%s", id, diff)
	}
}

func TestSameValueBundles_ReadsKeyOfFirstMember(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	sid := save(t, s, pid,
		pwKey("local.db.password"), pwKey("dev.db.password"),
		keyed(password("conf/other.properties"), "db.password"))
	local := bundleOfKey(t, s, pid, fpA, keysFile, "local.db.password")
	dev := bundleOfKey(t, s, pid, fpA, keysFile, "dev.db.password")

	got, err := s.SameValueBundles(t.Context(), pid, sid, dev)
	if err != nil {
		t.Fatalf("SameValueBundles(%d, %d, %d) error = %v", pid, sid, dev, err)
	}
	want := []triage.Entry{{
		Bundle: local, Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "****",
		File: keysFile, Key: "local.db.password", FirstLine: 3, Count: 2, Bundles: 1,
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SameValueBundles(%d, %d, %d) (-want +got):\n%s", pid, sid, dev, diff)
	}
}
