package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
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

func (fakeScanner) Scan(_ context.Context, dir string) ([]scan.Raw, error) {
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
	scanID, err := store.RequestScan(ctx, projectID, "worker-test")
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
	deps := func(config) (scanDeps, error) { //nolint:unparam // the signature is the one runWith takes
		return scanDeps{fingerprints: secrets, originals: secrets, scanners: []scan.Scanner{fakeScanner{}}}, nil
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
