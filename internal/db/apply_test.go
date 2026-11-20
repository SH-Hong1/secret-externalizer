package db_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

var externalizeDB = triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}

// decidedProject registers a project, stores mixed as its scan, and
// externalizes the shared password and redacts the phone number in x.log.
// It returns the project, the scan and the two decided bundles.
func decidedProject(t *testing.T, s *db.Store, name string) (pid project.ID, sid scan.ID, pw, ph triage.BundleID) {
	t.Helper()
	pid = newProject(t, s, name)
	sid = save(t, s, pid, mixed()...)
	pw = bundleOf(t, s, pid, fpA, "conf/a.yml")
	ph = bundleOf(t, s, pid, fpC, "logs/x.log")
	decide(t, s, pid, externalizeDB, pw)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, ph)
	return pid, sid, pw, ph
}

func requestCheck(t *testing.T, s *db.Store, pid project.ID) apply.ID {
	t.Helper()
	id, err := s.RequestApplyCheck(t.Context(), pid, tester)
	if err != nil {
		t.Fatalf("RequestApplyCheck(%d) error = %v", pid, err)
	}
	return id
}

func readRun(t *testing.T, s *db.Store, id apply.ID) apply.Run {
	t.Helper()
	r, err := s.ApplyRun(t.Context(), id)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", id, err)
	}
	return r
}

// readyRun requests a check of the project and puts the run in ready, as a
// check that found nothing in the way would.
func readyRun(t *testing.T, s *db.Store, pid project.ID) apply.Run {
	t.Helper()
	id := requestCheck(t, s, pid)
	if err := s.SetApplyState(t.Context(), id, apply.StateReady); err != nil {
		t.Fatalf("SetApplyState(%d, ready) error = %v", id, err)
	}
	return readRun(t, s, id)
}

func applyRunIn(t *testing.T, s *db.Store, pid project.ID, sid scan.ID, state apply.State) apply.ID {
	t.Helper()
	id, err := s.InsertApplyRun(t.Context(), pid, sid, string(state))
	if err != nil {
		t.Fatalf("InsertApplyRun(%d, %s) error = %v", pid, state, err)
	}
	return apply.ID(id)
}

func jobsOfKind(t *testing.T, s *db.Store, kind string) int {
	t.Helper()
	n, err := s.JobsOfKind(t.Context(), kind)
	if err != nil {
		t.Fatalf("JobsOfKind(%s) error = %v", kind, err)
	}
	return n
}

// assertRunsAndChecks fails the test unless there are runs apply runs and
// checks apply-check jobs.
func assertRunsAndChecks(t *testing.T, s *db.Store, runs, checks int) {
	t.Helper()
	if n, err := s.Count(t.Context(), "apply_run"); err != nil || n != runs {
		t.Errorf("rows in apply_run = %d, %v, want %d", n, err, runs)
	}
	if n := jobsOfKind(t, s, "apply-check"); n != checks {
		t.Errorf("apply-check jobs = %d, want %d", n, checks)
	}
}

func TestRequestApplyCheck(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, ph := decidedProject(t, s, "shop")
	ready := applyRunIn(t, s, pid, sid, apply.StateReady)
	blocked := applyRunIn(t, s, pid, sid, apply.StateBlocked)
	done := applyRunIn(t, s, pid, sid, apply.StateSucceeded)
	otherPid, otherSid, _, _ := decidedProject(t, s, "other")
	otherReady := applyRunIn(t, s, otherPid, otherSid, apply.StateReady)

	id := requestCheck(t, s, pid)
	got := readRun(t, s, id)
	want := apply.Digest(sid, []triage.Bundle{readBundle(t, s, pid, sid, pw), readBundle(t, s, pid, sid, ph)})
	if got.State != apply.StateChecking || got.ProjectID != pid || got.ScanID != sid || got.Requester != tester ||
		got.RequestedAt.IsZero() || got.PlanDigest != want {
		t.Errorf("ApplyRun(%d) = %+v, want a checking run of project %d, scan %d, by %v, digest %s", id, got, pid, sid, tester, want)
	}
	if n := jobsOfKind(t, s, "apply-check"); n != 1 {
		t.Errorf("apply-check jobs = %d, want 1", n)
	}
	if j, err := s.ApplyJob(ctx, int64(id), "apply-check"); err != nil || j.State != "queued" {
		t.Errorf("ApplyJob(%d, apply-check) = %+v, %v, want a queued job", id, j, err)
	}
	for run, state := range map[apply.ID]apply.State{
		ready: apply.StateSuperseded, blocked: apply.StateSuperseded, done: apply.StateSucceeded, otherReady: apply.StateReady,
	} {
		if r := readRun(t, s, run); r.State != state {
			t.Errorf("ApplyRun(%d) after the check of project %d = %s, want %s", run, pid, r.State, state)
		}
	}

	t.Run("second request", func(t *testing.T) {
		if _, err := s.RequestApplyCheck(ctx, pid, tester); !errors.Is(err, db.ErrApplyActive) {
			t.Errorf("RequestApplyCheck(%d) while run %d checks error = %v, want ErrApplyActive", pid, id, err)
		}
		assertRunsAndChecks(t, s, 5, 1)
	})
	t.Run("no succeeded scan", func(t *testing.T) {
		bare := newProject(t, s, "bare")
		if _, err := s.RequestApplyCheck(ctx, bare, tester); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("RequestApplyCheck(project without scan) error = %v, want ErrNotFound", err)
		}
		assertRunsAndChecks(t, s, 5, 1)
	})
	t.Run("no project", func(t *testing.T) {
		if _, err := s.RequestApplyCheck(ctx, 424242, tester); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("RequestApplyCheck(424242) error = %v, want ErrNotFound", err)
		}
	})
}

func TestRequestApplyCheck_PlanLeavesOutUndecided(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	sid := save(t, s, pid, mixed()...)
	host := bundleOf(t, s, pid, fpB, "conf/a.yml")
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, host)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater}, bundleOf(t, s, pid, fpB, "conf/b.xml"))

	r := readRun(t, s, requestCheck(t, s, pid))
	if want := apply.Digest(sid, nil); r.PlanDigest != want {
		t.Errorf("PlanDigest with no bundle to externalize or redact = %s, want %s", r.PlanDigest, want)
	}
}

func TestConfirmApply(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run("allow irreversible "+strconv.FormatBool(allow), func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			pid, _, _, _ := decidedProject(t, s, "shop")
			r := readyRun(t, s, pid)
			events, err := s.Count(ctx, "audit_event")
			if err != nil {
				t.Fatalf("Count(audit_event) error = %v", err)
			}

			if err := s.ConfirmApply(ctx, r.ID, r.PlanDigest, allow, tester); err != nil {
				t.Fatalf("ConfirmApply(%d) error = %v", r.ID, err)
			}
			got := readRun(t, s, r.ID)
			if got.State != apply.StateQueued || got.ConfirmedAt.IsZero() || got.AllowIrreversible != allow {
				t.Errorf("ApplyRun(%d) after ConfirmApply = %+v, want queued, confirmed, allow irreversible %v", r.ID, got, allow)
			}
			if j, err := s.ApplyJob(ctx, int64(r.ID), "apply"); err != nil || j.State != "queued" {
				t.Errorf("ApplyJob(%d, apply) = %+v, %v, want a queued job", r.ID, j, err)
			}
			if n := jobsOfKind(t, s, "apply"); n != 1 {
				t.Errorf("apply jobs = %d, want 1", n)
			}
			if n, err := s.Count(ctx, "audit_event"); err != nil || n != events+1 {
				t.Errorf("audit events after ConfirmApply = %d, %v, want %d", n, err, events+1)
			}
			e, err := s.LastAuditEvent(ctx)
			if err != nil {
				t.Fatalf("LastAuditEvent() error = %v", err)
			}
			irreversible := map[bool]string{true: "yes", false: "no"}[allow]
			want := audit.Event{
				Actor: tester, Action: audit.ActionApply, ProjectID: pid,
				Detail: map[string]string{
					audit.DetailApply: strconv.FormatInt(int64(r.ID), 10), audit.DetailIrreversible: irreversible,
				},
			}
			e.ID, e.At = 0, time.Time{}
			if diff := cmp.Diff(want, e); diff != "" {
				t.Errorf("audit event of ConfirmApply(%d) mismatch (-want +got):\n%s", r.ID, diff)
			}
		})
	}
}

func TestConfirmApply_PlanChanged(t *testing.T) {
	t.Run("verdict changed", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, _, ph := decidedProject(t, s, "shop")
		r := readyRun(t, s, pid)
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, ph)
		assertRefused(t, s, r.ID, r.PlanDigest, db.ErrPlanChanged)
	})
	t.Run("name changed", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, pw, _ := decidedProject(t, s, "shop")
		r := readyRun(t, s, pid)
		decide(t, s, pid, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.pass"}, pw)
		assertRefused(t, s, r.ID, r.PlanDigest, db.ErrPlanChanged)
	})
	t.Run("member moved", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, _, _ := decidedProject(t, s, "shop")
		r := readyRun(t, s, pid)
		if _, err := s.MoveFinding(t.Context(), pid, findingID(t, s, pid, fpA, "conf/b.xml"), 0, tester); err != nil {
			t.Fatalf("MoveFinding() error = %v", err)
		}
		assertRefused(t, s, r.ID, r.PlanDigest, db.ErrPlanChanged)
	})
	t.Run("other digest", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, _, _ := decidedProject(t, s, "shop")
		r := readyRun(t, s, pid)
		assertRefused(t, s, r.ID, strings.Repeat("0", 64), db.ErrPlanChanged)
	})
	t.Run("newer scan", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, _, _ := decidedProject(t, s, "shop")
		r := readyRun(t, s, pid)
		save(t, s, pid, mixed()...)
		assertRefused(t, s, r.ID, r.PlanDigest, db.ErrPlanChanged)
	})
	for _, state := range []apply.State{apply.StateBlocked, apply.StateChecking, apply.StateSuperseded, apply.StateQueued} {
		t.Run("run "+string(state), func(t *testing.T) {
			s := newMigratedStore(t)
			pid, _, _, _ := decidedProject(t, s, "shop")
			r := readyRun(t, s, pid)
			if err := s.SetApplyState(t.Context(), r.ID, state); err != nil {
				t.Fatalf("SetApplyState(%d, %s) error = %v", r.ID, state, err)
			}
			assertRefused(t, s, r.ID, r.PlanDigest, db.ErrNotFound)
		})
	}
	t.Run("no run", func(t *testing.T) {
		s := newMigratedStore(t)
		assertRefused(t, s, 424242, strings.Repeat("0", 64), db.ErrNotFound)
	})
	t.Run("scan requested", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, _, _ := decidedProject(t, s, "shop")
		r := readyRun(t, s, pid)
		if _, err := s.RequestScan(t.Context(), pid, tester); err != nil {
			t.Fatalf("RequestScan(%d) error = %v", pid, err)
		}
		assertRefused(t, s, r.ID, r.PlanDigest, db.ErrScanActive)
	})
}

// assertRefused checks that ConfirmApply refuses run id with want and leaves
// no apply job, no audit event of an apply, and the run's state as it was.
func assertRefused(t *testing.T, s *db.Store, id apply.ID, digest string, want error) {
	t.Helper()
	ctx := t.Context()
	before, beforeErr := s.ApplyRun(ctx, id)
	if err := s.ConfirmApply(ctx, id, digest, true, tester); !errors.Is(err, want) {
		t.Errorf("ConfirmApply(%d, %.8s) error = %v, want %v", id, digest, err, want)
	}
	if n := jobsOfKind(t, s, "apply"); n != 0 {
		t.Errorf("apply jobs after a refused ConfirmApply = %d, want 0", n)
	}
	if e, err := s.LastAuditEvent(ctx); err == nil && e.Action == audit.ActionApply {
		t.Errorf("LastAuditEvent() after a refused ConfirmApply = %+v, want no apply event", e)
	}
	if beforeErr == nil {
		if after := readRun(t, s, id); after.State != before.State || !after.ConfirmedAt.IsZero() {
			t.Errorf("ApplyRun(%d) after a refused ConfirmApply = %s, confirmed %v, want %s, unconfirmed",
				id, after.State, after.ConfirmedAt, before.State)
		}
	}
}

func TestApplyRun(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, _, _ := decidedProject(t, s, "shop")
	sid := save(t, s, pid, mixed()...)
	otherPid := newProjectWithScan(t, s, "other")
	first := requestCheck(t, s, pid)
	// Any scan will do as the rescan; one of another project keeps this
	// project's next check free to start.
	rescan, err := s.InsertRawScan(ctx, newProject(t, s, "rescan"))
	if err != nil {
		t.Fatalf("InsertRawScan() error = %v", err)
	}
	at := time.Date(2026, 11, 20, 3, 4, 5, 123456000, time.UTC)
	want := apply.Run{
		ID: first, ProjectID: pid, ScanID: sid, State: apply.StateFailed, PlanDigest: strings.Repeat("9", 64),
		Requester:   tester,
		RequestedAt: at, CheckedAt: at.Add(time.Second), ConfirmedAt: at.Add(2 * time.Second),
		StartedAt: at.Add(3 * time.Second), FinishedAt: at.Add(4 * time.Second),
		AllowIrreversible: true,
		IrreversibleFiles: []string{"logs/x.log", "logs/y.log"},
		Blockers:          []apply.Blocker{apply.BlockerDirty, apply.BlockerScanNotLatest},
		DirtyFiles:        []string{"conf/a.yml"},
		DirtyCount:        3,
		FailureReason:     "the work tree has uncommitted changes",
		Applied:           4, Skipped: 2, Failed: 1,
		RescanID: rescan,
	}
	if err := s.SetApplyRun(ctx, want); err != nil {
		t.Fatalf("SetApplyRun(%d) error = %v", first, err)
	}
	second := requestCheck(t, s, pid)

	equalTimes := cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })
	if diff := cmp.Diff(want, readRun(t, s, first), equalTimes); diff != "" {
		t.Errorf("ApplyRun(%d) mismatch (-want +got):\n%s", first, diff)
	}
	empty := readRun(t, s, second)
	if empty.Blockers != nil || empty.IrreversibleFiles != nil || empty.DirtyFiles != nil ||
		!empty.CheckedAt.IsZero() || !empty.FinishedAt.IsZero() || empty.RescanID != 0 {
		t.Errorf("ApplyRun(%d) of a new run = %+v, want no lists, times or rescan", second, empty)
	}
	if _, err := s.ApplyRun(ctx, 424242); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("ApplyRun(424242) error = %v, want ErrNotFound", err)
	}

	requestCheck(t, s, otherPid)
	runs, err := s.ApplyRunsByProject(ctx, pid)
	if err != nil {
		t.Fatalf("ApplyRunsByProject(%d) error = %v", pid, err)
	}
	var ids []apply.ID
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	if diff := cmp.Diff([]apply.ID{second, first}, ids); diff != "" {
		t.Errorf("ApplyRunsByProject(%d) IDs mismatch (-want +got):\n%s", pid, diff)
	}
	if runs, err := s.ApplyRunsByProject(ctx, newProject(t, s, "bare")); err != nil || len(runs) != 0 {
		t.Errorf("ApplyRunsByProject(project without runs) = %v, %v, want none", runs, err)
	}
}

func TestApplyItems(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, ph := decidedProject(t, s, "shop")
	otherPid := newProjectWithScan(t, s, "other")
	id := requestCheck(t, s, pid)
	items := []apply.Item{
		{
			BundleID: pw, FindingID: 2, File: "conf/b.xml", StartLine: 3, StartCol: 1, Line: 3,
			Action: apply.ActionReplace, Category: finding.CategoryStorage, Kind: finding.KindPassword,
			Name: "ext.db.main.password", StorePath: "secret/data/projects/1/ext.db.main.password", StoreVersion: 2,
			Tracked: true, Status: apply.StatusApplied,
		},
		{
			BundleID: pw, FindingID: 1, File: "conf/a.yml", StartLine: 9, StartCol: 4, Line: 9,
			Action: apply.ActionReplace, Category: finding.CategoryStorage, Kind: finding.KindPassword,
			Name: "ext.db.main.password", Tracked: true, CodeChange: true, Status: apply.StatusSkipped,
			Reason: apply.ReasonFileChanged,
		},
		{
			BundleID: ph, FindingID: 5, File: "conf/a.yml", StartLine: 9, StartCol: 2,
			Action: apply.ActionRedact, Category: finding.CategoryCustomerData, Kind: finding.KindPhone,
			Status: apply.StatusFailed, Reason: apply.ReasonUntrackedIrreversible,
		},
		{
			BundleID: pw, FindingID: 1, File: "conf/a.yml", StartLine: 2, StartCol: 7,
			Action: apply.ActionRemoveFile, Category: finding.CategoryStorage, Kind: finding.KindKeyFile,
			Status: apply.StatusWriting,
		},
	}
	for _, it := range items {
		if err := s.InsertApplyItem(ctx, id, it); err != nil {
			t.Fatalf("InsertApplyItem(%d, %+v) error = %v", id, it, err)
		}
	}
	got, err := s.ApplyItems(ctx, id)
	if err != nil {
		t.Fatalf("ApplyItems(%d) error = %v", id, err)
	}
	want := []apply.Item{items[3], items[2], items[1], items[0]}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ApplyItems(%d) mismatch (-want +got):\n%s", id, diff)
	}

	empty := requestCheck(t, s, otherPid)
	if got, err := s.ApplyItems(ctx, empty); err != nil || len(got) != 0 {
		t.Errorf("ApplyItems(run without items) = %v, %v, want none", got, err)
	}
	if _, err := s.ApplyItems(ctx, 424242); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("ApplyItems(424242) error = %v, want ErrNotFound", err)
	}
}

// newProjectWithScan registers a project with one succeeded scan of mixed.
func newProjectWithScan(t *testing.T, s *db.Store, name string) project.ID {
	t.Helper()
	pid := newProject(t, s, name)
	save(t, s, pid, mixed()...)
	return pid
}

// TestProjectLock checks that the project row serialises what an apply
// excludes: a transaction that holds the row FOR UPDATE makes a scan insert
// and a decision wait, and they then see what it committed; a transaction
// that inserted a scan makes a check request wait.
func TestProjectLock(t *testing.T) {
	s := newMigratedStore(t)
	other := db.NewTestStoreLike(t, s)
	ctx := t.Context()

	t.Run("scan waits for a check", func(t *testing.T) {
		pid, sid, _, _ := decidedProject(t, s, "scan-waits")
		held := holdWithRun(t, s, pid, sid)
		done := make(chan error, 1)
		go func() {
			_, err := other.RequestScan(ctx, pid, tester)
			done <- err
		}()
		releaseWhenBlocked(t, s, held, done)
		if err := <-done; !errors.Is(err, db.ErrApplyActive) {
			t.Errorf("RequestScan() behind a check error = %v, want ErrApplyActive", err)
		}
	})
	t.Run("decision waits for a check", func(t *testing.T) {
		pid, sid, _, ph := decidedProject(t, s, "decision-waits")
		held := holdWithRun(t, s, pid, sid)
		done := make(chan error, 1)
		go func() {
			done <- other.Decide(ctx, pid, []triage.BundleID{ph}, triage.Decision{Verdict: triage.VerdictFalsePositive}, tester)
		}()
		releaseWhenBlocked(t, s, held, done)
		if err := <-done; !errors.Is(err, db.ErrApplyActive) {
			t.Errorf("Decide() behind a check error = %v, want ErrApplyActive", err)
		}
	})
	t.Run("check waits for a scan", func(t *testing.T) {
		pid := newProjectWithScan(t, s, "check-waits")
		held, err := s.LockProject(ctx, pid, "FOR KEY SHARE")
		if err != nil {
			t.Fatalf("LockProject(%d) error = %v", pid, err)
		}
		if err := held.Exec(ctx, "INSERT INTO scan (project_id, requested_by) VALUES ($1, 'fake-subject')", pid); err != nil {
			t.Fatalf("inserting a scan error = %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := other.RequestApplyCheck(ctx, pid, tester)
			done <- err
		}()
		releaseWhenBlocked(t, s, held, done)
		if err := <-done; !errors.Is(err, db.ErrScanActive) {
			t.Errorf("RequestApplyCheck() behind a scan error = %v, want ErrScanActive", err)
		}
	})
}

// holdWithRun locks the project FOR UPDATE and inserts a checking run of
// it, uncommitted.
func holdWithRun(t *testing.T, s *db.Store, pid project.ID, sid scan.ID) *db.HeldLock {
	t.Helper()
	ctx := t.Context()
	held, err := s.LockProject(ctx, pid, "FOR UPDATE")
	if err != nil {
		t.Fatalf("LockProject(%d) error = %v", pid, err)
	}
	if err := held.Exec(ctx, `
		INSERT INTO apply_run (project_id, scan_id, plan_digest, requested_by, requester_name)
		VALUES ($1, $2, $3, 'fake-subject', 'fake-user')`, pid, sid, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("inserting a run error = %v", err)
	}
	return held
}

// releaseWhenBlocked waits until another backend waits for held's lock,
// failing if the call returns first or nothing waits within 10s, and then
// commits held.
func releaseWhenBlocked(t *testing.T, s *db.Store, held *db.HeldLock, done <-chan error) {
	t.Helper()
	ctx := t.Context()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("call returned %v while the lock was held, want it to wait", err)
		case <-timeout.C:
			t.Fatalf("no call waited for the held lock within 10s")
		default:
		}
		blocking, err := held.Blocking(ctx, s)
		if err != nil {
			t.Fatalf("Blocking() error = %v", err)
		}
		if blocking {
			break
		}
	}
	if err := held.Commit(ctx); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}
