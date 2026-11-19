package bao_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
)

func TestReadSecretIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret-id")
	if err := os.WriteFile(path, []byte("  "+testSecretID+"\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	got, err := bao.ReadSecretIDFile(path)
	if err != nil || got != testSecretID {
		t.Errorf("ReadSecretIDFile(%q) = %d bytes, %v; want the trimmed secret-id", path, len(got), err)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", empty, err)
	}
	if _, err := bao.ReadSecretIDFile(empty); err == nil {
		t.Errorf("ReadSecretIDFile(%q) of a blank file error = nil, want an error", empty)
	}

	missing := filepath.Join(dir, "absent")
	_, err = bao.ReadSecretIDFile(missing)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadSecretIDFile(%q) error = %v, want one matching fs.ErrNotExist", missing, err)
	}
	if err != nil && strings.Contains(err.Error(), testSecretID) {
		t.Errorf("ReadSecretIDFile(%q) error = %q, contains the secret-id", missing, err)
	}
}
