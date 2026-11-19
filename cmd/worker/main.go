// Command worker runs the scans that the web program queues.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/betterleaks"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/keyfile"
	"github.com/SH-Hong1/secret-externalizer/internal/opengrep"
	"github.com/SH-Hong1/secret-externalizer/internal/ruleset"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
)

// version identifies the build. It is set at link time with -ldflags -X and
// is never assigned while the program runs.
var version = "dev"

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdout, os.Stderr); err != nil {
		// Standard error is the last place left to report to, so a failed
		// write has nowhere to go.
		_, _ = fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}

// run parses args and executes the program. It takes its inputs and outputs
// as parameters so tests can call it without touching process state.
//
// A flag error is returned and not printed, so the caller reports it once.
// Asking for help prints the usage to stdout and is not an error. Otherwise it
// reads the configuration, prepares the OpenBao client and the scanners,
// reads the rules version, opens and migrates the database, and runs queued
// scans until SIGINT, SIGTERM or cancellation of ctx.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	return runWith(ctx, args, getenv, stdout, stderr, nil, newScanDeps)
}

// scanDeps are what a scan runs with besides the database.
type scanDeps struct {
	fingerprints scan.Fingerprinter
	originals    scan.Originals
	scanners     []scan.Scanner
}

// scannerPath is the PATH the scanners run with.
const scannerPath = "/usr/local/bin:/usr/bin:/bin"

// newScanDeps returns the OpenBao client and the three scanners that cfg
// describes. The client does not contact OpenBao until it is first used.
func newScanDeps(cfg config) (scanDeps, error) {
	client, err := bao.New(cfg.baoAddr, cfg.baoCreds, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		return scanDeps{}, fmt.Errorf("preparing openbao client: %w", err)
	}
	env := scanexec.Env{Path: scannerPath, Home: scanHome(cfg), TmpDir: scanTmp(cfg)}
	return scanDeps{
		fingerprints: client,
		originals:    client,
		scanners: []scan.Scanner{
			betterleaks.New(cfg.betterleaksBin, filepath.Join(cfg.rulesDir, "betterleaks.toml"), env),
			// Project excludes are applied by the runner, which drops results
			// for files it did not list.
			opengrep.New(cfg.opengrepBin, filepath.Join(cfg.rulesDir, "opengrep"), nil, env),
			keyfile.New(),
		},
	}, nil
}

// The folders under cfg.scanTempDir: the scanners' home and temporary
// folders, and the folder that holds each scan's copies.
func scanHome(cfg config) string   { return filepath.Join(cfg.scanTempDir, "home") }
func scanTmp(cfg config) string    { return filepath.Join(cfg.scanTempDir, "tmp") }
func scanCopies(cfg config) string { return filepath.Join(cfg.scanTempDir, "copies") }

// runWith is run with a function that adjusts the configuration after it is
// loaded, so tests can shorten the queue intervals, and a function that
// builds the fingerprinter, the originals store, and the scanners, so tests
// can replace them. A nil tune changes nothing.
func runWith(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer,
	tune func(*config), deps func(config) (scanDeps, error),
) error {
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
		if _, err := fmt.Fprintf(stdout, "secret-externalizer worker %s\n", version); err != nil {
			return fmt.Errorf("writing version: %w", err)
		}
		return nil
	}

	cfg, err := loadConfig(getenv)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if tune != nil {
		tune(&cfg)
	}
	sd, err := deps(cfg)
	if err != nil {
		return err
	}
	rulesVersion, err := ruleset.Version(cfg.rulesDir)
	if err != nil {
		return fmt.Errorf("reading rules version: %w", err)
	}
	for _, d := range []string{scanHome(cfg), scanTmp(cfg), scanCopies(cfg)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("making scan folder: %w", err)
		}
	}
	workerID, err := newWorkerID()
	if err != nil {
		return fmt.Errorf("naming the worker: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(stderr, nil))

	database, err := db.Open(ctx, cfg.dsn)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	logger.InfoContext(ctx, "worker started", "worker_id", workerID, "rules_version", rulesVersion, "config", cfg)
	runner := scan.NewRunner(logger, database, sd.fingerprints, sd.originals, sd.scanners, scan.Options{
		WorkerID:     workerID,
		Root:         cfg.root,
		RulesVersion: rulesVersion,
		TempDir:      scanCopies(cfg),
		Git:          cfg.gitBin,
		MaxFileBytes: scan.DefaultMaxFileBytes,
		TimeLimit:    scan.DefaultTimeLimit,
	})
	opts := job.Options{WorkerID: workerID, Poll: cfg.poll, Lease: cfg.lease, Heartbeat: cfg.heartbeat}
	if err := job.Run(ctx, logger, database, runner.Handle, opts); err != nil {
		return fmt.Errorf("running jobs: %w", err)
	}
	logger.InfoContext(ctx, "worker stopped", "worker_id", workerID)
	return nil
}

// newWorkerID returns an ID for this process: the host name and a random
// suffix. The queue tells workers apart by this ID alone, so two processes
// on one host must not share it.
func newWorkerID() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("reading host name: %w", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return host + "-" + hex.EncodeToString(suffix), nil
}
