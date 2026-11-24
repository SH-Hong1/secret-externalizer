// Package cafile reads the CA certificates that a client trusts for one
// service and builds TLS settings that trust only those certificates.
package cafile

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
)

// errNoCertificate means the PEM holds no certificate.
var errNoCertificate = errors.New("holds no PEM certificate")

// Read reads the PEM certificates in the file at path and checks that there
// is at least one.
func Read(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own CA file setting
	if err != nil {
		return nil, fmt.Errorf("reading the CA file: %w", err)
	}
	if _, err := pool(b); err != nil {
		return nil, fmt.Errorf("%q %w", path, errNoCertificate)
	}
	return b, nil
}

// TLSConfig returns client TLS settings that trust only the certificates in
// pemCerts, check the server's certificate against serverName, and accept
// TLS 1.2 or later.
func TLSConfig(pemCerts []byte, serverName string) (*tls.Config, error) {
	roots, err := pool(pemCerts)
	if err != nil {
		return nil, err
	}
	return &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}, nil
}

// Transport returns a copy of http.DefaultTransport. When pemCerts is not
// nil, the copy trusts only the certificates in it, accepts TLS 1.2 or
// later, and checks the server's certificate against the host of each
// request; otherwise it trusts the system's roots.
func Transport(pemCerts []byte) (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("http.DefaultTransport is not an *http.Transport")
	}
	t := base.Clone()
	if pemCerts == nil {
		return t, nil
	}
	cfg, err := TLSConfig(pemCerts, "")
	if err != nil {
		return nil, err
	}
	t.TLSClientConfig = cfg
	return t, nil
}

// pool returns a certificate pool of the certificates in pemCerts.
func pool(pemCerts []byte) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemCerts) {
		return nil, fmt.Errorf("the CA %w", errNoCertificate)
	}
	return roots, nil
}
