package web

import "net/http"

// addRoutes registers every route of the server.
func addRoutes(mux *http.ServeMux) {
	mux.Handle("GET /healthz", handleHealthz())
}
