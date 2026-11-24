package db_test

import (
	"testing"
	"time"
)

func TestWorkerAlive(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()

	alive := func(within time.Duration) bool {
		t.Helper()
		got, err := s.WorkerAlive(ctx, within)
		if err != nil {
			t.Fatalf("WorkerAlive(%s) error = %v", within, err)
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
	got, err := s.WorkerAlive(ctx, 0)
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
	got, err := s.WorkerAlive(ctx, 0)
	if err != nil {
		t.Fatalf("WorkerAlive(0) error = %v", err)
	}
	if got {
		t.Error("WorkerAlive(0) with only a lapsed lease = true, want false")
	}
}
