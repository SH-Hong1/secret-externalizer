package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// config holds the settings read once at start-up. Its fields are
// unexported, and fmt prints unexported fields by reflection without calling
// auth.ClientSecret's redacting methods, so config redacts itself through
// String, Format, and LogValue.
type config struct {
	addr       string
	valkeyAddr string
	oidc       auth.Config
}

// String returns every setting with the client secret redacted.
func (c config) String() string {
	// c.oidc is passed as an argument of its own, so fmt calls the
	// ClientSecret field's Format method.
	return fmt.Sprintf("{addr:%s valkey_addr:%s oidc:%+v}", c.addr, c.valkeyAddr, c.oidc)
}

// Format prints the form String returns for every fmt verb, including %#v,
// which would otherwise print every field by reflection.
func (c config) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(c.String())) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns every setting with the client secret redacted.
func (c config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.addr),
		slog.String("valkey_addr", c.valkeyAddr),
		slog.Any("oidc", c.oidc),
	)
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
// path other than "/", and no user info, query, or fragment, and returns it
// without a trailing slash. Routes are registered at the root, so a base URL
// with a path would build a callback address that no route answers.
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
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return "", errors.New("must not have user info, a query, or a fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("must not have a path other than /")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
