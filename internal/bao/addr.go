package bao

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/cafile"
)

// UsesTLS reports whether addr, the address of an OpenBao server, is an
// https address. It returns an error unless addr is an absolute http or
// https URL with a host and with no user info, path, query, or fragment.
// The error never repeats addr, which could hold user info.
func UsesTLS(addr string) (bool, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return false, errors.New("the address is not a URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false, errors.New("the address must start with http:// or https://")
	}
	if u.Host == "" {
		return false, errors.New("the address has no host")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false, errors.New("the address must not have user info, a path, a query, or a fragment")
	}
	return scheme == "https", nil
}

// LoadCA checks addr, the address of an OpenBao server, and reads the PEM
// certificates in the file at caPath when caPath is not empty. It returns
// whether addr is an https address and the certificates, which a client
// trusts instead of the system's roots. A CA file needs an https address.
func LoadCA(addr, caPath string) (bool, []byte, error) {
	useTLS, err := UsesTLS(addr)
	if err != nil {
		return false, nil, fmt.Errorf("checking the address: %w", err)
	}
	if caPath == "" {
		return useTLS, nil, nil
	}
	if !useTLS {
		return false, nil, errors.New("a CA file needs an https address")
	}
	ca, err := cafile.Read(caPath)
	if err != nil {
		return false, nil, err
	}
	return true, ca, nil
}

// NewHTTPClient returns an HTTP client for New. timeout bounds one attempt
// of a request, including sending its body and reading the answer, so it
// must allow for the largest value the caller sends or reads. answer bounds
// the wait for the server's answer after the whole request is sent, so a
// server that takes a request and never answers fails in that time even
// when timeout is long. When ca is not nil, the client trusts only the
// certificates in it; otherwise it trusts the system's roots.
func NewHTTPClient(ca []byte, timeout, answer time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		return nil, errors.New("the timeout must be positive")
	}
	if answer <= 0 || answer > timeout {
		return nil, errors.New("the answer limit must be positive and no longer than the timeout")
	}
	transport, err := cafile.Transport(ca)
	if err != nil {
		return nil, err
	}
	transport.ResponseHeaderTimeout = answer
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
