// Command web serves the secret-externalizer user interface.
package main

import (
	"context"
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
// serves HTTP until SIGINT, SIGTERM or cancellation of ctx.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
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
	return serve(ctx, logger, cfg)
}

// serve listens on cfg.addr and returns after ctx is cancelled and in-flight
// requests have finished or the grace period has passed.
func serve(ctx context.Context, logger *slog.Logger, cfg config) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.addr, err)
	}

	srv := &http.Server{
		Handler:           web.NewServer(logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

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
