package web

import (
	"log/slog"
	"net/http"
)

// addRoutes registers every route of the server.
func addRoutes(mux *http.ServeMux, logger *slog.Logger, rd *renderer) {
	mux.Handle("GET /healthz", handleHealthz())
	mux.Handle("/", handleNotFound(logger, rd))
}
