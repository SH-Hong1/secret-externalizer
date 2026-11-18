package web

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

const (
	sessionCookie = "se_session"
	loginCookie   = "se_login"
	// loginCookieMaxAge is session.AttemptTimeout in seconds, so the cookie
	// lives as long as the attempt it names.
	loginCookieMaxAge = int(session.AttemptTimeout / time.Second)
)

// setCookie sets a cookie with the attributes every cookie of this server
// carries. maxAge 0 makes a browser-session cookie.
func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearCookie tells the browser to delete the cookie name.
func clearCookie(w http.ResponseWriter, name string) {
	setCookie(w, name, "", -1)
}

// safeReturnTo returns s when it is a path on this site, and "/" otherwise.
// It rejects "//host" and "/\host", which browsers read as another host.
func safeReturnTo(s string) string {
	if len(s) == 0 || s[0] != '/' {
		return "/"
	}
	if len(s) > 1 && (s[1] == '/' || s[1] == '\\') {
		return "/"
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return s
}

// handleLogin starts a login attempt and sends the browser to the identity
// provider. The attempt is found again through the se_login cookie.
func handleLogin(logger *slog.Logger, rd *renderer, authn Authenticator, attempts AttemptStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := auth.NewAttempt(safeReturnTo(r.URL.Query().Get("return_to")))
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("creating login attempt: %w", err))
			return
		}
		id, err := attempts.PutAttempt(r.Context(), a)
		if err != nil {
			rd.fail(w, r, logger, http.StatusServiceUnavailable, fmt.Errorf("storing login attempt: %w", err))
			return
		}
		setCookie(w, loginCookie, id.Reveal(), loginCookieMaxAge)
		http.Redirect(w, r, authn.AuthCodeURL(a.State, a.Nonce, a.Verifier), http.StatusSeeOther)
	})
}

// handleCallback finishes a login. It takes the attempt named by the se_login
// cookie, so each attempt is used at most once, checks the state, exchanges
// the code, and only then creates a session.
func handleCallback(logger *slog.Logger, rd *renderer, authn Authenticator, sessions SessionStore, attempts AttemptStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		c, cookieErr := r.Cookie(loginCookie)
		clearCookie(w, loginCookie)

		if q.Has("error") {
			if cookieErr == nil {
				// The attempt expires on its own, and the user sees the
				// error page whether or not this removal succeeds.
				_, _ = attempts.TakeAttempt(r.Context(), session.ID(c.Value))
			}
			rd.fail(w, r, logger, http.StatusBadRequest, errors.New("identity provider returned an error"))
			return
		}
		if cookieErr != nil {
			rd.fail(w, r, logger, http.StatusBadRequest, errors.New("no login attempt cookie"))
			return
		}

		a, err := attempts.TakeAttempt(r.Context(), session.ID(c.Value))
		if errors.Is(err, session.ErrNotFound) {
			rd.fail(w, r, logger, http.StatusBadRequest, errors.New("unknown login attempt"))
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusServiceUnavailable, fmt.Errorf("taking login attempt: %w", err))
			return
		}
		if a.State == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.State)) != 1 {
			rd.fail(w, r, logger, http.StatusBadRequest, errors.New("state mismatch"))
			return
		}

		u, err := authn.Exchange(r.Context(), q.Get("code"), a.Verifier, a.Nonce)
		if errors.Is(err, auth.ErrNoRole) {
			rd.fail(w, r, logger, http.StatusForbidden, err)
			return
		}
		if err != nil {
			logger.WarnContext(r.Context(), "login exchange failed", "error", err)
			rd.fail(w, r, logger, http.StatusBadRequest, err)
			return
		}

		id, err := sessions.Create(r.Context(), u)
		if err != nil {
			rd.fail(w, r, logger, http.StatusServiceUnavailable, fmt.Errorf("creating session: %w", err))
			return
		}
		setCookie(w, sessionCookie, id.Reveal(), 0)
		http.Redirect(w, r, safeReturnTo(a.ReturnTo), http.StatusSeeOther)
	})
}

// handleLogout ends the session and sends the browser to the identity
// provider's logout page. It runs behind requireLogin, so the session cookie
// is present.
func handleLogout(logger *slog.Logger, rd *renderer, authn Authenticator, sessions SessionStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			rd.fail(w, r, logger, http.StatusUnauthorized, err)
			return
		}
		if err := sessions.Delete(r.Context(), session.ID(c.Value)); err != nil {
			rd.fail(w, r, logger, http.StatusServiceUnavailable, fmt.Errorf("deleting session: %w", err))
			return
		}
		clearCookie(w, sessionCookie)
		http.Redirect(w, r, authn.LogoutURL(), http.StatusSeeOther)
	})
}
