// Command web serves the secret-externalizer user interface.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
	"github.com/SH-Hong1/secret-externalizer/internal/web"
)

// version identifies the build. It is set at link time with -ldflags -X and
// is never assigned while the program runs.
var version = "dev"

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdout, os.Stderr); err != nil {
		// Standard error is the last place left to report to, so a failed
		// write has nowhere to go.
		_, _ = fmt.Fprintf(os.Stderr, "web: %v\n", err)
		os.Exit(1)
	}
}

// run parses args and executes the program. It takes its inputs and outputs
// as parameters so tests can call it without touching process state.
//
// A flag error is returned and not printed, so the caller reports it once.
// Asking for help prints the usage to stdout and is not an error. Otherwise it
// reads the configuration, waits for the identity provider, connects to
// valkey, opens and migrates the database, prepares the OpenBao client
// without contacting OpenBao, and serves HTTP until SIGINT,
// SIGTERM or cancellation of ctx.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	return runWith(ctx, args, getenv, stdout, stderr, discoveryRetry{interval: 2 * time.Second, limit: 60 * time.Second})
}

// discoveryRetry sets how often and for how long start-up asks the identity
// provider for its discovery document. The provider often starts after this
// program, so the first attempts are expected to fail.
type discoveryRetry struct {
	interval time.Duration
	limit    time.Duration
}

// runWith is run with the discovery retry policy as a parameter, so tests can
// shorten it.
func runWith(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, retry discoveryRetry) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stdout)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("parsing flags: %w", err)
	}

	if *showVersion {
		if _, err := fmt.Fprintf(stdout, "secret-externalizer web %s\n", version); err != nil {
			return fmt.Errorf("writing version: %w", err)
		}
		return nil
	}

	cfg, err := loadConfig(args[1:], getenv)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	logger.InfoContext(ctx, "web starting", "version", version, "config", cfg)

	authn, err := discover(ctx, logger, cfg.oidc, outboundClient(), retry)
	if err != nil {
		return err
	}
	if cfg.valkeyCA == nil {
		logger.WarnContext(ctx, "valkey connection is not encrypted", "valkey_addr", cfg.valkeyAddr)
	}
	opt, err := valkeyOption(cfg)
	if err != nil {
		return fmt.Errorf("preparing valkey client: %w", err)
	}
	vk, err := valkey.NewClient(opt)
	if err != nil {
		return fmt.Errorf("connecting to valkey at %s: %w", cfg.valkeyAddr, err)
	}
	defer vk.Close()
	store := session.NewStore(vk, time.Now)

	database, err := db.Open(ctx, cfg.dsn)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	// bao.New does not contact OpenBao, so web starts while OpenBao is
	// down, and only a reveal says that the secret store cannot be reached.
	originals, err := bao.New(cfg.baoAddr, cfg.baoCreds, outboundClient())
	if err != nil {
		return fmt.Errorf("preparing openbao client: %w", err)
	}

	// pdf.New does not contact the converter either, so web starts while
	// it is down, and only the PDF export says that it cannot be reached.
	converter, err := pdf.New(cfg.pdfURL, &http.Client{Timeout: pdfClientLimit})
	if err != nil {
		return fmt.Errorf("preparing pdf client: %w", err)
	}

	return serve(ctx, logger, cfg.addr, web.NewServer(logger, authn, store, store,
		web.Stores{Projects: database, Scans: database, Triage: database, Audits: database, Applies: database, Reports: database, Dashboards: database},
		web.NewHistorySuggester(database), originals, converter, web.Limits{Store: databaseLimit, PDF: pdfLimit, Conversions: pdfConversions}))
}

// pdfLimit bounds one conversion of a report to a PDF. With databaseLimit
// for reading the report it stays inside the server's WriteTimeout, and it
// is longer than the converter's own limit, so the converter's answer to a
// slow page normally arrives first. pdfClientLimit is the client's own
// bound, a little longer, for a call made without a deadline.
// pdfConversions is how many conversions run at one time: the converter's
// memory limit holds one report filled to every cap, so a second request
// waits inside its own pdfLimit.
const (
	pdfLimit       = 20 * time.Second
	pdfClientLimit = 25 * time.Second
	pdfConversions = 1
)

// databaseLimit bounds the database and secret store calls of one request,
// so that a page shows the error page instead of hanging when postgres or
// OpenBao does not answer.
const databaseLimit = 5 * time.Second

// valkeyOption configures the valkey client to sign in as cfg's valkey
// user and, when cfg holds a CA, to reach valkey over TLS, trusting only that
// CA and checking the certificate against the host of the address.
//
// A command fails within a few seconds when valkey is gone. Sessions are
// read under a request context that carries no deadline, and without a limit
// the client would retry a read until valkey came back, so a page would hang
// instead of showing the 503 page.
func valkeyOption(cfg config) (valkey.ClientOption, error) {
	opt := valkey.ClientOption{
		InitAddress: []string{cfg.valkeyAddr},
		Username:    cfg.valkeyUser,
		Password:    string(cfg.valkeyPassword),
		Dialer:      net.Dialer{Timeout: 2 * time.Second},
		RetryDelay:  valkeyRetryDelay,
	}
	if cfg.valkeyCA == nil {
		return opt, nil
	}
	host, _, err := net.SplitHostPort(cfg.valkeyAddr)
	if err != nil {
		return valkey.ClientOption{}, fmt.Errorf("reading the host of %s: %w", cfg.valkeyAddr, err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cfg.valkeyCA) {
		return valkey.ClientOption{}, errors.New("the valkey CA holds no PEM certificate")
	}
	opt.TLSConfig = &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}
	return opt, nil
}

// valkeyRetryDelay retries a failed read twice, shortly after it fails, and
// then gives up by returning a negative delay.
func valkeyRetryDelay(attempts int, _ valkey.Completed, _ error) time.Duration {
	const maxRetries = 2
	if attempts > maxRetries {
		return -1
	}
	return time.Duration(attempts) * 100 * time.Millisecond
}

// outboundClient returns the client for every request this program sends to
// another service.
func outboundClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// discover builds the authenticator, asking the provider for its discovery
// document every retry.interval until it answers or retry.limit has passed.
// It stops as soon as ctx is cancelled.
func discover(ctx context.Context, logger *slog.Logger, cfg auth.Config, client *http.Client, retry discoveryRetry) (*auth.Authenticator, error) {
	// Errors name the address actually contacted as well as the issuer, since
	// the two differ when DiscoveryURL is set.
	contacted := cfg.DiscoveryURL
	if contacted == "" {
		contacted = cfg.IssuerURL
	}
	start := time.Now()
	timer := time.NewTimer(retry.interval)
	defer timer.Stop()
	for {
		authn, err := auth.NewAuthenticator(ctx, cfg, client)
		if err == nil {
			return authn, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("reaching oidc issuer %s at %s: %w", cfg.IssuerURL, contacted, ctx.Err())
		}
		if time.Since(start)+retry.interval > retry.limit {
			return nil, fmt.Errorf("reaching oidc issuer %s at %s for %s: %w", cfg.IssuerURL, contacted, retry.limit, err)
		}
		logger.WarnContext(ctx, "oidc issuer not ready, retrying",
			"issuer", cfg.IssuerURL, "discovery_url", contacted, "retry_in", retry.interval.String(), "error", err.Error())

		timer.Reset(retry.interval)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("reaching oidc issuer %s at %s: %w", cfg.IssuerURL, contacted, ctx.Err())
		case <-timer.C:
		}
	}
}

// serve listens on addr and returns after ctx is cancelled and in-flight
// requests have finished or the grace period has passed.
func serve(ctx context.Context, logger *slog.Logger, addr string, handler http.Handler) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	srv := newHTTPServer(logger, handler)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(listener) }()
	logger.InfoContext(ctx, "listening", "addr", listener.Addr().String())

	select {
	case err := <-errc:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	}

	// The cancelled ctx cannot bound the shutdown, so the grace period gets a
	// fresh context.
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}

// newHTTPServer returns the server for handler with every timeout set. The
// messages net/http logs itself, such as accept errors, go through logger at
// level ERROR, so they are JSON on the same stream.
func newHTTPServer(logger *slog.Logger, handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
