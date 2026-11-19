package db_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// newProject registers a project with a name of its own and returns its ID.
func newProject(t *testing.T, s *db.Store, name string) project.ID {
	t.Helper()
	id, err := s.CreateProject(t.Context(), project.Project{Name: name, Path: name, CreatedBy: "fake-subject"})
	if err != nil {
		t.Fatalf("CreateProject(%q) error = %v", name, err)
	}
	return id
}

func TestRequestScan_CreatesScanAndJob(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")

	id, err := s.RequestScan(ctx, pid, "fake-subject")
	if err != nil {
		t.Fatalf("RequestScan(%d) error = %v", pid, err)
	}
	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusQueued || got.ProjectID != pid || got.RequestedBy != "fake-subject" || got.RequestedAt.IsZero() {
		t.Errorf("Scan(%d) = %+v, want a queued scan of project %d requested by fake-subject", id, got, pid)
	}
	if !got.StartedAt.IsZero() || !got.FinishedAt.IsZero() {
		t.Errorf("Scan(%d) started at %v, finished at %v, want both unset", id, got.StartedAt, got.FinishedAt)
	}
	state, err := s.JobState(ctx, int64(id))
	if err != nil {
		t.Fatalf("JobState(%d) error = %v", id, err)
	}
	if state != "queued" {
		t.Errorf("JobState(%d) = %q, want %q", id, state, "queued")
	}
	list, err := s.ScansByProject(ctx, pid)
	if err != nil {
		t.Fatalf("ScansByProject(%d) error = %v", pid, err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Errorf("ScansByProject(%d) = %+v, want only scan %d", pid, list, id)
	}
}

func TestRequestScan_UnknownProject(t *testing.T) {
	s := newMigratedStore(t)
	_, err := s.RequestScan(t.Context(), 424242, "fake-subject")
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RequestScan(424242) error = %v, want ErrNotFound", err)
	}
}

func TestScan_NotFound(t *testing.T) {
	s := newMigratedStore(t)
	_, err := s.Scan(t.Context(), 424242)
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Scan(424242) error = %v, want ErrNotFound", err)
	}
}

func TestRequestScan_ActiveScan(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")

	first, err := s.RequestScan(ctx, pid, "fake-subject")
	if err != nil {
		t.Fatalf("RequestScan(first) error = %v", err)
	}
	second, err := s.RequestScan(ctx, pid, "fake-subject")
	if !errors.Is(err, db.ErrScanActive) || second != first {
		t.Errorf("RequestScan(second) = %d, %v, want %d, ErrScanActive", second, err, first)
	}

	other := newProject(t, s, "other")
	if _, err := s.RequestScan(ctx, other, "fake-subject"); err != nil {
		t.Errorf("RequestScan(other project) error = %v, want nil", err)
	}
}

func TestRequestScan_Concurrent(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")

	const callers = 8
	ids := make([]scan.ID, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], errs[i] = s.RequestScan(ctx, pid, "fake-subject")
		}()
	}
	wg.Wait()

	winners := 0
	for i := range callers {
		switch {
		case errs[i] == nil:
			winners++
		case !errors.Is(errs[i], db.ErrScanActive):
			t.Errorf("RequestScan() call %d error = %v, want nil or ErrScanActive", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("RequestScan() call %d = scan %d, want scan %d like the others", i, ids[i], ids[0])
		}
	}
	if winners != 1 {
		t.Errorf("RequestScan() winners = %d, want 1", winners)
	}
	for _, table := range []string{"scan", "job"} {
		n, err := s.Count(ctx, table)
		if err != nil {
			t.Fatalf("Count(%q) error = %v", table, err)
		}
		if n != 1 {
			t.Errorf("rows in %s after %d concurrent requests = %d, want 1", table, callers, n)
		}
	}
}

func TestRequestScan_AfterFinish(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	first, err := s.RequestScan(ctx, pid, "fake-subject")
	if err != nil {
		t.Fatalf("RequestScan(first) error = %v", err)
	}
	j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if err := s.FinishEmptyScan(ctx, j.ID, "worker-a", first, 0, 0); err != nil {
		t.Fatalf("FinishEmptyScan() error = %v", err)
	}
	second, err := s.RequestScan(ctx, pid, "fake-subject")
	if err != nil || second == first {
		t.Errorf("RequestScan(after finish) = %d, %v, want a new scan other than %d", second, err, first)
	}
}
