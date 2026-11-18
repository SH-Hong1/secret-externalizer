package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRenderer_FailHidesError(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	rec := httptest.NewRecorder()
	req := newTestRequest(t)

	newRenderer().fail(rec, req, logger, http.StatusInternalServerError, errors.New("dial tcp 10.0.0.5"))

	body := rec.Body.String()
	if strings.Contains(body, "10.0.0.5") {
		t.Errorf("fail(500) body = %q, want the error text left out", body)
	}
	if want := "처리 중 문제가 생겼습니다. 잠시 뒤 다시 시도해 주세요."; !strings.Contains(body, want) {
		t.Errorf("fail(500) body = %q, want it to contain %q", body, want)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("fail(500) status = %d, want 500", rec.Code)
	}
	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Errorf("fail(500) log = %q, want exactly one line", logBuf.String())
	}
}

func TestRenderer_FailClientErrorNotLogged(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	rec := httptest.NewRecorder()
	req := newTestRequest(t)

	newRenderer().fail(rec, req, logger, http.StatusNotFound, errors.New("missing"))

	if logBuf.Len() != 0 {
		t.Errorf("fail(404) log = %q, want no log line", logBuf.String())
	}
	if want := "화면을 찾을 수 없습니다."; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("fail(404) body = %q, want it to contain %q", rec.Body.String(), want)
	}
}

func TestRenderer_UnknownTemplate(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	rec := httptest.NewRecorder()
	req := newTestRequest(t)

	newRenderer().page(rec, req, logger, http.StatusOK, "nope.html", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("page(unknown) status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if want := "처리 중 문제가 생겼습니다."; !strings.Contains(body, want) {
		t.Errorf("page(unknown) body = %q, want it to contain %q", body, want)
	}
	if n := strings.Count(body, "<html"); n != 1 {
		t.Errorf("page(unknown) body has %d <html> tags, want 1 (no partial page)", n)
	}
}

func TestRecoverPanic(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	h := recoverPanic(logger, newRenderer(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("fake-secret-value")
	}))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newTestRequest(t))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panicking handler status = %d, want 500", rec.Code)
	}
	if want := "처리 중 문제가 생겼습니다."; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("panicking handler body = %q, want it to contain %q", rec.Body.String(), want)
	}
	if strings.Contains(rec.Body.String(), "fake-secret-value") {
		t.Errorf("body = %q, want the panic message left out", rec.Body.String())
	}
	if strings.Contains(logBuf.String(), "fake-secret-value") {
		t.Errorf("log = %q, want the panic message left out", logBuf.String())
	}
	if got := panicType(t, logBuf.String()); got != "string" {
		t.Errorf("log panic_type = %q, want %q", got, "string")
	}
}

func TestRecoverPanic_AfterPartialWrite(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	h := recoverPanic(logger, newRenderer(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial")) // the recorder never fails a write
		panic("fake-secret-value")
	}))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newTestRequest(t))

	if got := rec.Body.String(); got != "partial" {
		t.Errorf("body after a panic past the first write = %q, want %q with no error page", got, "partial")
	}
	if got := panicType(t, logBuf.String()); got != "string" {
		t.Errorf("log panic_type = %q, want %q", got, "string")
	}
}

func TestParseTemplates_MissingErrorPage(t *testing.T) {
	fsys := fstest.MapFS{
		"templates/layout.html": {Data: []byte(`{{define "layout"}}{{template "body" .}}{{end}}`)},
		"templates/home.html":   {Data: []byte(`{{define "title"}}t{{end}}{{define "body"}}b{{end}}`)},
	}
	defer func() {
		if recover() == nil {
			t.Errorf("parseTemplates(no error.html) did not panic, want a start-up panic")
		}
	}()
	parseTemplates(fsys)
}

// panicType returns the panic_type attribute of the first log line that has one.
func panicType(t *testing.T, log string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var rec struct {
			PanicType string `json:"panic_type"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if rec.PanicType != "" {
			return rec.PanicType
		}
	}
	return ""
}

func TestRecoverPanic_AbortHandler(t *testing.T) {
	h := recoverPanic(slog.New(slog.DiscardHandler), newRenderer(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // recover returns the sentinel itself, never a wrapped error
			t.Errorf("recovered %v, want http.ErrAbortHandler", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), newTestRequest(t))
}

func TestStatusRecorder_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	if got := sr.Unwrap(); got != http.ResponseWriter(rec) {
		t.Errorf("Unwrap() = %v, want the wrapped writer", got)
	}
}

func newTestRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
}
