package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// config holds the settings read once at start-up.
type config struct {
	addr       string
	valkeyAddr string
	oidc       auth.Config
}

// loadConfig builds a config from the environment. The flags in args are
// handled by run, so none of them change the configuration.
//
// The client secret is converted to auth.ClientSecret as it is read, and no
// error returned here contains it.
func loadConfig(_ []string, getenv func(string) string) (config, error) {
	var missing []string
	required := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	baseURL := required("WEB_BASE_URL")
	valkeyAddr := required("VALKEY_ADDR")
	issuerURL := required("OIDC_ISSUER_URL")
	clientID := required("OIDC_CLIENT_ID")
	secret := auth.ClientSecret(required("OIDC_CLIENT_SECRET"))
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	cfg := config{addr: ":8080", valkeyAddr: valkeyAddr}
	if v := getenv("WEB_ADDR"); v != "" {
		cfg.addr = v
	}
	if _, _, err := net.SplitHostPort(cfg.addr); err != nil {
		return config{}, fmt.Errorf("checking WEB_ADDR: %w", err)
	}
	if _, _, err := net.SplitHostPort(valkeyAddr); err != nil {
		return config{}, fmt.Errorf("checking VALKEY_ADDR: %w", err)
	}

	base, err := parseBaseURL(baseURL)
	if err != nil {
		return config{}, fmt.Errorf("checking WEB_BASE_URL: %w", err)
	}
	cfg.oidc = auth.Config{
		IssuerURL:    issuerURL,
		DiscoveryURL: getenv("OIDC_DISCOVERY_URL"),
		ClientID:     clientID,
		ClientSecret: secret,
		RedirectURL:  base + "/auth/callback",
		LogoutReturn: base + "/",
	}
	return cfg, nil
}

// parseBaseURL checks that raw is an absolute http or https address with no
// query or fragment, and returns it without a trailing slash.
func parseBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("host is empty")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("must not have user info, a query, or a fragment")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
