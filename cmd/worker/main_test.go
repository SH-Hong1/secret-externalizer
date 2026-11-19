package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

func noEnv(string) string { return "" }

func writeFile(t *testing.T, name string) {
	t.Helper()
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
	for _, f := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		writeFile(t, filepath.Join(dir, f))
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

	env := map[string]string{"DATABASE_URL": databaseURL, "WORKSPACE_ROOT": root}
	getenv := func(k string) string { return env[k] }
	tune := func(c *config) {
		c.poll = 20 * time.Millisecond
		c.heartbeat = 20 * time.Millisecond
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- runWith(runCtx, []string{"worker"}, getenv, io.Discard, io.Discard, tune)
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
	if got.Status != scan.StatusSucceeded || got.Files != 3 {
		t.Errorf("Scan(%d) = status %q, files %d, failure %q; want succeeded with 3 files",
			scanID, got.Status, got.Files, got.FailureReason)
	}

	stop()
	if err := <-done; err != nil {
		t.Errorf("runWith() after cancel = %v, want nil", err)
	}
}
