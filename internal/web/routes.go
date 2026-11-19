package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// addRoutes registers every route of the server. Only the routes that need a
// signed-in user read the session, so /healthz answers and logout works even
// when the session store is down. The routes that call projects or scans get
// a deadline of storeLimit. The static files need no session.
func addRoutes(mux *http.ServeMux, logger *slog.Logger, rd *renderer, a *assets, authn Authenticator, sessions SessionStore, attempts AttemptStore, projects ProjectStore, scans ScanStore, storeLimit time.Duration) {
	signedIn := func(h http.Handler) http.Handler {
		return chain(h, loadSession(logger, rd, sessions), requireLogin(logger, rd))
	}
	// usesStore is signedIn for a route that calls the database.
	usesStore := func(h http.Handler) http.Handler {
		return signedIn(timeLimit(storeLimit)(h))
	}
	withRole := func(role auth.Role, h http.Handler) http.Handler {
		return chain(h, loadSession(logger, rd, sessions), requireLogin(logger, rd), requireRole(logger, rd, role))
	}

	mux.Handle("GET /healthz", handleHealthz())
	mux.Handle("GET /static/{path...}", handleStatic(logger, rd, a))
	mux.Handle("GET /login", handleLogin(logger, rd, authn, attempts))
	mux.Handle("GET /auth/callback", handleCallback(logger, rd, authn, sessions, attempts))
	// Logout needs no live session; see handleLogout.
	mux.Handle("POST /logout", handleLogout(logger, authn, sessions))
	mux.Handle("/logout", handleMethodNotAllowed(logger, rd, http.MethodPost))
	mux.Handle("GET /{$}", signedIn(handleHome(logger, rd)))
	mux.Handle("GET /audit", withRole(auth.RoleAdmin, handleAudit(logger, rd)))
	mux.Handle("GET /projects", usesStore(handleProjectList(logger, rd, projects)))
	mux.Handle("GET /projects/new", signedIn(handleProjectNew(logger, rd)))
	mux.Handle("POST /projects", usesStore(handleProjectCreate(logger, rd, projects)))
	mux.Handle("GET /projects/{id}", usesStore(handleProject(logger, rd, projects, scans)))
	mux.Handle("POST /projects/{id}/scans", usesStore(handleScanCreate(logger, rd, scans)))
	mux.Handle("GET /scans/{id}", usesStore(handleScan(logger, rd, projects, scans)))
	mux.Handle("/", handleNotFound(logger, rd))
}
