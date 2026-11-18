// Package web provides the HTTP handler for the secret-externalizer user
// interface.
package web

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// Authenticator runs the login with the identity provider.
type Authenticator interface {
	AuthCodeURL(state, nonce, verifier string) string
	Exchange(ctx context.Context, code, verifier, nonce string) (auth.User, error)
	LogoutURL() string
}

// SessionStore keeps the sessions of signed-in users.
type SessionStore interface {
	Create(ctx context.Context, u auth.User) (session.ID, error)
	Get(ctx context.Context, id session.ID) (session.Session, error)
	Delete(ctx context.Context, id session.ID) error
}

// AttemptStore keeps login attempts between the redirect to the identity
// provider and its callback. TakeAttempt removes the attempt it returns.
type AttemptStore interface {
	PutAttempt(ctx context.Context, a auth.Attempt) (session.ID, error)
	TakeAttempt(ctx context.Context, id session.ID) (auth.Attempt, error)
}

// NewServer returns the complete handler, including middleware. From the
// outside in, a request passes panic recovery, the request log, the security
// headers, and cross-origin protection before it reaches its route.
func NewServer(logger *slog.Logger, authn Authenticator, sessions SessionStore, attempts AttemptStore) http.Handler {
	mux := http.NewServeMux()
	rd := newRenderer()
	addRoutes(mux, logger, rd, authn, sessions, attempts)

	return chain(mux,
		recoverPanic(logger, rd),
		requestLog(logger),
		securityHeaders(),
		crossOriginProtection(logger, rd),
	)
}

// chain wraps h in middleware. The first middleware is the outermost, so a
// request passes them in the order given.
func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}
