package cafile_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/cafile"
)

// certPEM returns the certificate of srv in PEM. httptest signs its server
// certificate with itself as a CA, so the PEM is the CA a client must trust.
func certPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// otherCA returns the PEM of a self-signed CA that signed nothing.
func otherCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cafile test other CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the CA certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestRead(t *testing.T) {
	want := certPEM(newTLSServer(t))
	path := writeFile(t, want)
	got, err := cafile.Read(path)
	if err != nil {
		t.Fatalf("Read(%q) error = %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("Read(%q) = %q, want %q", path, got, want)
	}
}

func TestRead_Refuses(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.crt") }},
		{"empty", func(t *testing.T) string { return writeFile(t, nil) }},
		{"not PEM", func(t *testing.T) string { return writeFile(t, []byte("not a certificate\n")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path(t)
			if got, err := cafile.Read(path); err == nil {
				t.Errorf("Read(%s file) = %q, want an error", tt.name, got)
			}
		})
	}
}

func TestTLSConfig(t *testing.T) {
	got, err := cafile.TLSConfig(certPEM(newTLSServer(t)), "valkey")
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if got.RootCAs == nil {
		t.Errorf("TLSConfig().RootCAs = nil, want the given CA")
	}
	if got.ServerName != "valkey" {
		t.Errorf("TLSConfig().ServerName = %q, want %q", got.ServerName, "valkey")
	}
	if got.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLSConfig().MinVersion = %x, want %x", got.MinVersion, tls.VersionTLS12)
	}
}

func TestTLSConfig_RefusesNoCertificate(t *testing.T) {
	if got, err := cafile.TLSConfig([]byte("not a certificate"), "valkey"); err == nil {
		t.Errorf("TLSConfig(not PEM) = %v, want an error", got)
	}
}

// get sends a GET to srv through transport and returns the error.
func get(t *testing.T, transport *http.Transport, srv *httptest.Server) error {
	t.Helper()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close() // the body is empty and was only read to end the request
	return nil
}

func TestTransport_TrustsTheCA(t *testing.T) {
	srv := newTLSServer(t)
	transport, err := cafile.Transport(certPEM(srv))
	if err != nil {
		t.Fatalf("Transport() error = %v", err)
	}
	if err := get(t, transport, srv); err != nil {
		t.Errorf("GET %s through Transport(its CA) error = %v, want nil", srv.URL, err)
	}
}

func TestTransport_RefusesOtherCA(t *testing.T) {
	srv := newTLSServer(t)
	transport, err := cafile.Transport(otherCA(t))
	if err != nil {
		t.Fatalf("Transport() error = %v", err)
	}
	err = get(t, transport, srv)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("GET %s through Transport(another CA) error = %v, want a certificate error", srv.URL, err)
	}
}

func TestTransport_WithoutCA(t *testing.T) {
	transport, err := cafile.Transport(nil)
	if err != nil {
		t.Fatalf("Transport(nil) error = %v", err)
	}
	if transport == http.DefaultTransport {
		t.Errorf("Transport(nil) returned http.DefaultTransport, want a copy")
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.RootCAs != nil {
		t.Errorf("Transport(nil).TLSClientConfig.RootCAs is set, want the system roots")
	}
	srv := newTLSServer(t)
	if err := get(t, transport, srv); err == nil {
		t.Errorf("GET %s through Transport(nil) error = nil, want the test CA refused", srv.URL)
	}
}

func TestTransport_RefusesNoCertificate(t *testing.T) {
	if got, err := cafile.Transport([]byte("not a certificate")); err == nil {
		t.Errorf("Transport(not PEM) = %v, want an error", got)
	}
}
