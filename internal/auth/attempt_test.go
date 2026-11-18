package auth_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

func TestNewAttempt(t *testing.T) {
	a, err := auth.NewAttempt("/projects")
	if err != nil {
		t.Fatalf("NewAttempt() error = %v", err)
	}
	b, err := auth.NewAttempt("/projects")
	if err != nil {
		t.Fatalf("NewAttempt() error = %v", err)
	}
	if a.ReturnTo != "/projects" {
		t.Errorf("NewAttempt(%q).ReturnTo = %q, want %q", "/projects", a.ReturnTo, "/projects")
	}
	fields := []struct {
		name string
		a, b string
	}{
		{"State", a.State, b.State},
		{"Nonce", a.Nonce, b.Nonce},
		{"Verifier", a.Verifier, b.Verifier},
	}
	for _, f := range fields {
		if len(f.a) < 43 {
			t.Errorf("len(Attempt.%s) = %d, want at least 43", f.name, len(f.a))
		}
		if f.a == f.b {
			t.Errorf("two NewAttempt calls gave the same %s %q", f.name, f.a)
		}
	}
	if a.State == a.Nonce || a.State == a.Verifier || a.Nonce == a.Verifier {
		t.Errorf("State, Nonce, and Verifier of one attempt are not distinct")
	}
}

func TestAttempt_LogValue(t *testing.T) {
	a, err := auth.NewAttempt("/projects")
	if err != nil {
		t.Fatalf("NewAttempt() error = %v", err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("login", "attempt", a)
	out := buf.String()
	for name, v := range map[string]string{"State": a.State, "Nonce": a.Nonce, "Verifier": a.Verifier} {
		if strings.Contains(out, v) {
			t.Errorf("slog output = %q, contains %s", out, name)
		}
	}
}

func TestAttempt_Format(t *testing.T) {
	a := auth.Attempt{
		State:    "fake-state-value",
		Nonce:    "fake-nonce-value",
		Verifier: "fake-verifier-value",
		ReturnTo: "/projects",
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, a)
		for name, v := range map[string]string{"State": a.State, "Nonce": a.Nonce, "Verifier": a.Verifier} {
			if strings.Contains(out, v) {
				t.Errorf("Sprintf(%q, attempt) = %q, contains %s", verb, out, name)
			}
		}
		if !strings.Contains(out, a.ReturnTo) {
			t.Errorf("Sprintf(%q, attempt) = %q, want it to contain ReturnTo %q", verb, out, a.ReturnTo)
		}
	}
}

func TestAttempt_LogValueShowsReturnTo(t *testing.T) {
	a := auth.Attempt{State: "fake-state-value", ReturnTo: "/projects"}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("login", "attempt", a)
	out := buf.String()
	for _, want := range []string{"return_to=/projects", "[redacted]"} {
		if !strings.Contains(out, want) {
			t.Errorf("slog output = %q, want it to contain %q", out, want)
		}
	}
}
