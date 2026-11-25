// Command worker runs the scans, apply checks, and applies that the web
// program queues, or only the kinds that WORKER_JOBS names.
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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
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
// reads the configuration, prepares the OpenBao client and, when it scans,
// the scanners and the rules version, opens and migrates the database, and
// runs the queued jobs of the kinds it was configured for until SIGINT,
// SIGTERM or cancellation of ctx.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	return runWith(ctx, args, getenv, stdout, stderr, nil, newDeps)
}

// workerDeps are what scans and applies run with besides the database.
type workerDeps struct {
	fingerprints scan.Fingerprinter
	originals    scan.Originals
	vault        apply.Vault
	scanners     []scan.Scanner
}

// scannerPath is the PATH the scanners run with.
const scannerPath = "/usr/local/bin:/usr/bin:/bin"

// newDeps returns the OpenBao client, as the fingerprinter, the originals
// store and the vault, and, when the worker scans, the three scanners that
// cfg describes. The client does not contact OpenBao until it is first used.
func newDeps(cfg config) (workerDeps, error) {
	hc, err := bao.NewHTTPClient(cfg.baoCA, 10*time.Second)
	if err != nil {
		return workerDeps{}, fmt.Errorf("preparing openbao TLS: %w", err)
	}
	client, err := bao.New(cfg.baoAddr, cfg.baoCreds, hc)
	if err != nil {
		return workerDeps{}, fmt.Errorf("preparing openbao client: %w", err)
	}
	deps := workerDeps{fingerprints: client, originals: client, vault: client}
	if !cfg.jobs.scans() {
		return deps, nil
	}
	env := scanexec.Env{Path: scannerPath, Home: scanHome(cfg), TmpDir: scanTmp(cfg)}
	deps.scanners = []scan.Scanner{
		betterleaks.New(cfg.betterleaksBin, filepath.Join(cfg.rulesDir, "betterleaks.toml"), env),
		opengrep.New(cfg.opengrepBin, filepath.Join(cfg.rulesDir, "opengrep"), env),
		keyfile.New(),
	}
	return deps, nil
}

// The folders under cfg.scanTempDir: the scanners' home and temporary
// folders, and the folder that holds each scan's copies.
func scanHome(cfg config) string   { return filepath.Join(cfg.scanTempDir, "home") }
func scanTmp(cfg config) string    { return filepath.Join(cfg.scanTempDir, "tmp") }
func scanCopies(cfg config) string { return filepath.Join(cfg.scanTempDir, "copies") }

// runWith is run with a function that adjusts the configuration after it is
// loaded, so tests can shorten the queue intervals, and a function that
// builds the fingerprinter, the originals store, the vault of applies, and
// the scanners, so tests can replace them. A nil tune changes nothing.
func runWith(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer,
	tune func(*config), deps func(config) (workerDeps, error),
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
	var rulesVersion string
	if cfg.jobs.scans() {
		if rulesVersion, err = ruleset.Version(cfg.rulesDir); err != nil {
			return fmt.Errorf("reading rules version: %w", err)
		}
		for _, d := range []string{scanHome(cfg), scanTmp(cfg), scanCopies(cfg)} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				return fmt.Errorf("making scan folder: %w", err)
			}
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
	if !cfg.baoTLS {
		logger.WarnContext(ctx, "openbao connection is not encrypted", "bao_addr", cfg.baoAddr)
	}
	handlers := map[job.Kind]job.Handler{}
	if cfg.jobs.scans() {
		runner := scan.NewRunner(logger, database, sd.fingerprints, sd.originals, sd.scanners, scan.Options{
			WorkerID:      workerID,
			Root:          cfg.root,
			RulesVersion:  rulesVersion,
			TempDir:       scanCopies(cfg),
			ScannerTmpDir: scanTmp(cfg),
			Git:           cfg.gitBin,
			MaxFileBytes:  scan.DefaultMaxFileBytes,
			TimeLimit:     scan.DefaultTimeLimit,
		})
		handlers[job.KindScan] = runner.Handle
	}
	if cfg.jobs.applies() {
		// Scans read cfg.root, the read-only mount. Only an apply the user
		// confirmed changes files, through cfg.applyRoot.
		applier := apply.NewRunner(logger, database, sd.vault, apply.Options{
			WorkerID:     workerID,
			Root:         cfg.applyRoot,
			Git:          cfg.gitBin,
			MaxFileBytes: scan.DefaultMaxFileBytes,
		})
		handlers[job.KindApplyCheck] = applier.Check
		handlers[job.KindApply] = applier.Apply
	}
	opts := job.Options{WorkerID: workerID, Kinds: cfg.jobs.kinds(), Poll: cfg.poll, Lease: cfg.lease, Heartbeat: cfg.heartbeat}
	handle := job.Dispatch(handlers)
	if err := job.Run(ctx, logger, database, handle, opts); err != nil {
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
