package apply_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// fakeStore is an apply.Store in memory. Every call is recorded with its
// arguments; hooks[method] runs first and its error is returned, then
// errs[method] is returned.
type fakeStore struct {
	in      apply.Input
	open    []apply.OpenFile
	written map[string][]apply.Written
	errs    map[string]error
	hooks   map[string]func(ctx context.Context) error

	calls    []string
	args     []string
	resolved []resolvedFile
	saved    *savedCheck
	failed   *failedRun
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
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		written: map[string][]apply.Written{},
		errs:    map[string]error{},
		hooks:   map[string]func(ctx context.Context) error{},
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

func (s *fakeStore) ApplyInput(ctx context.Context, id apply.ID) (apply.Input, error) {
	if err := s.enter(ctx, "ApplyInput", id); err != nil {
		return apply.Input{}, err
	}
	return s.in, nil
}

func (s *fakeStore) StartApply(ctx context.Context, id apply.ID, phase job.Kind) error {
	return s.enter(ctx, "StartApply", id, phase)
}

func (s *fakeStore) OpenFiles(ctx context.Context, projectID project.ID) ([]apply.OpenFile, error) {
	if err := s.enter(ctx, "OpenFiles", projectID); err != nil {
		return nil, err
	}
	return slices.Clone(s.open), nil
}

func (s *fakeStore) ResolveFile(ctx context.Context, f apply.OpenFile, done bool) error {
	if err := s.enter(ctx, "ResolveFile", f, done); err != nil {
		return err
	}
	s.resolved = append(s.resolved, resolvedFile{f, done})
	s.open = slices.DeleteFunc(s.open, func(o apply.OpenFile) bool { return o == f })
	if done {
		s.written[f.File] = append(s.written[f.File], apply.Written{AfterHash: f.AfterHash})
	}
	return nil
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
	return s.enter(ctx, "BeginFile", id, file, beforeHash, afterHash, tempName, items)
}

func (s *fakeStore) EndFile(ctx context.Context, id apply.ID, file, afterHash, tempName string) error {
	return s.enter(ctx, "EndFile", id, file, afterHash, tempName)
}

func (s *fakeStore) MarkInManifest(ctx context.Context, projectID project.ID) error {
	return s.enter(ctx, "MarkInManifest", projectID)
}

func (s *fakeStore) SettledFingerprints(ctx context.Context, projectID project.ID) ([]string, error) {
	if err := s.enter(ctx, "SettledFingerprints", projectID); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *fakeStore) FinishApply(ctx context.Context, jobID int64, workerID string, id apply.ID, items []apply.Item) error {
	return s.enter(ctx, "FinishApply", jobID, workerID, id, items)
}

func (s *fakeStore) FailApply(ctx context.Context, jobID int64, workerID string, id apply.ID, reason string, blockers []apply.Blocker) error {
	if err := s.enter(ctx, "FailApply", jobID, workerID, id, reason, blockers); err != nil {
		return err
	}
	s.failed = &failedRun{Reason: reason, Blockers: slices.Clone(blockers)}
	return nil
}

// fakeVault is an apply.Vault in memory. The fingerprint of a value is its
// SHA-256 in hex, unless lie maps that to another fingerprint.
type fakeVault struct {
	lie map[string]string
	err error

	// seen holds the SHA-256 of every value passed to Fingerprints, taken
	// when it was passed.
	seen    []string
	reads   int
	puts    int
	deletes int
}

func newFakeVault() *fakeVault { return &fakeVault{lie: map[string]string{}} }

func (v *fakeVault) Fingerprints(_ context.Context, values [][]byte) ([]string, error) {
	out := make([]string, len(values))
	for i, val := range values {
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

func (v *fakeVault) ReadStored(context.Context, int64, string) (bao.Stored, error) {
	v.reads++
	return bao.Stored{}, bao.ErrNotStored
}

func (v *fakeVault) PutStored(context.Context, int64, string, bao.StoredValue, int) (int, error) {
	v.puts++
	return 1, nil
}

func (v *fakeVault) DeletePending(context.Context, int64, string) error {
	v.deletes++
	return nil
}

// sha returns the SHA-256 of b in hex.
func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
