package auth_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

func TestClientSecret_Redacted(t *testing.T) {
	const raw = "dev-only-not-a-secret"
	s := auth.ClientSecret(raw)

	formats := []string{"%v %s %+v", "%q %#v"}
	for _, f := range formats {
		if out := fmt.Sprintf(f, s, s); strings.Contains(out, raw) {
			t.Errorf("Sprintf(%q, secret) = %q, contains the secret", f, out)
		}
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("config", "secret", s)
	logger = slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config", "secret", s)
	if strings.Contains(buf.String(), raw) {
		t.Errorf("slog output = %q, contains the secret", buf.String())
	}
	if !strings.Contains(buf.String(), "[redacted]") {
		t.Errorf("slog output = %q, want it to contain [redacted]", buf.String())
	}

	if got := s.Reveal(); got != raw {
		t.Errorf("Reveal() = %q, want %q", got, raw)
	}
}

// checkRedacted logs v through the JSON and text slog handlers and formats it
// with every common fmt verb, and reports any output that contains raw.
func checkRedacted(t *testing.T, what string, v any, raw string) {
	t.Helper()
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("value", "v", v)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("value", "v", v)
	if strings.Contains(buf.String(), raw) {
		t.Errorf("slog output of %s = %q, contains the secret", what, buf.String())
	}
	if out, err := json.Marshal(v); err != nil || strings.Contains(string(out), raw) {
		t.Errorf("json.Marshal(%s) = %q, %v, want no secret and no error", what, out, err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(verb, v); strings.Contains(out, raw) {
			t.Errorf("Sprintf(%q, %s) = %q, contains the secret", verb, what, out)
		}
	}
}

func TestClientSecret_RedactedInsideStructs(t *testing.T) {
	const raw = "dev-only-not-a-secret"
	cfg := auth.Config{IssuerURL: "http://issuer.invalid", ClientID: "fake-client", ClientSecret: auth.ClientSecret(raw)}

	checkRedacted(t, "a struct with a ClientSecret field", struct{ Secret auth.ClientSecret }{auth.ClientSecret(raw)}, raw)
	checkRedacted(t, "Config", cfg, raw)
	checkRedacted(t, "*Config", &cfg, raw)
	checkRedacted(t, "a struct with a Config field", struct{ OIDC auth.Config }{cfg}, raw)
}

func TestConfig_LogValue(t *testing.T) {
	cfg := auth.Config{IssuerURL: "http://issuer.invalid", ClientID: "fake-client", ClientSecret: auth.ClientSecret("dev-only-not-a-secret")}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "oidc", cfg)
	var rec struct {
		OIDC map[string]string `json:"oidc"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line %q is not JSON: %v", buf.String(), err)
	}
	want := map[string]string{"issuer_url": "http://issuer.invalid", "client_id": "fake-client", "client_secret": "[redacted]"}
	for k, v := range want {
		if got := rec.OIDC[k]; got != v {
			t.Errorf("logged Config %s = %q, want %q", k, got, v)
		}
	}
}

func TestAttempt_RedactedInsideStructs(t *testing.T) {
	a := auth.Attempt{State: "fake-state-value", Nonce: "fake-nonce-value", Verifier: "fake-verifier-value", ReturnTo: "/projects"}
	wrapper := struct{ Attempt auth.Attempt }{a}
	for _, raw := range []string{a.State, a.Nonce, a.Verifier} {
		checkRedacted(t, "a struct with an Attempt field", wrapper, raw)
	}
}

func TestClientSecret_String(t *testing.T) {
	if got := auth.ClientSecret("x").String(); got != "[redacted]" {
		t.Errorf("ClientSecret(%q).String() = %q, want %q", "x", got, "[redacted]")
	}
}
