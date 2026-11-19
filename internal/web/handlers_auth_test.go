package web_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
	"github.com/SH-Hong1/secret-externalizer/internal/web"
)

const fakeCode = "fake-auth-code-0000"

// testEnv is a server built on fakes, with its log captured.
type testEnv struct {
	handler   http.Handler
	log       *bytes.Buffer
	authn     *fakeAuthenticator
	sessions  *fakeSessions
	attempts  *fakeAttempts
	projects  *fakeProjects
	scans     *fakeScans
	triage    *fakeTriage
	calls     *callLog
	audits    *fakeAudit
	originals *fakeOriginals
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{
		log:      &bytes.Buffer{},
		authn:    &fakeAuthenticator{user: auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}}},
		sessions: newFakeSessions(),
		attempts: newFakeAttempts(),
		projects: newFakeProjects(),
		scans:    newFakeScans(),
		triage:   newFakeTriage(),
		calls:    &callLog{},
	}
	e.audits = &fakeAudit{log: e.calls}
	e.originals = &fakeOriginals{log: e.calls}
	logger := slog.New(slog.NewJSONHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.handler = web.NewServer(logger, e.authn, e.sessions, e.attempts, e.projects, e.scans, e.triage, web.NewHistorySuggester(e.triage), e.audits, e.originals, time.Minute)
	return e
}

// cookie is a cookie a browser sends back.
type cookie struct{ name, value string }

// result is a response with its body read.
type result struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
	body    string
}

// location returns the Location header.
func (r result) location() string { return r.header.Get("Location") }

// cookie returns the cookie named name that the response sets, or nil.
func (r result) cookie(name string) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// do sends one request to the server and returns the response.
func (e *testEnv) do(t *testing.T, method, target string, header http.Header, cookies ...cookie) result {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for _, c := range cookies {
		req.Header.Add("Cookie", c.name+"="+c.value)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return result{status: rec.Code, header: rec.Header(), cookies: (&http.Response{Header: rec.Header()}).Cookies(), body: rec.Body.String()}
}

// login runs GET /login?return_to=returnTo and returns the se_login cookie
// and the state the provider was given.
func (e *testEnv) login(t *testing.T, returnTo string) (cookie, string) {
	t.Helper()
	res := e.do(t, http.MethodGet, "/login?"+url.Values{"return_to": {returnTo}}.Encode(), nil)
	if res.status != http.StatusSeeOther {
		t.Fatalf("GET /login status = %d, want 303", res.status)
	}
	loc, err := url.Parse(res.location())
	if err != nil {
		t.Fatalf("GET /login Location %q: %v", res.location(), err)
	}
	c := res.cookie("se_login")
	if c == nil {
		t.Fatalf("GET /login set no se_login cookie")
	}
	return cookie{c.Name, c.Value}, loc.Query().Get("state")
}

// callback runs GET /auth/callback with state and the fake code.
func (e *testEnv) callback(t *testing.T, state string, cookies ...cookie) result {
	t.Helper()
	q := url.Values{"state": {state}, "code": {fakeCode}}
	return e.do(t, http.MethodGet, "/auth/callback?"+q.Encode(), nil, cookies...)
}

// signIn stores a session for u and returns its cookie.
func (e *testEnv) signIn(t *testing.T, u auth.User) cookie {
	t.Helper()
	id, err := e.sessions.Create(t.Context(), u)
	if err != nil {
		t.Fatalf("fakeSessions.Create() error = %v", err)
	}
	return cookie{"se_session", id.Reveal()}
}

// checkCookie checks the attributes every cookie of this server carries and
// its lifetime. wantMaxAge is 0 for a browser-session cookie and negative for
// a deleted one.
func checkCookie(t *testing.T, res result, name string, wantMaxAge int) {
	t.Helper()
	c := res.cookie(name)
	if c == nil {
		t.Fatalf("response sets no %s cookie, want one", name)
	}
	if c.Path != "/" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie %s = Path %q HttpOnly %v Secure %v SameSite %v, want Path \"/\" HttpOnly Secure SameSite=Lax",
			name, c.Path, c.HttpOnly, c.Secure, c.SameSite)
	}
	switch {
	case wantMaxAge < 0 && c.MaxAge >= 0:
		t.Errorf("cookie %s MaxAge = %d, want a deletion", name, c.MaxAge)
	case wantMaxAge >= 0 && c.MaxAge != wantMaxAge:
		t.Errorf("cookie %s MaxAge = %d, want %d", name, c.MaxAge, wantMaxAge)
	}
	if wantMaxAge == 0 && !c.Expires.IsZero() {
		t.Errorf("cookie %s Expires = %v, want none for a browser-session cookie", name, c.Expires)
	}
}

func sameOrigin() http.Header { return http.Header{"Sec-Fetch-Site": {"same-origin"}} }

func TestHome_RedirectsToLogin(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/?x=1", nil)
	if res.status != http.StatusSeeOther {
		t.Errorf("GET /?x=1 status = %d, want 303", res.status)
	}
	if got, want := res.location(), "/login?return_to=%2F%3Fx%3D1"; got != want {
		t.Errorf("GET /?x=1 Location = %q, want %q", got, want)
	}
}

func TestLogin_StartsAttempt(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/login?return_to=/audit", nil)
	if res.status != http.StatusSeeOther {
		t.Fatalf("GET /login status = %d, want 303", res.status)
	}
	loc := res.location()
	if !strings.HasPrefix(loc, fakeAuthURL+"?") {
		t.Errorf("GET /login Location = %q, want the provider address %q", loc, fakeAuthURL)
	}
	checkCookie(t, res, "se_login", 600)

	a, ok := e.attempts.get(res.cookie("se_login").Value)
	if !ok {
		t.Fatalf("no attempt stored under the se_login cookie")
	}
	if a.ReturnTo != "/audit" {
		t.Errorf("stored attempt ReturnTo = %q, want %q", a.ReturnTo, "/audit")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parsing Location %q: %v", loc, err)
	}
	if got := u.Query().Get("state"); got != a.State {
		t.Errorf("provider was given state %q, want the stored attempt's state", got)
	}
}

func TestLogin_StoreDown(t *testing.T) {
	e := newTestEnv(t)
	e.attempts.putErr = errors.New("dial tcp 192.0.2.1:6379: connection refused")
	res := e.do(t, http.MethodGet, "/login", nil)
	if res.status != http.StatusServiceUnavailable {
		t.Errorf("GET /login with the store down status = %d, want 503", res.status)
	}
	if res.cookie("se_login") != nil {
		t.Errorf("GET /login with the store down set se_login, want no cookie")
	}
}

func TestCallback_CreatesSession(t *testing.T) {
	e := newTestEnv(t)
	c, state := e.login(t, "/audit")

	res := e.callback(t, state, c)

	if res.status != http.StatusSeeOther {
		t.Errorf("callback status = %d, want 303", res.status)
	}
	if got := res.location(); got != "/audit" {
		t.Errorf("callback Location = %q, want %q", got, "/audit")
	}
	checkCookie(t, res, "se_session", 0)
	checkCookie(t, res, "se_login", -1)
	if n := e.sessions.count(); n != 1 {
		t.Errorf("sessions after callback = %d, want 1", n)
	}
	if e.authn.gotCode != fakeCode {
		t.Errorf("Exchange code = %q, want %q", e.authn.gotCode, fakeCode)
	}
	if e.authn.gotVerifier == "" || e.authn.gotNonce == "" {
		t.Errorf("Exchange verifier or nonce is empty, want the attempt's values")
	}
}

func TestCallback_StateMismatch(t *testing.T) {
	e := newTestEnv(t)
	c, _ := e.login(t, "/")

	res := e.callback(t, "fake-other-state", c)

	if res.status != http.StatusBadRequest {
		t.Errorf("callback with another state status = %d, want 400", res.status)
	}
	if n := e.sessions.count(); n != 0 {
		t.Errorf("sessions = %d, want 0", n)
	}
	if e.authn.exchangeRuns != 0 {
		t.Errorf("Exchange ran %d times, want 0", e.authn.exchangeRuns)
	}
}

func TestCallback_NoAttemptCookie(t *testing.T) {
	e := newTestEnv(t)
	_, state := e.login(t, "/")

	res := e.callback(t, state)

	if res.status != http.StatusBadRequest {
		t.Errorf("callback without se_login status = %d, want 400", res.status)
	}
	if n := e.sessions.count(); n != 0 {
		t.Errorf("sessions = %d, want 0", n)
	}
}

func TestCallback_Replay(t *testing.T) {
	e := newTestEnv(t)
	c, state := e.login(t, "/")

	first := e.callback(t, state, c)
	second := e.callback(t, state, c)

	if first.status != http.StatusSeeOther {
		t.Errorf("first callback status = %d, want 303", first.status)
	}
	if second.status != http.StatusBadRequest {
		t.Errorf("replayed callback status = %d, want 400", second.status)
	}
	if n := e.sessions.count(); n != 1 {
		t.Errorf("sessions = %d, want 1", n)
	}
}

func TestCallback_ProviderError(t *testing.T) {
	e := newTestEnv(t)
	c, _ := e.login(t, "/")

	res := e.do(t, http.MethodGet, "/auth/callback?error=access_denied", nil, c)

	if res.status != http.StatusBadRequest {
		t.Errorf("callback with error status = %d, want 400", res.status)
	}
	if !strings.Contains(res.body, `href="/login"`) {
		t.Errorf("callback with error body = %q, want a link to /login", res.body)
	}
	if n := e.sessions.count(); n != 0 {
		t.Errorf("sessions = %d, want 0", n)
	}
	if _, ok := e.attempts.get(c.value); ok {
		t.Errorf("attempt still stored after a provider error, want it removed")
	}
	checkCookie(t, res, "se_login", -1)
}

func TestCallback_NoRole(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantSubject string
	}{
		{name: "with subject", err: fmt.Errorf("reading roles: %w", &auth.NoRoleError{Subject: "fake-subject"}), wantSubject: "fake-subject"},
		{name: "sentinel only", err: auth.ErrNoRole},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.authn.err = tt.err
			c, state := e.login(t, "/")

			res := e.callback(t, state, c)

			if res.status != http.StatusForbidden {
				t.Errorf("callback for a user without a role status = %d, want 403", res.status)
			}
			if n := e.sessions.count(); n != 0 {
				t.Errorf("sessions = %d, want 0", n)
			}
			for _, want := range []string{`action="/logout"`, `method="post"`} {
				if !strings.Contains(res.body, want) {
					t.Errorf("callback for a user without a role body = %q, want it to contain %q", res.body, want)
				}
			}
			refused := logRecords(t, e.log.String(), "login refused")
			if len(refused) != 1 {
				t.Fatalf("login refused log lines = %d, want 1; log = %q", len(refused), e.log.String())
			}
			if got := refused[0]["level"]; got != "INFO" {
				t.Errorf("login refused level = %v, want INFO", got)
			}
			got, _ := refused[0]["subject"].(string) // absent when the error carries no subject
			if got != tt.wantSubject {
				t.Errorf("login refused subject = %q, want %q", got, tt.wantSubject)
			}
		})
	}
}

func TestCallback_NoState(t *testing.T) {
	e := newTestEnv(t)
	c, _ := e.login(t, "/")

	res := e.do(t, http.MethodGet, "/auth/callback?"+url.Values{"code": {fakeCode}}.Encode(), nil, c)

	if res.status != http.StatusBadRequest {
		t.Errorf("callback without state status = %d, want 400", res.status)
	}
	if n := e.sessions.count(); n != 0 {
		t.Errorf("sessions = %d, want 0", n)
	}
	if e.authn.exchangeRuns != 0 {
		t.Errorf("Exchange ran %d times, want 0", e.authn.exchangeRuns)
	}
}

func TestCallback_StoreDown(t *testing.T) {
	storeErr := errors.New("dial tcp 192.0.2.1:6379: connection refused")
	tests := []struct {
		name string
		fail func(e *testEnv)
	}{
		{"taking the attempt", func(e *testEnv) { e.attempts.takeErr = storeErr }},
		{"creating the session", func(e *testEnv) { e.sessions.createErr = storeErr }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			c, state := e.login(t, "/")
			tt.fail(e)

			res := e.callback(t, state, c)

			if res.status != http.StatusServiceUnavailable {
				t.Errorf("callback with the store failing at %s status = %d, want 503", tt.name, res.status)
			}
			if want := "세션 저장소에 연결할 수 없습니다."; !strings.Contains(res.body, want) {
				t.Errorf("callback body = %q, want it to contain %q", res.body, want)
			}
			if res.cookie("se_session") != nil {
				t.Errorf("callback with the store failing at %s set se_session, want no session cookie", tt.name)
			}
			if n := countErrors(t, e.log.String()); n != 1 {
				t.Errorf("error log lines = %d, want 1; log = %q", n, e.log.String())
			}
		})
	}
}

func TestCallback_ReplacesPreviousSession(t *testing.T) {
	e := newTestEnv(t)
	old := e.signIn(t, auth.User{Subject: "fake-previous", Name: "Previous User", Roles: []auth.Role{auth.RoleUser}})
	c, state := e.login(t, "/")

	res := e.callback(t, state, c, old)

	if res.status != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", res.status)
	}
	if _, err := e.sessions.Get(t.Context(), session.ID(old.value)); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("previous session after a new login: Get error = %v, want ErrNotFound", err)
	}
	if n := e.sessions.count(); n != 1 {
		t.Errorf("sessions after a new login = %d, want 1", n)
	}
	if set := res.cookie("se_session"); set == nil || set.Value == old.value {
		t.Errorf("callback se_session = %v, want a new session cookie", set)
	}
}

func TestCallback_PreviousSessionDeleteFails(t *testing.T) {
	e := newTestEnv(t)
	old := e.signIn(t, auth.User{Subject: "fake-previous", Name: "Previous User", Roles: []auth.Role{auth.RoleUser}})
	e.sessions.deleteErr = errors.New("dial tcp 192.0.2.1:6379: connection refused")
	c, state := e.login(t, "/")

	res := e.callback(t, state, c, old)

	if res.status != http.StatusSeeOther {
		t.Errorf("callback status = %d, want 303 even when the previous session cannot be deleted", res.status)
	}
	checkCookie(t, res, "se_session", 0)
	if n := countErrors(t, e.log.String()); n != 1 {
		t.Errorf("error log lines = %d, want 1; log = %q", n, e.log.String())
	}
}

func TestCallback_ExchangeFails(t *testing.T) {
	e := newTestEnv(t)
	e.authn.err = errors.New("fake token endpoint said no")
	c, state := e.login(t, "/")

	res := e.callback(t, state, c)

	if res.status != http.StatusBadRequest {
		t.Errorf("callback with a failed exchange status = %d, want 400", res.status)
	}
	if n := e.sessions.count(); n != 0 {
		t.Errorf("sessions = %d, want 0", n)
	}
	if !strings.Contains(e.log.String(), "fake token endpoint said no") {
		t.Errorf("log = %q, want the exchange error", e.log.String())
	}
}

func TestCallback_ReturnTo(t *testing.T) {
	tests := []struct {
		returnTo string
		want     string
	}{
		{"//evil.example", "/"},
		{"https://evil.example", "/"},
		{`/\evil.example`, "/"},
		{"", "/"},
		{"/audit?page=2", "/audit?page=2"},
		{"/" + strings.Repeat("a", 2047), "/" + strings.Repeat("a", 2047)},
		{"/" + strings.Repeat("a", 2048), "/"},
	}
	for _, tt := range tests {
		label := tt.returnTo
		if len(label) > 40 {
			label = fmt.Sprintf("/a... (%d bytes)", len(label))
		}
		t.Run("login "+label, func(t *testing.T) {
			e := newTestEnv(t)
			c, state := e.login(t, tt.returnTo)
			res := e.callback(t, state, c)
			if got := res.location(); res.status != http.StatusSeeOther || got != tt.want {
				t.Errorf("callback after /login?return_to=%q = %d %q, want 303 %q", label, res.status, got, tt.want)
			}
		})
		t.Run("stored "+label, func(t *testing.T) {
			e := newTestEnv(t)
			a, err := auth.NewAttempt(tt.returnTo)
			if err != nil {
				t.Fatalf("NewAttempt(%q) error = %v", tt.returnTo, err)
			}
			id, err := e.attempts.PutAttempt(t.Context(), a)
			if err != nil {
				t.Fatalf("PutAttempt() error = %v", err)
			}
			res := e.callback(t, a.State, cookie{"se_login", id.Reveal()})
			if got := res.location(); res.status != http.StatusSeeOther || got != tt.want {
				t.Errorf("callback for a stored ReturnTo %q = %d %q, want 303 %q", label, res.status, got, tt.want)
			}
		})
	}
}

func TestHome_ShowsUser(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake <script>User</script>", Roles: []auth.Role{auth.RoleUser}})

	res := e.do(t, http.MethodGet, "/", nil, c)

	if res.status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.status)
	}
	if strings.Contains(res.body, "<script>") {
		t.Errorf("GET / body = %q, want the name escaped", res.body)
	}
	for _, want := range []string{"Fake &lt;script&gt;User&lt;/script&gt;", "역할: user", `action="/logout"`, `method="post"`} {
		if !strings.Contains(res.body, want) {
			t.Errorf("GET / body = %q, want it to contain %q", res.body, want)
		}
	}
}

func TestAudit_ForbiddenForUser(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})

	res := e.do(t, http.MethodGet, "/audit", nil, c)

	if res.status != http.StatusForbidden {
		t.Errorf("GET /audit as user status = %d, want 403", res.status)
	}
}

func TestAudit_AllowedForAdmin(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-admin", Name: "Fake Admin", Roles: []auth.Role{auth.RoleAdmin}})

	res := e.do(t, http.MethodGet, "/audit", nil, c)

	if res.status != http.StatusOK {
		t.Errorf("GET /audit as admin status = %d, want 200", res.status)
	}
	if !strings.Contains(res.body, "감사 로그") {
		t.Errorf("GET /audit body = %q, want the title 감사 로그", res.body)
	}
}

func TestAudit_RedirectsAnonymous(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/audit", nil)
	if got, want := res.location(), "/login?return_to=%2Faudit"; res.status != http.StatusSeeOther || got != want {
		t.Errorf("GET /audit without login = %d %q, want 303 %q", res.status, got, want)
	}
}

func TestSession_Expired(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/", nil, cookie{"se_session", "fake-gone-session"})

	if res.status != http.StatusSeeOther {
		t.Errorf("GET / with an expired session status = %d, want 303", res.status)
	}
	if got := res.location(); !strings.HasPrefix(got, "/login") {
		t.Errorf("GET / with an expired session Location = %q, want /login...", got)
	}
	checkCookie(t, res, "se_session", -1)
}

func TestSession_StoreDown(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})
	e.sessions.getErr = errors.New("dial tcp 192.0.2.1:6379: connection refused")

	res := e.do(t, http.MethodGet, "/", nil, c)

	if res.status != http.StatusServiceUnavailable {
		t.Errorf("GET / with the store down status = %d, want 503", res.status)
	}
	if want := "세션 저장소에 연결할 수 없습니다."; !strings.Contains(res.body, want) {
		t.Errorf("GET / with the store down body = %q, want it to contain %q", res.body, want)
	}
	if got := res.location(); got != "" {
		t.Errorf("GET / with the store down Location = %q, want none", got)
	}
	if n := countErrors(t, e.log.String()); n != 1 {
		t.Errorf("error log lines = %d, want 1; log = %q", n, e.log.String())
	}
}

func TestHealthz_StoreDown(t *testing.T) {
	e := newTestEnv(t)
	e.sessions.getErr = errors.New("dial tcp 192.0.2.1:6379: connection refused")

	res := e.do(t, http.MethodGet, "/healthz", nil, cookie{"se_session", "fake-session-id-1"})

	if res.status != http.StatusOK {
		t.Errorf("GET /healthz with the store down status = %d, want 200", res.status)
	}
}

func TestLogout(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})

	res := e.do(t, http.MethodPost, "/logout", sameOrigin(), c)

	if res.status != http.StatusSeeOther {
		t.Errorf("POST /logout status = %d, want 303", res.status)
	}
	if got := res.location(); got != fakeLogoutURL {
		t.Errorf("POST /logout Location = %q, want %q", got, fakeLogoutURL)
	}
	if n := e.sessions.count(); n != 0 {
		t.Errorf("sessions after logout = %d, want 0", n)
	}
	checkCookie(t, res, "se_session", -1)
}

func TestLogout_CrossOrigin(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})

	res := e.do(t, http.MethodPost, "/logout", http.Header{"Sec-Fetch-Site": {"cross-site"}}, c)

	if res.status != http.StatusForbidden {
		t.Errorf("cross-site POST /logout status = %d, want 403", res.status)
	}
	if n := e.sessions.count(); n != 1 {
		t.Errorf("sessions after a cross-site logout = %d, want 1", n)
	}
}

func TestLogout_Get(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})

	res := e.do(t, http.MethodGet, "/logout", nil, c)

	if res.status != http.StatusMethodNotAllowed {
		t.Errorf("GET /logout status = %d, want 405", res.status)
	}
	if n := e.sessions.count(); n != 1 {
		t.Errorf("sessions after GET /logout = %d, want 1", n)
	}
}

// checkLoggedOut checks that a logout response sends the browser to the
// provider's logout page and deletes the session cookie.
func checkLoggedOut(t *testing.T, res result, what string) {
	t.Helper()
	if res.status != http.StatusSeeOther {
		t.Errorf("POST /logout %s status = %d, want 303", what, res.status)
	}
	if got := res.location(); got != fakeLogoutURL {
		t.Errorf("POST /logout %s Location = %q, want %q", what, got, fakeLogoutURL)
	}
	checkCookie(t, res, "se_session", -1)
}

func TestLogout_NoCookie(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodPost, "/logout", sameOrigin())
	checkLoggedOut(t, res, "without a session cookie")
}

func TestLogout_SessionGone(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodPost, "/logout", sameOrigin(), cookie{"se_session", "fake-gone-session"})
	checkLoggedOut(t, res, "with an expired session")
	if n := countErrors(t, e.log.String()); n != 0 {
		t.Errorf("error log lines = %d, want 0; log = %q", n, e.log.String())
	}
}

func TestLogout_StoreDown(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})
	e.sessions.getErr = errors.New("dial tcp 192.0.2.1:6379: connection refused")
	e.sessions.deleteErr = errors.New("dial tcp 192.0.2.1:6379: connection refused")

	res := e.do(t, http.MethodPost, "/logout", sameOrigin(), c)

	checkLoggedOut(t, res, "with the store down")
	if n := countErrors(t, e.log.String()); n != 1 {
		t.Errorf("error log lines = %d, want 1; log = %q", n, e.log.String())
	}
}

func TestLogs_HaveNoSecrets(t *testing.T) {
	e := newTestEnv(t)
	c, state := e.login(t, "/audit")
	res := e.callback(t, state, c)
	set := res.cookie("se_session")
	if set == nil {
		t.Fatalf("callback set no se_session cookie")
	}
	sc := cookie{set.Name, set.Value}
	e.do(t, http.MethodGet, "/", nil, sc)
	e.do(t, http.MethodGet, "/audit", nil, sc)
	e.do(t, http.MethodPost, "/logout", sameOrigin(), sc)
	e.do(t, http.MethodGet, "/auth/callback?"+url.Values{"state": {state}, "code": {fakeCode}}.Encode(), nil, c)

	log := e.log.String()
	for name, secret := range map[string]string{
		"session ID": sc.value,
		"attempt ID": c.value,
		"state":      state,
		"code":       fakeCode,
	} {
		if strings.Contains(log, secret) {
			t.Errorf("log contains the %s %q; log = %q", name, secret, log)
		}
	}
}

// logRecords returns the JSON log lines whose message is msg.
func logRecords(t *testing.T, log, msg string) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if rec["msg"] == msg {
			recs = append(recs, rec)
		}
	}
	return recs
}

// countErrors returns the number of JSON log lines at level ERROR.
func countErrors(t *testing.T, log string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Level string `json:"level"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if rec.Level == "ERROR" {
			n++
		}
	}
	return n
}
