package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if !strings.Contains(logBuf.String(), "string") {
		t.Errorf("log = %q, want the panic value type", logBuf.String())
	}
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
