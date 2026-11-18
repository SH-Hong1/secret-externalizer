package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// fakeIDP is an OpenID Connect provider for tests. It serves a discovery
// document, a key set, and a token endpoint that returns an ID token signed
// with a key made for this test only.
type fakeIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu sync.Mutex
	// claims are the claims of the next ID token the token endpoint returns.
	claims map[string]any
	// tokenStatus, when not zero, makes the token endpoint fail with it.
	tokenStatus     int
	gotVerifier     string
	gotClientID     string
	gotClientSecret string
}

const fakeKeyID = "fake-key"

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	idp := &fakeIDP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", idp.discovery)
	mux.HandleFunc("GET /keys", idp.keys)
	mux.HandleFunc("POST /token", idp.token)
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// URL returns the issuer URL the provider reports in its discovery document.
func (p *fakeIDP) URL() string { return p.server.URL }

// Client returns an HTTP client that reaches the provider.
func (p *fakeIDP) Client() *http.Client {
	c := p.server.Client()
	c.Timeout = 5 * time.Second
	return c
}

// SetClaims sets the claims of the next ID token.
func (p *fakeIDP) SetClaims(claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = claims
}

// FailToken makes the token endpoint answer with status.
func (p *fakeIDP) FailToken(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenStatus = status
}

// Received returns the code verifier and client credentials of the last token
// request.
func (p *fakeIDP) Received() (verifier, clientID, clientSecret string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gotVerifier, p.gotClientID, p.gotClientSecret
}

func (p *fakeIDP) discovery(w http.ResponseWriter, _ *http.Request) {
	u := p.server.URL
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                u,
		"authorization_endpoint":                u + "/auth",
		"token_endpoint":                        u + "/token",
		"jwks_uri":                              u + "/keys",
		"end_session_endpoint":                  u + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *fakeIDP) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &p.key.PublicKey,
		KeyID:     fakeKeyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}})
}

func (p *fakeIDP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}

	p.mu.Lock()
	p.gotVerifier = r.PostForm.Get("code_verifier")
	p.gotClientID, p.gotClientSecret = id, secret
	status, claims := p.tokenStatus, p.claims
	p.mu.Unlock()

	if status != 0 {
		// The description echoes the secret so that a test can tell whether
		// the error a client returns carries the response body.
		writeJSON(w, status, map[string]string{
			"error":             "invalid_client",
			"error_description": "client secret " + secret + " was rejected",
		})
		return
	}

	idToken, err := p.sign(claims)
	if err != nil {
		http.Error(w, "signing id token", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "fake-access-token",
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     idToken,
	})
}

func (p *fakeIDP) sign(claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", fakeKeyID),
	)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the test client sees a broken body as its own failure
}
