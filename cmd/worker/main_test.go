package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
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
