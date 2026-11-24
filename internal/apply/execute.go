package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/manifest"
	"github.com/SH-Hong1/secret-externalizer/internal/rewrite"
)

const (
	// maxStoreWrites bounds the writes of one value: the first, and one more
	// after a version conflict.
	maxStoreWrites = 2
	// recordLimit bounds writing down that a file changed, which runs even
	// after the job's context ended (R9).
	recordLimit = 5 * time.Second
)

// Apply handles an apply job. It checks the conditions again and, when they
// hold, stores every value to externalize in OpenBao and confirms it by
// reading it back, then changes each file once, at the places of bundles
// whose value was confirmed and of customer data to redact, writes the
// manifest and finishes the run. A condition that does not hold fails the
// run before anything is stored or changed. Apply stops only between files;
// the change of each file is written down before and after it happens, so
// the next check or apply settles a file a stopped run left. Apply returns an
// error, leaving the run running, when ctx ends, when the lease on the job is
// lost, or when a failure cannot be recorded.
func (r *Runner) Apply(ctx context.Context, j job.Job) error {
	limited, cancel := context.WithTimeoutCause(ctx, r.opts.ApplyLimit, errTimeLimit)
	defer cancel()
	outcome, err := r.apply(limited, j)
	return r.end(ctx, limited, j, err, outcome)
}

// apply carries out the apply of j. When it returns an error after the plan
// was prepared, outcome holds the locations this attempt skipped or failed,
// for the run's failure to record.
func (r *Runner) apply(ctx context.Context, j job.Job) (outcome []Item, err error) {
	p, err := r.prepare(ctx, j, job.KindApply)
	if err != nil {
		return nil, err
	}
	defer p.close()
	// Runs before p.close, while the plan's outcomes are still there. A
	// location the attempt did not reach stays planned and is left out.
	defer func() {
		if err != nil {
			outcome = slices.DeleteFunc(finalItems(p), func(it Item) bool { return it.Status == StatusPlanned })
		}
	}()
	id := ID(j.ApplyID)
	if len(p.blockers) > 0 {
		r.logger.WarnContext(ctx, "apply refused", "apply_id", id, "job_id", j.ID, "blockers", p.blockers,
			"dirty", len(p.dirty))
		if err := r.store.FailApply(ctx, j.ID, r.opts.WorkerID, id, refusal(p.blockers), p.blockers, nil); err != nil {
			return nil, fmt.Errorf("refusing the run: %w", err)
		}
		return nil, nil
	}

	storeFailures, err := r.storeValues(ctx, j, p)
	if err != nil {
		return nil, err
	}
	settleRemovals(p)
	for _, f := range p.files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := r.changeFile(ctx, j, p, f); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.writeManifest(ctx, j, p); err != nil {
		return nil, err
	}
	return nil, r.close(ctx, j, p, storeFailures)
}

// close ends the run. A run that stored nothing it was asked to store and
// applied nothing, in this attempt or an earlier one, fails with the
// secret-store reason; any other run succeeds with its counts. Either way
// the outcome of every location is recorded.
func (r *Runner) close(ctx context.Context, j job.Job, p *prepared, storeFailures int) error {
	id := ID(j.ApplyID)
	outcome := finalItems(p)
	failed, err := r.storedNothing(ctx, p, id, storeFailures)
	if err != nil {
		return err
	}
	if failed {
		r.logger.WarnContext(ctx, "apply stored nothing", "apply_id", id, "job_id", j.ID, "store_failures", storeFailures)
		if err := r.store.FailApply(ctx, j.ID, r.opts.WorkerID, id, FailureSecretStore, nil, outcome); err != nil {
			return fmt.Errorf("failing the run: %w", err)
		}
		return nil
	}
	if err := r.store.FinishApply(ctx, j.ID, r.opts.WorkerID, id, outcome); err != nil {
		return fmt.Errorf("finishing the run: %w", err)
	}
	if run, err := r.store.ApplyRun(ctx, id); err != nil {
		r.logger.WarnContext(ctx, "apply finished", "apply_id", id, "job_id", j.ID, "error", err.Error())
	} else {
		r.logger.InfoContext(ctx, "apply finished", "apply_id", id, "job_id", j.ID, "applied", run.Applied,
			"skipped", run.Skipped, "failed", run.Failed)
	}
	r.dropSettled(ctx, j, p)
	return nil
}

// storedNothing reports whether the run failed at storing and has applied
// nothing: some value could not be stored, no file changed in this attempt,
// and no earlier attempt of the run applied an item.
func (r *Runner) storedNothing(ctx context.Context, p *prepared, id ID, storeFailures int) (bool, error) {
	if storeFailures == 0 {
		return false, nil
	}
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.status == StatusApplied {
				return false, nil
			}
		}
	}
	items, err := r.store.ApplyItems(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reading the items of the run: %w", err)
	}
	for _, it := range items {
		if it.Status == StatusApplied {
			return false, nil
		}
	}
	return true, nil
}

// refusal returns the failure reason of a run refused for blockers.
func refusal(blockers []Blocker) string {
	switch {
	case slices.Contains(blockers, BlockerDirty):
		return FailureDirty
	case slices.Contains(blockers, BlockerNotRepository), slices.Contains(blockers, BlockerNoCommit):
		return FailureNotRepository
	case slices.Contains(blockers, BlockerUnsafeGitConfig):
		return FailureUnsafeConfig
	case slices.Contains(blockers, BlockerManifestUnreadable):
		return FailureManifest
	}
	return FailureOther
}

// storeValues stores the value of every planned target that has a store
// key, once per key, and confirms it (R3), and returns how many keys could
// not be stored. A target whose value was not stored and confirmed fails
// with store-failed. A whole-file target keeps the hash of the bytes
// stored, which the file must still have when it is removed. Every value is
// zeroed afterwards: nothing later needs one.
func (r *Runner) storeValues(ctx context.Context, j job.Job, p *prepared) (int, error) {
	defer func() {
		for _, f := range p.files {
			for _, t := range f.targets {
				clear(t.value)
				t.value = nil
			}
		}
	}()
	var keys []string
	byKey := map[string][]*target{}
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.reason != "" || t.StoreKey == "" {
				continue
			}
			if byKey[t.StoreKey] == nil {
				keys = append(keys, t.StoreKey)
			}
			byKey[t.StoreKey] = append(byKey[t.StoreKey], t)
		}
	}
	failures := 0
	for _, key := range keys {
		targets := byKey[key]
		version, err := r.storeKey(ctx, p, key, targets)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, ctxErr
			}
			r.logger.WarnContext(ctx, "apply value not stored", "apply_id", j.ApplyID, "job_id", j.ID,
				"store_key", key, "error", err.Error())
			failures++
			for _, t := range targets {
				t.status, t.reason = StatusFailed, ReasonStoreFailed
			}
			continue
		}
		for _, t := range targets {
			t.Item.StoreVersion = version
			if t.WholeFile {
				sum := sha256.Sum256(t.value)
				t.storedHash = hex.EncodeToString(sum[:])
			}
		}
	}
	return failures, nil
}

// storeKey stores the value of targets, which all have the store key key,
// and returns the version that holds it. A path that already holds the
// value is not written again. A version conflict, or a write whose answer
// was an error, is followed by reading the path again: what counts is
// what the path holds. The value is only confirmed by reading it back and
// computing the fingerprint of the bytes read.
func (r *Runner) storeKey(ctx context.Context, p *prepared, key string, targets []*target) (int, error) {
	t := targets[0]
	for _, o := range targets[1:] {
		if o.Fingerprint != t.Fingerprint || o.Item.Kind != t.Item.Kind || o.WholeFile != t.WholeFile {
			return 0, errors.New("the places of one store key differ in value or kind")
		}
	}
	if t.Item.Kind.CustomerData() || t.Item.Category == finding.CategoryCustomerData {
		return 0, errors.New("customer data is never stored")
	}
	projectID := int64(p.in.Project.ID)
	v := bao.StoredValue{Value: t.value, Fingerprint: t.Fingerprint, Kind: string(t.Item.Kind), File: t.WholeFile}
	var lastErr error
	for writes := 0; ; {
		cur, err := r.vault.ReadStored(ctx, projectID, key)
		cas := 0
		switch {
		case errors.Is(err, bao.ErrNotStored):
		case err != nil:
			return 0, fmt.Errorf("reading the stored value: %w", err)
		case cur.Deleted:
			cas = cur.Version
		default:
			same, err := r.holds(ctx, cur, t)
			if err != nil {
				return 0, err
			}
			if same {
				return cur.Version, nil
			}
			cas = cur.Version
		}
		if lastErr != nil && (!errors.Is(lastErr, bao.ErrVersionConflict) || writes >= maxStoreWrites) {
			return 0, fmt.Errorf("storing the value: %w", lastErr)
		}
		version, err := r.vault.PutStored(ctx, projectID, key, v, cas)
		writes++
		if err == nil {
			return version, r.confirm(ctx, projectID, key, version, t)
		}
		lastErr = err
	}
}

// confirm reads key back and reports an error unless it is at version and
// its bytes have the fingerprint of t.
func (r *Runner) confirm(ctx context.Context, projectID int64, key string, version int, t *target) error {
	got, err := r.vault.ReadStored(ctx, projectID, key)
	if err != nil {
		return fmt.Errorf("reading the stored value back: %w", err)
	}
	if got.Deleted || got.Version != version {
		clear(got.Value)
		return errors.New("the value read back is not the version written")
	}
	same, err := r.holds(ctx, got, t)
	if err != nil {
		return err
	}
	if !same {
		return errors.New("the value read back is not the value written")
	}
	return nil
}

// holds reports whether st holds the value of t: its bytes have t's
// fingerprint, computed again by the vault, and it is a whole file exactly
// when t is. The fingerprint field of the record is not trusted. The bytes
// read are zeroed.
func (r *Runner) holds(ctx context.Context, st bao.Stored, t *target) (bool, error) {
	defer clear(st.Value)
	fps, err := r.vault.Fingerprints(ctx, [][]byte{st.Value})
	if err != nil {
		return false, fmt.Errorf("%w: fingerprinting the stored value: %w", errSecretStore, err)
	}
	if len(fps) != 1 {
		return false, fmt.Errorf("%w: got %d fingerprints for one value", errSecretStore, len(fps))
	}
	return fps[0] == t.Fingerprint && st.File == t.WholeFile, nil
}

// settleRemovals decides, for each file with a whole-file target, whether
// the file is removed (R5). It is removed only when the whole file was
// stored and confirmed; otherwise every planned target of the file fails
// with store-failed and the file stays. In a file that is removed, another
// secret whose value was stored is applied by the removal, and one whose
// value was not is skipped as file-removed: its value is in the whole file
// stored.
func settleRemovals(p *prepared) {
	for _, f := range p.files {
		i := slices.IndexFunc(f.targets, func(t *target) bool {
			return t.WholeFile && (t.reason == "" || t.reason == ReasonStoreFailed)
		})
		if i < 0 {
			continue
		}
		whole := f.targets[i]
		if whole.reason != "" {
			for _, t := range f.targets {
				if t.reason == "" {
					t.status, t.reason = StatusFailed, ReasonStoreFailed
				}
			}
			continue
		}
		for _, t := range f.targets {
			switch {
			case t == whole || t.StoreKey == "":
			case t.reason == "":
				t.Item.Action = ActionRemoveFile
			case t.reason == ReasonStoreFailed:
				t.status, t.reason = "", ReasonFileRemoved
			}
		}
	}
}

// live returns the targets of f still to apply.
func (f *sourceFile) live() []*target {
	var out []*target
	for _, t := range f.targets {
		if t.reason == "" && t.status == "" {
			out = append(out, t)
		}
	}
	return out
}

// clearBuffers zeroes the bytes f was read as and its decoded text.
func (f *sourceFile) clearBuffers() {
	clear(f.raw)
	clear(f.text.Text)
}

// changeFile changes f once: it removes the file when its whole value was
// stored, and otherwise replaces every place still to apply. The file's
// buffers are zeroed once it is done.
func (r *Runner) changeFile(ctx context.Context, j job.Job, p *prepared, f *sourceFile) error {
	defer f.clearBuffers()
	targets := f.live()
	if len(targets) == 0 {
		return nil
	}
	if i := slices.IndexFunc(targets, func(t *target) bool { return t.WholeFile }); i >= 0 {
		// Removing is the one change that cannot be undone, so the file
		// must still hold exactly the bytes that were stored.
		raw, seen, err := p.dir.Read(f.rel, r.opts.MaxFileBytes)
		clear(raw)
		if err != nil {
			return r.cannotChange(ctx, j, f, targets, err)
		}
		if seen.Hash != targets[i].storedHash {
			return r.cannotChange(ctx, j, f, targets, errors.New("the file is not the one stored"))
		}
		items := make([]Item, 0, len(targets))
		for _, t := range targets {
			t.Item.Line = 0
			items = append(items, t.Item)
		}
		return r.commit(ctx, j, p.dir, targets, change{
			file: f.rel, before: seen.Hash, items: items,
			do: func() error { return p.dir.Remove(f.rel, seen) },
		})
	}

	text, err := f.decode()
	if err != nil {
		return r.cannotChange(ctx, j, f, targets, err)
	}
	edits := make([]rewrite.Edit, 0, len(targets))
	for _, t := range targets {
		sp, err := rewrite.Span(text, t.Location)
		if err != nil {
			return r.cannotChange(ctx, j, f, targets, err)
		}
		edits = append(edits, rewrite.Edit{Span: sp, With: t.Text})
	}
	out, err := rewrite.Replace(f.raw, text, edits)
	if err != nil {
		return r.cannotChange(ctx, j, f, targets, err)
	}
	defer clear(out.Content)
	items := make([]Item, 0, len(targets))
	for i, t := range targets {
		t.Item.Line = out.Lines[i]
		items = append(items, t.Item)
	}
	tmp, err := p.dir.TempName(f.rel)
	if err != nil {
		return fmt.Errorf("naming a temporary file: %w", err)
	}
	return r.commit(ctx, j, p.dir, targets, change{
		file: f.rel, before: f.seen.Hash, after: out.Hash, temp: tmp, items: items,
		do: func() error { return p.dir.Write(f.rel, out.Content, f.seen, tmp) },
	})
}

// cannotChange fails the targets of f, whose change could not be computed,
// with write-failed. Nothing was written down or changed.
func (r *Runner) cannotChange(ctx context.Context, j job.Job, f *sourceFile, targets []*target, err error) error {
	r.logger.WarnContext(ctx, "apply file not changed", "apply_id", j.ApplyID, "job_id", j.ID, "file", f.rel,
		"error", err.Error())
	for _, t := range targets {
		t.status, t.reason = StatusFailed, ReasonWriteFailed
	}
	return nil
}

// change is one change of a file: the file, its hash before and after (empty
// after for a removal, empty before for a new file), the temporary file it
// goes through, its items and the step that makes it.
type change struct {
	file, before, after, temp string
	items                     []Item
	do                        func() error
}

// commit makes c, written down before and after (R6), and sets the outcome
// of targets: applied when the change is on disk, write-failed when it could
// not be made. A failure to write the change down stops the run: the file is
// then not changed, or, after the change, the next start settles it.
func (r *Runner) commit(ctx context.Context, j job.Job, dir projectDir, targets []*target, c change) error {
	done, err := r.commitChange(ctx, j, dir, c)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if done {
			t.status, t.reason = StatusApplied, ""
		} else {
			t.status, t.reason = StatusFailed, ReasonWriteFailed
		}
	}
	return nil
}

// commitChange writes c down, makes it in dir, and writes down that it was
// made, and reports whether it is on disk. A change that could not be made
// is written down as not made. The records after the change use a context
// detached from the job's, with a limit of their own (R9).
func (r *Runner) commitChange(ctx context.Context, j job.Job, dir projectDir, c change) (bool, error) {
	id := ID(j.ApplyID)
	if err := r.store.BeginFile(ctx, id, c.file, c.before, c.after, c.temp, c.items); err != nil {
		return false, fmt.Errorf("writing down the change of %s: %w", c.file, err)
	}
	err := c.do()
	// The records after the change run even when the job's context has
	// ended, and get their own limit, which starts once the change is made.
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordLimit)
	defer cancel()
	switch {
	case err == nil:
	case errors.Is(err, rewrite.ErrAfterCommit):
		// The change is on disk; only a step after it failed. A temporary
		// file left beside it is removed now, while a record still names it.
		// If it cannot be, the record stays open and the next start removes
		// the file and settles the record.
		r.logger.WarnContext(ctx, "apply file changed but not flushed", "apply_id", id, "job_id", j.ID,
			"file", c.file, "error", err.Error())
		if c.temp != "" {
			if err := dir.RemoveTemp(c.temp); err != nil {
				return true, fmt.Errorf("removing the temporary file of %s: %w", c.file, err)
			}
		}
	default:
		r.logger.WarnContext(ctx, "apply file not changed", "apply_id", id, "job_id", j.ID, "file", c.file,
			"error", err.Error())
		// The record is the only thing that names the temporary file, so
		// the file goes first; if it cannot, the record stays open for the
		// next start to settle.
		if c.temp != "" {
			if err := dir.RemoveTemp(c.temp); err != nil {
				return false, fmt.Errorf("removing the temporary file of %s: %w", c.file, err)
			}
		}
		f := OpenFile{Run: id, File: c.file, AfterHash: c.after, TempName: c.temp}
		if err := r.store.ResolveFile(record, f, false); err != nil {
			return false, fmt.Errorf("writing down that %s did not change: %w", c.file, err)
		}
		return false, nil
	}
	if err := r.store.EndFile(record, id, c.file, c.after, c.temp); err != nil {
		return true, fmt.Errorf("writing down that %s changed: %w", c.file, err)
	}
	return true, nil
}

// writeManifest adds to the manifest every applied secret of the project
// that it does not list yet: those of earlier runs and of this one (R8).
// The file is written only when its content changes, through the same
// records as any file, and the items are then marked as listed. A manifest
// that cannot be written fails the run with its own reason; the items stay
// applied, and the next apply lists them.
func (r *Runner) writeManifest(ctx context.Context, j job.Job, p *prepared) error {
	entries := manifestEntries(p)
	if len(entries) == 0 {
		return nil
	}
	// The rewrite stage may have changed the manifest itself, as any file of
	// the plan, so it is read again.
	current, has, seen, err := readManifestFile(p.dir)
	if err != nil {
		return fmt.Errorf("%w: reading it again: %w", errManifestWrite, err)
	}
	p.manifest, p.hasManifest, p.manifestSeen = current, has, seen
	content, err := p.manifest.Merge(entries).Encode()
	if err != nil {
		return fmt.Errorf("%w: encoding: %w", errManifestWrite, err)
	}
	sum := sha256.Sum256(content)
	after := hex.EncodeToString(sum[:])
	if !p.hasManifest || after != p.manifestSeen.Hash {
		tmp, err := p.dir.TempName(manifest.FileName)
		if err != nil {
			return fmt.Errorf("%w: naming a temporary file: %w", errManifestWrite, err)
		}
		c := change{file: manifest.FileName, after: after, temp: tmp, do: func() error {
			return p.dir.Create(manifest.FileName, content, tmp)
		}}
		if p.hasManifest {
			c.before = p.manifestSeen.Hash
			c.do = func() error { return p.dir.Write(manifest.FileName, content, p.manifestSeen, tmp) }
		}
		done, err := r.commitChange(ctx, j, p.dir, c)
		if err != nil {
			return fmt.Errorf("%w: %w", errManifestWrite, err)
		}
		if !done {
			return errManifestWrite
		}
	}
	if err := r.store.MarkInManifest(ctx, p.in.Project.ID); err != nil {
		return fmt.Errorf("%w: marking the listed items: %w", errManifestWrite, err)
	}
	return nil
}

// readManifestFile reads and parses the manifest in dir, and reports
// whether there is one and what it looked like.
func readManifestFile(dir projectDir) (manifest.Manifest, bool, rewrite.Seen, error) {
	raw, seen, err := dir.Read(manifest.FileName, manifest.MaxSize)
	if errors.Is(err, fs.ErrNotExist) {
		return manifest.Manifest{}, false, rewrite.Seen{}, nil
	}
	if err != nil {
		return manifest.Manifest{}, false, rewrite.Seen{}, err
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return manifest.Manifest{}, false, rewrite.Seen{}, err
	}
	return m, true, seen, nil
}

// manifestEntries returns an entry for each applied secret not yet in the
// manifest: those Input.Unlisted holds and those this attempt applied.
func manifestEntries(p *prepared) []manifest.Entry {
	var out []manifest.Entry
	add := func(it Item) {
		if it.Status != StatusApplied || it.Category == finding.CategoryCustomerData || it.Kind.CustomerData() {
			return
		}
		action := manifest.ActionExternalized
		switch it.Action {
		case ActionReplace:
		case ActionRemoveFile:
			action = manifest.ActionFileRemoved
		case ActionRedact:
			return
		default:
			return
		}
		out = append(out, manifest.Entry{
			Name: it.Name, Category: it.Category, Action: action, Store: it.StorePath,
			Locations: []manifest.Location{{File: it.File, Line: it.Line}},
		})
	}
	for _, it := range p.in.Unlisted {
		add(it)
	}
	for _, f := range p.files {
		for _, t := range f.targets {
			if t.status == StatusApplied {
				it := t.Item
				it.Status = StatusApplied
				add(it)
			}
		}
	}
	return out
}

// finalItems returns the outcomes FinishApply records: every location the
// run skipped or failed at. An applied location was recorded when its file
// changed.
func finalItems(p *prepared) []Item {
	var out []Item
	for _, it := range p.items() {
		if it.Status != StatusApplied {
			out = append(out, it)
		}
	}
	return out
}

// dropSettled deletes the pending originals of the values the project
// judged false positives in every bundle on the triage screen. A failure is logged once and changes
// nothing about the run.
func (r *Runner) dropSettled(ctx context.Context, j job.Job, p *prepared) {
	fps, err := r.store.SettledFingerprints(ctx, p.in.Project.ID)
	if err != nil {
		r.logger.WarnContext(ctx, "apply pending originals not dropped", "apply_id", j.ApplyID, "job_id", j.ID,
			"error", err.Error())
		return
	}
	failed := 0
	var last error
	for _, fp := range fps {
		if err := r.vault.DeletePending(ctx, int64(p.in.Project.ID), fp); err != nil {
			failed++
			last = err
		}
	}
	if failed > 0 {
		r.logger.WarnContext(ctx, "apply pending originals not dropped", "apply_id", j.ApplyID, "job_id", j.ID,
			"failed", failed, "of", len(fps), "error", last.Error())
	}
}
