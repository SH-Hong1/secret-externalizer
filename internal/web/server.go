// Package web provides the HTTP handler for the secret-externalizer user
// interface.
package web

import (
	"log/slog"
	"net/http"
)

// NewServer returns the complete handler, including middleware.
func NewServer(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	rd := newRenderer()
	addRoutes(mux, logger, rd)

	var h http.Handler = mux
	h = securityHeaders(h)
	h = requestLog(logger, h)
	h = recoverPanic(logger, rd, h)
	return h
}
