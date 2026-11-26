package apply_test

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// The errors the fake store answers with where the database has a sentinel.
var (
	errFakeSettled    = errors.New("fake: an item of the file is already applied or failed")
	errFakeSuperseded = errors.New("fake: the file was announced again")
	errFakeNotFound   = errors.New("fake: not found")
)

// itemKey identifies an item of a run, as the database does.
type itemKey struct {
	run       apply.ID
	finding   finding.ID
	line, col int
}

// runFile identifies the record of a file in a run.
type runFile struct {
	run  apply.ID
	file string
}

// fakeStore is an apply.Store in memory that keeps file records and items
// the way the database does. Every call is recorded with its arguments;
// hooks[method] runs first and its error is returned, then errs[method] is
// returned; after[method] runs once the call took effect, and its error is
// returned.
type fakeStore struct {
	in      apply.Input
	open    []apply.OpenFile
	written map[string][]apply.Written
	errs    map[string]error
	hooks   map[string]func(ctx context.Context) error
	after   map[string]func(ctx context.Context) error
	settled []string

	items      map[itemKey]apply.Item
	inManifest map[itemKey]bool
	// writtenBy holds the hash a run's confirmed change of a file left, and
	// prev the hash an open announcement of a written file goes back to.
	writtenBy map[runFile]string
	prev      map[runFile]string
	state     map[apply.ID]apply.State

	calls    []string
	args     []string
	resolved []resolvedFile
	saved    *savedCheck
	failed   *failedRun
	finished []apply.Item
}

// resolvedFile is one call of ResolveFile.
type resolvedFile struct {
	File apply.OpenFile
	Done bool
}

// savedCheck is the call of SaveCheck.
type savedCheck struct {
	Blockers     []apply.Blocker
	Dirty        []string
	Irreversible []string
	Items        []apply.Item
}

// failedRun is the call of FailApply.
type failedRun struct {
	Reason   string
	Blockers []apply.Blocker
	Dirty    []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		written:    map[string][]apply.Written{},
		errs:       map[string]error{},
		hooks:      map[string]func(ctx context.Context) error{},
		after:      map[string]func(ctx context.Context) error{},
		items:      map[itemKey]apply.Item{},
		inManifest: map[itemKey]bool{},
		writtenBy:  map[runFile]string{},
		prev:       map[runFile]string{},
		state:      map[apply.ID]apply.State{},
	}
}

// enter records a call of method with args and returns the error it is to
// fail with.
func (s *fakeStore) enter(ctx context.Context, method string, args ...any) error {
	s.calls = append(s.calls, method)
	s.args = append(s.args, fmt.Sprintf("%s %+v", method, args))
	if h := s.hooks[method]; h != nil {
		if err := h(ctx); err != nil {
			return err
		}
	}
	return s.errs[method]
}

// leave runs the hook that follows a call of method that took effect.
func (s *fakeStore) leave(ctx context.Context, method string) error {
	if h := s.after[method]; h != nil {
		return h(ctx)
	}
	return nil
}

// begins returns how many times BeginFile was called.
func (s *fakeStore) begins() int {
	n := 0
	for _, c := range s.calls {
		if c == "BeginFile" {
			n++
		}
	}
	return n
}

func keyOf(run apply.ID, it apply.Item) itemKey {
	return itemKey{run, it.FindingID, it.StartLine, it.StartCol}
}

// runItems returns the stored items of run, ordered by file and position.
func (s *fakeStore) runItems(run apply.ID) []apply.Item {
	var out []apply.Item
	for k, it := range s.items {
		if k.run == run {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, func(x, y apply.Item) int {
		return cmp.Or(cmp.Compare(x.File, y.File), cmp.Compare(x.StartLine, y.StartLine),
			cmp.Compare(x.StartCol, y.StartCol), cmp.Compare(x.FindingID, y.FindingID))
	})
	return out
}

// listed reports whether an item goes into the manifest once applied.
func listed(it apply.Item) bool {
	return (it.Action == apply.ActionReplace || it.Action == apply.ActionRemoveFile) &&
		it.Category != finding.CategoryCustomerData
}

func (s *fakeStore) ApplyInput(ctx context.Context, id apply.ID) (apply.Input, error) {
	if err := s.enter(ctx, "ApplyInput", id); err != nil {
		return apply.Input{}, err
	}
	in := s.in
	in.Unlisted = nil
	var keys []itemKey
	for k, it := range s.items {
		if it.Status == apply.StatusApplied && !s.inManifest[k] && listed(it) {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(x, y itemKey) int {
		a, b := s.items[x], s.items[y]
		return cmp.Or(cmp.Compare(x.run, y.run), cmp.Compare(a.File, b.File), cmp.Compare(x.line, y.line),
			cmp.Compare(x.col, y.col), cmp.Compare(x.finding, y.finding))
	})
	for _, k := range keys {
		in.Unlisted = append(in.Unlisted, s.items[k])
	}
	return in, nil
}

func (s *fakeStore) StartApply(ctx context.Context, id apply.ID, phase job.Kind) error {
	if err := s.enter(ctx, "StartApply", id, phase); err != nil {
		return err
	}
	if phase == job.KindApply {
		s.state[id] = apply.StateRunning
	}
	return nil
}

func (s *fakeStore) OpenFiles(ctx context.Context, projectID project.ID) ([]apply.OpenFile, error) {
	if err := s.enter(ctx, "OpenFiles", projectID); err != nil {
		return nil, err
	}
	return slices.Clone(s.open), nil
}

// markWritten confirms the open record f: it is written, and its run's
// writing items of the file are applied.
func (s *fakeStore) markWritten(f apply.OpenFile) {
	s.open = slices.DeleteFunc(s.open, func(o apply.OpenFile) bool { return o == f })
	s.written[f.File] = append(s.written[f.File], apply.Written{AfterHash: f.AfterHash})
	rf := runFile{f.Run, f.File}
	s.writtenBy[rf] = f.AfterHash
	delete(s.prev, rf)
	s.setWriting(f.Run, f.File, apply.StatusApplied, "")
}

// setWriting moves the writing items of file in run to status and reason.
func (s *fakeStore) setWriting(run apply.ID, file string, status apply.Status, reason apply.Reason) {
	for k, it := range s.items {
		if k.run == run && it.File == file && it.Status == apply.StatusWriting {
			it.Status, it.Reason = status, reason
			s.items[k] = it
		}
	}
}

func (s *fakeStore) ResolveFile(ctx context.Context, f apply.OpenFile, done bool) error {
	if err := s.enter(ctx, "ResolveFile", f, done); err != nil {
		return err
	}
	s.resolved = append(s.resolved, resolvedFile{f, done})
	if !slices.Contains(s.open, f) {
		if done {
			// A record seeded as open only through its written hash.
			s.written[f.File] = append(s.written[f.File], apply.Written{AfterHash: f.AfterHash})
		}
		return s.leave(ctx, "ResolveFile")
	}
	if done {
		s.markWritten(f)
		return s.leave(ctx, "ResolveFile")
	}
	s.open = slices.DeleteFunc(s.open, func(o apply.OpenFile) bool { return o == f })
	rf := runFile{f.Run, f.File}
	if h, ok := s.prev[rf]; ok {
		s.written[f.File] = append(s.written[f.File], apply.Written{AfterHash: h})
		s.writtenBy[rf] = h
		delete(s.prev, rf)
	}
	s.setWriting(f.Run, f.File, apply.StatusFailed, apply.ReasonInterrupted)
	return s.leave(ctx, "ResolveFile")
}

func (s *fakeStore) WrittenFiles(ctx context.Context, projectID project.ID) (map[string][]apply.Written, error) {
	if err := s.enter(ctx, "WrittenFiles", projectID); err != nil {
		return nil, err
	}
	out := make(map[string][]apply.Written, len(s.written))
	for f, w := range s.written {
		out[f] = slices.Clone(w)
	}
	return out, nil
}

func (s *fakeStore) SaveCheck(ctx context.Context, jobID int64, workerID string, id apply.ID, blockers []apply.Blocker,
	dirty, irreversible []string, items []apply.Item,
) error {
	if err := s.enter(ctx, "SaveCheck", jobID, workerID, id, blockers, dirty, irreversible, items); err != nil {
		return err
	}
	s.saved = &savedCheck{
		Blockers: slices.Clone(blockers), Dirty: slices.Clone(dirty),
		Irreversible: slices.Clone(irreversible), Items: slices.Clone(items),
	}
	return nil
}

func (s *fakeStore) BeginFile(ctx context.Context, id apply.ID, file, beforeHash, afterHash, tempName string, items []apply.Item) error {
	if err := s.enter(ctx, "BeginFile", id, file, beforeHash, afterHash, tempName, items); err != nil {
		return err
	}
	for _, it := range items {
		if it.File != file {
			return fmt.Errorf("fake: an item of another file")
		}
		old, ok := s.items[keyOf(id, it)]
		if ok && (old.Status == apply.StatusApplied ||
			old.Status == apply.StatusFailed && old.Reason != apply.ReasonInterrupted) {
			return errFakeSettled
		}
	}
	rf := runFile{id, file}
	i := slices.IndexFunc(s.open, func(o apply.OpenFile) bool { return o.Run == id && o.File == file })
	switch h, ok := s.writtenBy[rf]; {
	case i >= 0:
		s.open = slices.Delete(s.open, i, i+1)
	case ok:
		s.prev[rf] = h
		delete(s.writtenBy, rf)
		w := s.written[file]
		if j := slices.Index(w, apply.Written{AfterHash: h}); j >= 0 {
			s.written[file] = slices.Delete(w, j, j+1)
		}
	}
	s.open = append(s.open, apply.OpenFile{Run: id, File: file, AfterHash: afterHash, TempName: tempName})
	for _, it := range items {
		it.Status, it.Reason = apply.StatusWriting, ""
		s.items[keyOf(id, it)] = it
	}
	return s.leave(ctx, "BeginFile")
}

func (s *fakeStore) EndFile(ctx context.Context, id apply.ID, file, afterHash, tempName string) error {
	if err := s.enter(ctx, "EndFile", id, file, afterHash, tempName); err != nil {
		return err
	}
	f := apply.OpenFile{Run: id, File: file, AfterHash: afterHash, TempName: tempName}
	switch {
	case slices.Contains(s.open, f):
		s.markWritten(f)
	case slices.ContainsFunc(s.open, func(o apply.OpenFile) bool { return o.Run == id && o.File == file }):
		return errFakeSuperseded
	default:
		if h, ok := s.writtenBy[runFile{id, file}]; !ok || h != afterHash {
			return errFakeNotFound
		}
	}
	return s.leave(ctx, "EndFile")
}

func (s *fakeStore) MarkInManifest(ctx context.Context, projectID project.ID) error {
	if err := s.enter(ctx, "MarkInManifest", projectID); err != nil {
		return err
	}
	for k, it := range s.items {
		if it.Status == apply.StatusApplied && listed(it) {
			s.inManifest[k] = true
		}
	}
	return s.leave(ctx, "MarkInManifest")
}

func (s *fakeStore) SettledFingerprints(ctx context.Context, projectID project.ID) ([]string, error) {
	if err := s.enter(ctx, "SettledFingerprints", projectID); err != nil {
		return nil, err
	}
	return slices.Clone(s.settled), nil
}

// merge stores the outcomes in items as the database does when a run ends:
// an item replaces one that is planned, skipped or interrupted.
func (s *fakeStore) merge(id apply.ID, items []apply.Item) {
	for _, it := range items {
		k := keyOf(id, it)
		old, ok := s.items[k]
		if !ok || old.Status == apply.StatusPlanned || old.Status == apply.StatusSkipped ||
			old.Status == apply.StatusFailed && old.Reason == apply.ReasonInterrupted {
			s.items[k] = it
		}
	}
}

func (s *fakeStore) FinishApply(ctx context.Context, jobID int64, workerID string, id apply.ID, items []apply.Item) error {
	if err := s.enter(ctx, "FinishApply", jobID, workerID, id, items); err != nil {
		return err
	}
	s.merge(id, items)
	s.finished = slices.Clone(items)
	s.state[id] = apply.StateSucceeded
	return s.leave(ctx, "FinishApply")
}

func (s *fakeStore) FailApply(ctx context.Context, jobID int64, workerID string, id apply.ID, reason string,
	blockers []apply.Blocker, dirty []string, items []apply.Item,
) error {
	if err := s.enter(ctx, "FailApply", jobID, workerID, id, reason, blockers, dirty, items); err != nil {
		return err
	}
	s.merge(id, items)
	s.failed = &failedRun{Reason: reason, Blockers: slices.Clone(blockers), Dirty: slices.Clone(dirty)}
	s.state[id] = apply.StateFailed
	return nil
}

func (s *fakeStore) ApplyItems(ctx context.Context, id apply.ID) ([]apply.Item, error) {
	if err := s.enter(ctx, "ApplyItems", id); err != nil {
		return nil, err
	}
	return s.runItems(id), nil
}

func (s *fakeStore) ApplyRun(ctx context.Context, id apply.ID) (apply.Run, error) {
	if err := s.enter(ctx, "ApplyRun", id); err != nil {
		return apply.Run{}, err
	}
	r := apply.Run{ID: id, State: s.state[id]}
	for _, it := range s.runItems(id) {
		switch it.Status {
		case apply.StatusApplied:
			r.Applied++
		case apply.StatusSkipped:
			r.Skipped++
		case apply.StatusFailed:
			r.Failed++
		case apply.StatusPlanned, apply.StatusWriting:
		}
	}
	return r, nil
}

// errSavedButLost, queued for a PutStored, stores the value and answers
// with an error, as a write whose answer was lost does.
var errSavedButLost = errors.New("fake vault: the answer to the write was lost")

// fakeRecord is a path of the fake vault: its versions, oldest first.
type fakeRecord struct {
	versions [][]byte
	file     []bool
	deleted  bool
}

// keyedSum is a store key and the SHA-256 of a value given for it.
type keyedSum struct{ key, sum string }

// fakeVault is an apply.Vault in memory. The fingerprint of a value is its
// SHA-256 in hex, unless lie maps that to another fingerprint.
type fakeVault struct {
	lie map[string]string
	err error

	// stored holds the paths by key; putErrs the errors the next PutStored
	// calls of a key answer with, in order (nil: the write succeeds);
	// onRead, when set, changes what the call-th ReadStored of a key returns.
	stored  map[string]*fakeRecord
	putErrs map[string][]error
	onRead  func(key string, call int, st bao.Stored, err error) (bao.Stored, error)
	// deleteErr is what DeletePending answers with.
	deleteErr error

	// seen holds the SHA-256 of every value passed to Fingerprints, taken
	// when it was passed; put the SHA-256 of every value passed to
	// PutStored; held every value slice passed to either, as passed.
	seen  []string
	put   []string
	held  [][]byte
	calls []string
	// stores holds, for every PutStored call, the key and the SHA-256 of
	// the bytes it was given, taken when it was called.
	stores    []keyedSum
	readCalls map[string]int
	cas       map[string][]int
	dropped   []string
	reads     int
	puts      int
	deletes   int
}

func newFakeVault() *fakeVault {
	return &fakeVault{
		lie: map[string]string{}, stored: map[string]*fakeRecord{}, putErrs: map[string][]error{},
		readCalls: map[string]int{}, cas: map[string][]int{},
	}
}

func (v *fakeVault) Fingerprints(_ context.Context, values [][]byte) ([]string, error) {
	out := make([]string, len(values))
	for i, val := range values {
		v.held = append(v.held, val)
		fp := sha(val)
		v.seen = append(v.seen, fp)
		if other, ok := v.lie[fp]; ok {
			fp = other
		}
		out[i] = fp
	}
	if v.err != nil {
		return nil, v.err
	}
	return out, nil
}

// value returns the latest bytes stored under key, or nil.
func (v *fakeVault) value(key string) []byte {
	rec := v.stored[key]
	if rec == nil || rec.deleted || len(rec.versions) == 0 {
		return nil
	}
	return rec.versions[len(rec.versions)-1]
}

// version returns the latest version of key, 0 when it has none.
func (v *fakeVault) version(key string) int {
	if rec := v.stored[key]; rec != nil {
		return len(rec.versions)
	}
	return 0
}

func (v *fakeVault) ReadStored(_ context.Context, projectID int64, key string) (bao.Stored, error) {
	v.reads++
	v.readCalls[key]++
	v.calls = append(v.calls, fmt.Sprintf("ReadStored %+v", []any{projectID, key}))
	st, err := v.read(key)
	if v.onRead != nil {
		return v.onRead(key, v.readCalls[key], st, err)
	}
	return st, err
}

func (v *fakeVault) read(key string) (bao.Stored, error) {
	rec := v.stored[key]
	if rec == nil || len(rec.versions) == 0 {
		return bao.Stored{}, fmt.Errorf("fake vault: %w", bao.ErrNotStored)
	}
	n := len(rec.versions)
	if rec.deleted {
		return bao.Stored{Version: n, Deleted: true}, nil
	}
	return bao.Stored{Value: bao.Value(slices.Clone(rec.versions[n-1])), Version: n, File: rec.file[n-1]}, nil
}

func (v *fakeVault) PutStored(_ context.Context, projectID int64, key string, val bao.StoredValue, cas int) (int, error) {
	v.puts++
	v.held = append(v.held, val.Value)
	v.calls = append(v.calls, fmt.Sprintf("PutStored %+v", []any{projectID, key, val, cas}))
	v.stores = append(v.stores, keyedSum{key, sha(val.Value)})
	v.cas[key] = append(v.cas[key], cas)
	if kind, err := finding.ParseKind(val.Kind); err != nil || kind.CustomerData() {
		return 0, errors.New("fake vault: the kind is not a kind of secret")
	}
	var queued error
	if q := v.putErrs[key]; len(q) > 0 {
		queued, v.putErrs[key] = q[0], q[1:]
	}
	if queued != nil && !errors.Is(queued, errSavedButLost) {
		return 0, queued
	}
	if cas != v.version(key) {
		return 0, fmt.Errorf("fake vault: %w", bao.ErrVersionConflict)
	}
	rec := v.stored[key]
	if rec == nil {
		rec = &fakeRecord{}
		v.stored[key] = rec
	}
	rec.versions = append(rec.versions, slices.Clone(val.Value))
	rec.file = append(rec.file, val.File)
	rec.deleted = false
	v.put = append(v.put, sha(val.Value))
	if queued != nil {
		return 0, queued
	}
	return len(rec.versions), nil
}

func (v *fakeVault) DeletePending(_ context.Context, _ int64, fingerprint string) error {
	v.deletes++
	v.dropped = append(v.dropped, fingerprint)
	return v.deleteErr
}

// sha returns the SHA-256 of b in hex.
func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
