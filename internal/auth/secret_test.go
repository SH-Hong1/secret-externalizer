package auth_test

import (
	"bytes"
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
