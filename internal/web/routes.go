package web

import (
	"log/slog"
	"net/http"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// addRoutes registers every route of the server. Only the routes that need a
// signed-in user read the session, so /healthz answers even when the session
// store is down.
func addRoutes(mux *http.ServeMux, logger *slog.Logger, rd *renderer, authn Authenticator, sessions SessionStore, attempts AttemptStore) {
	withSession := loadSession(logger, rd, sessions)
	signedIn := func(h http.Handler) http.Handler {
		return withSession(requireLogin(logger, rd, h))
	}
	withRole := func(role auth.Role, h http.Handler) http.Handler {
		return signedIn(requireRole(logger, rd, role, h))
	}

	mux.Handle("GET /healthz", handleHealthz())
	mux.Handle("GET /login", handleLogin(logger, rd, authn, attempts))
	mux.Handle("GET /auth/callback", handleCallback(logger, rd, authn, sessions, attempts))
	mux.Handle("POST /logout", signedIn(handleLogout(logger, rd, authn, sessions)))
	mux.Handle("/logout", handleMethodNotAllowed(logger, rd, http.MethodPost))
	mux.Handle("GET /{$}", signedIn(handleHome(logger, rd)))
	mux.Handle("GET /audit", withRole(auth.RoleAdmin, handleAudit(logger, rd)))
	mux.Handle("/", handleNotFound(logger, rd))
}
