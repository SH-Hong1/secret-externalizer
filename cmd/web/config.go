package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/cafile"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// config holds the settings read once at start-up. Its fields are
// unexported, and fmt prints unexported fields by reflection without calling
// the redacting methods of auth.ClientSecret, db.DSN, bao.SecretID and
// session.Password, so config redacts itself through String, Format, and
// LogValue.
type config struct {
	addr           string
	valkeyAddr     string
	valkeyUser     string
	valkeyPassword session.Password
	// valkeyCA holds the PEM certificates that valkey's certificate must
	// chain to. Nil means the connection to valkey is not encrypted.
	valkeyCA []byte
	dsn      db.DSN
	oidc     auth.Config
	baoAddr  string
	// baoTLS is whether baoAddr is an https address.
	baoTLS bool
	// baoCA holds the PEM certificates that OpenBao's certificate must
	// chain to. Nil means the system's roots, or no TLS when baoTLS is
	// false.
	baoCA    []byte
	baoCreds bao.Credentials
	pdfURL   string
}

// String returns every setting with the client secret, the database URL,
// the secret-id and the valkey password redacted. Of the CAs it says only
// whether each connection uses TLS.
func (c config) String() string {
	// c.dsn, c.oidc, the secret-id and the password are passed as arguments
	// of their own, so fmt calls the Format methods of db.DSN, of the
	// ClientSecret field, of bao.SecretID and of session.Password.
	return fmt.Sprintf("{addr:%s valkey_addr:%s valkey_username:%s valkey_password:%v valkey_tls:%t database_url:%v oidc:%+v bao_addr:%s bao_tls:%t bao_role_id:%s bao_secret_id:%v pdf_url:%s}",
		c.addr, c.valkeyAddr, c.valkeyUser, c.valkeyPassword, c.valkeyCA != nil, c.dsn, c.oidc, c.baoAddr, c.baoTLS, c.baoCreds.RoleID, c.baoCreds.SecretID, c.pdfURL)
}

// Format prints the form String returns for every fmt verb, including %#v,
// which would otherwise print every field by reflection.
func (c config) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(c.String())) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns every setting with the client secret, the database URL,
// the secret-id and the valkey password redacted.
func (c config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.addr),
		slog.String("valkey_addr", c.valkeyAddr),
		slog.String("valkey_username", c.valkeyUser),
		slog.Any("valkey_password", c.valkeyPassword),
		slog.Bool("valkey_tls", c.valkeyCA != nil),
		slog.Any("database_url", c.dsn),
		slog.Any("oidc", c.oidc),
		slog.String("bao_addr", c.baoAddr),
		slog.Bool("bao_tls", c.baoTLS),
		slog.String("bao_role_id", c.baoCreds.RoleID),
		slog.Any("bao_secret_id", c.baoCreds.SecretID),
		slog.String("pdf_url", c.pdfURL),
	)
}

// loadConfig builds a config from the environment. The flags in args are
// handled by run, so none of them change the configuration.
//
// The client secret and the database URL are converted to auth.ClientSecret
// and db.DSN as they are read, the secret-id to bao.SecretID as it is read
// from BAO_SECRET_ID_FILE, and the valkey password to session.Password as it
// is read from VALKEY_PASSWORD_FILE. No error returned here contains them.
//
// VALKEY_CA_FILE is optional. When it is set, web reaches valkey over TLS
// and trusts only the certificates in that file; when it is not, the
// connection is not encrypted.
//
// BAO_CA_FILE is optional too. When it is set, BAO_ADDR must be an https
// address and web trusts only the certificates in that file for OpenBao;
// when it is not, an https address is checked against the system's roots.
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
	valkeyUser := required("VALKEY_USERNAME")
	valkeyPasswordFile := required("VALKEY_PASSWORD_FILE")
	dsn := db.DSN(required("DATABASE_URL"))
	issuerURL := required("OIDC_ISSUER_URL")
	clientID := required("OIDC_CLIENT_ID")
	secret := auth.ClientSecret(required("OIDC_CLIENT_SECRET"))
	baoAddr := required("BAO_ADDR")
	roleID := required("BAO_ROLE_ID")
	secretIDFile := required("BAO_SECRET_ID_FILE")
	pdfURL := required("PDF_URL")
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	secretID, err := bao.ReadSecretIDFile(secretIDFile)
	if err != nil {
		return config{}, fmt.Errorf("reading BAO_SECRET_ID_FILE: %w", err)
	}
	valkeyPassword, err := session.ReadPasswordFile(valkeyPasswordFile)
	if err != nil {
		return config{}, fmt.Errorf("reading VALKEY_PASSWORD_FILE: %w", err)
	}
	var valkeyCA []byte
	if path := getenv("VALKEY_CA_FILE"); path != "" {
		valkeyCA, err = cafile.Read(path)
		if err != nil {
			return config{}, fmt.Errorf("reading VALKEY_CA_FILE: %w", err)
		}
	}
	baoTLS, baoCA, err := bao.LoadCA(baoAddr, getenv("BAO_CA_FILE"))
	if err != nil {
		return config{}, fmt.Errorf("checking BAO_ADDR and BAO_CA_FILE: %w", err)
	}

	cfg := config{
		addr: ":8080", valkeyAddr: valkeyAddr, valkeyUser: valkeyUser, valkeyPassword: valkeyPassword, valkeyCA: valkeyCA,
		dsn:     dsn,
		baoAddr: baoAddr, baoTLS: baoTLS, baoCA: baoCA, baoCreds: bao.Credentials{RoleID: roleID, SecretID: secretID},
		pdfURL: pdfURL,
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

	// pdf.New checks the address and contacts nothing; run builds the
	// client it keeps.
	if _, err := pdf.New(pdfURL, &http.Client{}); err != nil {
		return config{}, fmt.Errorf("checking PDF_URL: %w", err)
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
