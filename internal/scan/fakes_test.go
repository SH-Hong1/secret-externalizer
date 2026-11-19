package scan_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/keyfile"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// fakeKey is the key of the fake fingerprinter's HMAC.
const fakeKey = "dev-only-fake-fingerprint-key"

// fingerprintOf returns the fingerprint the fake fingerprinter gives v.
func fingerprintOf(v string) string {
	m := hmac.New(sha256.New, []byte(fakeKey))
	m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))
}

// events records, in order, the calls the runner makes to the store and the
// originals that change something.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, s)
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

// outcome is what the runner wrote to the store about a scan.
type outcome struct {
	status  scan.Status
	reason  string
	files   int
	skipped int
}

// fakeStore keeps one scan and one project and records how the scan ended.
// A non-nil error field makes the matching method return that error.
type fakeStore struct {
	startErr   error
	scanErr    error
	projectErr error
	failErr    error
	saveErr    error

	events  *events
	project project.Project
	got     []outcome
	results []scan.Result
}

func (f *fakeStore) StartScan(context.Context, scan.ID) error { return f.startErr }

func (f *fakeStore) Scan(_ context.Context, id scan.ID) (scan.Scan, error) {
	return scan.Scan{ID: id, ProjectID: f.project.ID}, f.scanErr
}

func (f *fakeStore) Project(context.Context, project.ID) (project.Project, error) {
	return f.project, f.projectErr
}

func (f *fakeStore) FailScan(_ context.Context, _ int64, _ string, _ scan.ID, reason string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.events.add("fail")
	f.got = append(f.got, outcome{status: scan.StatusFailed, reason: reason})
	return nil
}

func (f *fakeStore) SaveResult(_ context.Context, _ int64, _ string, _ scan.ID, r scan.Result) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.events.add("save")
	f.got = append(f.got, outcome{status: scan.StatusSucceeded, files: r.Files, skipped: r.Skipped})
	f.results = append(f.results, r)
	return nil
}

// fakeFingerprinter computes an HMAC with fakeKey, or fails with err.
type fakeFingerprinter struct {
	err   error
	calls int
}

func (f *fakeFingerprinter) Fingerprints(_ context.Context, values [][]byte) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = fingerprintOf(string(v))
	}
	return out, nil
}

// fakeOriginals keeps pending originals in memory. A non-nil error field
// makes the matching method return that error.
type fakeOriginals struct {
	putErr, listErr, deleteErr error

	events  *events
	pending map[string][]byte
	puts    int
	deleted []string
}

func (f *fakeOriginals) PutPending(_ context.Context, _ int64, fingerprint string, value []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.events.add("put")
	f.puts++
	f.pending[fingerprint] = bytes.Clone(value)
	return nil
}

func (f *fakeOriginals) ListPending(context.Context, int64) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return slices.Sorted(maps.Keys(f.pending)), nil
}

func (f *fakeOriginals) DeletePending(_ context.Context, _ int64, fingerprint string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.events.add("delete")
	f.deleted = append(f.deleted, fingerprint)
	delete(f.pending, fingerprint)
	return nil
}

// needleScanner reports every place a fixed value appears in the bytes of
// the files under the scanned folder, as the real scanners would: by byte
// offsets (like opengrep) or, with lineCol, by line, byte column, and value
// (like betterleaks). Offsets and columns count the bytes on disk, a byte
// order mark included. It walks every folder and reports extra as well.
type needleScanner struct {
	tool, rule string
	category   finding.Category
	kind       finding.Kind
	needle     string
	lineCol    bool
	extra      []scan.Raw
	dropped    int
	err        error
	// onScan, when set, runs first with the scanned folder.
	onScan func(dir string)

	mu   sync.Mutex
	dirs []string
}

func (s *needleScanner) Name() string { return s.tool }

func (*needleScanner) Version(context.Context) (string, error) { return "fake-1", nil }

func (s *needleScanner) Scan(ctx context.Context, dir string) ([]scan.Raw, error) {
	raws, _, err := s.ScanWithCounts(ctx, dir)
	return raws, err
}

func (s *needleScanner) ScanWithCounts(ctx context.Context, dir string) ([]scan.Raw, scan.Counts, error) {
	s.mu.Lock()
	s.dirs = append(s.dirs, dir)
	s.mu.Unlock()
	if s.onScan != nil {
		s.onScan(dir)
	}
	if err := ctx.Err(); err != nil {
		return nil, scan.Counts{}, err
	}
	if s.err != nil {
		return nil, scan.Counts{}, s.err
	}
	var raws []scan.Raw
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil //nolint:nilerr // like the real scanners, an unreadable entry is passed over
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: a test file under the test's own folder
		if err != nil {
			return nil //nolint:nilerr // like the real scanners, an unreadable file is passed over
		}
		rel, _ := filepath.Rel(dir, p)
		for off := 0; ; {
			i := bytes.Index(data[off:], []byte(s.needle))
			if i < 0 {
				break
			}
			start := off + i
			r := scan.Raw{Tool: s.tool, Rule: s.rule, Category: s.category, Kind: s.kind, File: filepath.ToSlash(rel)}
			if s.lineCol {
				before := data[:start]
				r.Offset, r.EndOffset = -1, -1
				r.Line = 1 + bytes.Count(before, []byte{'\n'})
				r.ByteCol = start - bytes.LastIndexByte(before, '\n')
				r.Value = scan.Value(s.needle)
			} else {
				r.Offset, r.EndOffset = start, start+len(s.needle)
			}
			raws = append(raws, r)
			off = start + len(s.needle)
		}
		return nil
	})
	if err != nil {
		return nil, scan.Counts{}, err
	}
	return append(raws, s.extra...), scan.Counts{Dropped: s.dropped}, nil
}

// scannedDirs returns the folders the scanner was run on.
func (s *needleScanner) scannedDirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dirs)
}

// Fake values. None of them is a real secret.
const (
	password = "dev-only-fake-password-1" //nolint:gosec // G101: an obviously fake fixture value
	account  = "dev-only-fake-account-2"
	hostURL  = "dev-only-fake-url-host-3"
	phone    = "010-0000-1234"
)

// passwordBL and passwordOG find password the way betterleaks and opengrep
// report values.
func passwordBL() *needleScanner {
	return &needleScanner{tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: password, lineCol: true}
}

func passwordOG() *needleScanner {
	return &needleScanner{tool: "opengrep", rule: "properties-password", category: finding.CategoryStorage, kind: finding.KindPassword, needle: password}
}

// env is a workspace with a project folder named shop, and the fakes a
// runner records into.
type env struct {
	root, dir, tempDir string
	store              *fakeStore
	fps                *fakeFingerprinter
	orig               *fakeOriginals
	events             *events
	logs               *bytes.Buffer
	opts               scan.Options
}

// newEnv writes files, given by path relative to the project folder, and
// returns the environment of a scan of that folder.
func newEnv(t *testing.T, files map[string]string) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(TempDir) error = %v", err)
	}
	e := &env{root: filepath.Join(base, "root"), tempDir: filepath.Join(base, "tmp"), events: &events{}, logs: &bytes.Buffer{}}
	e.dir = filepath.Join(e.root, "shop")
	for _, d := range []string{e.dir, e.tempDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}
	for rel, content := range files {
		writeFile(t, filepath.Join(e.dir, filepath.FromSlash(rel)), content)
	}
	e.store = &fakeStore{events: e.events, project: project.Project{ID: 7, Path: "shop"}}
	e.fps = &fakeFingerprinter{}
	e.orig = &fakeOriginals{events: e.events, pending: map[string][]byte{}}
	e.opts = scan.Options{WorkerID: "fake-worker", Root: e.root, RulesVersion: "fake-rules-1", TempDir: e.tempDir}
	return e
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}

// runner returns a Runner over the environment with the given scanners and
// the real key file detector.
func (e *env) runner(scanners ...scan.Scanner) *scan.Runner {
	logger := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	all := append(slices.Clone(scanners), keyfile.New())
	return scan.NewRunner(logger, e.store, e.fps, e.orig, all, e.opts)
}

// handle runs the scan of job 1, scan 5, with the given scanners.
func (e *env) handle(ctx context.Context, scanners ...scan.Scanner) error {
	return e.runner(scanners...).Handle(ctx, job.Job{ID: 1, ScanID: 5})
}

// result returns the one result the runner saved.
func (e *env) result(t *testing.T) scan.Result {
	t.Helper()
	if len(e.store.results) != 1 {
		t.Fatalf("Handle() saved %d results, want 1; outcomes %+v; log:\n%s", len(e.store.results), e.store.got, e.logs)
	}
	return e.store.results[0]
}

// failure returns the reason of the one failure the runner recorded.
func (e *env) failure(t *testing.T) string {
	t.Helper()
	if len(e.store.got) != 1 || e.store.got[0].status != scan.StatusFailed {
		t.Fatalf("Handle() wrote %+v, want one failure; log:\n%s", e.store.got, e.logs)
	}
	return e.store.got[0].reason
}

// tempEntries returns the names in the runner's temporary folder.
func (e *env) tempEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.tempDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", e.tempDir, err)
	}
	var names []string
	for _, d := range entries {
		names = append(names, d.Name())
	}
	return names
}

// hashOf returns the hex SHA-256 of s.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// containsAny reports whether s contains any of the fake values.
func containsAny(s string, values ...string) string {
	for _, v := range values {
		if strings.Contains(s, v) {
			return v
		}
	}
	return ""
}
