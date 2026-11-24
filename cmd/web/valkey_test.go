package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// newTestCA makes a certificate authority and a server certificate it
// signed for 127.0.0.1, and returns the authority's certificate in PEM and
// the server certificate with its key. Both live only for the test.
func newTestCA(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the CA key: %v", err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cmd/web test valkey CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating the CA certificate: %v", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing the CA certificate: %v", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "valkey"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating the server certificate: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return caPEM, tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}
}

// fakeValkey answers the commands a client sends on connecting and records
// each command as its words joined by spaces.
type fakeValkey struct {
	mu       sync.Mutex
	commands []string
}

func (f *fakeValkey) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.commands)
}

// serve accepts connections on ln until it is closed. It answers HELLO with
// a RESP3 map and every other command with OK. The returned function closes
// ln and every connection and waits for the goroutines.
func (f *fakeValkey) serve(t *testing.T, ln net.Listener) (stop func()) {
	t.Helper()
	var (
		mu    sync.Mutex
		conns []net.Conn

		wg sync.WaitGroup
	)
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Go(func() { f.answer(conn) })
		}
	})
	return func() {
		_ = ln.Close() // a listener the test already closed has nothing left to report
		mu.Lock()
		for _, c := range conns {
			_ = c.Close() // the peer may have closed it already
		}
		mu.Unlock()
		wg.Wait()
	}
}

func (f *fakeValkey) answer(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "*") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(line, "*%d", &n); err != nil {
			return
		}
		words := make([]string, 0, n)
		for range n {
			if _, err := r.ReadString('\n'); err != nil {
				return
			}
			arg, err := r.ReadString('\n')
			if err != nil {
				return
			}
			words = append(words, strings.TrimSuffix(arg, "\r\n"))
		}
		f.mu.Lock()
		f.commands = append(f.commands, strings.Join(words, " "))
		f.mu.Unlock()
		reply := "+OK\r\n"
		if len(words) > 0 && strings.EqualFold(words[0], "HELLO") {
			reply = "%2\r\n+server\r\n+valkey\r\n+proto\r\n:3\r\n"
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func TestValkeyOption_TLS(t *testing.T) {
	caPEM, serverCert := newTestCA(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("tls.Listen() error = %v", err)
	}
	var server fakeValkey
	stop := server.serve(t, ln)
	t.Cleanup(stop)

	cfg := config{
		valkeyAddr:     ln.Addr().String(),
		valkeyUser:     "se-web",
		valkeyPassword: session.Password(testValkeyPassword),
		valkeyCA:       caPEM,
	}
	opt, err := valkeyOption(cfg)
	if err != nil {
		t.Fatalf("valkeyOption() error = %v", err)
	}
	c, err := valkey.NewClient(opt)
	if err != nil {
		t.Fatalf("valkey.NewClient(valkeyOption()) over TLS error = %v", err)
	}
	c.Close()

	want := "HELLO 3 AUTH se-web " + testValkeyPassword
	if !slices.ContainsFunc(server.seen(), func(cmd string) bool { return strings.HasPrefix(cmd, want) }) {
		t.Errorf("valkey.NewClient(valkeyOption()) sent %d commands without one starting %q", len(server.seen()), "HELLO 3 AUTH se-web")
	}
}

func TestValkeyOption_TLSRefusesOtherCA(t *testing.T) {
	_, serverCert := newTestCA(t)
	otherCA, _ := newTestCA(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("tls.Listen() error = %v", err)
	}
	var server fakeValkey
	stop := server.serve(t, ln)
	t.Cleanup(stop)

	cfg := config{
		valkeyAddr:     ln.Addr().String(),
		valkeyUser:     "se-web",
		valkeyPassword: session.Password(testValkeyPassword),
		valkeyCA:       otherCA,
	}
	opt, err := valkeyOption(cfg)
	if err != nil {
		t.Fatalf("valkeyOption() error = %v", err)
	}
	c, err := valkey.NewClient(opt)
	if err == nil {
		c.Close()
		t.Fatal("valkey.NewClient(valkeyOption()) with a certificate from another CA error = nil, want an error")
	}
	if got := server.seen(); len(got) != 0 {
		t.Errorf("valkey.NewClient(valkeyOption()) with a certificate from another CA sent %d commands, want none", len(got))
	}
}

func TestValkeyOption_Plaintext(t *testing.T) {
	cfg := config{
		valkeyAddr:     "valkey:6379",
		valkeyUser:     "se-web",
		valkeyPassword: session.Password(testValkeyPassword),
	}
	opt, err := valkeyOption(cfg)
	if err != nil {
		t.Fatalf("valkeyOption() error = %v", err)
	}
	if opt.TLSConfig != nil {
		t.Errorf("valkeyOption() without a CA TLSConfig = %v, want nil", opt.TLSConfig)
	}
	if opt.Username != "se-web" || opt.Password != testValkeyPassword {
		t.Errorf("valkeyOption() without a CA Username = %q and a different password, want se-web and the configured password", opt.Username)
	}
}
