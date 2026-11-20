package db_test

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
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

	id, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject", Name: "fake-user"})
	if err != nil {
		t.Fatalf("RequestScan(%d) error = %v", pid, err)
	}
	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusQueued || got.ProjectID != pid || got.RequestedBy != "fake-subject" ||
		got.RequesterName != "fake-user" || got.RequestedAt.IsZero() {
		t.Errorf("Scan(%d) = %+v, want a queued scan of project %d requested by fake-subject named fake-user", id, got, pid)
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
	_, err := s.RequestScan(t.Context(), 424242, audit.Actor{Subject: "fake-subject"})
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

	first, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"})
	if err != nil {
		t.Fatalf("RequestScan(first) error = %v", err)
	}
	second, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"})
	if !errors.Is(err, db.ErrScanActive) || second != first {
		t.Errorf("RequestScan(second) = %d, %v, want %d, ErrScanActive", second, err, first)
	}

	other := newProject(t, s, "other")
	if _, err := s.RequestScan(ctx, other, audit.Actor{Subject: "fake-subject"}); err != nil {
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
			ids[i], errs[i] = s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"})
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
	first, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"})
	if err != nil {
		t.Fatalf("RequestScan(first) error = %v", err)
	}
	j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if err := s.SaveResult(ctx, j.ID, "worker-a", first, scan.Result{}); err != nil {
		t.Fatalf("SaveResult() error = %v", err)
	}
	second, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"})
	if err != nil || second == first {
		t.Errorf("RequestScan(after finish) = %d, %v, want a new scan other than %d", second, err, first)
	}
}

func TestMigration0006KeepsOlderScans(t *testing.T) {
	s := db.NewTestStore(t)
	ctx := t.Context()
	if err := s.MigrateThrough(ctx, 5); err != nil {
		t.Fatalf("MigrateThrough(5) error = %v", err)
	}
	id, err := s.InsertRawScan(ctx, newProject(t, s, "shop"))
	if err != nil {
		t.Fatalf("InsertRawScan() error = %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() over 0005 error = %v", err)
	}
	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.RequestedBy != "fake-subject" || got.RequesterName != "" || got.SkippedFiles != nil || got.ScannerErrors != nil {
		t.Errorf("Scan(%d) = %+v, want requested by fake-subject with no name, skipped files or scanner errors", id, got)
	}
}

func TestApplyExcludesScan(t *testing.T) {
	for _, state := range []apply.State{apply.StateChecking, apply.StateQueued, apply.StateRunning} {
		t.Run("apply "+string(state), func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			pid := newProject(t, s, "shop")
			sid := save(t, s, pid, mixed()...)
			applyRunIn(t, s, pid, sid, state)
			if _, err := s.RequestScan(ctx, pid, tester); !errors.Is(err, db.ErrApplyActive) {
				t.Errorf("RequestScan(%d) while an apply is %s error = %v, want ErrApplyActive", pid, state, err)
			}
			if n, err := s.Count(ctx, "scan"); err != nil || n != 1 {
				t.Errorf("rows in scan = %d, %v, want 1", n, err)
			}
		})
	}
	for _, status := range []scan.Status{scan.StatusQueued, scan.StatusRunning} {
		t.Run("scan "+string(status), func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			pid := newProject(t, s, "shop")
			save(t, s, pid, mixed()...)
			id, err := s.RequestScan(ctx, pid, tester)
			if err != nil {
				t.Fatalf("RequestScan(%d) error = %v", pid, err)
			}
			if status == scan.StatusRunning {
				if err := s.StartScan(ctx, id); err != nil {
					t.Fatalf("StartScan(%d) error = %v", id, err)
				}
			}
			if _, err := s.RequestApplyCheck(ctx, pid, tester); !errors.Is(err, db.ErrScanActive) {
				t.Errorf("RequestApplyCheck(%d) while a scan is %s error = %v, want ErrScanActive", pid, status, err)
			}
			if n, err := s.Count(ctx, "apply_run"); err != nil || n != 0 {
				t.Errorf("rows in apply_run = %d, %v, want 0", n, err)
			}
		})
	}
	t.Run("concurrent", func(t *testing.T) {
		s := newMigratedStore(t)
		ctx := t.Context()
		const rounds = 20
		var pids []project.ID
		for i := range rounds {
			pid := newProject(t, s, "race-"+strconv.Itoa(i))
			save(t, s, pid, mixed()...)
			pids = append(pids, pid)
		}
		for i, pid := range pids {
			start := make(chan struct{})
			scanDone, checkDone := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				_, err := s.RequestScan(ctx, pid, tester)
				scanDone <- err
			}()
			go func() {
				<-start
				_, err := s.RequestApplyCheck(ctx, pid, tester)
				checkDone <- err
			}()
			close(start)
			scanErr, checkErr := <-scanDone, <-checkDone
			switch {
			case scanErr == nil && errors.Is(checkErr, db.ErrScanActive):
			case checkErr == nil && errors.Is(scanErr, db.ErrApplyActive):
			default:
				t.Errorf("round %d: RequestScan() error = %v, RequestApplyCheck() error = %v, want one nil and the other refused",
					i, scanErr, checkErr)
			}
		}
	})
}
