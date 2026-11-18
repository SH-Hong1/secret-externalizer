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
