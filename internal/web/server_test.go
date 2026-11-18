package web_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/web"
)

func newTestServer(t *testing.T, logOut io.Writer) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logOut, nil))
	srv := httptest.NewServer(web.NewServer(logger, &fakeAuthenticator{}, newFakeSessions(), newFakeAttempts()))
	t.Cleanup(srv.Close)
	return srv
}

type response struct {
	status int
	header http.Header
	body   string
}

func get(t *testing.T, url string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest(%q) error = %v", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s error = %v", url, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body of %s: %v", url, err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing body of %s: %v", url, err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, io.Discard)
	resp := get(t, srv.URL+"/healthz")
	if resp.status != http.StatusOK || resp.body != "ok" {
		t.Errorf("GET /healthz = %d %q, want 200 %q", resp.status, resp.body, "ok")
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newTestServer(t, io.Discard)
	resp := get(t, srv.URL+"/healthz")
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	}
	for k, v := range want {
		if got := resp.header.Get(k); got != v {
			t.Errorf("GET /healthz header %s = %q, want %q", k, got, v)
		}
	}
}

func TestUnknownPath(t *testing.T) {
	srv := newTestServer(t, io.Discard)
	resp := get(t, srv.URL+"/nope")
	if resp.status != http.StatusNotFound {
		t.Errorf("GET /nope status = %d, want 404", resp.status)
	}
	if want := "화면을 찾을 수 없습니다."; !strings.Contains(resp.body, want) {
		t.Errorf("GET /nope body = %q, want it to contain %q", resp.body, want)
	}
	if got, want := resp.header.Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Errorf("GET /nope Content-Type = %q, want %q", got, want)
	}
}

func TestRequestLog(t *testing.T) {
	var buf bytes.Buffer
	srv := newTestServer(t, &buf)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/healthz?token=abc", nil)
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	req.Header.Set("Cookie", "session=fake-cookie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing body: %v", err)
	}

	line := strings.TrimSpace(buf.String())
	if strings.Count(line, "\n") != 0 || line == "" {
		t.Fatalf("log = %q, want exactly one line", line)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line %q is not JSON: %v", line, err)
	}
	for _, k := range []string{"method", "path", "status", "duration_ms"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("log line %q lacks attribute %q", line, k)
		}
	}
	for _, bad := range []string{"fake-cookie", "token", "abc"} {
		if strings.Contains(line, bad) {
			t.Errorf("log line %q contains %q, want it left out", line, bad)
		}
	}
}
