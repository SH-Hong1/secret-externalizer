package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/manifest"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/rewrite"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
	"github.com/SH-Hong1/secret-externalizer/internal/worktree"
)

// Store is the part of the database the runner uses: ApplyInput,
// StartApply, OpenFiles, ResolveFile, WrittenFiles, SaveCheck, BeginFile,
// EndFile, MarkInManifest, SettledFingerprints, FinishApply, FailApply,
// ApplyItems, ApplyRun.
type Store interface {
	ApplyInput(ctx context.Context, id ID) (Input, error)
	StartApply(ctx context.Context, id ID, phase job.Kind) error
	OpenFiles(ctx context.Context, projectID project.ID) ([]OpenFile, error)
	ResolveFile(ctx context.Context, f OpenFile, done bool) error
	WrittenFiles(ctx context.Context, projectID project.ID) (map[string][]Written, error)
	SaveCheck(ctx context.Context, jobID int64, workerID string, id ID, blockers []Blocker,
		dirty, irreversible []string, items []Item) error
	BeginFile(ctx context.Context, id ID, file, beforeHash, afterHash, tempName string, items []Item) error
	EndFile(ctx context.Context, id ID, file, afterHash, tempName string) error
	MarkInManifest(ctx context.Context, projectID project.ID) error
	SettledFingerprints(ctx context.Context, projectID project.ID) ([]string, error)
	FinishApply(ctx context.Context, jobID int64, workerID string, id ID, items []Item) error
	FailApply(ctx context.Context, jobID int64, workerID string, id ID, reason string, blockers []Blocker, items []Item) error
	ApplyItems(ctx context.Context, id ID) ([]Item, error)
	ApplyRun(ctx context.Context, id ID) (Run, error)
}

// Vault computes fingerprints, keeps externalized values, and drops
// pending originals.
type Vault interface {
	Fingerprints(ctx context.Context, values [][]byte) ([]string, error)
	ReadStored(ctx context.Context, projectID int64, key string) (bao.Stored, error)
	PutStored(ctx context.Context, projectID int64, key string, v bao.StoredValue, cas int) (int, error)
	DeletePending(ctx context.Context, projectID int64, fingerprint string) error
}

// The defaults of Options.
const (
	// DefaultMaxStoredBytes is the largest value an apply stores, 16 MiB.
	// Its base64 form, about 21.4 MiB, goes alone in one fingerprint request
	// and in one write, under the 32 MiB that OpenBao accepts in a request.
	DefaultMaxStoredBytes = 16 << 20
	// DefaultCheckLimit is how long one check may take.
	DefaultCheckLimit = 10 * time.Minute
	// DefaultApplyLimit is how long one apply may take.
	DefaultApplyLimit = 30 * time.Minute
)

// The reasons a check or an apply fails with. They are shown on the page,
// so they never hold text from an error.
const (
	FailureDirty         = "the work tree has uncommitted changes"
	FailureNotRepository = "the project folder is not a git work tree"
	FailureUnsafeConfig  = "the repository configuration is not safe to read"
	FailureManifest      = "the existing manifest could not be read"
	FailurePlanChanged   = "the verdicts changed after the check"
	FailureSecretStore   = "the secret store could not be reached; see the worker log"
	FailureTimeLimit     = "apply took longer than the time limit"
	FailureOther         = "apply could not finish; see the worker log"
	FailureManifestWrite = "the manifest could not be written; see the worker log"
)

// Options are the settings of a Runner.
type Options struct {
	// WorkerID is the job holder the runner records results as.
	WorkerID string
	// Root is the read-write workspace root that project paths are relative
	// to. Empty blocks every check with workspace-read-only.
	Root string
	// Git is the path of the git executable. Empty blocks every check with
	// git-missing.
	Git string
	// MaxFileBytes is the size above which a file is not read. Zero means
	// scan.DefaultMaxFileBytes.
	MaxFileBytes int64
	// MaxStoredBytes is the largest value stored. Zero means
	// DefaultMaxStoredBytes.
	MaxStoredBytes int
	// CheckLimit bounds a check. Zero means DefaultCheckLimit.
	CheckLimit time.Duration
	// ApplyLimit bounds an apply. Zero means DefaultApplyLimit.
	ApplyLimit time.Duration
}

var (
	// errPlanChanged means the plan is not the one the check was requested
	// for.
	errPlanChanged = errors.New("the plan differs from the one the run was requested for")
	// errSecretStore marks a failure to use OpenBao.
	errSecretStore = errors.New("using the secret store")
	// errTimeLimit is the cause of the context of a run that ran out of
	// time.
	errTimeLimit = errors.New("the time limit of the run has passed")
	// errManifestWrite marks a failure to write the manifest or to record
	// that it was written.
	errManifestWrite = errors.New("writing the manifest")
)

// projectDir is a project folder as the runner reads and changes it.
// *rewrite.Dir is the implementation; tests wrap it to make a step fail.
type projectDir interface {
	Read(rel string, maxBytes int64) ([]byte, rewrite.Seen, error)
	TempName(rel string) (string, error)
	Write(rel string, content []byte, was rewrite.Seen, tmp string) error
	Create(rel string, content []byte, tmp string) error
	Remove(rel string, was rewrite.Seen) error
	RemoveTemp(tmp string) error
	Close() error
}

// Runner carries out the checks and applies of claimed jobs. It keeps no
// value it reads once a handler has returned.
type Runner struct {
	logger *slog.Logger
	store  Store
	vault  Vault
	opts   Options
	// wrapDir, when set, replaces each project folder the runner opens.
	// Only tests set it.
	wrapDir func(projectDir) projectDir
}

// NewRunner returns a Runner that records runs in store and uses vault for
// fingerprints and stored values.
func NewRunner(logger *slog.Logger, store Store, vault Vault, opts Options) *Runner {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = scan.DefaultMaxFileBytes
	}
	if opts.MaxStoredBytes <= 0 {
		opts.MaxStoredBytes = DefaultMaxStoredBytes
	}
	if opts.CheckLimit <= 0 {
		opts.CheckLimit = DefaultCheckLimit
	}
	if opts.ApplyLimit <= 0 {
		opts.ApplyLimit = DefaultApplyLimit
	}
	return &Runner{logger: logger, store: store, vault: vault, opts: opts}
}

// Check handles an apply-check job: it changes nothing in the project
// folder or the vault and records what an apply would do. A condition that
// keeps the project from being applied is recorded as a blocker. A check
// that cannot be carried out, or that runs past the time limit, fails the
// run with a fixed reason and Check returns nil. Check returns an error,
// leaving the run as it is, when ctx ends, when the lease on the job is
// lost, or when the failure itself cannot be recorded.
func (r *Runner) Check(ctx context.Context, j job.Job) error {
	limited, cancel := context.WithTimeoutCause(ctx, r.opts.CheckLimit, errTimeLimit)
	defer cancel()
	return r.end(ctx, limited, j, r.check(limited, j), nil)
}

// check carries out the check of j.
func (r *Runner) check(ctx context.Context, j job.Job) error {
	p, err := r.prepare(ctx, j, job.KindApplyCheck)
	if err != nil {
		return err
	}
	defer p.close()
	items := p.items()
	if err := r.store.SaveCheck(ctx, j.ID, r.opts.WorkerID, ID(j.ApplyID), p.blockers, p.dirty, p.irreversible, items); err != nil {
		return fmt.Errorf("saving the check: %w", err)
	}
	planned := 0
	for _, it := range items {
		if it.Status == StatusPlanned {
			planned++
		}
	}
	r.logger.InfoContext(ctx, "apply check finished", "apply_id", j.ApplyID, "job_id", j.ID,
		"blockers", p.blockers, "planned", planned, "skipped", len(items)-planned, "dirty", len(p.dirty),
		"irreversible_files", len(p.irreversible))
	return nil
}

// end returns what a handler of j returns once its work ended with err
// under limited, a context derived from ctx. A run that could not be carried
// out is failed with a fixed reason and outcome, the outcomes an apply
// found before it stopped, and its cause logged once.
func (r *Runner) end(ctx, limited context.Context, j job.Job, err error, outcome []Item) error {
	id := ID(j.ApplyID)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("running %s of apply %d: %w", j.Kind, id, ctx.Err())
	case errors.Is(err, job.ErrLeaseLost):
		return fmt.Errorf("running %s of apply %d: %w", j.Kind, id, err)
	}
	reason := failureFor(err)
	if errors.Is(context.Cause(limited), errTimeLimit) {
		reason = FailureTimeLimit
	}
	r.logger.ErrorContext(ctx, "apply run failed", "apply_id", id, "job_id", j.ID, "kind", string(j.Kind),
		"error", err.Error())
	if err := r.store.FailApply(ctx, j.ID, r.opts.WorkerID, id, reason, nil, outcome); err != nil {
		return fmt.Errorf("failing apply %d: %w", id, err)
	}
	return nil
}

// failureFor returns the failure reason shown for err.
func failureFor(err error) string {
	switch {
	case errors.Is(err, errPlanChanged):
		return FailurePlanChanged
	case errors.Is(err, errSecretStore):
		return FailureSecretStore
	case errors.Is(err, errManifestWrite):
		return FailureManifestWrite
	}
	return FailureOther
}

// redactedText is what fmt, slog and encoding/json print for a value.
const redactedText = "[redacted]"

// value is bytes read from the source: a secret, customer data or a whole
// key file. fmt, slog and encoding/json print a fixed text.
type value []byte

// String returns a fixed text that holds no value.
func (value) String() string { return redactedText }

// GoString returns a fixed text that holds no value.
func (value) GoString() string { return redactedText }

// Format prints the form String returns for every fmt verb.
func (value) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redactedText)) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns a fixed text that holds no value.
func (value) LogValue() slog.Value { return slog.StringValue(redactedText) }

// MarshalJSON returns the fixed text as a JSON string.
func (value) MarshalJSON() ([]byte, error) { return json.Marshal(redactedText) }

// target is a target of the plan and what the run found at its place.
type target struct {
	Target
	// value is what the place holds, a copy that shares no bytes with the
	// file's buffers or another value, so zeroing it changes nothing else.
	// It is nil until read.
	value value
	// reason is why the target is skipped or failed, empty while it is
	// planned or once it is applied.
	reason Reason
	// status is set once an apply has applied the target or failed at it;
	// it is empty while the target is planned or skipped for reason.
	status Status
	// storedHash is, for a whole-file target, the hash of the bytes the
	// apply stored and confirmed.
	storedHash string
}

// sourceFile is a file the plan changes, as the run read it. It is only ever
// used through a pointer, which is the form whose String, Format and
// LogValue hide the file's bytes; the type is unexported so that no other
// package can copy it.
type sourceFile struct {
	rel     string
	raw     value         // the bytes on disk, nil until read
	seen    rewrite.Seen  // what the file looked like when read
	text    textfile.File // the decoded text, zero until decoded
	decoded bool          // text was decoded, or decodeErr says why not
	// decodeErr is why the file could not be decoded as text.
	decodeErr error
	targets   []*target
}

// sourceText is what fmt and slog print for a sourceFile.
const sourceText = "apply.sourceFile{[redacted]}"

// String returns the file's path and a fixed text; the file's bytes can
// hold values, so they are never printed.
func (f *sourceFile) String() string { return sourceText + " " + f.rel }

// Format prints the form String returns for every fmt verb.
func (f *sourceFile) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte(f.String())) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns the file's path and the number of its targets.
func (f *sourceFile) LogValue() slog.Value {
	return slog.GroupValue(slog.String("file", f.rel), slog.Int("targets", len(f.targets)))
}

// skipAll skips every planned target of f for reason.
func (f *sourceFile) skipAll(reason Reason) {
	for _, t := range f.targets {
		if t.reason == "" {
			t.reason = reason
		}
	}
}

// decode returns the text of f, decoding it once.
func (f *sourceFile) decode() (textfile.File, error) {
	if !f.decoded {
		f.text, f.decodeErr = textfile.Decode(f.raw)
		f.decoded = true
	}
	return f.text, f.decodeErr
}

// prepared is a run after the steps a check and an apply share: the
// records reconciled with the disk, the plan, the conditions, and each
// target's value read again and checked. close zeroes every value. Like
// sourceFile, it is only ever used through a pointer, the form that hides
// what it holds when printed.
type prepared struct {
	in   Input
	plan Plan
	// folder is the resolved project folder and dir the folder opened;
	// both are unset when the folder could not be opened.
	folder string
	dir    projectDir
	// state is what git reports, valid when inspected is set.
	state     worktree.State
	inspected bool
	blockers  []Blocker
	// dirty holds the uncommitted changes the project's applies did not
	// make.
	dirty []string
	// manifest is the manifest in the folder; hasManifest says whether there
	// is one, and manifestSeen what it looked like.
	manifest     manifest.Manifest
	hasManifest  bool
	manifestSeen rewrite.Seen
	files        []*sourceFile
	// irreversible holds, for a check, the untracked files with customer
	// data to redact.
	irreversible []string
}

// preparedText is what fmt prints for a prepared run.
const preparedText = "apply.prepared{[redacted]}"

// String returns a fixed text; a prepared run holds values read from the
// source, so they are never printed.
func (p *prepared) String() string { return preparedText }

// Format prints the form String returns for every fmt verb.
func (p *prepared) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte(preparedText)) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns the run's ID, its blockers and the number of files.
func (p *prepared) LogValue() slog.Value {
	return slog.GroupValue(slog.Int64("apply_id", int64(p.in.Run.ID)), slog.Any("blockers", p.blockers),
		slog.Int("files", len(p.files)))
}

// close zeroes every value and file buffer of p and releases the folder.
func (p *prepared) close() {
	for _, f := range p.files {
		for _, t := range f.targets {
			clear(t.value)
			t.value = nil
		}
		clear(f.raw)
		clear(f.text.Text)
		f.raw, f.text = nil, textfile.File{}
	}
	if p.dir != nil {
		_ = p.dir.Close() // the folder handle was only used to open files; closing it loses nothing
		p.dir = nil
	}
}

// items returns the outcome of every location of the plan: the targets,
// planned or skipped with their reason, then the locations the plan
// skipped.
func (p *prepared) items() []Item {
	var out []Item
	for _, f := range p.files {
		for _, t := range f.targets {
			it := t.Item
			switch {
			case t.status != "":
				it.Status, it.Reason = t.status, t.reason
			case t.reason != "":
				it.Status, it.Reason = StatusSkipped, t.reason
			}
			out = append(out, it)
		}
	}
	for _, it := range p.plan.Skipped {
		if p.inspected {
			it.Tracked = p.state.Tracked(it.File)
		}
		out = append(out, it)
	}
	return out
}

// block adds b to the blockers of p once.
func (p *prepared) block(b Blocker) {
	if !slices.Contains(p.blockers, b) {
		p.blockers = append(p.blockers, b)
	}
}

// prepare carries out the steps a check and an apply of j share, for phase.
// The records the project's runs left open are reconciled with the disk
// first, before anything is planned. A condition that keeps the project
// from being applied becomes a blocker; for an apply, preparing stops at
// the first stage that found one. A check whose every location turned out
// skipped, with no applied value waiting for the manifest, is blocked as
// nothing-to-apply; an apply in that state closes with what it skipped. The
// caller closes the result.
func (r *Runner) prepare(ctx context.Context, j job.Job, phase job.Kind) (_ *prepared, err error) {
	id := ID(j.ApplyID)
	if err := r.store.StartApply(ctx, id, phase); err != nil {
		return nil, fmt.Errorf("starting the run: %w", err)
	}
	in, err := r.store.ApplyInput(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading the input of the run: %w", err)
	}
	p := &prepared{in: in}
	defer func() {
		if err != nil {
			p.close()
		}
	}()
	if r.opts.Git == "" {
		p.block(BlockerGitMissing)
	}
	if err := r.openFolder(ctx, j, p); err != nil {
		return nil, err
	}

	p.plan = NewPlan(p.in)
	if p.plan.Digest != p.in.Run.PlanDigest {
		return nil, errPlanChanged
	}
	for _, b := range p.plan.Blockers {
		p.block(b)
	}
	p.files = filesOf(p.plan.Targets)
	if p.dir == nil {
		return p, nil
	}
	if err := r.inspect(ctx, j, p); err != nil {
		return nil, err
	}
	if err := r.readManifest(ctx, j, p); err != nil {
		return nil, err
	}
	if !p.inspected || phase == job.KindApply && len(p.blockers) > 0 {
		return p, nil
	}

	if err := r.readTargets(ctx, p); err != nil {
		return nil, err
	}
	r.dropLarge(p)
	if err := r.verify(ctx, p); err != nil {
		return nil, err
	}
	takeWholeFiles(p)
	r.markUntracked(p, phase)
	if phase == job.KindApplyCheck && !p.anyPlanned() && len(p.in.Unlisted) == 0 {
		p.block(BlockerNothingToApply)
	}
	return p, nil
}

// anyPlanned reports whether a location is left that an apply would change:
// one that reading the files gave no reason to skip.
func (p *prepared) anyPlanned() bool {
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.status == "" && t.reason == "" {
				return true
			}
		}
	}
	return false
}

// openFolder resolves and opens the project folder under the workspace
// root, reconciles the records the project's runs left open, and reads the
// input of the run again, since reconciling changes it. A folder that
// cannot be resolved is a blocker.
func (r *Runner) openFolder(ctx context.Context, j job.Job, p *prepared) error {
	if r.opts.Root == "" {
		p.block(BlockerWorkspaceReadOnly)
		return nil
	}
	folder, err := project.Resolve(r.opts.Root, p.in.Project.Path)
	if errors.Is(err, project.ErrInvalidPath) || errors.Is(err, project.ErrOutsideRoot) ||
		errors.Is(err, project.ErrNotDirectory) {
		r.logger.WarnContext(ctx, "apply project folder not found", "apply_id", j.ApplyID, "job_id", j.ID,
			"error", err.Error())
		p.block(BlockerProjectPath)
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolving the project folder: %w", err)
	}
	opened, err := rewrite.Open(folder)
	if err != nil {
		return err
	}
	var dir projectDir = opened
	if r.wrapDir != nil {
		dir = r.wrapDir(dir)
	}
	p.folder, p.dir = folder, dir
	if err := r.reconcile(ctx, j, p.in.Project.ID, dir); err != nil {
		return err
	}
	in, err := r.store.ApplyInput(ctx, ID(j.ApplyID))
	if err != nil {
		return fmt.Errorf("reading the input of the run again: %w", err)
	}
	p.in = in
	return nil
}

// reconcile closes every change of a file that a run of the project
// announced and did not confirm (R1): the change happened when the file now
// has the announced hash, or, for a removal, is gone. The record's
// temporary file, if any, is removed first; then the record goes to
// ResolveFile as it was read.
func (r *Runner) reconcile(ctx context.Context, j job.Job, projectID project.ID, dir projectDir) error {
	open, err := r.store.OpenFiles(ctx, projectID)
	if err != nil {
		return fmt.Errorf("listing open file records: %w", err)
	}
	for _, f := range open {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := r.announced(dir, f)
		if err != nil {
			return err
		}
		if err := r.removeTemp(ctx, j, dir, f); err != nil {
			return err
		}
		if err := r.store.ResolveFile(ctx, f, done); err != nil {
			return fmt.Errorf("closing the record of %s: %w", f.File, err)
		}
	}
	return nil
}

// removeTemp removes the temporary file f names, if any, before its record
// is closed: once closed, no record names the file and the tool would never
// remove it. A link, a folder or anything else that is not a regular file
// at that name is not the tool's and is left in place, and so is a file
// outside the folder of the record's file, where the tool never makes one.
func (r *Runner) removeTemp(ctx context.Context, j job.Job, dir projectDir, f OpenFile) error {
	if f.TempName == "" {
		return nil
	}
	if filepath.Dir(filepath.FromSlash(f.TempName)) != filepath.Dir(filepath.FromSlash(f.File)) {
		r.logger.WarnContext(ctx, "apply temporary file left in place", "apply_id", j.ApplyID, "job_id", j.ID,
			"error", "the recorded temporary file is not beside the file it was for")
		return nil
	}
	err := dir.RemoveTemp(f.TempName)
	if errors.Is(err, rewrite.ErrNotRegular) {
		r.logger.WarnContext(ctx, "apply temporary file left in place", "apply_id", j.ApplyID, "job_id", j.ID,
			"error", err.Error())
		return nil
	}
	if err != nil {
		return fmt.Errorf("removing a temporary file: %w", err)
	}
	return nil
}

// announced reports whether the file of f now is as f announced it.
func (r *Runner) announced(dir projectDir, f OpenFile) (bool, error) {
	hash, exists, err := r.hashOf(dir, f.File)
	if err != nil {
		return false, err
	}
	if f.AfterHash == "" {
		return !exists, nil
	}
	return exists && hash == f.AfterHash, nil
}

// hashOf returns the hash of the file rel and whether it exists. A file
// that is not a regular file, or is too large, has no hash. Changes write
// placeholders that can be longer than the values they replace, so the
// limit is twice the one for reading a file to change.
func (r *Runner) hashOf(dir projectDir, rel string) (hash string, exists bool, err error) {
	raw, seen, err := dir.Read(rel, 2*r.opts.MaxFileBytes)
	clear(raw)
	switch {
	case err == nil:
		return seen.Hash, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return "", false, nil
	case errors.Is(err, rewrite.ErrNotRegular), errors.Is(err, rewrite.ErrTooLarge):
		return "", true, nil
	}
	return "", false, err
}

// inspect asks git about the project folder. A folder git cannot work on
// safely is a blocker; so is an uncommitted change that no apply of the
// project made (R2).
func (r *Runner) inspect(ctx context.Context, j job.Job, p *prepared) error {
	if r.opts.Git == "" {
		return nil
	}
	st, err := worktree.Inspect(ctx, r.opts.Git, r.opts.Root, p.folder)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("inspecting the work tree: %w", ctxErr)
		}
		b := blockerFor(err)
		r.logger.WarnContext(ctx, "apply work tree refused", "apply_id", j.ApplyID, "job_id", j.ID,
			"blocker", string(b), "error", err.Error())
		p.block(b)
		return nil
	}
	p.state, p.inspected = st, true
	if len(st.Dirty) == 0 {
		return nil
	}
	written, err := r.store.WrittenFiles(ctx, p.in.Project.ID)
	if err != nil {
		return fmt.Errorf("listing written files: %w", err)
	}
	for _, rel := range st.Dirty {
		if !r.ownChange(p.dir, rel, written[rel]) {
			p.dirty = append(p.dirty, rel)
		}
	}
	if len(p.dirty) > 0 {
		p.block(BlockerDirty)
	}
	return nil
}

// ownChange reports whether the file rel is as one of the changes in
// written left it: it has the hash of one, or one removed it and it is
// gone. A file that cannot be read is not.
func (r *Runner) ownChange(dir projectDir, rel string, written []Written) bool {
	if len(written) == 0 {
		return false
	}
	hash, exists, err := r.hashOf(dir, rel)
	if err != nil {
		// A file that cannot be read now cannot be shown to be the
		// apply's own change, so it stays dirty.
		return false
	}
	for _, w := range written {
		if w.AfterHash == "" && !exists || exists && hash != "" && w.AfterHash == hash {
			return true
		}
	}
	return false
}

// blockerFor returns the blocker for an error of worktree.Inspect.
func blockerFor(err error) Blocker {
	switch {
	case errors.Is(err, worktree.ErrNotRepository):
		return BlockerNotRepository
	case errors.Is(err, worktree.ErrNoCommit):
		return BlockerNoCommit
	case errors.Is(err, worktree.ErrUnsafeConfig), errors.Is(err, worktree.ErrUnsafePath):
		return BlockerUnsafeGitConfig
	}
	return BlockerGitFailed
}

// readManifest reads the manifest in the project folder, if there is one.
// A manifest that cannot be read is a blocker.
func (r *Runner) readManifest(ctx context.Context, j job.Job, p *prepared) error {
	raw, seen, err := p.dir.Read(manifest.FileName, manifest.MaxSize)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, rewrite.ErrNotRegular), errors.Is(err, rewrite.ErrTooLarge):
	case err != nil:
		return fmt.Errorf("reading the manifest: %w", err)
	default:
		m, parseErr := manifest.Parse(raw)
		if parseErr == nil {
			p.manifest, p.hasManifest, p.manifestSeen = m, true, seen
			return nil
		}
		err = parseErr
	}
	r.logger.WarnContext(ctx, "apply manifest unreadable", "apply_id", j.ApplyID, "job_id", j.ID, "error", err.Error())
	p.block(BlockerManifestUnreadable)
	return nil
}

// filesOf groups targets, ordered by file, into the files they change.
func filesOf(targets []Target) []*sourceFile {
	var out []*sourceFile
	for _, t := range targets {
		if len(out) == 0 || out[len(out)-1].rel != t.Item.File {
			out = append(out, &sourceFile{rel: t.Item.File})
		}
		f := out[len(out)-1]
		f.targets = append(f.targets, &target{Target: t})
	}
	return out
}

// readTargets reads each file of the plan once and takes the value at each
// of its targets: a file that is gone, in a repository of its own, or
// changed since the scan (R7) is skipped whole; a whole-file target's value
// is the bytes on disk, and a file that may be removed only if it holds
// nothing but PEM blocks is checked for that; any other value is a copy of
// its range in the decoded text.
func (r *Runner) readTargets(ctx context.Context, p *prepared) error {
	for _, f := range p.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		tracked := p.state.Tracked(f.rel)
		for _, t := range f.targets {
			t.Item.Tracked = tracked
		}
		nested, err := worktree.Nested(p.folder, f.rel)
		if err != nil {
			return err
		}
		if nested {
			f.skipAll(ReasonNestedRepository)
			continue
		}
		raw, seen, err := p.dir.Read(f.rel, r.opts.MaxFileBytes)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			f.skipAll(ReasonFileMissing)
			continue
		case errors.Is(err, rewrite.ErrNotRegular), errors.Is(err, rewrite.ErrTooLarge), errors.Is(err, rewrite.ErrChanged):
			f.skipAll(ReasonFileChanged)
			continue
		case err != nil:
			return err
		}
		f.raw, f.seen = value(raw), seen
		for _, t := range f.targets {
			t.reason, t.value = f.valueAt(t, r.opts.MaxStoredBytes)
		}
	}
	return nil
}

// valueAt returns a copy of the value of t, a target in f, or why it is
// skipped. A whole-file value longer than maxStored is too large and is
// not copied.
func (f *sourceFile) valueAt(t *target, maxStored int) (Reason, value) {
	if t.Location.FileHash != f.seen.Hash {
		return ReasonFileChanged, nil
	}
	if t.WholeFile {
		if t.PEMOnly {
			text, err := f.decode()
			if err != nil || !PEMOnly(text.Text) {
				return ReasonKeyInSourceFile, nil
			}
		}
		if len(f.raw) > maxStored {
			return ReasonTooLarge, nil
		}
		return "", value(bytes.Clone(f.raw))
	}
	text, err := f.decode()
	if err != nil {
		return ReasonNotText, nil
	}
	sp, err := rewrite.Span(text, t.Location)
	if err != nil {
		return ReasonValueMismatch, nil
	}
	return "", value(bytes.Clone(text.Text[sp.Start:sp.End]))
}

// dropLarge skips a value to store that is larger than the store takes.
// Such a value is never sent to OpenBao.
func (r *Runner) dropLarge(p *prepared) {
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.reason == "" && t.StoreKey != "" && len(t.value) > r.opts.MaxStoredBytes {
				t.reason = ReasonTooLarge
			}
		}
	}
}

// verify computes the fingerprint of every value still planned and skips
// each whose fingerprint is not its bundle's (R4).
func (r *Runner) verify(ctx context.Context, p *prepared) error {
	var (
		planned []*target
		values  [][]byte
	)
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.reason == "" {
				planned = append(planned, t)
				values = append(values, t.value)
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	fps, err := r.vault.Fingerprints(ctx, values)
	if err != nil {
		return fmt.Errorf("%w: %w", errSecretStore, err)
	}
	if len(fps) != len(values) {
		return fmt.Errorf("%w: got %d fingerprints for %d values", errSecretStore, len(fps), len(values))
	}
	for i, t := range planned {
		if fps[i] != t.Fingerprint {
			t.reason = ReasonValueMismatch
		}
	}
	return nil
}

// takeWholeFiles skips every target of a file whose whole-file target was
// skipped, for the same reason, since the file is removed only when its
// whole value is kept (R5). A key in a source file leaves the file and its
// other targets as they are.
func takeWholeFiles(p *prepared) {
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.WholeFile && t.reason != "" && t.reason != ReasonKeyInSourceFile {
				f.skipAll(t.reason)
				break
			}
		}
	}
}

// markUntracked handles customer data to redact in files git does not
// track, which no commit can bring back. A check lists those files for the
// user to confirm; an apply skips such data as untracked-irreversible
// unless the user confirmed the file.
func (r *Runner) markUntracked(p *prepared, phase job.Kind) {
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.reason != "" || t.Item.Action != ActionRedact || t.Item.Tracked {
				continue
			}
			if phase == job.KindApplyCheck {
				if !slices.Contains(p.irreversible, f.rel) {
					p.irreversible = append(p.irreversible, f.rel)
				}
				continue
			}
			if !p.in.Run.AllowIrreversible || !slices.Contains(p.in.Run.IrreversibleFiles, f.rel) {
				t.reason = ReasonUntrackedIrreversible
			}
		}
	}
}
