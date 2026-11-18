package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config describes the OpenID Connect client this application is.
type Config struct {
	// IssuerURL is the issuer as it appears in tokens and in the browser.
	IssuerURL string
	// DiscoveryURL is where this process reaches the issuer. IssuerURL is used
	// when it is empty.
	DiscoveryURL string
	ClientID     string
	ClientSecret ClientSecret
	// RedirectURL is <base URL>/auth/callback.
	RedirectURL string
	// LogoutReturn is where the identity provider sends the browser after
	// logout, normally <base URL>/.
	LogoutReturn string
}

// Authenticator runs the Authorization Code flow with PKCE against an OpenID
// Connect provider and turns a verified ID token into a User.
type Authenticator struct {
	client      *http.Client
	endpoint    oauth2.Endpoint
	clientID    string
	secret      ClientSecret
	redirectURL string
	verifier    *oidc.IDTokenVerifier
	logoutURL   string
}

// NewAuthenticator reads the provider's discovery document and returns an
// Authenticator for cfg. Every request to the provider goes through client,
// which must have a timeout.
func NewAuthenticator(ctx context.Context, cfg Config, client *http.Client) (*Authenticator, error) {
	if client == nil {
		return nil, errors.New("creating authenticator: http client is nil")
	}
	discoveryURL := cfg.DiscoveryURL
	if discoveryURL == "" {
		discoveryURL = cfg.IssuerURL
	}
	ctx = oidc.ClientContext(ctx, client)
	if discoveryURL != cfg.IssuerURL {
		// The provider is reached at a different address from the one in its
		// tokens; tokens are still checked against IssuerURL.
		ctx = oidc.InsecureIssuerURLContext(ctx, cfg.IssuerURL)
	}
	provider, err := oidc.NewProvider(ctx, discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("discovering oidc provider: %w", err)
	}

	var metadata struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("reading oidc discovery document: %w", err)
	}
	logoutURL, err := buildLogoutURL(metadata.EndSessionEndpoint, cfg.ClientID, cfg.LogoutReturn)
	if err != nil {
		return nil, err
	}

	return &Authenticator{
		client:      client,
		endpoint:    provider.Endpoint(),
		clientID:    cfg.ClientID,
		secret:      cfg.ClientSecret,
		redirectURL: cfg.RedirectURL,
		verifier:    provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		logoutURL:   logoutURL,
	}, nil
}

func buildLogoutURL(endpoint, clientID, returnTo string) (string, error) {
	if endpoint == "" {
		return "", errors.New("oidc discovery document has no end_session_endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parsing end_session_endpoint: %w", err)
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("post_logout_redirect_uri", returnTo)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// oauthConfig builds the oauth2 configuration. It reveals the client secret,
// so the result is used for one call and not kept.
func (a *Authenticator) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.clientID,
		ClientSecret: a.secret.Reveal(),
		Endpoint:     a.endpoint,
		RedirectURL:  a.redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile"},
	}
}

// AuthCodeURL returns the provider's login page address for one login
// attempt. The PKCE challenge is derived from verifier with S256.
func (a *Authenticator) AuthCodeURL(state, nonce, verifier string) string {
	return a.oauthConfig().AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// Exchange trades an authorization code for tokens, verifies the ID token's
// signature, issuer, audience, expiry, and nonce, and returns the user it
// names. It returns an error wrapping ErrNoRole when the user has neither
// role. Its errors never contain the client secret, the code, or the verifier.
func (a *Authenticator) Exchange(ctx context.Context, code, verifier, nonce string) (User, error) {
	if nonce == "" {
		return User{}, errors.New("exchanging authorization code: nonce is empty")
	}
	ctx = oidc.ClientContext(ctx, a.client)
	token, err := a.oauthConfig().Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return User{}, exchangeError(err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return User{}, errors.New("exchanging authorization code: token response has no id token")
	}
	idToken, err := a.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return User{}, fmt.Errorf("verifying id token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return User{}, errors.New("verifying id token: nonce does not match the login attempt")
	}

	var claims struct {
		PreferredUsername string   `json:"preferred_username"`
		Roles             []string `json:"roles"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return User{}, fmt.Errorf("reading id token claims: %w", err)
	}
	roles, err := rolesFromClaim(claims.Roles)
	if err != nil {
		return User{}, fmt.Errorf("reading roles: %w", err)
	}
	return User{Subject: idToken.Subject, Name: claims.PreferredUsername, Roles: roles}, nil
}

// exchangeError describes a failed token request. An oauth2.RetrieveError
// carries the response body, which a provider may fill with request details,
// so only its status and RFC 6749 error code are kept.
func exchangeError(err error) error {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return fmt.Errorf("exchanging authorization code: %w", err)
	}
	status := "no response"
	if re.Response != nil {
		status = re.Response.Status
	}
	return fmt.Errorf("exchanging authorization code: token endpoint returned %q with error code %q", status, re.ErrorCode)
}

// LogoutURL returns the provider's end-session address, which sends the
// browser back to Config.LogoutReturn.
func (a *Authenticator) LogoutURL() string { return a.logoutURL }
