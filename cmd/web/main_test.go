package main

import (
	"bytes"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func TestRun_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"web", "-version"}, noEnv, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := stdout.String(); !strings.HasPrefix(got, "secret-externalizer web ") {
		t.Errorf("stdout = %q, want prefix %q", got, "secret-externalizer web ")
	}
}

func TestRun_UnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"web", "-nope"}, noEnv, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want error")
	}
	if got := stderr.String(); got != "" {
		t.Errorf("run() wrote %q to stderr, want nothing, because main reports the returned error", got)
	}
}

func TestRun_HelpFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"web", "-h"}, noEnv, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if got := stdout.String(); !strings.Contains(got, "-version") {
		t.Errorf("stdout = %q, want usage that lists -version", got)
	}
	if got := stderr.String(); got != "" {
		t.Errorf("stderr = %q, want empty", got)
	}
}
