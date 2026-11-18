package auth_test

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

const (
	testClientID     = "secret-externalizer"
	testClientSecret = "dev-only-not-a-secret" //nolint:gosec // G101: an obviously fake fixture value
	testRedirectURL  = "http://localhost:8080/auth/callback"
	testLogoutReturn = "http://localhost:8080/"
	testNonce        = "fake-nonce"
	testVerifier     = "fake-verifier-fake-verifier-fake-verifier-fake"
)

func testConfig(issuer string) auth.Config {
	return auth.Config{
		IssuerURL:    issuer,
		ClientID:     testClientID,
		ClientSecret: auth.ClientSecret(testClientSecret),
		RedirectURL:  testRedirectURL,
		LogoutReturn: testLogoutReturn,
	}
}

// validClaims returns the claims of an ID token that Exchange accepts. Tests
// change one claim to make it invalid.
func validClaims(issuer string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":                issuer,
		"sub":                "fake-subject",
		"aud":                testClientID,
		"exp":                now.Add(5 * time.Minute).Unix(),
		"iat":                now.Unix(),
		"nonce":              testNonce,
		"preferred_username": "fake-user",
		"roles":              []string{"admin", "offline_access"},
	}
}

func newTestAuthenticator(t *testing.T, idp *fakeIDP, cfg auth.Config) *auth.Authenticator {
	t.Helper()
	a, err := auth.NewAuthenticator(t.Context(), cfg, idp.Client())
	if err != nil {
		t.Fatalf("NewAuthenticator() error = %v", err)
	}
	return a
}

func TestAuthCodeURL(t *testing.T) {
	idp := newFakeIDP(t)
	a := newTestAuthenticator(t, idp, testConfig(idp.URL()))

	raw := a.AuthCodeURL("fake-state", testNonce, testVerifier)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	if got, want := u.Scheme+"://"+u.Host+u.Path, idp.URL()+"/auth"; got != want {
		t.Errorf("AuthCodeURL() endpoint = %q, want %q", got, want)
	}
	q := u.Query()
	want := map[string]string{
		"response_type":         "code",
		"client_id":             testClientID,
		"code_challenge_method": "S256",
		"state":                 "fake-state",
		"nonce":                 testNonce,
		"redirect_uri":          testRedirectURL,
		"scope":                 "openid profile",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("AuthCodeURL() query %s = %q, want %q", k, got, v)
		}
	}
	challenge := q.Get("code_challenge")
	if challenge == "" || challenge == testVerifier {
		t.Errorf("AuthCodeURL() code_challenge = %q, want a value derived from the verifier", challenge)
	}
	if strings.Contains(raw, testClientSecret) {
		t.Errorf("AuthCodeURL() = %q, contains the client secret", raw)
	}
}

func TestExchange_ReturnsUser(t *testing.T) {
	idp := newFakeIDP(t)
	a := newTestAuthenticator(t, idp, testConfig(idp.URL()))
	idp.SetClaims(validClaims(idp.URL()))

	got, err := a.Exchange(t.Context(), "fake-code", testVerifier, testNonce)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	want := auth.User{Subject: "fake-subject", Name: "fake-user", Roles: []auth.Role{auth.RoleAdmin}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Exchange() mismatch (-want +got):\n%s", diff)
	}
	verifier, clientID, clientSecret := idp.Received()
	if verifier != testVerifier {
		t.Errorf("token endpoint got code_verifier %q, want %q", verifier, testVerifier)
	}
	if clientID != testClientID || clientSecret != testClientSecret {
		t.Errorf("token endpoint got client %q with secret %q, want %q with %q", clientID, clientSecret, testClientID, testClientSecret)
	}
}

func TestExchange_RejectsInvalidIDToken(t *testing.T) {
	tests := []struct {
		name   string
		change func(claims map[string]any)
	}{
		{"NonceMismatch", func(c map[string]any) { c["nonce"] = "other-nonce" }},
		{"WrongAudience", func(c map[string]any) { c["aud"] = "other-client" }},
		{"Expired", func(c map[string]any) {
			c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
			c["exp"] = time.Now().Add(-time.Hour).Unix()
		}},
		{"WrongIssuer", func(c map[string]any) { c["iss"] = "http://other-issuer.invalid" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idp := newFakeIDP(t)
			a := newTestAuthenticator(t, idp, testConfig(idp.URL()))
			claims := validClaims(idp.URL())
			tt.change(claims)
			idp.SetClaims(claims)

			got, err := a.Exchange(t.Context(), "fake-code", testVerifier, testNonce)
			if err == nil {
				t.Fatalf("Exchange() with claims %v = %v, want an error", claims, got)
			}
			if errors.Is(err, auth.ErrNoRole) {
				t.Errorf("Exchange() with claims %v error = %v, want a token error, not ErrNoRole", claims, err)
			}
		})
	}
}

func TestExchange_NoRole(t *testing.T) {
	idp := newFakeIDP(t)
	a := newTestAuthenticator(t, idp, testConfig(idp.URL()))
	claims := validClaims(idp.URL())
	delete(claims, "roles")
	idp.SetClaims(claims)

	_, err := a.Exchange(t.Context(), "fake-code", testVerifier, testNonce)
	if !errors.Is(err, auth.ErrNoRole) {
		t.Errorf("Exchange() without roles error = %v, want %v", err, auth.ErrNoRole)
	}
}

func TestExchange_ErrorHidesSecret(t *testing.T) {
	idp := newFakeIDP(t)
	a := newTestAuthenticator(t, idp, testConfig(idp.URL()))
	idp.FailToken(http.StatusUnauthorized)

	_, err := a.Exchange(t.Context(), "fake-code", testVerifier, testNonce)
	if err == nil {
		t.Fatal("Exchange() against a failing token endpoint error = nil, want an error")
	}
	if _, _, secret := idp.Received(); secret != testClientSecret {
		t.Fatalf("token endpoint got secret %q, want %q", secret, testClientSecret)
	}
	for name, v := range map[string]string{
		"client secret": testClientSecret,
		"code":          "fake-code",
		"verifier":      testVerifier,
	} {
		if strings.Contains(err.Error(), v) {
			t.Errorf("Exchange() error = %q, contains the %s", err, name)
		}
	}
}

func TestNewAuthenticator_SeparateDiscoveryURL(t *testing.T) {
	idp := newFakeIDP(t)
	const issuer = "http://issuer.invalid/realms/dev"
	cfg := testConfig(issuer)
	cfg.DiscoveryURL = idp.URL()
	a := newTestAuthenticator(t, idp, cfg)

	idp.SetClaims(validClaims(issuer))
	if _, err := a.Exchange(t.Context(), "fake-code", testVerifier, testNonce); err != nil {
		t.Errorf("Exchange() with iss %q error = %v, want nil", issuer, err)
	}

	idp.SetClaims(validClaims(idp.URL()))
	if _, err := a.Exchange(t.Context(), "fake-code", testVerifier, testNonce); err == nil {
		t.Errorf("Exchange() with iss %q error = nil, want an error", idp.URL())
	}
}

func TestLogoutURL(t *testing.T) {
	idp := newFakeIDP(t)
	a := newTestAuthenticator(t, idp, testConfig(idp.URL()))

	raw := a.LogoutURL()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	if got, want := u.Scheme+"://"+u.Host+u.Path, idp.URL()+"/logout"; got != want {
		t.Errorf("LogoutURL() endpoint = %q, want %q", got, want)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != testClientID {
		t.Errorf("LogoutURL() client_id = %q, want %q", got, testClientID)
	}
	if got := q.Get("post_logout_redirect_uri"); got != testLogoutReturn {
		t.Errorf("LogoutURL() post_logout_redirect_uri = %q, want %q", got, testLogoutReturn)
	}
}
