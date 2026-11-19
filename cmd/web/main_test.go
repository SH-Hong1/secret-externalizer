package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/valkey-io/valkey-go"
)

func noEnv(string) string { return "" }

// fastRetry keeps tests that wait for the issuer short.
func fastRetry() discoveryRetry {
	return discoveryRetry{interval: 10 * time.Millisecond, limit: 300 * time.Millisecond}
}

// fakeIssuer serves the OpenID Connect discovery document, which is all that
// start-up reads. It answers 503 to the first failures requests, or to every
// request when failures is negative.
type fakeIssuer struct {
	server *httptest.Server

	mu       sync.Mutex
	failures int
	requests int

	// firstRequest is closed when the discovery document is first asked for.
	firstRequest chan struct{}
	once         sync.Once
}

func newFakeIssuer(t *testing.T, failures int) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{failures: failures, firstRequest: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// URL returns the issuer URL the discovery document reports.
func (f *fakeIssuer) URL() string { return f.server.URL }

// Requests returns how many times the discovery document was asked for.
func (f *fakeIssuer) Requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakeIssuer) discovery(w http.ResponseWriter, _ *http.Request) {
	f.once.Do(func() { close(f.firstRequest) })
	f.mu.Lock()
	f.requests++
	fail := f.failures < 0 || f.requests <= f.failures
	f.mu.Unlock()
	if fail {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	u := f.server.URL
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{ // the client under test reports a broken body itself
		"issuer":                                u,
		"authorization_endpoint":                u + "/auth",
		"token_endpoint":                        u + "/token",
		"jwks_uri":                              u + "/keys",
		"end_session_endpoint":                  u + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

// runEnv returns an environment that points run at issuerURL and at the
// valkey in TEST_VALKEY_ADDR and the postgres in TEST_DATABASE_URL, listening
// on a free local port. OpenBao is at an address where nothing listens, since
// web starts without reaching it.
func runEnv(t *testing.T, issuerURL, valkeyAddr, databaseURL string) func(string) string {
	t.Helper()
	env := validEnv(t)
	env["BAO_ADDR"] = "http://127.0.0.1:1"
	env["WEB_ADDR"] = "127.0.0.1:0"
	env["VALKEY_ADDR"] = valkeyAddr
	env["DATABASE_URL"] = databaseURL
	env["OIDC_ISSUER_URL"] = issuerURL
	delete(env, "OIDC_DISCOVERY_URL")
	return getenvFrom(env)
}

func testValkeyAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("set TEST_VALKEY_ADDR to run against valkey")
	}
	return addr
}

func testDatabaseURLFromEnv(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("set TEST_DATABASE_URL to run against postgres")
	}
	return u
}

// startRun calls runWith in a goroutine and returns the address it logs as
// listening, and a channel that receives its result.
func startRun(ctx context.Context, t *testing.T, getenv func(string) string, retry discoveryRetry) (string, <-chan error) {
	t.Helper()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := runWith(ctx, []string{"web"}, getenv, io.Discard, pw, retry)
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
		t.Fatalf("no listening log line; runWith() returned %v", <-done)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }() // keeps later log writes from blocking
	return addr, done
}

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

func TestRun_MissingConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"web"}, noEnv, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() with an empty environment error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "WEB_BASE_URL") {
		t.Errorf("run() with an empty environment error = %q, want it to name WEB_BASE_URL", err)
	}
}

func TestRun_ErrorHidesClientSecret(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	unreachable := closed.URL
	closed.Close()

	var stderr bytes.Buffer
	err := runWith(t.Context(), []string{"web"}, runEnv(t, unreachable, "127.0.0.1:1", testDatabaseURL), io.Discard, &stderr, fastRetry())
	if err == nil {
		t.Fatalf("runWith() with issuer %s error = nil, want an error", unreachable)
	}
	if strings.Contains(err.Error(), testClientSecret) {
		t.Errorf("runWith() error = %q, contains the client secret", err)
	}
	if strings.Contains(stderr.String(), testClientSecret) {
		t.Errorf("runWith() stderr = %q, contains the client secret", stderr.String())
	}
	if strings.Contains(stderr.String(), testSecretID) {
		t.Errorf("runWith() stderr = %q, contains the secret-id", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"bao_role_id":"se-web"`) {
		t.Errorf("runWith() stderr = %q, want a start-up line with the configuration", stderr.String())
	}
}

func TestRun_GivesUpOnDiscovery(t *testing.T) {
	issuer := newFakeIssuer(t, -1)

	var stderr bytes.Buffer
	err := runWith(t.Context(), []string{"web"}, runEnv(t, issuer.URL(), "127.0.0.1:1", testDatabaseURL), io.Discard, &stderr, fastRetry())
	if err == nil {
		t.Fatalf("runWith() with issuer %s that always answers 503 error = nil, want an error", issuer.URL())
	}
	if !strings.Contains(err.Error(), issuer.URL()) {
		t.Errorf("runWith() error = %q, want it to name the issuer %s", err, issuer.URL())
	}
	if got := issuer.Requests(); got < 2 {
		t.Errorf("issuer got %d discovery requests, want at least 2", got)
	}
}

func TestDiscover_GiveUpNamesDiscoveryURL(t *testing.T) {
	issuer := newFakeIssuer(t, -1)
	cfg := validConfigFor(t, "http://localhost:8081/realms/secret-externalizer")
	cfg.oidc.DiscoveryURL = issuer.URL()

	_, err := discover(t.Context(), discardLogger(), cfg.oidc, outboundClient(), fastRetry())
	if err == nil {
		t.Fatalf("discover() against %s that always answers 503 error = nil, want an error", issuer.URL())
	}
	for _, want := range []string{cfg.oidc.IssuerURL, issuer.URL()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("discover() error = %q, want it to name %s", err, want)
		}
	}
}

func TestRun_RetriesDiscovery(t *testing.T) {
	valkeyAddr := testValkeyAddr(t)
	databaseURL := testDatabaseURLFromEnv(t)
	issuer := newFakeIssuer(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	_, done := startRun(ctx, t, runEnv(t, issuer.URL(), valkeyAddr, databaseURL), fastRetry())
	if got := issuer.Requests(); got != 3 {
		t.Errorf("issuer got %d discovery requests before run listened, want 3", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("runWith() after cancel = %v, want nil", err)
	}
}

func TestRun_ServesUntilContextIsCancelled(t *testing.T) {
	valkeyAddr := testValkeyAddr(t)
	databaseURL := testDatabaseURLFromEnv(t)
	issuer := newFakeIssuer(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addr, done := startRun(ctx, t, runEnv(t, issuer.URL(), valkeyAddr, databaseURL), fastRetry())

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
		t.Errorf("runWith() after cancel = %v, want nil", err)
	}
}

func TestDiscover_RetriesUntilReady(t *testing.T) {
	issuer := newFakeIssuer(t, 2)
	cfg := validConfigFor(t, issuer.URL())

	a, err := discover(t.Context(), discardLogger(), cfg.oidc, outboundClient(), fastRetry())
	if err != nil {
		t.Fatalf("discover() with issuer that fails twice error = %v, want nil", err)
	}
	if a == nil {
		t.Fatal("discover() returned a nil authenticator")
	}
	if got := issuer.Requests(); got != 3 {
		t.Errorf("issuer got %d discovery requests, want 3", got)
	}
}

func TestDiscover_StopsWhenContextIsCancelled(t *testing.T) {
	issuer := newFakeIssuer(t, -1)
	cfg := validConfigFor(t, issuer.URL())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The interval is far longer than the test may run, so only the
	// cancellation can end the wait.
	slow := discoveryRetry{interval: time.Hour, limit: 2 * time.Hour}
	done := make(chan error, 1)
	go func() {
		_, err := discover(ctx, discardLogger(), cfg.oidc, outboundClient(), slow)
		done <- err
	}()
	bound := time.NewTimer(10 * time.Second)
	defer bound.Stop()
	select {
	case <-issuer.firstRequest:
	case err := <-done:
		t.Fatalf("discover() returned %v before asking the issuer", err)
	case <-bound.C:
		t.Fatal("discover() did not ask the issuer within 10s")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("discover() after cancel error = %v, want context.Canceled", err)
		}
	case <-bound.C:
		t.Fatal("discover() did not return within 10s of cancellation")
	}
}

func validConfigFor(t *testing.T, issuerURL string) config {
	t.Helper()
	env := validEnv(t)
	env["OIDC_ISSUER_URL"] = issuerURL
	delete(env, "OIDC_DISCOVERY_URL")
	cfg, err := loadConfig(nil, getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	return cfg
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestNewHTTPServer_ErrorLogIsJSON(t *testing.T) {
	var buf bytes.Buffer
	srv := newHTTPServer(slog.New(slog.NewJSONHandler(&buf, nil)), http.NotFoundHandler())
	if srv.ErrorLog == nil {
		t.Fatal("newHTTPServer().ErrorLog = nil, want a logger that writes through slog")
	}
	srv.ErrorLog.Print("http: fake server message")

	var rec struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("ErrorLog output %q is not JSON: %v", buf.String(), err)
	}
	if rec.Level != "ERROR" || rec.Msg != "http: fake server message" {
		t.Errorf("ErrorLog output level, msg = %q, %q, want %q, %q", rec.Level, rec.Msg, "ERROR", "http: fake server message")
	}
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("newHTTPServer() timeouts = %v %v %v %v, want all set", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}

func TestValkeyRetryDelay_GivesUp(t *testing.T) {
	var cmd valkey.Completed
	for attempts := 1; attempts <= 2; attempts++ {
		if got := valkeyRetryDelay(attempts, cmd, io.EOF); got < 0 {
			t.Errorf("valkeyRetryDelay(%d) = %v, want a retry", attempts, got)
		}
	}
	if got := valkeyRetryDelay(3, cmd, io.EOF); got >= 0 {
		t.Errorf("valkeyRetryDelay(3) = %v, want a negative delay that stops retrying", got)
	}
}

func TestValkeyOption_BoundsWaiting(t *testing.T) {
	opt := valkeyOption("valkey:6379")
	if diff := cmp.Diff([]string{"valkey:6379"}, opt.InitAddress); diff != "" {
		t.Errorf("valkeyOption().InitAddress mismatch (-want +got):\n%s", diff)
	}
	if opt.Dialer.Timeout <= 0 || opt.Dialer.Timeout > 5*time.Second {
		t.Errorf("valkeyOption().Dialer.Timeout = %v, want between 0 and 5s", opt.Dialer.Timeout)
	}
	if opt.RetryDelay == nil {
		t.Error("valkeyOption().RetryDelay = nil, want a bounded retry")
	}
}
