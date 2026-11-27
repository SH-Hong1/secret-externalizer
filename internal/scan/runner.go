package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// The defaults of Options.
const (
	// DefaultMaxFileBytes is the size above which a file is not scanned as
	// text, 20 MiB.
	DefaultMaxFileBytes = 20 << 20
	// DefaultTimeLimit is how long one scan may take, 30 minutes.
	DefaultTimeLimit = 30 * time.Minute
)

// Store is the part of the database the runner uses.
type Store interface {
	StartScan(ctx context.Context, id ID) error
	Scan(ctx context.Context, id ID) (Scan, error)
	Project(ctx context.Context, id project.ID) (project.Project, error)
	FailScan(ctx context.Context, jobID int64, workerID string, id ID, reason string) error
	SaveResult(ctx context.Context, jobID int64, workerID string, id ID, r Result) error
	SettledFingerprints(ctx context.Context, id project.ID) ([]string, error)
	HasNewerScan(ctx context.Context, projectID project.ID, id ID) (bool, error)
}

// Fingerprinter computes the fingerprints of values, in the order of values.
type Fingerprinter interface {
	Fingerprints(ctx context.Context, values [][]byte) ([]string, error)
}

// Originals keeps the original values of findings until a verdict is made
// on them.
type Originals interface {
	PutPending(ctx context.Context, projectID int64, fingerprint string, value []byte) error
	ListPending(ctx context.Context, projectID int64) ([]string, error)
	DeletePending(ctx context.Context, projectID int64, fingerprint string) error
}

// Options are the settings of a Runner.
type Options struct {
	// WorkerID is the job holder the runner records results as.
	WorkerID string
	// Root is the workspace root that project paths are relative to.
	Root string
	// RulesVersion is the version of the rules the scanners run with.
	RulesVersion string
	// TempDir is the absolute path of the folder under which each scan gets
	// a folder of UTF-8 copies of its EUC-KR files. It should be on a
	// temporary file system. A scan fails when it is not absolute.
	TempDir string
	// ScannerTmpDir is the absolute path of the scanners' temporary folder
	// (their TMPDIR). Empty means the runner leaves it alone.
	ScannerTmpDir string
	// Git is the path of the git executable that reads a project's commit
	// hash. Empty means the hash is not read.
	Git string
	// MaxFileBytes is the size above which a file is not scanned as text.
	// Zero means DefaultMaxFileBytes.
	MaxFileBytes int64
	// TimeLimit bounds a whole scan. Zero means DefaultTimeLimit.
	TimeLimit time.Duration
}

// The reasons a scan is failed with. They are shown on the scan page, so
// they never contain text from an error.
const (
	reasonOutsideRoot  = "project path points outside the workspace root"
	reasonNotDirectory = "project path is not a directory in the workspace"
	reasonOther        = "scan could not finish; see the worker log"
	reasonIgnoreFile   = "project has a scanner ignore file (.gitleaksignore or .betterleaksignore); remove it and scan again"
	reasonSecretStore  = "the secret store could not be reached; see the worker log"
	reasonTimeLimit    = "scan took longer than the time limit"
	// reasonTool is completed with the name of the scanner, which is fixed
	// by the code.
	reasonTool = "%s could not finish; see the worker log"
)

var (
	// errIgnoreFile means the project folder holds a file that makes
	// betterleaks hide results.
	errIgnoreFile = errors.New("the project folder holds a scanner ignore file")
	// errSecretStore marks a failure to fingerprint or park values.
	errSecretStore = errors.New("using the secret store")
	// errTimeLimit is the cause of the context of a scan that ran out of
	// time.
	errTimeLimit = errors.New("the scan time limit has passed")
)

// toolError is the failure of one scanner.
type toolError struct {
	tool string
	err  error
}

func (e *toolError) Error() string { return e.tool + ": " + e.err.Error() }

func (e *toolError) Unwrap() error { return e.err }

// Runner runs the scan of a claimed job. It keeps no value it finds once
// Handle has returned.
type Runner struct {
	logger       *slog.Logger
	store        Store
	fingerprints Fingerprinter
	originals    Originals
	scanners     []Scanner
	opts         Options
}

// NewRunner returns a Runner that runs scanners over the projects in store,
// computes fingerprints with fp, and keeps the originals of secrets in
// originals.
func NewRunner(logger *slog.Logger, store Store, fp Fingerprinter, originals Originals, scanners []Scanner, opts Options) *Runner {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	if opts.TimeLimit <= 0 {
		opts.TimeLimit = DefaultTimeLimit
	}
	return &Runner{
		logger: logger, store: store, fingerprints: fp, originals: originals,
		scanners: slices.Clone(scanners), opts: opts,
	}
}

// Handle runs the scan of j and records how it ended. A scan that cannot be
// carried out, or that runs past the time limit, is failed with a fixed
// reason and Handle returns nil. Handle returns an error, leaving the scan
// as it is, when ctx ends, when the lease on the job is lost, or when the
// failure itself cannot be recorded. The folder of copies is removed on
// every path.
func (r *Runner) Handle(ctx context.Context, j job.Job) error {
	id := ID(j.ScanID)
	limited, cancel := context.WithTimeoutCause(ctx, r.opts.TimeLimit, errTimeLimit)
	defer cancel()
	r.removeStale(ctx, id)
	copies := filepath.Join(r.opts.TempDir, strconv.FormatInt(int64(id), 10))
	defer r.removeCopies(ctx, id, copies)

	err := r.run(limited, j, id, copies)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("running scan %d: %w", id, ctx.Err())
	case errors.Is(err, job.ErrLeaseLost):
		return fmt.Errorf("running scan %d: %w", id, err)
	}

	reason := reasonFor(err)
	if errors.Is(context.Cause(limited), errTimeLimit) {
		reason = reasonTimeLimit
	}
	r.logger.ErrorContext(ctx, "scan failed", "scan_id", id, "job_id", j.ID, "error", err.Error())
	if err := r.store.FailScan(ctx, j.ID, r.opts.WorkerID, id, reason); err != nil {
		return fmt.Errorf("failing scan %d: %w", id, err)
	}
	return nil
}

// run carries out the scan, saves its result, and then removes pending
// originals that the scan no longer found or that it does not keep. copies
// is the folder for the UTF-8 copies of EUC-KR files.
func (r *Runner) run(ctx context.Context, j job.Job, id ID, copies string) error {
	if !filepath.IsAbs(copies) {
		return fmt.Errorf("the folder for copies %q is not an absolute path", copies)
	}
	if err := r.store.StartScan(ctx, id); err != nil {
		return fmt.Errorf("starting scan: %w", err)
	}
	sc, err := r.store.Scan(ctx, id)
	if err != nil {
		return fmt.Errorf("reading scan: %w", err)
	}
	p, err := r.store.Project(ctx, sc.ProjectID)
	if err != nil {
		return fmt.Errorf("reading project: %w", err)
	}
	dir, err := project.Resolve(r.opts.Root, p.Path)
	if err != nil {
		return fmt.Errorf("resolving project path: %w", err)
	}
	if err := checkIgnoreFiles(dir); err != nil {
		return err
	}

	res := Result{RulesVersion: r.opts.RulesVersion, CommitHash: commitHash(ctx, r.opts.Git, r.opts.Root, dir)}
	files, err := listFiles(ctx, dir, p.Excludes, r.opts.MaxFileBytes, copies)
	if err != nil {
		return err
	}
	res.Files, res.Skipped = len(files.files)+files.unstorable, files.skipped

	d, err := r.detect(ctx, dir, copies, p.Excludes, files)
	if err != nil {
		return err
	}
	res.ToolVersions = d.versions
	res.SkippedFiles = keptPaths(files.skippedPaths)
	res.ScannerErrors = reportedErrors(d.errors)
	placed, notPlaced, err := locate(ctx, dir, files, d.raws, r.opts.MaxFileBytes)
	if err != nil {
		return err
	}
	dropContext(placed, files, d.dropped)
	placed = splitConnections(placed)
	res.Dropped = d.dropped.count + notPlaced

	spans := maskedSpans(placed, newValueIndex(maskPatterns(placed, d.raws)))
	fingerprints, err := r.fingerprint(ctx, placed, maskedKeys(placed, spans))
	if err != nil {
		return err
	}
	settled := r.settled(ctx, id, p.ID)
	findings, originals := merge(placed, fingerprints, spans)
	dropSettled(findings, originals, settled)
	if err := r.park(ctx, int64(p.ID), originals); err != nil {
		return err
	}
	res.Findings = findings
	keep := make(map[string]bool, len(originals))
	for fp := range originals {
		keep[fp] = true
	}
	forget(placed)

	if err := r.store.SaveResult(ctx, j.ID, r.opts.WorkerID, id, res); err != nil {
		return fmt.Errorf("saving scan result: %w", err)
	}
	r.logSummary(ctx, j, id, res, d, files.skippedPaths)
	r.removeStalePending(ctx, id, p.ID, keep)
	return nil
}

// keptPaths returns the first MaxSkippedFiles of paths in path order,
// leaving out the paths that printablePath rejects. paths is not changed.
func keptPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if printablePath(p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out[:min(len(out), MaxSkippedFiles)]
}

// printablePath reports whether p is valid UTF-8 without control characters
// (tab included), format characters such as a direction override or a zero
// width space, and line or paragraph separators. Such a path would be shown
// in the list of skipped files differently from how it is named.
func printablePath(p string) bool {
	if !utf8.ValidString(p) {
		return false
	}
	return !strings.ContainsFunc(p, func(r rune) bool {
		return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
	})
}

// reportedErrors returns the entries of errs that are not zero, or nil.
func reportedErrors(errs map[string]int) map[string]int {
	var out map[string]int
	for tool, n := range errs {
		if n == 0 {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[tool] = n
	}
	return out
}

// maxLoggedPaths is the number of skipped paths logged for a scan.
const maxLoggedPaths = 50

// logSummary logs what the scan of j found and what it could not look at:
// the counts of res, the errors each scanner reported for files it could
// analyse only in part, the number of scanner results in files that could
// not be read as text, and the first maxLoggedPaths skipped paths with
// their total. A path goes through finding.Printable first. No value,
// matched text or surrounding line is logged.
func (r *Runner) logSummary(ctx context.Context, j job.Job, id ID, res Result, d detection, skipped []string) {
	errs := make([]any, 0, len(d.versions))
	for _, tool := range slices.Sorted(maps.Keys(d.versions)) {
		errs = append(errs, slog.Int(tool, d.errors[tool]))
	}
	r.logger.InfoContext(ctx, "scan finished", "scan_id", id, "job_id", j.ID,
		"files", res.Files, "skipped", res.Skipped, "dropped", res.Dropped, "findings", len(res.Findings),
		"results_in_skipped_files", d.inSkipped, slog.Group("scanner_errors", errs...))
	if len(skipped) == 0 {
		return
	}
	paths := make([]string, 0, min(len(skipped), maxLoggedPaths))
	for _, p := range skipped[:min(len(skipped), maxLoggedPaths)] {
		paths = append(paths, finding.Printable(p))
	}
	r.logger.InfoContext(ctx, "scan skipped files", "scan_id", id, "job_id", j.ID, "total", len(skipped), "paths", paths)
}

// fingerprint returns the fingerprint of every distinct value in placed and
// of every masked key text in keys, keyed by the text, from one batch.
func (r *Runner) fingerprint(ctx context.Context, placed []*placedFile, keys [][]byte) (map[string]string, error) {
	index := map[string]int{}
	var values [][]byte
	add := func(v []byte) {
		if _, seen := index[string(v)]; !seen {
			index[string(v)] = len(values)
			values = append(values, v)
		}
	}
	for _, pf := range placed {
		for _, s := range pf.spots {
			add(s.value)
		}
	}
	for _, k := range keys {
		add(k)
	}
	if len(values) == 0 {
		return map[string]string{}, nil
	}
	fps, err := r.fingerprints.Fingerprints(ctx, values)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errSecretStore, err)
	}
	if len(fps) != len(values) {
		return nil, fmt.Errorf("%w: got %d fingerprints for %d values", errSecretStore, len(fps), len(values))
	}
	out := make(map[string]string, len(values))
	for v, i := range index {
		out[v] = fps[i]
	}
	return out, nil
}

// settled returns the fingerprints of the project whose bundles on the
// triage screen are all judged false positives, as a set. Their originals
// are not kept. A failure
// to read them is logged and gives none, so the scan keeps every original
// as it would without verdicts.
func (r *Runner) settled(ctx context.Context, id ID, projectID project.ID) map[string]bool {
	fps, err := r.store.SettledFingerprints(ctx, projectID)
	if err != nil {
		r.logger.WarnContext(ctx, "reading settled fingerprints failed", "scan_id", id, "project_id", projectID, "error", err.Error())
		return nil
	}
	out := make(map[string]bool, len(fps))
	for _, fp := range fps {
		out[fp] = true
	}
	return out
}

// dropSettled marks the findings whose fingerprint is in settled as having
// no original and removes their originals from originals. Without a kept
// original, removeStalePending deletes the one an earlier scan parked.
func dropSettled(findings []finding.Finding, originals map[string][]byte, settled map[string]bool) {
	for i := range findings {
		if settled[findings[i].Fingerprint] {
			findings[i].HasOriginal = false
			delete(originals, findings[i].Fingerprint)
		}
	}
}

// park stores each original once per fingerprint, in fingerprint order.
func (r *Runner) park(ctx context.Context, projectID int64, originals map[string][]byte) error {
	for _, fp := range slices.Sorted(maps.Keys(originals)) {
		if err := r.originals.PutPending(ctx, projectID, fp, originals[fp]); err != nil {
			return fmt.Errorf("%w: %w", errSecretStore, err)
		}
	}
	return nil
}

// removeStalePending deletes the pending originals of the project whose
// fingerprints are not in keep. It runs only after the result is saved. A
// newer scan of the project may have parked values since, so before each
// delete it checks that no newer scan exists and stops at the first one that
// does. A failure is logged and leaves the rest for the next scan.
func (r *Runner) removeStalePending(ctx context.Context, id ID, projectID project.ID, keep map[string]bool) {
	fps, err := r.originals.ListPending(ctx, int64(projectID))
	if err != nil {
		r.logger.WarnContext(ctx, "removing stale pending values failed", "scan_id", id, "project_id", projectID, "error", err.Error())
		return
	}
	for _, fp := range fps {
		if keep[fp] {
			continue
		}
		newer, err := r.store.HasNewerScan(ctx, projectID, id)
		if err != nil {
			r.logger.WarnContext(ctx, "removing stale pending values failed", "scan_id", id, "project_id", projectID, "error", err.Error())
			return
		}
		if newer {
			return
		}
		if err := r.originals.DeletePending(ctx, int64(projectID), fp); err != nil {
			r.logger.WarnContext(ctx, "removing stale pending values failed", "scan_id", id, "project_id", projectID, "error", err.Error())
			return
		}
	}
}

// removeCopies removes the folder of copies of the scan. It runs also after
// ctx has ended. A relative path, which run refuses, is never removed.
func (r *Runner) removeCopies(ctx context.Context, id ID, copies string) {
	if !filepath.IsAbs(copies) {
		return
	}
	if err := os.RemoveAll(copies); err != nil {
		r.logger.ErrorContext(context.WithoutCancel(ctx), "removing the folder of copies failed", "scan_id", id, "error", err.Error())
	}
}

// removeStale removes, from the folder of copies and the scanners'
// temporary folder, every entry that has not changed for twice the time
// limit. Such an entry was left by a scan that ended without removing it,
// as when the worker was killed. A running scan, on this worker or another
// one that shares the folders, changes its entries within the time limit,
// so they stay. A symbolic link is removed, not followed. A failure is
// logged and the scan goes on.
func (r *Runner) removeStale(ctx context.Context, id ID) {
	cutoff := time.Now().Add(-2 * r.opts.TimeLimit)
	for _, dir := range []string{r.opts.TempDir, r.opts.ScannerTmpDir} {
		if !filepath.IsAbs(dir) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			r.logger.WarnContext(ctx, "listing temporary folder failed", "scan_id", id, "error", err.Error())
			continue
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				// Another scan removed it after the folder was listed.
				continue
			}
			if err != nil {
				r.logger.WarnContext(ctx, "reading a temporary entry failed", "scan_id", id, "error", err.Error())
				continue
			}
			if !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				r.logger.WarnContext(ctx, "removing a stale temporary entry failed", "scan_id", id, "error", err.Error())
			}
		}
	}
}

// reasonFor returns the failure reason shown for err.
func reasonFor(err error) string {
	var te *toolError
	switch {
	case errors.Is(err, project.ErrOutsideRoot):
		return reasonOutsideRoot
	case errors.Is(err, project.ErrNotDirectory):
		return reasonNotDirectory
	case errors.Is(err, errIgnoreFile):
		return reasonIgnoreFile
	case errors.Is(err, errSecretStore):
		return reasonSecretStore
	case errors.As(err, &te):
		return fmt.Sprintf(reasonTool, te.tool)
	}
	return reasonOther
}
