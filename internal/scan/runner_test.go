package scan_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

const (
	reasonOutside      = "project path points outside the workspace root"
	reasonNotDirectory = "project path is not a directory in the workspace"
	reasonOther        = "scan could not finish; see the worker log"
)

// outcome is what the runner wrote to the store about a scan.
type outcome struct {
	status  scan.Status
	reason  string
	files   int
	skipped int
}

// fakeStore keeps one scan and one project and records how the scan ended.
// A non-nil error field makes the matching method return that error.
type fakeStore struct {
	startErr   error
	scanErr    error
	projectErr error
	failErr    error
	finishErr  error

	project project.Project
	got     []outcome
}

func (f *fakeStore) StartScan(context.Context, scan.ID) error { return f.startErr }

func (f *fakeStore) Scan(_ context.Context, id scan.ID) (scan.Scan, error) {
	return scan.Scan{ID: id, ProjectID: f.project.ID}, f.scanErr
}

func (f *fakeStore) Project(context.Context, project.ID) (project.Project, error) {
	return f.project, f.projectErr
}

func (f *fakeStore) FailScan(_ context.Context, _ int64, _ string, _ scan.ID, reason string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.got = append(f.got, outcome{status: scan.StatusFailed, reason: reason})
	return nil
}

func (f *fakeStore) FinishEmptyScan(_ context.Context, _ int64, _ string, _ scan.ID, files, skipped int) error {
	if f.finishErr != nil {
		return f.finishErr
	}
	f.got = append(f.got, outcome{status: scan.StatusSucceeded, files: files, skipped: skipped})
	return nil
}

// workspace returns a resolved workspace root that holds a folder named
// shop with three files, one of them in a subfolder, and a folder outside the
// root with a file of its own.
func workspace(t *testing.T) (root, outside string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(TempDir) error = %v", err)
	}
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, f := range []string{
		filepath.Join(root, "shop", "a.txt"),
		filepath.Join(root, "shop", "b.txt"),
		filepath.Join(root, "shop", "sub", "c.txt"),
		filepath.Join(outside, "secret.txt"),
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(f), err)
		}
		if err := os.WriteFile(f, []byte("fake content"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", f, err)
		}
	}
	return root, outside
}

func newRunner(store scan.Store, root string, logs *bytes.Buffer) *scan.Runner {
	return scan.NewRunner(slog.New(slog.NewJSONHandler(logs, nil)), store, root, "fake-worker")
}

func TestHandle_CountsFiles(t *testing.T) {
	root, _ := workspace(t)
	// A symbolic link is not a regular file and is not followed.
	if err := os.Symlink(filepath.Join(root, "shop", "a.txt"), filepath.Join(root, "shop", "link")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	store := &fakeStore{project: project.Project{ID: 1, Path: "shop"}}
	var logs bytes.Buffer

	if err := newRunner(store, root, &logs).Handle(t.Context(), job.Job{ID: 1, ScanID: 5}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	want := []outcome{{status: scan.StatusSucceeded, files: 3}}
	if diff := cmp.Diff(want, store.got, cmp.AllowUnexported(outcome{})); diff != "" {
		t.Errorf("Handle() wrote (-want +got):\n%s", diff)
	}
}

func TestHandle_FailureReasons(t *testing.T) {
	root, outside := workspace(t)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	tests := []struct {
		name  string
		path  string
		store func(*fakeStore)
		want  string
	}{
		{name: "outside root", path: "escape", want: reasonOutside},
		{name: "missing folder", path: "nope", want: reasonNotDirectory},
		{name: "a file", path: "file.txt", want: reasonNotDirectory},
		{name: "through a file", path: "file.txt/sub", want: reasonNotDirectory},
		{name: "store error on start", path: "shop", store: func(f *fakeStore) { f.startErr = errors.New("dial tcp 10.0.0.5:5432: refused") }, want: reasonOther},
		{name: "store error on scan", path: "shop", store: func(f *fakeStore) { f.scanErr = errors.New("dial tcp 10.0.0.5:5432: refused") }, want: reasonOther},
		{name: "store error on project", path: "shop", store: func(f *fakeStore) { f.projectErr = errors.New("dial tcp 10.0.0.5:5432: refused") }, want: reasonOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{project: project.Project{ID: 1, Path: tt.path}}
			if tt.store != nil {
				tt.store(store)
			}
			var logs bytes.Buffer
			if err := newRunner(store, root, &logs).Handle(t.Context(), job.Job{ID: 1, ScanID: 5}); err != nil {
				t.Fatalf("Handle(path %q) error = %v", tt.path, err)
			}
			want := []outcome{{status: scan.StatusFailed, reason: tt.want}}
			if diff := cmp.Diff(want, store.got, cmp.AllowUnexported(outcome{})); diff != "" {
				t.Errorf("Handle(path %q) wrote (-want +got):\n%s", tt.path, diff)
			}
		})
	}
}

func TestHandle_OutsideRootReadsNothing(t *testing.T) {
	root, outside := workspace(t)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	store := &fakeStore{project: project.Project{ID: 1, Path: "escape"}}
	var logs bytes.Buffer

	if err := newRunner(store, root, &logs).Handle(t.Context(), job.Job{ID: 1, ScanID: 5}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	for _, o := range store.got {
		if o.status == scan.StatusSucceeded || o.files != 0 {
			t.Errorf("Handle(project through a link to outside) wrote %+v, want only a failure with no files counted", o)
		}
	}
}

func TestHandle_FailureReasonHidesError(t *testing.T) {
	root, _ := workspace(t)
	store := &fakeStore{
		project:  project.Project{ID: 1, Path: "shop"},
		startErr: errors.New("dial tcp 10.0.0.5:5432: connection refused"),
	}
	var logs bytes.Buffer

	if err := newRunner(store, root, &logs).Handle(t.Context(), job.Job{ID: 1, ScanID: 5}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(store.got) != 1 || strings.Contains(store.got[0].reason, "10.0.0.5") || strings.Contains(store.got[0].reason, "dial") {
		t.Errorf("Handle() wrote %+v, want a failure reason without the store error text", store.got)
	}
	if n := strings.Count(logs.String(), "10.0.0.5"); n != 1 {
		t.Errorf("Handle() logged the store error %d times, want once; log:\n%s", n, logs.String())
	}
}

func TestHandle_LeaseLostWritesNothingElse(t *testing.T) {
	root, _ := workspace(t)
	store := &fakeStore{
		project:   project.Project{ID: 1, Path: "shop"},
		finishErr: job.ErrLeaseLost,
	}
	var logs bytes.Buffer

	err := newRunner(store, root, &logs).Handle(t.Context(), job.Job{ID: 1, ScanID: 5})
	if !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("Handle() with a lost lease error = %v, want ErrLeaseLost", err)
	}
	if len(store.got) != 0 {
		t.Errorf("Handle() with a lost lease wrote %+v, want nothing", store.got)
	}
}

func TestHandle_CancelledLeavesScanRunning(t *testing.T) {
	root, _ := workspace(t)
	store := &fakeStore{project: project.Project{ID: 1, Path: "shop"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var logs bytes.Buffer

	err := newRunner(store, root, &logs).Handle(ctx, job.Job{ID: 1, ScanID: 5})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Handle(cancelled) error = %v, want context.Canceled", err)
	}
	if len(store.got) != 0 {
		t.Errorf("Handle(cancelled) wrote %+v, want nothing so the scan is retried", store.got)
	}
}
