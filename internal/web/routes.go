package web

import (
	"log/slog"
	"net/http"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// addRoutes registers every route of the server. Only the routes that need a
// signed-in user read the session, so /healthz answers and logout works even
// when the session store is down.
func addRoutes(mux *http.ServeMux, logger *slog.Logger, rd *renderer, authn Authenticator, sessions SessionStore, attempts AttemptStore, projects ProjectStore, scans ScanStore) {
	signedIn := func(h http.Handler) http.Handler {
		return chain(h, loadSession(logger, rd, sessions), requireLogin(logger, rd))
	}
	withRole := func(role auth.Role, h http.Handler) http.Handler {
		return chain(h, loadSession(logger, rd, sessions), requireLogin(logger, rd), requireRole(logger, rd, role))
	}

	mux.Handle("GET /healthz", handleHealthz())
	mux.Handle("GET /login", handleLogin(logger, rd, authn, attempts))
	mux.Handle("GET /auth/callback", handleCallback(logger, rd, authn, sessions, attempts))
	// Logout needs no live session; see handleLogout.
	mux.Handle("POST /logout", handleLogout(logger, authn, sessions))
	mux.Handle("/logout", handleMethodNotAllowed(logger, rd, http.MethodPost))
	mux.Handle("GET /{$}", signedIn(handleHome(logger, rd)))
	mux.Handle("GET /audit", withRole(auth.RoleAdmin, handleAudit(logger, rd)))
	mux.Handle("GET /projects", signedIn(handleProjectList(logger, rd, projects)))
	mux.Handle("GET /projects/new", signedIn(handleProjectNew(logger, rd)))
	mux.Handle("POST /projects", signedIn(handleProjectCreate(logger, rd, projects)))
	mux.Handle("GET /projects/{id}", signedIn(handleProject(logger, rd, projects)))
	mux.Handle("POST /projects/{id}/scans", signedIn(handleScanCreate(logger, rd, scans)))
	mux.Handle("GET /scans/{id}", signedIn(handleScan(logger, rd, scans)))
	mux.Handle("/", handleNotFound(logger, rd))
}
