package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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

func TestRun_ServesUntilContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	pr, pw := io.Pipe()
	getenv := func(k string) string {
		if k == "WEB_ADDR" {
			return "127.0.0.1:0"
		}
		return ""
	}
	done := make(chan error, 1)
	go func() {
		err := run(ctx, []string{"web"}, getenv, io.Discard, pw)
		_ = pw.Close() // unblocks the reader below; the pipe has no other closer
		done <- err
	}()

	var addr string
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		var rec struct {
			Msg  string `json:"msg"`
			Addr string `json:"addr"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", sc.Text(), err)
		}
		if rec.Msg == "listening" {
			addr = rec.Addr
			break
		}
	}
	if addr == "" {
		t.Fatalf("no listening log line; run() returned %v", <-done)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }() // keeps later log writes from blocking

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz error = %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want 200", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("run() after cancel = %v, want nil", err)
	}
}
