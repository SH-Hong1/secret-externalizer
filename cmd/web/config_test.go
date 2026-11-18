package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// testClientSecret is an obviously fake client secret.
const testClientSecret = "dev-only-cmd-web-test-secret" //nolint:gosec // G101: an obviously fake fixture value

// validEnv returns a complete environment that loadConfig accepts. Tests
// change or remove one variable at a time.
func validEnv() map[string]string {
	return map[string]string{
		"WEB_BASE_URL":       "http://localhost:8080",
		"VALKEY_ADDR":        "valkey:6379",
		"OIDC_ISSUER_URL":    "http://localhost:8081/realms/secret-externalizer",
		"OIDC_DISCOVERY_URL": "http://keycloak:8080/realms/secret-externalizer",
		"OIDC_CLIENT_ID":     "secret-externalizer-web",
		"OIDC_CLIENT_SECRET": testClientSecret,
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestLoadConfig(t *testing.T) {
	got, err := loadConfig(nil, getenvFrom(validEnv()))
	if err != nil {
		t.Fatalf("loadConfig(nil, validEnv()) error = %v", err)
	}
	want := config{
		addr:       ":8080",
		valkeyAddr: "valkey:6379",
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
		t.Errorf("loadConfig(nil, validEnv()) mismatch (-want +got):\n%s", diff)
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
			env := validEnv()
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
		"OIDC_ISSUER_URL",
		"OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET",
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
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
	env := validEnv()
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
	env := validEnv()
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
	} {
		t.Run(base, func(t *testing.T) {
			env := validEnv()
			env["WEB_BASE_URL"] = base
			if _, err := loadConfig(nil, getenvFrom(env)); err == nil {
				t.Errorf("loadConfig() with WEB_BASE_URL=%q error = nil, want an error", base)
			}
		})
	}
}

func TestLoadConfig_BadValkeyAddr(t *testing.T) {
	env := validEnv()
	env["VALKEY_ADDR"] = "valkey"
	if _, err := loadConfig(nil, getenvFrom(env)); err == nil {
		t.Errorf("loadConfig() with VALKEY_ADDR=%q error = nil, want an error", env["VALKEY_ADDR"])
	}
}

func TestConfig_Redacted(t *testing.T) {
	cfg, err := loadConfig(nil, getenvFrom(validEnv()))
	if err != nil {
		t.Fatalf("loadConfig(nil, validEnv()) error = %v", err)
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
	}
	if want := "secret-externalizer-web"; !strings.Contains(fmt.Sprintf("%v", cfg), want) {
		t.Errorf("Sprintf(%%v, config) = %q, want the client ID %q", fmt.Sprintf("%v", cfg), want)
	}
}

func TestLoadConfig_ErrorHidesClientSecret(t *testing.T) {
	env := validEnv()
	env["WEB_BASE_URL"] = "localhost"
	_, err := loadConfig(nil, getenvFrom(env))
	if err == nil {
		t.Fatal("loadConfig() with a bad WEB_BASE_URL error = nil, want an error")
	}
	if strings.Contains(err.Error(), testClientSecret) {
		t.Errorf("loadConfig() error = %q, contains the client secret", err)
	}
}
