package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// securityHeaders sets the headers every response carries.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code written to the response.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// Unwrap returns the underlying writer so http.ResponseController can reach it.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// requestLog logs the method, path, status and duration of each request. It
// leaves out the query string and headers because they can hold secrets.
func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// recoverPanic turns a panic in a handler into a 500 error page. It logs the
// type of the panic value and a stack trace, never the value itself, because
// the value can carry secret data. It re-panics http.ErrAbortHandler so that
// net/http can abort the response as intended.
func recoverPanic(logger *slog.Logger, rd *renderer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // net/http panics with the sentinel itself
				panic(v)
			}
			logger.ErrorContext(r.Context(), "handler panic",
				"panic_type", fmt.Sprintf("%T", v),
				"stack", string(debug.Stack()),
			)
			rd.fail(w, r, logger, http.StatusInternalServerError, errors.New("handler panic"))
		}()
		next.ServeHTTP(w, r)
	})
}
