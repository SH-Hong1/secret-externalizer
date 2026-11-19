package db_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// requestScan registers a project named shop and requests a scan of it.
func requestScan(t *testing.T, s *db.Store) scan.ID {
	t.Helper()
	id, err := s.RequestScan(t.Context(), newProject(t, s, "shop"), "fake-subject")
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
	if _, err := s.RequestScan(ctx, got.ProjectID, "fake-subject"); err != nil {
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
