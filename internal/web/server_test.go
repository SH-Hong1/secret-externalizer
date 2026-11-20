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
	"testing/synctest"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/web"
)

func newTestServer(t *testing.T, logOut io.Writer) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logOut, nil))
	tr := newFakeTriage()
	srv := httptest.NewServer(web.NewServer(logger, &fakeAuthenticator{}, newFakeSessions(), newFakeAttempts(),
		web.Stores{Projects: newFakeProjects(), Scans: newFakeScans(), Triage: tr, Audits: &fakeAudit{log: &callLog{}}, Applies: newFakeApplies()},
		web.NewHistorySuggester(tr), &fakeOriginals{log: &callLog{}}, time.Minute))
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
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; base-uri 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	}
	// /healthz is a plain page, /nope an error page, and / a redirect to the
	// login page; every kind of response carries the headers.
	for _, path := range []string{"/healthz", "/nope", "/"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q) error = %v", path, err)
		}
		resp, err := noRedirectClient().Do(req)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("closing body of %s: %v", path, err)
		}
		for k, v := range want {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("GET %s header %s = %q, want %q", path, k, got, v)
			}
		}
	}
}

// noRedirectClient returns a client that hands back redirects instead of
// following them.
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
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

func TestStoreCallsHaveTimeLimit(t *testing.T) {
	const limit = 3 * time.Second
	tests := []struct {
		method, target, body string
	}{
		{http.MethodGet, "/projects", ""},
		{http.MethodPost, "/projects", "name=shop&path=shop"},
		{http.MethodGet, "/projects/1", ""},
		{http.MethodPost, "/projects/1/scans", ""},
		{http.MethodGet, "/scans/1", ""},
		{http.MethodGet, "/projects/1/triage", ""},
		{http.MethodPost, "/projects/1/bundles/1/verdict", "verdict=false-positive"},
		{http.MethodPost, "/projects/1/customer-data/verdict", "kind=phone&file=a.log&verdict=redact"},
		{http.MethodPost, "/projects/1/findings/1/move", "to=new"},
		{http.MethodPost, "/projects/1/bundles/1/original", ""},
		{http.MethodGet, "/audit", ""},
		{http.MethodGet, "/projects/1/apply", ""},
		{http.MethodPost, "/projects/1/apply/check", ""},
		{http.MethodPost, "/projects/1/apply", "run=1&digest=" + strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			// Inside the bubble the clock moves only when every goroutine
			// waits, so the request takes exactly as long as the limit.
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, nil))
				sessions := newFakeSessions()
				id, err := sessions.Create(t.Context(), auth.User{Subject: "fake-admin", Name: "Fake Admin", Roles: []auth.Role{auth.RoleAdmin}})
				if err != nil {
					t.Fatalf("fakeSessions.Create() error = %v", err)
				}
				store := &blockingStore{}
				h := web.NewServer(logger, &fakeAuthenticator{}, sessions, newFakeAttempts(),
					web.Stores{Projects: store, Scans: store, Triage: store, Audits: store, Applies: store},
					web.NewHistorySuggester(store), store, limit)

				req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.target, strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				req.Header.Set("Cookie", "se_session="+string(id))
				rec := httptest.NewRecorder()
				start := time.Now()
				h.ServeHTTP(rec, req)
				elapsed := time.Since(start)

				if rec.Code != http.StatusInternalServerError || elapsed != limit {
					t.Errorf("%s %s with a hung store = %d after %v, want 500 after %v", tt.method, tt.target, rec.Code, elapsed, limit)
				}
				if calls, deadlines := store.counts(); calls != 1 || deadlines != 1 {
					t.Errorf("%s %s made %d store calls, %d with a deadline, want 1 and 1", tt.method, tt.target, calls, deadlines)
				}
				if n := strings.Count(logs.String(), `"msg":"request failed"`); n != 1 {
					t.Errorf("%s %s logged %d failures, want 1; log:\n%s", tt.method, tt.target, n, logs.String())
				}
			})
		})
	}
}
