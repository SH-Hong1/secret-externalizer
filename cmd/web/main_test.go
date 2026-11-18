package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"web", "-version"}, func(string) string { return "" }, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := stdout.String(); !strings.HasPrefix(got, "secret-externalizer web ") {
		t.Errorf("stdout = %q, want prefix %q", got, "secret-externalizer web ")
	}
}

func TestRun_UnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"web", "-nope"}, func(string) string { return "" }, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want error")
	}
}
