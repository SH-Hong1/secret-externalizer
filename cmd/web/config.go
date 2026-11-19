package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
)

// config holds the settings read once at start-up. Its fields are
// unexported, and fmt prints unexported fields by reflection without calling
// the redacting methods of auth.ClientSecret, db.DSN and bao.SecretID, so
// config redacts itself through String, Format, and LogValue.
type config struct {
	addr       string
	valkeyAddr string
	dsn        db.DSN
	oidc       auth.Config
	baoAddr    string
	baoCreds   bao.Credentials
}

// String returns every setting with the client secret, the database URL and
// the secret-id redacted.
func (c config) String() string {
	// c.dsn, c.oidc and the secret-id are passed as arguments of their own,
	// so fmt calls the Format methods of db.DSN, of the ClientSecret field and
	// of bao.SecretID.
	return fmt.Sprintf("{addr:%s valkey_addr:%s database_url:%v oidc:%+v bao_addr:%s bao_role_id:%s bao_secret_id:%v}",
		c.addr, c.valkeyAddr, c.dsn, c.oidc, c.baoAddr, c.baoCreds.RoleID, c.baoCreds.SecretID)
}

// Format prints the form String returns for every fmt verb, including %#v,
// which would otherwise print every field by reflection.
func (c config) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(c.String())) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns every setting with the client secret, the database URL
// and the secret-id redacted.
func (c config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.addr),
		slog.String("valkey_addr", c.valkeyAddr),
		slog.Any("database_url", c.dsn),
		slog.Any("oidc", c.oidc),
		slog.String("bao_addr", c.baoAddr),
		slog.String("bao_role_id", c.baoCreds.RoleID),
		slog.Any("bao_secret_id", c.baoCreds.SecretID),
	)
}

// loadConfig builds a config from the environment. The flags in args are
// handled by run, so none of them change the configuration.
//
// The client secret and the database URL are converted to auth.ClientSecret
// and db.DSN as they are read, and the secret-id to bao.SecretID as it is
// read from BAO_SECRET_ID_FILE. No error returned here contains them.
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
	dsn := db.DSN(required("DATABASE_URL"))
	issuerURL := required("OIDC_ISSUER_URL")
	clientID := required("OIDC_CLIENT_ID")
	secret := auth.ClientSecret(required("OIDC_CLIENT_SECRET"))
	baoAddr := required("BAO_ADDR")
	roleID := required("BAO_ROLE_ID")
	secretIDFile := required("BAO_SECRET_ID_FILE")
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	secretID, err := readSecretID(secretIDFile)
	if err != nil {
		return config{}, fmt.Errorf("reading BAO_SECRET_ID_FILE: %w", err)
	}

	cfg := config{
		addr: ":8080", valkeyAddr: valkeyAddr, dsn: dsn,
		baoAddr: baoAddr, baoCreds: bao.Credentials{RoleID: roleID, SecretID: secretID},
	}
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

// readSecretID reads an AppRole secret-id from the file at path, without
// surrounding white space. The errors of os.ReadFile name the path and never
// the content.
func readSecretID(path string) (bao.SecretID, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own BAO_SECRET_ID_FILE setting
	if err != nil {
		return "", fmt.Errorf("reading the secret-id file: %w", err)
	}
	s := bao.SecretID(strings.TrimSpace(string(b)))
	if s == "" {
		return "", fmt.Errorf("%q is empty", path)
	}
	return s, nil
}
