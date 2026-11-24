package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// testClientSecret is an obviously fake client secret.
const testClientSecret = "dev-only-cmd-web-test-secret" //nolint:gosec // G101: an obviously fake fixture value

// testDatabaseURL is an obviously fake database URL.
const testDatabaseURL = "postgres://se_app:dev-only-cmd-web-test-db@postgres:5432/secret_externalizer" //nolint:gosec // G101: an obviously fake fixture value

// testSecretID is an obviously fake AppRole secret-id.
const testSecretID = "dev-only-cmd-web-test-secret-id" //nolint:gosec // G101: an obviously fake fixture value

// testValkeyPassword is an obviously fake valkey password.
const testValkeyPassword = "dev-only-cmd-web-test-valkey-password" //nolint:gosec // G101: an obviously fake fixture value

// writeSecretID writes content to a new file and returns its path.
func writeSecretID(t *testing.T, content string) string {
	t.Helper()
	return writeFile(t, content)
}

// writeFile writes content to a new file in a temporary folder and returns
// its path.
func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	//nolint:gosec // G703: the path is a new file in t.TempDir; only the content may come from the environment
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing a test file: %v", err)
	}
	return path
}

// validEnv returns a complete environment that loadConfig accepts, with a
// secret-id file in a temporary folder. Tests change or remove one variable
// at a time.
func validEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"BAO_ADDR":             "http://openbao:8200",
		"BAO_ROLE_ID":          "se-web",
		"BAO_SECRET_ID_FILE":   writeSecretID(t, testSecretID+"\n"),
		"WEB_BASE_URL":         "http://localhost:8080",
		"VALKEY_ADDR":          "valkey:6379",
		"VALKEY_USERNAME":      "se-web",
		"VALKEY_PASSWORD_FILE": writeFile(t, testValkeyPassword+"\n"),
		"DATABASE_URL":         testDatabaseURL,
		"OIDC_ISSUER_URL":      "http://localhost:8081/realms/secret-externalizer",
		"OIDC_DISCOVERY_URL":   "http://keycloak:8080/realms/secret-externalizer",
		"OIDC_CLIENT_ID":       "secret-externalizer-web",
		"OIDC_CLIENT_SECRET":   testClientSecret,
		"PDF_URL":              "http://pdf:3000",
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestLoadConfig(t *testing.T) {
	got, err := loadConfig(nil, getenvFrom(validEnv(t)))
	if err != nil {
		t.Fatalf("loadConfig(nil, validEnv(t)) error = %v", err)
	}
	want := config{
		addr:           ":8080",
		valkeyAddr:     "valkey:6379",
		valkeyUser:     "se-web",
		valkeyPassword: session.Password(testValkeyPassword),
		dsn:            db.DSN(testDatabaseURL),
		baoAddr:        "http://openbao:8200",
		baoCreds:       bao.Credentials{RoleID: "se-web", SecretID: testSecretID},
		pdfURL:         "http://pdf:3000",
		oidc: auth.Config{
			IssuerURL:    "http://localhost:8081/realms/secret-externalizer",
			DiscoveryURL: "http://keycloak:8080/realms/secret-externalizer",
			ClientID:     "secret-externalizer-web",
			ClientSecret: auth.ClientSecret(testClientSecret),
			RedirectURL:  "http://localhost:8080/auth/callback",
			LogoutReturn: "http://localhost:8080/",
		},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(config{})); diff != "" {
		t.Errorf("loadConfig(nil, validEnv(t)) mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfig_Addr(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		want    string
		wantErr bool
	}{
		{name: "default", addr: "", want: ":8080"},
		{name: "set", addr: "127.0.0.1:9000", want: "127.0.0.1:9000"},
		{name: "invalid", addr: "bad", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			env["WEB_ADDR"] = tt.addr
			got, err := loadConfig(nil, getenvFrom(env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("loadConfig() with WEB_ADDR=%q error = nil, want an error", tt.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() with WEB_ADDR=%q error = %v", tt.addr, err)
			}
			if got.addr != tt.want {
				t.Errorf("loadConfig() with WEB_ADDR=%q addr = %q, want %q", tt.addr, got.addr, tt.want)
			}
		})
	}
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	for _, name := range []string{
		"WEB_BASE_URL",
		"VALKEY_ADDR",
		"VALKEY_USERNAME",
		"VALKEY_PASSWORD_FILE",
		"DATABASE_URL",
		"OIDC_ISSUER_URL",
		"OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET",
		"BAO_ADDR",
		"BAO_ROLE_ID",
		"BAO_SECRET_ID_FILE",
		"PDF_URL",
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnv(t)
			delete(env, name)
			_, err := loadConfig(nil, getenvFrom(env))
			if err == nil {
				t.Fatalf("loadConfig() without %s error = nil, want an error", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("loadConfig() without %s error = %q, want it to name %s", name, err, name)
			}
		})
	}
}

func TestLoadConfig_DiscoveryURLIsOptional(t *testing.T) {
	env := validEnv(t)
	delete(env, "OIDC_DISCOVERY_URL")
	got, err := loadConfig(nil, getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() without OIDC_DISCOVERY_URL error = %v", err)
	}
	if got.oidc.DiscoveryURL != "" {
		t.Errorf("loadConfig() without OIDC_DISCOVERY_URL DiscoveryURL = %q, want empty", got.oidc.DiscoveryURL)
	}
}

func TestLoadConfig_DerivedURLs(t *testing.T) {
	env := validEnv(t)
	env["WEB_BASE_URL"] = "http://localhost:8080/"
	got, err := loadConfig(nil, getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() with WEB_BASE_URL=%q error = %v", env["WEB_BASE_URL"], err)
	}
	if want := "http://localhost:8080/auth/callback"; got.oidc.RedirectURL != want {
		t.Errorf("loadConfig() RedirectURL = %q, want %q", got.oidc.RedirectURL, want)
	}
	if want := "http://localhost:8080/"; got.oidc.LogoutReturn != want {
		t.Errorf("loadConfig() LogoutReturn = %q, want %q", got.oidc.LogoutReturn, want)
	}
}

func TestLoadConfig_BadBaseURL(t *testing.T) {
	for _, base := range []string{
		"localhost",
		"localhost:8080",
		"ftp://localhost",
		"http://",
		"http://localhost:8080/app?x=1",
		"http://localhost:8080/app",
		"http://localhost:8080/app/",
		"http://localhost:8080//",
		"http://localhost:8080/?",
		"http://localhost:8080?",
	} {
		t.Run(base, func(t *testing.T) {
			env := validEnv(t)
			env["WEB_BASE_URL"] = base
			if _, err := loadConfig(nil, getenvFrom(env)); err == nil {
				t.Errorf("loadConfig() with WEB_BASE_URL=%q error = nil, want an error", base)
			}
		})
	}
}

func TestLoadConfig_BadValkeyAddr(t *testing.T) {
	env := validEnv(t)
	env["VALKEY_ADDR"] = "valkey"
	if _, err := loadConfig(nil, getenvFrom(env)); err == nil {
		t.Errorf("loadConfig() with VALKEY_ADDR=%q error = nil, want an error", env["VALKEY_ADDR"])
	}
}

func TestConfig_Redacted(t *testing.T) {
	cfg, err := loadConfig(nil, getenvFrom(validEnv(t)))
	if err != nil {
		t.Fatalf("loadConfig(nil, validEnv(t)) error = %v", err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", cfg)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("config", "config", cfg)
	outputs := []string{buf.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		outputs = append(outputs, fmt.Sprintf(verb, cfg), fmt.Sprintf(verb, &cfg))
	}
	for _, out := range outputs {
		if strings.Contains(out, testClientSecret) {
			t.Errorf("output %q of config contains the client secret", out)
		}
		if strings.Contains(out, testSecretID) {
			t.Errorf("output %q of config contains the secret-id", out)
		}
		if strings.Contains(out, testValkeyPassword) {
			t.Errorf("output %q of config contains the valkey password", out)
		}
	}
	if want := "se-web"; !strings.Contains(fmt.Sprintf("%v", cfg), want) {
		t.Errorf("Sprintf(%%v, config) = %q, want the role-id %q", fmt.Sprintf("%v", cfg), want)
	}
	if want := "secret-externalizer-web"; !strings.Contains(fmt.Sprintf("%v", cfg), want) {
		t.Errorf("Sprintf(%%v, config) = %q, want the client ID %q", fmt.Sprintf("%v", cfg), want)
	}
}

func TestLoadConfig_BadPDFURL(t *testing.T) {
	for _, v := range []string{"pdf:3000", "ftp://pdf:3000", "http://pdf:3000/forms", "http://user@pdf:3000"} {
		t.Run(v, func(t *testing.T) {
			env := validEnv(t)
			env["PDF_URL"] = v
			_, err := loadConfig(nil, getenvFrom(env))
			if err == nil {
				t.Fatalf("loadConfig() with PDF_URL=%q error = nil, want an error", v)
			}
			if !strings.Contains(err.Error(), "checking PDF_URL") {
				t.Errorf("loadConfig() with PDF_URL=%q error = %q, want it to say checking PDF_URL", v, err)
			}
		})
	}
}

func TestConfig_ShowsPDFURL(t *testing.T) {
	cfg, err := loadConfig(nil, getenvFrom(validEnv(t)))
	if err != nil {
		t.Fatalf("loadConfig(nil, validEnv(t)) error = %v", err)
	}
	if want := "pdf_url:http://pdf:3000"; !strings.Contains(cfg.String(), want) {
		t.Errorf("config.String() = %q, want it to contain %q", cfg.String(), want)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", cfg)
	if want := `"pdf_url":"http://pdf:3000"`; !strings.Contains(buf.String(), want) {
		t.Errorf("config.LogValue() logged %q, want it to contain %q", buf.String(), want)
	}
}

func TestLoadConfig_ErrorHidesClientSecret(t *testing.T) {
	env := validEnv(t)
	env["WEB_BASE_URL"] = "localhost"
	_, err := loadConfig(nil, getenvFrom(env))
	if err == nil {
		t.Fatal("loadConfig() with a bad WEB_BASE_URL error = nil, want an error")
	}
	if strings.Contains(err.Error(), testClientSecret) {
		t.Errorf("loadConfig() error = %q, contains the client secret", err)
	}
}

func TestLoadConfig_RequiresDatabaseURL(t *testing.T) {
	env := validEnv(t)
	delete(env, "DATABASE_URL")
	_, err := loadConfig(nil, getenvFrom(env))
	if err == nil {
		t.Fatal("loadConfig() without DATABASE_URL error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("loadConfig() without DATABASE_URL error = %q, want it to name DATABASE_URL", err)
	}
}

func TestConfig_RedactsDatabaseURL(t *testing.T) {
	cfg, err := loadConfig(nil, getenvFrom(validEnv(t)))
	if err != nil {
		t.Fatalf("loadConfig(nil, validEnv(t)) error = %v", err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", cfg)
	outputs := []string{buf.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		outputs = append(outputs, fmt.Sprintf(verb, cfg), fmt.Sprintf(verb, &cfg))
	}
	for _, out := range outputs {
		if strings.Contains(out, "dev-only-cmd-web-test-db") {
			t.Errorf("output %q of config contains the database password", out)
		}
	}
}

func TestLoadConfig_SecretIDFile(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }},
		{"empty", func(t *testing.T) string { return writeSecretID(t, " \n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			env["BAO_SECRET_ID_FILE"] = tt.path(t)
			_, err := loadConfig(nil, getenvFrom(env))
			if err == nil {
				t.Fatalf("loadConfig() with a %s secret-id file error = nil, want an error", tt.name)
			}
			if !strings.Contains(err.Error(), "BAO_SECRET_ID_FILE") {
				t.Errorf("loadConfig() with a %s secret-id file error = %q, want it to name BAO_SECRET_ID_FILE", tt.name, err)
			}
		})
	}
}

func TestLoadConfig_ValkeyPasswordFile(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }},
		{"empty", func(t *testing.T) string { return writeFile(t, " \n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			env["VALKEY_PASSWORD_FILE"] = tt.path(t)
			_, err := loadConfig(nil, getenvFrom(env))
			if err == nil {
				t.Fatalf("loadConfig() with a %s valkey password file error = nil, want an error", tt.name)
			}
			if !strings.Contains(err.Error(), "VALKEY_PASSWORD_FILE") {
				t.Errorf("loadConfig() with a %s valkey password file error = %q, want it to name VALKEY_PASSWORD_FILE", tt.name, err)
			}
		})
	}
}

func TestLoadConfig_ValkeyCAFile(t *testing.T) {
	caPEM, _ := newTestCA(t)
	env := validEnv(t)
	env["VALKEY_CA_FILE"] = writeFile(t, string(caPEM))
	got, err := loadConfig(nil, getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() with VALKEY_CA_FILE error = %v", err)
	}
	if diff := cmp.Diff(caPEM, got.valkeyCA); diff != "" {
		t.Errorf("loadConfig() with VALKEY_CA_FILE valkeyCA mismatch (-want +got):\n%s", diff)
	}
	if want := "valkey_tls:true"; !strings.Contains(got.String(), want) {
		t.Errorf("config.String() = %q, want it to contain %q", got.String(), want)
	}
}

func TestLoadConfig_ValkeyCAFileIsOptional(t *testing.T) {
	got, err := loadConfig(nil, getenvFrom(validEnv(t)))
	if err != nil {
		t.Fatalf("loadConfig(nil, validEnv(t)) error = %v", err)
	}
	if got.valkeyCA != nil {
		t.Errorf("loadConfig() without VALKEY_CA_FILE valkeyCA = %q, want nil", got.valkeyCA)
	}
	if want := "valkey_tls:false"; !strings.Contains(got.String(), want) {
		t.Errorf("config.String() = %q, want it to contain %q", got.String(), want)
	}
}

func TestLoadConfig_BadValkeyCAFile(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }},
		{"no certificate", func(t *testing.T) string { return writeFile(t, "not a certificate\n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			env["VALKEY_CA_FILE"] = tt.path(t)
			_, err := loadConfig(nil, getenvFrom(env))
			if err == nil {
				t.Fatalf("loadConfig() with a %s VALKEY_CA_FILE error = nil, want an error", tt.name)
			}
			if !strings.Contains(err.Error(), "VALKEY_CA_FILE") {
				t.Errorf("loadConfig() with a %s VALKEY_CA_FILE error = %q, want it to name VALKEY_CA_FILE", tt.name, err)
			}
		})
	}
}

func TestLoadConfig_BaoCAFile(t *testing.T) {
	caPEM, _ := newTestCA(t)
	env := validEnv(t)
	env["BAO_ADDR"] = "https://openbao:8200"
	env["BAO_CA_FILE"] = writeFile(t, string(caPEM))
	got, err := loadConfig(nil, getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() with BAO_CA_FILE error = %v", err)
	}
	if diff := cmp.Diff(caPEM, got.baoCA); diff != "" {
		t.Errorf("loadConfig() with BAO_CA_FILE baoCA mismatch (-want +got):\n%s", diff)
	}
	if want := "bao_tls:true"; !strings.Contains(got.String(), want) {
		t.Errorf("config.String() = %q, want it to contain %q", got.String(), want)
	}
}

func TestLoadConfig_BaoTLS(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"http://openbao:8200", "bao_tls:false"},
		{"https://openbao.example:8200", "bao_tls:true"},
	}
	for _, tt := range tests {
		env := validEnv(t)
		env["BAO_ADDR"] = tt.addr
		got, err := loadConfig(nil, getenvFrom(env))
		if err != nil {
			t.Fatalf("loadConfig() with BAO_ADDR=%q error = %v", tt.addr, err)
		}
		if got.baoCA != nil {
			t.Errorf("loadConfig() with BAO_ADDR=%q and no BAO_CA_FILE baoCA = %q, want nil", tt.addr, got.baoCA)
		}
		if !strings.Contains(got.String(), tt.want) {
			t.Errorf("loadConfig() with BAO_ADDR=%q String() = %q, want it to contain %q", tt.addr, got.String(), tt.want)
		}
	}
}

func TestLoadConfig_BadBaoTLS(t *testing.T) {
	caPEM, _ := newTestCA(t)
	tests := []struct {
		name   string
		addr   string
		caFile func(t *testing.T) string
		names  string
	}{
		{"missing CA file", "https://openbao:8200", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }, "BAO_CA_FILE"},
		{"CA file without a certificate", "https://openbao:8200", func(t *testing.T) string { return writeFile(t, "not a certificate\n") }, "BAO_CA_FILE"},
		{"CA file with an http address", "http://openbao:8200", func(t *testing.T) string { return writeFile(t, string(caPEM)) }, "BAO_CA_FILE"},
		{"address that is not http or https", "ftp://openbao:8200", func(*testing.T) string { return "" }, "BAO_ADDR"},
		{"address without a host", "https://", func(*testing.T) string { return "" }, "BAO_ADDR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			env["BAO_ADDR"] = tt.addr
			if path := tt.caFile(t); path != "" {
				env["BAO_CA_FILE"] = path
			}
			_, err := loadConfig(nil, getenvFrom(env))
			if err == nil {
				t.Fatalf("loadConfig() with a %s error = nil, want an error", tt.name)
			}
			if !strings.Contains(err.Error(), tt.names) {
				t.Errorf("loadConfig() with a %s error = %q, want it to name %s", tt.name, err, tt.names)
			}
		})
	}
}
