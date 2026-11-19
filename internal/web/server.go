// Package web provides the HTTP handler for the secret-externalizer user
// interface.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
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

// ProjectStore keeps the registered projects. CreateProject fails with an
// error matching db.ErrDuplicate when the name or path is taken, and Project
// with one matching db.ErrNotFound when the ID is unknown.
type ProjectStore interface {
	CreateProject(ctx context.Context, p project.Project) (project.ID, error)
	Project(ctx context.Context, id project.ID) (project.Project, error)
	Projects(ctx context.Context) ([]project.Project, error)
}

// ScanStore keeps the scans. RequestScan returns the ID of the project's
// active scan together with an error matching db.ErrScanActive when one is
// already queued or running, and an error matching db.ErrNotFound when the
// project is unknown. Scan fails with an error matching db.ErrNotFound when
// the ID is unknown. ScansByProject returns the project's scans, newest
// first. FindingsByScan returns the findings of one scan, ordered by file and
// line, with the locations of that scan only.
type ScanStore interface {
	RequestScan(ctx context.Context, projectID project.ID, requestedBy string) (scan.ID, error)
	Scan(ctx context.Context, id scan.ID) (scan.Scan, error)
	ScansByProject(ctx context.Context, projectID project.ID) ([]scan.Scan, error)
	FindingsByScan(ctx context.Context, id scan.ID) ([]finding.Finding, error)
}

// NewServer returns the complete handler, including middleware. From the
// outside in, a request passes panic recovery, the request log, the security
// headers, and cross-origin protection before it reaches its route. A route
// that calls projects or scans runs under a deadline storeLimit from the
// moment it is reached; the session is read before that deadline starts.
func NewServer(logger *slog.Logger, authn Authenticator, sessions SessionStore, attempts AttemptStore, projects ProjectStore, scans ScanStore, storeLimit time.Duration) http.Handler {
	mux := http.NewServeMux()
	a := loadAssets(staticFiles())
	rd := newRenderer(a)
	addRoutes(mux, logger, rd, a, authn, sessions, attempts, projects, scans, storeLimit)

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
