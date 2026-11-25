package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
	"github.com/SH-Hong1/secret-externalizer/internal/worktree/worktreetest"
)

func noEnv(string) string { return "" }

// fakeValue is the value fakeScanner finds. It is not a real secret.
const fakeValue = "dev-only-fake-worker-value"

// fingerprintOf returns the fingerprint fakeSecrets gives v.
func fingerprintOf(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// fakeSecrets stands in for OpenBao: it fingerprints values with SHA-256
// and keeps pending originals in memory.
type fakeSecrets struct {
	mu      sync.Mutex
	pending map[string][]byte
}

func (*fakeSecrets) Fingerprints(_ context.Context, values [][]byte) ([]string, error) {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = fingerprintOf(string(v))
	}
	return out, nil
}

func (f *fakeSecrets) PutPending(_ context.Context, _ int64, fingerprint string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[fingerprint] = bytes.Clone(value)
	return nil
}

func (f *fakeSecrets) ListPending(context.Context, int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Collect(maps.Keys(f.pending)), nil
}

func (f *fakeSecrets) DeletePending(_ context.Context, _ int64, fingerprint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, fingerprint)
	return nil
}

// errNoStore is what fakeSecrets answers a stored-value call with. A check
// never stores or reads a stored value.
var errNoStore = errors.New("fake secrets: stored values are not kept")

func (*fakeSecrets) ReadStored(context.Context, int64, string) (bao.Stored, error) {
	return bao.Stored{}, errNoStore
}

func (*fakeSecrets) PutStored(context.Context, int64, string, bao.StoredValue, int) (int, error) {
	return 0, errNoStore
}

func (f *fakeSecrets) get(fingerprint string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending[fingerprint]
}

// fakeScanner reports, by byte offsets, every place fakeValue appears in the
// files of the scanned folder.
type fakeScanner struct{}

func (fakeScanner) Name() string { return "fake" }

func (fakeScanner) Version(context.Context) (string, error) { return "fake-1", nil }

func (fakeScanner) Scan(_ context.Context, dir string, _ []string) ([]scan.Raw, error) {
	var raws []scan.Raw
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: a test file under the test's own folder
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if i := bytes.Index(data, []byte(fakeValue)); i >= 0 {
			raws = append(raws, scan.Raw{
				Tool: "fake", Rule: "fake-value", Category: finding.CategoryOther, Kind: finding.KindOther,
				File: filepath.ToSlash(rel), Offset: i, EndOffset: i + len(fakeValue),
			})
		}
		return nil
	})
	return raws, err
}

// writeFile writes a file with fake content at name, making its folder.
func writeFile(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte("fake content"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}

func TestRun_Worker(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		env        func(string) string
		wantErr    string
		wantStdout string
	}{
		{name: "version", args: []string{"worker", "-version"}, env: noEnv, wantStdout: "secret-externalizer worker "},
		{name: "help", args: []string{"worker", "-h"}, env: noEnv, wantStdout: "-version"},
		{name: "missing database url", args: []string{"worker"}, env: noEnv, wantErr: "DATABASE_URL"},
		{name: "missing workspace root", args: []string{"worker"}, env: noEnv, wantErr: "WORKSPACE_ROOT"},
		{name: "unknown flag", args: []string{"worker", "-nope"}, env: noEnv, wantErr: "parsing flags"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(t.Context(), tt.args, tt.env, &stdout, &stderr)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("run(%v) error = %v, want nil", tt.args, err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("run(%v) error = %v, want one containing %q", tt.args, err, tt.wantErr)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("run(%v) stdout = %q, want it to contain %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if stderr.Len() != 0 {
				t.Errorf("run(%v) wrote %q to stderr, want nothing, because main reports the returned error", tt.args, stderr.String())
			}
		})
	}
}

func TestNewWorkerID(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}
	a, err := newWorkerID()
	if err != nil {
		t.Fatalf("newWorkerID() error = %v", err)
	}
	b, err := newWorkerID()
	if err != nil {
		t.Fatalf("newWorkerID() error = %v", err)
	}
	if a == b {
		t.Errorf("newWorkerID() twice = %q and %q, want different IDs", a, b)
	}
	if !strings.HasPrefix(a, host+"-") {
		t.Errorf("newWorkerID() = %q, want prefix %q", a, host+"-")
	}
}

// fakeDeps returns the dependencies of a worker that scans with fakeScanner
// and keeps values in secrets.
func fakeDeps(secrets *fakeSecrets) workerDeps {
	return workerDeps{fingerprints: secrets, originals: secrets, vault: secrets, scanners: []scan.Scanner{fakeScanner{}}}
}

func TestRun_RunsQueuedScanUntilCancelled(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run against postgres")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	root := t.TempDir()
	name := fmt.Sprintf("worker-test-%d", time.Now().UnixNano())
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		writeFile(t, filepath.Join(dir, f))
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("password="+fakeValue+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	store, err := db.Open(ctx, db.DSN(databaseURL))
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	projectID, err := store.CreateProject(ctx, project.Project{Name: name, Path: name, CreatedBy: "worker-test"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	scanID, err := store.RequestScan(ctx, projectID, audit.Actor{Subject: "worker-test"})
	if err != nil {
		t.Fatalf("RequestScan() error = %v", err)
	}

	env := validEnv(t)
	env["DATABASE_URL"] = databaseURL
	env["WORKSPACE_ROOT"] = root
	getenv := func(k string) string { return env[k] }
	tune := func(c *config) {
		c.poll = 20 * time.Millisecond
		c.heartbeat = 20 * time.Millisecond
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	secrets := &fakeSecrets{pending: map[string][]byte{}}
	deps := func(config) (workerDeps, error) { //nolint:unparam // the signature is the one runWith takes
		return fakeDeps(secrets), nil
	}
	done := make(chan error, 1)
	go func() {
		done <- runWith(runCtx, []string{"worker"}, getenv, io.Discard, io.Discard, tune, deps)
	}()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var got scan.Scan
	for {
		got, err = store.Scan(ctx, scanID)
		if err != nil {
			t.Fatalf("Scan(%d) error = %v", scanID, err)
		}
		if !got.Status.Active() {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("scan %d status = %q when the deadline passed, want it finished", scanID, got.Status)
		case err := <-done:
			t.Fatalf("runWith() returned %v before the scan finished", err)
		case <-ticker.C:
		}
	}
	if got.Status != scan.StatusSucceeded || got.Files != 3 || got.Findings != 1 {
		t.Errorf("Scan(%d) = status %q, files %d, findings %d, failure %q; want succeeded with 3 files and 1 finding",
			scanID, got.Status, got.Files, got.Findings, got.FailureReason)
	}
	findings, err := store.FindingsByScan(ctx, scanID)
	if err != nil {
		t.Fatalf("FindingsByScan(%d) error = %v", scanID, err)
	}
	if len(findings) != 1 || findings[0].File != "sub/c.txt" || findings[0].Fingerprint != fingerprintOf(fakeValue) {
		t.Errorf("FindingsByScan(%d) = %+v, want the fake value in sub/c.txt", scanID, findings)
	}
	if v := secrets.get(fingerprintOf(fakeValue)); string(v) != fakeValue {
		t.Errorf("pending original = %q, want the fake value", v)
	}

	stop()
	if err := <-done; err != nil {
		t.Errorf("runWith() after cancel = %v, want nil", err)
	}
}

// testSchemaURL returns TEST_DATABASE_URL with its search path set to a
// schema of its own, created for this test and dropped when it ends, so the
// worker claims only the jobs the test queues. It skips the test when
// TEST_DATABASE_URL is not set.
func testSchemaURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to run against postgres")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(admin.Close)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	schema := "test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("CREATE SCHEMA %s error = %v", schema, err)
	}
	t.Cleanup(func() {
		// The test context is already cancelled during cleanup.
		if _, err := admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("DROP SCHEMA %s error = %v", schema, err)
		}
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL is not a URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// logWatch is the worker's standard error. It keeps what the worker logs
// and wakes wait whenever a line is written.
type logWatch struct {
	wrote chan struct{}

	mu  sync.Mutex
	buf bytes.Buffer
}

func newLogWatch() *logWatch { return &logWatch{wrote: make(chan struct{}, 1)} }

func (w *logWatch) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return n, err
}

// count returns how many log lines have msg as their message.
func (w *logWatch) count(msg string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Count(w.buf.String(), `"msg":"`+msg+`"`)
}

// wait returns once a log line has msg as its message. It fails the test
// when ctx ends or the worker returns first.
func (w *logWatch) wait(ctx context.Context, t *testing.T, done <-chan error, msg string) {
	t.Helper()
	for w.count(msg) == 0 {
		select {
		case <-ctx.Done():
			t.Fatalf("no %q log line when the deadline passed; log:\n%s", msg, w.String())
		case err := <-done:
			t.Fatalf("runWith() returned %v before logging %q; log:\n%s", err, msg, w.String())
		case <-w.wrote:
		}
	}
}

func (w *logWatch) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestRun_HandlesApplyJobs runs a worker that scans a project from
// WORKSPACE_ROOT, where the project folder is no git work tree, and checks
// it from APPLY_WORKSPACE_ROOT, where the same files are a work tree. Only
// a check that reads the apply root can find the work tree.
func TestRun_HandlesApplyJobs(t *testing.T) {
	tests := []struct {
		name      string
		git       bool
		applyRoot bool
		state     apply.State
		blockers  []apply.Blocker
	}{
		{name: "ready", git: true, applyRoot: true, state: apply.StateReady},
		{name: "no git", applyRoot: true, state: apply.StateBlocked, blockers: []apply.Blocker{apply.BlockerGitMissing}},
		{name: "no apply root", git: true, state: apply.StateBlocked, blockers: []apply.Blocker{apply.BlockerWorkspaceReadOnly}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			databaseURL := testSchemaURL(t)
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()

			scanRoot, applyRoot := t.TempDir(), t.TempDir()
			for _, root := range []string{scanRoot, applyRoot} {
				p := filepath.Join(root, "shop", "conf", "app.properties")
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatalf("MkdirAll error = %v", err)
				}
				if err := os.WriteFile(p, []byte("password="+fakeValue+"\n"), 0o600); err != nil {
					t.Fatalf("WriteFile error = %v", err)
				}
			}
			worktreetest.Init(t, filepath.Join(applyRoot, "shop"))

			store, err := db.Open(ctx, db.DSN(databaseURL))
			if err != nil {
				t.Fatalf("db.Open() error = %v", err)
			}
			defer store.Close()
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("Migrate() error = %v", err)
			}
			actor := audit.Actor{Subject: "worker-test"}
			projectID, err := store.CreateProject(ctx, project.Project{Name: "shop", Path: "shop", CreatedBy: "worker-test"})
			if err != nil {
				t.Fatalf("CreateProject() error = %v", err)
			}
			scanID, err := store.RequestScan(ctx, projectID, actor)
			if err != nil {
				t.Fatalf("RequestScan() error = %v", err)
			}

			env := validEnv(t)
			env["DATABASE_URL"] = databaseURL
			env["WORKSPACE_ROOT"] = scanRoot
			if tt.applyRoot {
				env["APPLY_WORKSPACE_ROOT"] = applyRoot
			}
			git := ""
			if tt.git {
				git = gitPath(t)
			}
			tune := func(c *config) {
				c.poll = 20 * time.Millisecond
				c.heartbeat = 20 * time.Millisecond
				c.gitBin = git
			}
			secrets := &fakeSecrets{pending: map[string][]byte{}}
			deps := func(config) (workerDeps, error) { //nolint:unparam // the signature is the one runWith takes
				return fakeDeps(secrets), nil
			}
			logs := newLogWatch()
			runCtx, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan error, 1)
			go func() {
				done <- runWith(runCtx, []string{"worker"}, getenvFrom(env), io.Discard, logs, tune, deps)
			}()

			logs.wait(ctx, t, done, "scan finished")
			scanned, err := store.Scan(ctx, scanID)
			if err != nil {
				t.Fatalf("Scan(%d) error = %v", scanID, err)
			}
			// Only the apply root is a work tree, so a scan of it would
			// have read a commit hash.
			if scanned.Status != scan.StatusSucceeded || scanned.CommitHash != "" {
				t.Errorf("Scan(%d) = status %q, commit hash %q; want succeeded with no commit hash, read from WORKSPACE_ROOT",
					scanID, scanned.Status, scanned.CommitHash)
			}
			page, err := store.Entries(ctx, projectID, scanID, triage.Filter{Status: triage.StatusAll, Page: 1})
			if err != nil || len(page.Entries) != 1 || page.Entries[0].Bundle == 0 {
				t.Fatalf("Entries(%d, %d) = %+v, %v; want one bundle", projectID, scanID, page.Entries, err)
			}
			decision := triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}
			if err := store.Decide(ctx, projectID, []triage.BundleID{page.Entries[0].Bundle}, decision, actor); err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			id, err := store.RequestApplyCheck(ctx, projectID, actor)
			if err != nil {
				t.Fatalf("RequestApplyCheck(%d) error = %v", projectID, err)
			}

			logs.wait(ctx, t, done, "apply check finished")
			run, err := store.ApplyRun(ctx, id)
			if err != nil {
				t.Fatalf("ApplyRun(%d) error = %v", id, err)
			}
			if run.State != tt.state || !slices.Equal(run.Blockers, tt.blockers) {
				t.Errorf("ApplyRun(%d) = state %q, blockers %v; want %q, %v; log:\n%s", id, run.State, run.Blockers, tt.state, tt.blockers, logs)
			}
			if strings.Contains(logs.String(), fakeValue) {
				t.Errorf("the worker log holds the value")
			}

			stop()
			if err := <-done; err != nil {
				t.Errorf("runWith() after cancel = %v, want nil", err)
			}
		})
	}
}

// TestRun_JobsSplit runs a scan-only worker and then an apply-only worker
// on one queue. The apply-only worker runs the apply check and leaves alone
// a scan that was queued before it, and starts without any scanner
// setting.
func TestRun_JobsSplit(t *testing.T) {
	databaseURL := testSchemaURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	root := t.TempDir()
	for _, name := range []string{"shop", "other"} {
		p := filepath.Join(root, name, "conf", "app.properties")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("MkdirAll error = %v", err)
		}
		if err := os.WriteFile(p, []byte("password="+fakeValue+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}
	}
	worktreetest.Init(t, filepath.Join(root, "shop"))

	store, err := db.Open(ctx, db.DSN(databaseURL))
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	actor := audit.Actor{Subject: "worker-test"}
	shop, err := store.CreateProject(ctx, project.Project{Name: "shop", Path: "shop", CreatedBy: "worker-test"})
	if err != nil {
		t.Fatalf("CreateProject(shop) error = %v", err)
	}
	other, err := store.CreateProject(ctx, project.Project{Name: "other", Path: "other", CreatedBy: "worker-test"})
	if err != nil {
		t.Fatalf("CreateProject(other) error = %v", err)
	}
	if _, err := store.RequestScan(ctx, shop, actor); err != nil {
		t.Fatalf("RequestScan(shop) error = %v", err)
	}

	tune := func(c *config) {
		c.poll = 20 * time.Millisecond
		c.heartbeat = 20 * time.Millisecond
		c.gitBin = gitPath(t)
	}
	secrets := &fakeSecrets{pending: map[string][]byte{}}
	deps := func(config) (workerDeps, error) { //nolint:unparam // the signature is the one runWith takes
		return fakeDeps(secrets), nil
	}
	start := func(env map[string]string) (logs *logWatch, done chan error, stop func()) {
		logs = newLogWatch()
		runCtx, stop := context.WithCancel(ctx)
		done = make(chan error, 1)
		go func() {
			done <- runWith(runCtx, []string{"worker"}, getenvFrom(env), io.Discard, logs, tune, deps)
		}()
		return logs, done, stop
	}

	scanEnv := validEnv(t)
	scanEnv["DATABASE_URL"] = databaseURL
	scanEnv["WORKSPACE_ROOT"] = root
	scanEnv["WORKER_JOBS"] = "scan"
	logs, done, stop := start(scanEnv)
	logs.wait(ctx, t, done, "scan finished")
	stop()
	if err := <-done; err != nil {
		t.Fatalf("runWith(WORKER_JOBS=scan) after cancel = %v, want nil", err)
	}

	waiting, err := store.RequestScan(ctx, other, actor)
	if err != nil {
		t.Fatalf("RequestScan(other) error = %v", err)
	}
	checkID, err := store.RequestApplyCheck(ctx, shop, actor)
	if err != nil {
		t.Fatalf("RequestApplyCheck(shop) error = %v", err)
	}

	applyEnv := map[string]string{
		"DATABASE_URL":         databaseURL,
		"BAO_ADDR":             scanEnv["BAO_ADDR"],
		"BAO_ROLE_ID":          scanEnv["BAO_ROLE_ID"],
		"BAO_SECRET_ID_FILE":   scanEnv["BAO_SECRET_ID_FILE"],
		"APPLY_WORKSPACE_ROOT": root,
		"WORKER_JOBS":          "apply",
	}
	logs, done, stop = start(applyEnv)
	logs.wait(ctx, t, done, "apply check finished")
	stop()
	if err := <-done; err != nil {
		t.Fatalf("runWith(WORKER_JOBS=apply) after cancel = %v, want nil", err)
	}

	run, err := store.ApplyRun(ctx, checkID)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", checkID, err)
	}
	if run.State == apply.StateChecking {
		t.Errorf("ApplyRun(%d) state = %q after the apply-only worker, want the check done", checkID, run.State)
	}
	sc, err := store.Scan(ctx, waiting)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", waiting, err)
	}
	if sc.Status != scan.StatusQueued {
		t.Errorf("Scan(%d) status = %q after the apply-only worker, want queued", waiting, sc.Status)
	}
	// A job the apply-only worker had claimed would be under its lease, or
	// on its second attempt once the lease ran out.
	probe, err := store.ClaimJob(ctx, "probe", time.Minute, job.KindScan)
	if err != nil {
		t.Fatalf("ClaimJob(scan) after the apply-only worker error = %v, want the job of scan %d", err, waiting)
	}
	if probe.ScanID != int64(waiting) || probe.Attempts != 1 {
		t.Errorf("ClaimJob(scan) after the apply-only worker = %+v, want the job of scan %d on its first attempt", probe, waiting)
	}
}

// gitPath returns the git executable, skipping the test when there is none.
func gitPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	return p
}
