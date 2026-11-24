package session_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// testPassword is an obviously fake valkey password.
const testPassword = "dev-only-session-test-valkey-password"

func TestPassword_Redacted(t *testing.T) {
	p := session.Password(testPassword)
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("password", "password", p)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("password", "password", p)
	outputs := []string{buf.String(), p.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		outputs = append(outputs, fmt.Sprintf(verb, p), fmt.Sprintf(verb, &p))
	}
	text, err := p.MarshalText()
	if err != nil {
		t.Fatalf("Password.MarshalText() error = %v", err)
	}
	outputs = append(outputs, string(text))
	for _, out := range outputs {
		if strings.Contains(out, testPassword) {
			t.Errorf("output %q of Password contains the password", out)
		}
	}
}

func TestReadPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(testPassword+"\n"), 0o600); err != nil {
		t.Fatalf("writing the password file: %v", err)
	}
	got, err := session.ReadPasswordFile(path)
	if err != nil {
		t.Fatalf("ReadPasswordFile(%q) error = %v", path, err)
	}
	if got != session.Password(testPassword) {
		t.Errorf("ReadPasswordFile(%q) returned a different password, want the file content without the newline", path)
	}
}

func TestReadPasswordFile_Missing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	_, err := session.ReadPasswordFile(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadPasswordFile(%q) error = %v, want fs.ErrNotExist", path, err)
	}
}

func TestReadPasswordFile_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("writing the password file: %v", err)
	}
	if _, err := session.ReadPasswordFile(path); err == nil {
		t.Errorf("ReadPasswordFile(%q) with only white space error = nil, want an error", path)
	}
}
