package web

import (
	"errors"
	"log/slog"
	"net/http"
)

func handleHealthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok")) // a failed write means the client is gone; there is nobody to tell
	})
}

func handleNotFound(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd.fail(w, r, logger, http.StatusNotFound, errors.New("no route"))
	})
}

// handleMethodNotAllowed answers a path that exists only for other methods.
// allow lists those methods.
func handleMethodNotAllowed(logger *slog.Logger, rd *renderer, allow string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		rd.fail(w, r, logger, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	})
}

// handleHome shows the signed-in user's name and roles and a logout button.
func handleHome(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		rd.page(w, r, logger, http.StatusOK, "home.html", u)
	})
}

// handleAudit shows the audit log page.
func handleAudit(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd.page(w, r, logger, http.StatusOK, "audit.html", nil)
	})
}
