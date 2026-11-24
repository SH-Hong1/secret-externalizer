package db_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// requestScan registers a project named shop and requests a scan of it.
func requestScan(t *testing.T, s *db.Store) scan.ID {
	t.Helper()
	id, err := s.RequestScan(t.Context(), newProject(t, s, "shop"), audit.Actor{Subject: "fake-subject"})
	if err != nil {
		t.Fatalf("RequestScan() error = %v", err)
	}
	return id
}

// assertJobState fails the test unless the job of scan id is in state want.
func assertJobState(t *testing.T, s *db.Store, id scan.ID, want string) {
	t.Helper()
	got, err := s.JobState(t.Context(), int64(id))
	if err != nil {
		t.Fatalf("JobState(%d) error = %v", id, err)
	}
	if got != want {
		t.Errorf("JobState(%d) = %q, want %q", id, got, want)
	}
}

func TestClaimJob_NoJob(t *testing.T) {
	s := newMigratedStore(t)
	_, err := s.ClaimJob(t.Context(), "worker-a", time.Minute)
	if !errors.Is(err, job.ErrNoJob) {
		t.Errorf("ClaimJob() on an empty queue error = %v, want ErrNoJob", err)
	}
}

func TestClaimJob_ClaimsOnce(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	got, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if got.ScanID != int64(id) || got.Attempts != 1 {
		t.Errorf("ClaimJob() = %+v, want scan %d and 1 attempt", got, id)
	}
	assertJobState(t, s, id, "running")
	if _, err := s.ClaimJob(ctx, "worker-b", time.Minute); !errors.Is(err, job.ErrNoJob) {
		t.Errorf("ClaimJob() of a leased job error = %v, want ErrNoJob", err)
	}
}

func TestClaimJob_OneWinner(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	requestScan(t, s)

	const claimers = 10
	errs := make([]error, claimers)
	var wg sync.WaitGroup
	for i := range claimers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.ClaimJob(ctx, "worker", time.Minute)
		}()
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case !errors.Is(err, job.ErrNoJob):
			t.Errorf("ClaimJob() call %d error = %v, want nil or ErrNoJob", i, err)
		}
	}
	if winners != 1 {
		t.Errorf("ClaimJob() winners among %d callers = %d, want 1", claimers, winners)
	}
}

func TestClaimJob_ReclaimsExpiredLease(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	first, err := s.ClaimJob(ctx, "worker-a", 0)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	second, err := s.ClaimJob(ctx, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(worker-b) error = %v", err)
	}
	if second.ID != first.ID || second.ScanID != int64(id) || second.Attempts != 2 {
		t.Errorf("ClaimJob(worker-b) = %+v, want job %d of scan %d with 2 attempts", second, first.ID, id)
	}
}

func TestClaimJob_GivesUpAfterThree(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	for i := 1; i <= 3; i++ {
		got, err := s.ClaimJob(ctx, "worker", 0)
		if err != nil {
			t.Fatalf("ClaimJob() call %d error = %v", i, err)
		}
		if got.Attempts != i {
			t.Fatalf("ClaimJob() call %d Attempts = %d, want %d", i, got.Attempts, i)
		}
	}
	if _, err := s.ClaimJob(ctx, "worker", time.Minute); !errors.Is(err, job.ErrNoJob) {
		t.Fatalf("ClaimJob() call 4 error = %v, want ErrNoJob", err)
	}

	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusFailed || got.FailureReason != "worker stopped during the scan three times" || got.FinishedAt.IsZero() {
		t.Errorf("Scan(%d) = %+v, want failed with the three-attempts reason and a finish time", id, got)
	}
	assertJobState(t, s, id, "failed")
	// The failed scan no longer blocks the project.
	if _, err := s.RequestScan(ctx, got.ProjectID, audit.Actor{Subject: "fake-subject"}); err != nil {
		t.Errorf("RequestScan() after the scan failed error = %v, want nil", err)
	}
}

func TestExtendJob_LeaseLost(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	requestScan(t, s)

	a, err := s.ClaimJob(ctx, "worker-a", 0)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if _, err := s.ClaimJob(ctx, "worker-b", time.Minute); err != nil {
		t.Fatalf("ClaimJob(worker-b) error = %v", err)
	}
	if err := s.ExtendJob(ctx, a.ID, "worker-a", time.Minute); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("ExtendJob(worker-a, taken over) error = %v, want ErrLeaseLost", err)
	}
	if err := s.ExtendJob(ctx, a.ID, "worker-b", time.Minute); err != nil {
		t.Errorf("ExtendJob(worker-b, holder) error = %v, want nil", err)
	}
	if err := s.ExtendJob(ctx, 424242, "worker-b", time.Minute); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("ExtendJob(unknown job) error = %v, want ErrLeaseLost", err)
	}
}

func TestExtendJob_Closed(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if err := s.StartScan(ctx, id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}
	if err := s.FailScan(ctx, j.ID, "worker-a", id, "scan failed"); err != nil {
		t.Fatalf("FailScan(worker-a) error = %v", err)
	}

	if err := s.ExtendJob(ctx, j.ID, "worker-a", time.Minute); !errors.Is(err, job.ErrClosed) {
		t.Errorf("ExtendJob(worker-a, closed by worker-a) error = %v, want ErrClosed", err)
	}
	if err := s.ExtendJob(ctx, j.ID, "worker-b", time.Minute); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("ExtendJob(worker-b, closed by worker-a) error = %v, want ErrLeaseLost", err)
	}
}

// TestExtendJob_WhileCloseCommits renews a job while the worker's own close
// of it holds the row: the renewal waits for the close and then finds the
// job closed, not taken over.
func TestExtendJob_WhileCloseCommits(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	requestScan(t, s)

	j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	held, err := s.HoldJobClose(ctx, j.ID)
	if err != nil {
		t.Fatalf("HoldJobClose(%d) error = %v", j.ID, err)
	}
	extended := make(chan error, 1)
	go func() { extended <- s.ExtendJob(ctx, j.ID, "worker-a", time.Minute) }()
	if err := held.WaitBlocked(ctx, s); err != nil {
		t.Fatalf("WaitBlocked() error = %v", err)
	}
	if err := held.Commit(ctx); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := <-extended; !errors.Is(err, job.ErrClosed) {
		t.Errorf("ExtendJob(worker-a) during its own close error = %v, want ErrClosed", err)
	}
}

func TestFailScan_OnlyByLeaseHolder(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	a, err := s.ClaimJob(ctx, "worker-a", 0)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if _, err := s.ClaimJob(ctx, "worker-b", time.Minute); err != nil {
		t.Fatalf("ClaimJob(worker-b) error = %v", err)
	}
	if err := s.StartScan(ctx, id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}

	if err := s.FailScan(ctx, a.ID, "worker-a", id, "scan failed"); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("FailScan(worker-a, taken over) error = %v, want ErrLeaseLost", err)
	}
	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusRunning || got.FailureReason != "" {
		t.Errorf("Scan(%d) after a refused FailScan = %+v, want running with no reason", id, got)
	}
	assertJobState(t, s, id, "running")

	if err := s.FailScan(ctx, a.ID, "worker-b", id, "scan failed"); err != nil {
		t.Fatalf("FailScan(worker-b, holder) error = %v", err)
	}
	got, err = s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusFailed || got.FailureReason != "scan failed" || got.FinishedAt.IsZero() {
		t.Errorf("Scan(%d) after FailScan = %+v, want failed with the reason and a finish time", id, got)
	}
	assertJobState(t, s, id, "failed")
}

// assertQueued fails the test unless the job of scan id is queued with
// attempts attempts and no lease, and the scan is queued with no start time.
func assertQueued(t *testing.T, s *db.Store, id scan.ID, attempts int) {
	t.Helper()
	got, err := s.Job(t.Context(), int64(id))
	if err != nil {
		t.Fatalf("Job(%d) error = %v", id, err)
	}
	if got.State != "queued" || got.Attempts != attempts || got.LockedBy != nil || got.LockedUntil != nil {
		t.Errorf("Job(%d) = %+v, want queued with %d attempts and no lease", id, got, attempts)
	}
	sc, err := s.Scan(t.Context(), id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if sc.Status != scan.StatusQueued || !sc.StartedAt.IsZero() {
		t.Errorf("Scan(%d) = %+v, want queued with no start time", id, sc)
	}
}

func TestReleaseJob_RequeuesJobAndScan(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	a, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if err := s.StartScan(ctx, id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}
	if err := s.ReleaseJob(ctx, a.ID, "worker-a"); err != nil {
		t.Fatalf("ReleaseJob(worker-a, holder) error = %v", err)
	}
	assertQueued(t, s, id, 0)

	b, err := s.ClaimJob(ctx, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(worker-b) after release error = %v", err)
	}
	if b.ID != a.ID || b.Attempts != 1 {
		t.Errorf("ClaimJob(worker-b) after release = %+v, want job %d with 1 attempt", b, a.ID)
	}
}

func TestReleaseJob_OnlyByLeaseHolder(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	a, err := s.ClaimJob(ctx, "worker-a", 0)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if _, err := s.ClaimJob(ctx, "worker-b", time.Minute); err != nil {
		t.Fatalf("ClaimJob(worker-b) error = %v", err)
	}
	if err := s.StartScan(ctx, id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}
	before, err := s.Job(ctx, int64(id))
	if err != nil {
		t.Fatalf("Job(%d) error = %v", id, err)
	}

	if err := s.ReleaseJob(ctx, a.ID, "worker-a"); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("ReleaseJob(worker-a, taken over) error = %v, want ErrLeaseLost", err)
	}
	after, err := s.Job(ctx, int64(id))
	if err != nil {
		t.Fatalf("Job(%d) error = %v", id, err)
	}
	if after.State != "running" || after.Attempts != before.Attempts || after.LockedBy == nil || *after.LockedBy != "worker-b" ||
		after.LockedUntil == nil || !after.LockedUntil.Equal(*before.LockedUntil) {
		t.Errorf("Job(%d) after a refused ReleaseJob = %+v, want it unchanged from %+v", id, after, before)
	}
	sc, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if sc.Status != scan.StatusRunning || sc.StartedAt.IsZero() {
		t.Errorf("Scan(%d) after a refused ReleaseJob = %+v, want still running", id, sc)
	}
}

func TestReleaseJob_AfterDone(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id := requestScan(t, s)

	a, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if err := s.StartScan(ctx, id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}
	if err := s.SaveResult(ctx, a.ID, "worker-a", id, scan.Result{}); err != nil {
		t.Fatalf("SaveResult(%d) error = %v", id, err)
	}

	if err := s.ReleaseJob(ctx, a.ID, "worker-a"); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("ReleaseJob(done job) error = %v, want ErrLeaseLost", err)
	}
	assertJobState(t, s, id, "done")
	sc, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if sc.Status != scan.StatusSucceeded {
		t.Errorf("Scan(%d) after a refused ReleaseJob = %+v, want succeeded", id, sc)
	}
}

func TestMigration0007(t *testing.T) {
	s := db.NewTestStore(t)
	ctx := t.Context()
	if err := s.MigrateThrough(ctx, 6); err != nil {
		t.Fatalf("MigrateThrough(6) error = %v", err)
	}
	var ids []scan.ID
	for _, c := range []struct{ project, status, jobState string }{
		{"done", "succeeded", "done"},
		{"running", "running", "running"},
		{"queued", "queued", "queued"},
	} {
		id, err := s.InsertRawScanJob(ctx, newProject(t, s, c.project), c.status, c.jobState)
		if err != nil {
			t.Fatalf("InsertRawScanJob(%s) error = %v", c.project, err)
		}
		ids = append(ids, id)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() over 0006 error = %v", err)
	}

	for _, id := range ids {
		if got, err := s.JobKind(ctx, int64(id)); err != nil || got != "scan" {
			t.Errorf("JobKind(%d) after 0007 = %q, %v, want scan", id, got, err)
		}
	}
	got, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() after 0007 error = %v", err)
	}
	if got.Kind != job.KindScan || got.ScanID != int64(ids[2]) || got.ApplyID != 0 || got.Attempts != 1 {
		t.Errorf("ClaimJob() after 0007 = %+v, want the queued scan job of scan %d", got, ids[2])
	}
	if _, err := s.ClaimJob(ctx, "worker-a", time.Minute); !errors.Is(err, job.ErrNoJob) {
		t.Errorf("ClaimJob() of the leased and done jobs error = %v, want ErrNoJob", err)
	}

	applyID := newApplyRun(t, s, "apply", "running", job.KindApply)
	if err := s.InsertApplyRecords(ctx, applyID, "conf/db.properties"); err != nil {
		t.Fatalf("InsertApplyRecords(%d) error = %v", applyID, err)
	}
	for _, c := range []struct {
		hash string
		ok   bool
	}{{strings.Repeat("d", 64), true}, {"", true}, {"not-a-hash", false}} {
		err := s.SetPrevAfterHash(ctx, applyID, "conf/db.properties", c.hash)
		if (err == nil) != c.ok {
			t.Errorf("SetPrevAfterHash(%q) error = %v, want accepted %t", c.hash, err, c.ok)
		}
	}
}

// newApplyRun registers a project with a scan and stores an apply run of it
// in state, with a job of kind for the run. It returns the run's ID.
func newApplyRun(t *testing.T, s *db.Store, name, state string, kind job.Kind) int64 {
	t.Helper()
	ctx := t.Context()
	projectID := newProject(t, s, name)
	scanID, err := s.InsertRawScan(ctx, projectID)
	if err != nil {
		t.Fatalf("InsertRawScan(%s) error = %v", name, err)
	}
	applyID, err := s.InsertApplyRun(ctx, projectID, scanID, state)
	if err != nil {
		t.Fatalf("InsertApplyRun(%s) error = %v", name, err)
	}
	if _, err := s.InsertApplyJob(ctx, applyID, string(kind)); err != nil {
		t.Fatalf("InsertApplyJob(%d, %s) error = %v", applyID, kind, err)
	}
	return applyID
}

func TestClaimJob_Kinds(t *testing.T) {
	t.Run("in order", func(t *testing.T) {
		s := newMigratedStore(t)
		ctx := t.Context()
		scanID := requestScan(t, s)
		checkID := newApplyRun(t, s, "check", "checking", job.KindApplyCheck)
		applyID := newApplyRun(t, s, "apply", "queued", job.KindApply)

		want := []job.Job{
			{Kind: job.KindScan, ScanID: int64(scanID), Attempts: 1},
			{Kind: job.KindApplyCheck, ApplyID: checkID, Attempts: 1},
			{Kind: job.KindApply, ApplyID: applyID, Attempts: 1},
		}
		var prev int64
		for i, w := range want {
			got, err := s.ClaimJob(ctx, "worker-a", time.Minute)
			if err != nil {
				t.Fatalf("ClaimJob() call %d error = %v", i+1, err)
			}
			if got.ID <= prev {
				t.Errorf("ClaimJob() call %d job ID = %d, want after %d", i+1, got.ID, prev)
			}
			prev = got.ID
			w.ID = got.ID
			if got != w {
				t.Errorf("ClaimJob() call %d = %+v, want %+v", i+1, got, w)
			}
		}
	})

	t.Run("only the kinds asked for", func(t *testing.T) {
		s := newMigratedStore(t)
		ctx := t.Context()
		scanID := requestScan(t, s)
		applyID := newApplyRun(t, s, "apply", "queued", job.KindApply)

		got, err := s.ClaimJob(ctx, "applier", time.Minute, job.KindApplyCheck, job.KindApply)
		if err != nil {
			t.Fatalf("ClaimJob(apply-check, apply) error = %v", err)
		}
		if got.Kind != job.KindApply || got.ApplyID != applyID {
			t.Errorf("ClaimJob(apply-check, apply) = %+v, want the apply job of run %d", got, applyID)
		}
		if _, err := s.ClaimJob(ctx, "applier", time.Minute, job.KindApplyCheck, job.KindApply); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("ClaimJob(apply-check, apply) with only a scan queued error = %v, want ErrNoJob", err)
		}
		got, err = s.ClaimJob(ctx, "scanner", time.Minute, job.KindScan)
		if err != nil {
			t.Fatalf("ClaimJob(scan) error = %v", err)
		}
		if got.Kind != job.KindScan || got.ScanID != int64(scanID) {
			t.Errorf("ClaimJob(scan) = %+v, want the job of scan %d", got, scanID)
		}
	})

	t.Run("a claim of other kinds gives up an exhausted job", func(t *testing.T) {
		s := newMigratedStore(t)
		ctx := t.Context()
		applyID := newApplyRun(t, s, "shop", "queued", job.KindApply)
		for i := 1; i <= 3; i++ {
			if _, err := s.ClaimJob(ctx, "applier", 0, job.KindApply); err != nil {
				t.Fatalf("ClaimJob(apply) call %d error = %v", i, err)
			}
		}
		if _, err := s.ClaimJob(ctx, "scanner", time.Minute, job.KindScan); !errors.Is(err, job.ErrNoJob) {
			t.Fatalf("ClaimJob(scan) error = %v, want ErrNoJob", err)
		}
		run, err := s.ApplyRunState(ctx, applyID)
		if err != nil {
			t.Fatalf("ApplyRunState(%d) error = %v", applyID, err)
		}
		if run.State != "failed" {
			t.Errorf("ApplyRunState(%d) after a scan-only claim = %q, want failed", applyID, run.State)
		}
	})

	for _, c := range []struct {
		kind   job.Kind
		state  string
		reason string
	}{
		{job.KindApply, "running", "worker stopped during the apply three times"},
		{job.KindApply, "queued", "worker stopped during the apply three times"},
		{job.KindApplyCheck, "checking", "worker stopped during the check three times"},
	} {
		t.Run(string(c.kind)+" "+c.state+" gives up after three", func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			applyID := newApplyRun(t, s, "shop", c.state, c.kind)
			if err := s.InsertApplyRecords(ctx, applyID, "conf/db.properties"); err != nil {
				t.Fatalf("InsertApplyRecords(%d) error = %v", applyID, err)
			}

			for i := 1; i <= 3; i++ {
				if _, err := s.ClaimJob(ctx, "worker", 0); err != nil {
					t.Fatalf("ClaimJob() call %d error = %v", i, err)
				}
			}
			if _, err := s.ClaimJob(ctx, "worker", time.Minute); !errors.Is(err, job.ErrNoJob) {
				t.Fatalf("ClaimJob() call 4 error = %v, want ErrNoJob", err)
			}

			run, err := s.ApplyRunState(ctx, applyID)
			if err != nil {
				t.Fatalf("ApplyRun(%d) error = %v", applyID, err)
			}
			if run.State != "failed" || run.FailureReason != c.reason || run.FinishedAt == nil {
				t.Errorf("ApplyRun(%d) = %+v, want failed with %q and a finish time", applyID, run, c.reason)
			}
			jr, err := s.ApplyJob(ctx, applyID, string(c.kind))
			if err != nil {
				t.Fatalf("ApplyJob(%d) error = %v", applyID, err)
			}
			if jr.State != "failed" {
				t.Errorf("ApplyJob(%d) state = %q, want failed", applyID, jr.State)
			}
			statuses, written, err := s.ApplyRecords(ctx, applyID)
			if err != nil {
				t.Fatalf("ApplyRecords(%d) error = %v", applyID, err)
			}
			if diff := cmp.Diff([]string{"writing"}, statuses); diff != "" {
				t.Errorf("ApplyRecords(%d) item statuses after giving up (-want +got):\n%s", applyID, diff)
			}
			if diff := cmp.Diff([]bool{false}, written); diff != "" {
				t.Errorf("ApplyRecords(%d) file written flags after giving up (-want +got):\n%s", applyID, diff)
			}
		})
	}
}

// TestClaimJob_GivesUpScanAndApplyTogether gives up a scan job and an apply
// job in one claim, where the scan's ID and the run's ID are the same
// number, and checks that each record gets only its own reason.
func TestClaimJob_GivesUpScanAndApplyTogether(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	projectID := newProject(t, s, "shop")
	scanID, err := s.InsertRawScan(ctx, projectID)
	if err != nil {
		t.Fatalf("InsertRawScan() error = %v", err)
	}
	applyID, err := s.InsertApplyRun(ctx, projectID, scanID, "running")
	if err != nil {
		t.Fatalf("InsertApplyRun() error = %v", err)
	}
	if int64(scanID) != applyID {
		t.Fatalf("scan ID %d and run ID %d differ, want them equal for this test", scanID, applyID)
	}
	if err := s.InsertScanJob(ctx, scanID); err != nil {
		t.Fatalf("InsertScanJob(%d) error = %v", scanID, err)
	}
	if _, err := s.InsertApplyJob(ctx, applyID, string(job.KindApply)); err != nil {
		t.Fatalf("InsertApplyJob(%d) error = %v", applyID, err)
	}
	if err := s.ExhaustJobs(ctx); err != nil {
		t.Fatalf("ExhaustJobs() error = %v", err)
	}

	if _, err := s.ClaimJob(ctx, "worker", time.Minute); !errors.Is(err, job.ErrNoJob) {
		t.Fatalf("ClaimJob() of exhausted jobs error = %v, want ErrNoJob", err)
	}
	status, reason, err := s.ScanStatus(ctx, scanID)
	if err != nil {
		t.Fatalf("ScanStatus(%d) error = %v", scanID, err)
	}
	if status != "failed" || reason != "worker stopped during the scan three times" {
		t.Errorf("ScanStatus(%d) = %q, %q, want failed with the scan reason", scanID, status, reason)
	}
	run, err := s.ApplyRunState(ctx, applyID)
	if err != nil {
		t.Fatalf("ApplyRunState(%d) error = %v", applyID, err)
	}
	if run.State != "failed" || run.FailureReason != "worker stopped during the apply three times" {
		t.Errorf("ApplyRunState(%d) = %+v, want failed with the apply reason", applyID, run)
	}
}

func TestCloseApplyJob_Kind(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	applyID := newApplyRun(t, s, "shop", "checking", job.KindApplyCheck)
	if _, err := s.InsertApplyJob(ctx, applyID, string(job.KindApply)); err != nil {
		t.Fatalf("InsertApplyJob(%d, apply) error = %v", applyID, err)
	}
	check, err := s.ClaimJob(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(check) error = %v", err)
	}
	run, err := s.ClaimJob(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(apply) error = %v", err)
	}
	if check.Kind != job.KindApplyCheck || run.Kind != job.KindApply {
		t.Fatalf("ClaimJob() kinds = %s, %s, want apply-check, apply", check.Kind, run.Kind)
	}
	write := func(pgx.Tx) error { return nil }

	if err := s.CloseApplyJob(ctx, check.ID, "worker", applyID, job.KindApply, "done", write); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("CloseApplyJob(check job as apply) error = %v, want ErrLeaseLost", err)
	}
	if err := s.CloseApplyJob(ctx, run.ID, "worker", applyID, job.KindApplyCheck, "done", write); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("CloseApplyJob(apply job as apply-check) error = %v, want ErrLeaseLost", err)
	}
	for _, k := range []job.Kind{job.KindApplyCheck, job.KindApply} {
		if jr, err := s.ApplyJob(ctx, applyID, string(k)); err != nil || jr.State != "running" {
			t.Errorf("ApplyJob(%d, %s) after refused closes = %+v, %v, want running", applyID, k, jr, err)
		}
	}
	if err := s.CloseApplyJob(ctx, check.ID, "worker", applyID, job.KindApplyCheck, "done", write); err != nil {
		t.Errorf("CloseApplyJob(check job as apply-check) error = %v", err)
	}
	if err := s.CloseApplyJob(ctx, run.ID, "worker", applyID, job.KindApply, "done", write); err != nil {
		t.Errorf("CloseApplyJob(apply job as apply) error = %v", err)
	}
}

func TestReleaseJob_Apply(t *testing.T) {
	for _, c := range []struct {
		kind  job.Kind
		state string
		want  string
	}{
		{job.KindApplyCheck, "checking", "checking"},
		{job.KindApply, "running", "queued"},
	} {
		t.Run(string(c.kind), func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			applyID := newApplyRun(t, s, "shop", c.state, c.kind)

			a, err := s.ClaimJob(ctx, "worker-a", time.Minute)
			if err != nil {
				t.Fatalf("ClaimJob(worker-a) error = %v", err)
			}
			if err := s.ReleaseJob(ctx, a.ID, "worker-a"); err != nil {
				t.Fatalf("ReleaseJob(worker-a, holder) error = %v", err)
			}
			jr, err := s.ApplyJob(ctx, applyID, string(c.kind))
			if err != nil {
				t.Fatalf("ApplyJob(%d) error = %v", applyID, err)
			}
			if jr.State != "queued" || jr.Attempts != 0 || jr.LockedBy != nil || jr.LockedUntil != nil {
				t.Errorf("ApplyJob(%d) after release = %+v, want queued with 0 attempts and no lease", applyID, jr)
			}
			run, err := s.ApplyRunState(ctx, applyID)
			if err != nil {
				t.Fatalf("ApplyRun(%d) error = %v", applyID, err)
			}
			if run.State != c.want || run.StartedAt != nil {
				t.Errorf("ApplyRun(%d) after release = %+v, want %s with no start time", applyID, run, c.want)
			}

			b, err := s.ClaimJob(ctx, "worker-b", time.Minute)
			if err != nil {
				t.Fatalf("ClaimJob(worker-b) after release error = %v", err)
			}
			if b.ID != a.ID || b.Kind != c.kind || b.ApplyID != applyID || b.Attempts != 1 {
				t.Errorf("ClaimJob(worker-b) after release = %+v, want job %d of run %d with 1 attempt", b, a.ID, applyID)
			}
		})
	}
}

func TestCloseApplyJob(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	applyID := newApplyRun(t, s, "shop", "running", job.KindApply)

	a, err := s.ClaimJob(ctx, "worker-a", 0)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if _, err := s.ClaimJob(ctx, "worker-b", time.Minute); err != nil {
		t.Fatalf("ClaimJob(worker-b) error = %v", err)
	}

	called := false
	write := func(pgx.Tx) error {
		called = true
		return nil
	}
	if err := s.CloseApplyJob(ctx, a.ID, "worker-a", applyID, job.KindApply, "done", write); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("CloseApplyJob(worker-a, taken over) error = %v, want ErrLeaseLost", err)
	}
	if called {
		t.Errorf("CloseApplyJob(worker-a, taken over) called write, want it not called")
	}
	if err := s.CloseApplyJob(ctx, a.ID, "worker-b", applyID+1, job.KindApply, "done", write); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("CloseApplyJob(worker-b, other run) error = %v, want ErrLeaseLost", err)
	}
	if called {
		t.Errorf("CloseApplyJob(worker-b, other run) called write, want it not called")
	}
	if jr, err := s.ApplyJob(ctx, applyID, "apply"); err != nil || jr.State != "running" {
		t.Errorf("ApplyJob(%d) after refused closes = %+v, %v, want running", applyID, jr, err)
	}

	if err := s.CloseApplyJob(ctx, a.ID, "worker-b", applyID, job.KindApply, "done", write); err != nil {
		t.Fatalf("CloseApplyJob(worker-b, holder) error = %v", err)
	}
	if !called {
		t.Errorf("CloseApplyJob(worker-b, holder) did not call write")
	}
	if jr, err := s.ApplyJob(ctx, applyID, "apply"); err != nil || jr.State != "done" {
		t.Errorf("ApplyJob(%d) after close = %+v, %v, want done", applyID, jr, err)
	}

	fail := errors.New("fake write failure")
	applyID2 := newApplyRun(t, s, "shop2", "running", job.KindApply)
	c, err := s.ClaimJob(ctx, "worker-c", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob(worker-c) error = %v", err)
	}
	if err := s.CloseApplyJob(ctx, c.ID, "worker-c", applyID2, job.KindApply, "done", func(pgx.Tx) error { return fail }); !errors.Is(err, fail) {
		t.Fatalf("CloseApplyJob(failing write) error = %v, want the write's error", err)
	}
	if jr, err := s.ApplyJob(ctx, applyID2, "apply"); err != nil || jr.State != "running" {
		t.Errorf("ApplyJob(%d) after a failing write = %+v, %v, want still running", applyID2, jr, err)
	}
}
