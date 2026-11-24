package db_test

import (
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/job"
)

func TestWorkerAlive(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()

	alive := func(within time.Duration) bool {
		t.Helper()
		got, err := s.WorkerAlive(ctx, job.KindScan, within)
		if err != nil {
			t.Fatalf("WorkerAlive(scan, %s) error = %v", within, err)
		}
		return got
	}

	if alive(time.Hour) {
		t.Error("WorkerAlive(1h) with no worker seen = true, want false")
	}
	if err := s.Beat(ctx, "worker-a"); err != nil {
		t.Fatalf("Beat() error = %v", err)
	}
	if err := s.Beat(ctx, "worker-a"); err != nil {
		t.Fatalf("Beat() again error = %v", err)
	}
	if !alive(time.Hour) {
		t.Error("WorkerAlive(1h) after a beat = false, want true")
	}
	if alive(0) {
		t.Error("WorkerAlive(0) after a beat = true, want false: the beat is older than the window")
	}
}

func TestWorkerAlive_RunningJobCounts(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	requestScan(t, s)

	if _, err := s.ClaimJob(ctx, "worker-a", time.Minute); err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	got, err := s.WorkerAlive(ctx, job.KindScan, 0)
	if err != nil {
		t.Fatalf("WorkerAlive(0) error = %v", err)
	}
	if !got {
		t.Error("WorkerAlive(0) with a job under a live lease = false, want true")
	}
}

func TestWorkerAlive_ExpiredLeaseDoesNotCount(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	requestScan(t, s)

	if _, err := s.ClaimJob(ctx, "worker-a", time.Nanosecond); err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	got, err := s.WorkerAlive(ctx, job.KindScan, 0)
	if err != nil {
		t.Fatalf("WorkerAlive(0) error = %v", err)
	}
	if got {
		t.Error("WorkerAlive(0) with only a lapsed lease = true, want false")
	}
}

func TestWorkerAlive_ByKind(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()

	alive := func(k job.Kind) bool {
		t.Helper()
		got, err := s.WorkerAlive(ctx, k, time.Hour)
		if err != nil {
			t.Fatalf("WorkerAlive(%s, 1h) error = %v", k, err)
		}
		return got
	}

	if err := s.Beat(ctx, "scanner", job.KindScan); err != nil {
		t.Fatalf("Beat(scanner, scan) error = %v", err)
	}
	if !alive(job.KindScan) {
		t.Error("WorkerAlive(scan, 1h) after a scan worker's beat = false, want true")
	}
	if alive(job.KindApply) {
		t.Error("WorkerAlive(apply, 1h) after only a scan worker's beat = true, want false")
	}
	if err := s.Beat(ctx, "applier", job.KindApplyCheck, job.KindApply); err != nil {
		t.Fatalf("Beat(applier, apply-check, apply) error = %v", err)
	}
	if !alive(job.KindApply) {
		t.Error("WorkerAlive(apply, 1h) after an apply worker's beat = false, want true")
	}
}

func TestWorkerAlive_NoKindsMeansEvery(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	if err := s.Beat(ctx, "worker-a"); err != nil {
		t.Fatalf("Beat(worker-a) error = %v", err)
	}
	for _, k := range job.Kinds() {
		got, err := s.WorkerAlive(ctx, k, time.Hour)
		if err != nil {
			t.Fatalf("WorkerAlive(%s, 1h) error = %v", k, err)
		}
		if !got {
			t.Errorf("WorkerAlive(%s, 1h) after a beat with no kinds = false, want true", k)
		}
	}
}

func TestWorkerAlive_RunningJobCountsForItsKind(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	requestScan(t, s)

	if _, err := s.ClaimJob(ctx, "worker-a", time.Minute); err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	got, err := s.WorkerAlive(ctx, job.KindApply, 0)
	if err != nil {
		t.Fatalf("WorkerAlive(apply, 0) error = %v", err)
	}
	if got {
		t.Error("WorkerAlive(apply, 0) with only a scan job under a live lease = true, want false")
	}
}
