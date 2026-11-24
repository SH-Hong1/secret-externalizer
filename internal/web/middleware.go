package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// securityHeaders sets the headers every response carries. The CSP has no
// form-action directive, because the logout form's redirect goes to the
// identity provider, which form-action 'self' would block.
func securityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			// Pages show who is signed in and what they may see, so neither
			// the browser nor a proxy keeps a copy.
			h.Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder remembers the status code written to the response and
// whether the response has started.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	started bool
}

// Unwrap returns the underlying writer so http.ResponseController can reach it.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	if code >= http.StatusOK {
		s.started = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.started = true
	return s.ResponseWriter.Write(b)
}

// requestLog logs the method, path, status and duration of each request. It
// leaves out the query string and headers because they can hold secrets.
func requestLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.InfoContext(r.Context(), "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// recoverPanic turns a panic in a handler into a 500 error page. It logs the
// request method and path, the type of the panic value, and a stack trace,
// never the value itself or the query string, because they can carry secret
// data. When the response has already started, the error page would be
// appended to it, so it only logs. It re-panics
// http.ErrAbortHandler so that net/http can abort the response as intended.
func recoverPanic(logger *slog.Logger, rd *renderer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			defer func(r *http.Request) {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler { //nolint:errorlint // net/http panics with the sentinel itself
					panic(v)
				}
				logger.ErrorContext(r.Context(), "handler panic",
					"method", r.Method,
					"path", r.URL.Path,
					"panic_type", fmt.Sprintf("%T", v),
					"stack", string(debug.Stack()),
				)
				if rec.started {
					return
				}
				rd.fail(rec, r, logger, http.StatusInternalServerError, errors.New("handler panic"))
			}(r)
			next.ServeHTTP(rec, r)
		})
	}
}

// crossOriginProtection rejects state-changing requests that a browser sends
// from another origin, and shows the 403 page for them.
func crossOriginProtection(logger *slog.Logger, rd *renderer) func(http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd.fail(w, r, logger, http.StatusForbidden, errors.New("cross-origin request"))
	}))
	return cop.Handler
}

// userKey is the context key under which loadSession stores the user.
type userKey struct{}

// userFrom returns the signed-in user of the request, if there is one.
func userFrom(ctx context.Context) (auth.User, bool) {
	u, ok := ctx.Value(userKey{}).(auth.User)
	return u, ok
}

// loadSession reads the session named by the se_session cookie and puts its
// user in the request context. A cookie for a session that no longer exists is
// deleted and the request goes on without a user. Any other store error shows
// the 503 page, so a store outage never looks like a logout.
func loadSession(logger *slog.Logger, rd *renderer, sessions SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(sessionCookie)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			s, err := sessions.Get(r.Context(), session.ID(c.Value))
			if errors.Is(err, session.ErrNotFound) {
				clearCookie(w, sessionCookie)
				next.ServeHTTP(w, r)
				return
			}
			if err != nil {
				rd.fail(w, r, logger, http.StatusServiceUnavailable, fmt.Errorf("loading signed-in user: %w", err))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, s.User)))
		})
	}
}

// requireLogin lets only signed-in requests through. A GET without a user is
// sent to the login page, which returns it here afterwards; any other method
// gets 401, because replaying a form after login is not safe.
func requireLogin(logger *slog.Logger, rd *renderer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := userFrom(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				q := url.Values{"return_to": {r.URL.RequestURI()}}
				if r.Header.Get("HX-Request") == "true" {
					// htmx would follow a redirect inside its own
					// request, which cannot reach the identity
					// provider. This header makes the browser leave
					// the page for the login.
					w.Header().Set("HX-Redirect", "/login?"+q.Encode())
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, "/login?"+q.Encode(), http.StatusSeeOther)
				return
			}
			rd.fail(w, r, logger, http.StatusUnauthorized, errors.New("not signed in"))
		})
	}
}

// requireRole lets through only users who have role. It runs inside
// requireLogin.
func requireRole(logger *slog.Logger, rd *renderer, role auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := userFrom(r.Context())
			if !ok || !u.Has(role) {
				rd.fail(w, r, logger, http.StatusForbidden, errors.New("missing role"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// timeLimit gives the request's context a deadline limit from now, so that
// the handler's calls to the database end with an error instead of hanging
// when the database does not answer.
func timeLimit(limit time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), limit)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
