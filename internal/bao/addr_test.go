package bao_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
)

func TestUsesTLS(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"http://openbao:8200", false},
		{"https://openbao:8200", true},
		{"HTTPS://openbao.example", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := bao.UsesTLS(tt.addr)
			if err != nil {
				t.Fatalf("UsesTLS(%q) error = %v", tt.addr, err)
			}
			if got != tt.want {
				t.Errorf("UsesTLS(%q) = %t, want %t", tt.addr, got, tt.want)
			}
		})
	}
}

func TestUsesTLS_Refuses(t *testing.T) {
	for _, addr := range []string{
		"",
		"openbao:8200",
		"ftp://openbao:8200",
		"https://",
		"https://user:pass@openbao:8200",
		"https://openbao:8200/v1",
		"https://openbao:8200?x=1",
		"https://openbao:8200#x",
		"://bad",
	} {
		t.Run(addr, func(t *testing.T) {
			if got, err := bao.UsesTLS(addr); err == nil {
				t.Errorf("UsesTLS(%q) = %t, want an error", addr, got)
			}
		})
	}
}

func TestLoadCA(t *testing.T) {
	caPEM := testCAPEM(t)
	path := writeTestFile(t, caPEM)
	tests := []struct {
		name    string
		addr    string
		caPath  string
		wantTLS bool
		wantCA  []byte
	}{
		{"http without a CA", "http://openbao:8200", "", false, nil},
		{"https without a CA", "https://openbao:8200", "", true, nil},
		{"https with a CA", "https://openbao:8200", path, true, caPEM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTLS, gotCA, err := bao.LoadCA(tt.addr, tt.caPath)
			if err != nil {
				t.Fatalf("LoadCA(%q, %q) error = %v", tt.addr, tt.caPath, err)
			}
			if gotTLS != tt.wantTLS || string(gotCA) != string(tt.wantCA) {
				t.Errorf("LoadCA(%q, %q) = %t, %q, want %t, %q", tt.addr, tt.caPath, gotTLS, gotCA, tt.wantTLS, tt.wantCA)
			}
		})
	}
}

func TestLoadCA_Refuses(t *testing.T) {
	caPath := writeTestFile(t, testCAPEM(t))
	tests := []struct {
		name   string
		addr   string
		caPath string
	}{
		{"bad address", "ftp://openbao:8200", ""},
		{"CA with an http address", "http://openbao:8200", caPath},
		{"missing CA file", "https://openbao:8200", filepath.Join(t.TempDir(), "absent")},
		{"CA file without a certificate", "https://openbao:8200", writeTestFile(t, []byte("not a certificate\n"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotTLS, gotCA, err := bao.LoadCA(tt.addr, tt.caPath); err == nil {
				t.Errorf("LoadCA(%q, %q) = %t, %q, want an error", tt.addr, tt.caPath, gotTLS, gotCA)
			}
		})
	}
}

// testCAPEM returns the PEM of a self-signed CA certificate.
func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "bao test CA"},
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

// writeTestFile writes content to a new file and returns its path.
func writeTestFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestNewHTTPClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	srvCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	tests := []struct {
		name    string
		ca      []byte
		wantErr bool
	}{
		{"the server's CA", srvCA, false},
		{"another CA", testCAPEM(t), true},
		{"the system's roots", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc, err := bao.NewHTTPClient(tt.ca, 5*time.Second, time.Second)
			if err != nil {
				t.Fatalf("NewHTTPClient(%s) error = %v", tt.name, err)
			}
			if hc.Timeout != 5*time.Second {
				t.Errorf("NewHTTPClient(%s).Timeout = %v, want 5s", tt.name, hc.Timeout)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			resp, err := hc.Do(req)
			if err == nil {
				_ = resp.Body.Close() // the body is empty
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("GET %s through NewHTTPClient(%s) error = %v, want error %t", srv.URL, tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestNewHTTPClient_Refuses(t *testing.T) {
	if _, err := bao.NewHTTPClient([]byte("not a certificate"), time.Second, time.Second); err == nil {
		t.Errorf("NewHTTPClient(not PEM) error = nil, want an error")
	}
	if _, err := bao.NewHTTPClient(nil, 0, time.Second); err == nil {
		t.Errorf("NewHTTPClient(nil, 0, 1s) error = nil, want an error for no timeout")
	}
	if _, err := bao.NewHTTPClient(nil, time.Second, 0); err == nil {
		t.Errorf("NewHTTPClient(nil, 1s, 0) error = nil, want an error for no answer limit")
	}
	if _, err := bao.NewHTTPClient(nil, time.Second, 2*time.Second); err == nil {
		t.Errorf("NewHTTPClient(nil, 1s, 2s) error = nil, want an error for an answer limit over the timeout")
	}
}

func TestNewHTTPClient_AnswerLimit(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) })

	hc, err := bao.NewHTTPClient(nil, time.Hour, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewHTTPClient(nil, 1h, 50ms) error = %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := hc.Do(req)
	if err == nil {
		_ = resp.Body.Close() // the body is not read
		t.Fatalf("GET %s from a server that never answers error = nil, want a timeout", srv.URL)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("GET %s from a server that never answers error = %v, want a timeout", srv.URL, err)
	}
}
