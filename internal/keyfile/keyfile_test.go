package keyfile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/keyfile"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

const (
	fakeCert = "-----BEGIN CERTIFICATE-----\nZGV2LW9ubHktZmFrZS1jZXJ0aWZpY2F0ZQ==\n-----END CERTIFICATE-----\n"
	fakeKey  = "-----BEGIN RSA PRIVATE KEY-----\nZGV2LW9ubHktZmFrZS1rZXk=\n-----END RSA PRIVATE KEY-----\n" //nolint:gosec // G101: the body is base64 of "dev-only-fake-key"
)

func TestKeyfile_Table(t *testing.T) {
	files := map[string]string{
		"deploy/id_rsa":         "dev-only-fake-key",
		"deploy/id_ed25519.pub": "ssh-ed25519 dev-only-fake-public-key",
		"certs/server.pem":      fakeCert,
		"conf/store.jks":        "dev-only-fake-keystore",
		"conf/STORE.P12":        "dev-only-fake-keystore",
		"conf/app.keystore":     "dev-only-fake-keystore",
		"keys/app.key":          fakeKey,
		"keys/crlf.txt":         strings.ReplaceAll("-----BEGIN OPENSSH PRIVATE KEY-----\nZGV2\n", "\n", "\r\n"),
		"keys/pkcs8":            "comment line\n-----BEGIN PRIVATE KEY-----\nZGV2\n",
		"keys/encrypted.pem":    "-----BEGIN ENCRYPTED PRIVATE KEY-----\nZGV2\n",
		"README.md":             "Put the PRIVATE KEY in the vault, never in the repository.\n",
		"src/Crypto.java":       "class Crypto {\n  // loads the PRIVATE KEY and the CERTIFICATE from the vault\n}\n",
		"src/Embedded.java":     "class Embedded {\n  String k = \"-----BEGIN PRIVATE KEY-----\";\n}\n",
		"src/Late.txt":          strings.Repeat("x", 4096) + "\n" + fakeKey,
		"target/id_rsa":         "dev-only-fake-key",
		"empty.pem":             "",
	}
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "keys", "app.key"), filepath.Join(dir, "link.pem")); err != nil {
		t.Fatal(err)
	}

	got, err := keyfile.New().Scan(t.Context(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].File < got[j].File })

	raw := func(file, rule string) scan.Raw {
		return scan.Raw{
			Tool: "keyfile", Rule: rule, Category: finding.CategorySSH, Kind: finding.KindKeyFile,
			File: file, Offset: 0, EndOffset: -1,
		}
	}
	want := []scan.Raw{
		raw("certs/server.pem", "pem-certificate"),
		raw("conf/STORE.P12", "key-file-extension"),
		raw("conf/app.keystore", "key-file-extension"),
		raw("conf/store.jks", "key-file-extension"),
		raw("deploy/id_rsa", "key-file-name"),
		raw("keys/app.key", "pem-private-key"),
		raw("keys/crlf.txt", "pem-private-key"),
		raw("keys/encrypted.pem", "pem-private-key"),
		raw("keys/pkcs8", "pem-private-key"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Scan() mismatch (-want +got):\n%s", diff)
	}
}

func TestKeyfile_Unreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of their mode")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "locked.pem")
	if err := os.WriteFile(p, []byte(fakeKey), 0o000); err != nil {
		t.Fatal(err)
	}
	got, err := keyfile.New().Scan(t.Context(), dir)
	if err != nil || len(got) != 0 {
		t.Errorf("Scan(unreadable file) = %+v, %v, want nothing and no error", got, err)
	}
}

func TestKeyfile_Cancelled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.pem"), []byte(fakeKey), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := keyfile.New().Scan(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("Scan(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

func TestKeyfile_Version(t *testing.T) {
	s := keyfile.New()
	got, err := s.Version(t.Context())
	if err != nil || got == "" {
		t.Errorf("Version() = %q, %v, want a fixed version", got, err)
	}
	if s.Name() != "keyfile" {
		t.Errorf("Name() = %q, want keyfile", s.Name())
	}
}
