package apply_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/text/encoding/korean"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
	"github.com/SH-Hong1/secret-externalizer/internal/worktree/worktreetest"
)

// The IDs of the run every test checks.
const (
	testProject project.ID = 7
	testScan    scan.ID    = 3
	testRun     apply.ID   = 1
	testJob     int64      = 11
)

// Files and values of the example project. Every value is obviously fake.
const (
	ymlFile     = "src/main/resources/application-prod.yml"
	ymlValue    = "dev-only-fake-yaml-password"
	xmlFile     = "src/main/webapp/WEB-INF/spring/root-context.xml"
	xmlValue    = "dev-only-fake-xml-password"
	jdbcFile    = "src/main/resources/jdbc.properties"
	jdbcValue   = "dev-only-fake-properties-password"
	shFile      = "scripts/deploy.sh"
	shValue     = "dev-only-fake-sshpass"
	euckrFile   = "conf/euckr.properties"
	euckrValue  = "dev-only-fake-euckr-password"
	localFile   = "local/dev.properties"
	localValue  = "dev-only-fake-local-password"
	vendorFile  = "vendor/lib/config.properties"
	vendorValue = "dev-only-fake-vendor-password"
	trackedLog  = "logs/app-2026-11-01.log"
	untrackLog  = "local/debug.log"
	phone       = "010-0000-0000"
	pemFile     = "conf/partner.pem"
	notesFile   = "notes.md"
	pemLine     = "FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"
)

// layout is where the example project lies in the workspace.
type layout int

const (
	// inRepository: the project folder is a repository with one commit.
	inRepository layout = iota + 1
	// noRepository: the project folder is no repository.
	noRepository
	// inSubfolder: the project folder is a subfolder of a repository.
	inSubfolder
)

// fixture is the example project in a workspace, and what a check of it
// is given and records.
type fixture struct {
	t     *testing.T
	root  string // the workspace root
	repo  string // the repository's folder
	path  string // the project path under root
	dir   string // the project folder
	store *fakeStore
	vault *fakeVault
	logs  bytes.Buffer
	opts  apply.Options

	bundles   []triage.Bundle
	customer  []string
	spans     []apply.CustomerSpan
	newest    scan.ID
	digest    string
	values    []string
	nextID    int64
	gitBinary string

	// run is the run an apply works on, testRun when zero; allow and
	// irreversible are the run's consent to redact untracked files.
	run          apply.ID
	allow        bool
	irreversible []string
	// storeSums holds, by store key, the SHA-256 of the one value that may
	// be stored under it: the fingerprint of the bundle, which the fake
	// vault computes as that SHA-256.
	storeSums map[string]string
	// wrap, when set, wraps the project folder an apply opens.
	wrap func(apply.ProjectDir) apply.ProjectDir
}

// newShop copies the example project into a new workspace with the files
// the tests add: an EUC-KR file with CRLF line ends, a log and a
// properties file that .gitignore keeps out of git, a note with a key in
// it, a binary file, and a folder that is a repository of its own. It
// commits everything unless l is noRepository.
func newShop(t *testing.T, l layout) *fixture {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	f := &fixture{t: t, root: t.TempDir(), store: newFakeStore(), vault: newFakeVault(), gitBinary: git}
	f.path = "shop"
	f.repo = filepath.Join(f.root, "shop")
	if l == inSubfolder {
		f.path = "repo/shop"
		f.repo = filepath.Join(f.root, "repo")
		write(t, f.repo, "outside.txt", "outside the project\n")
	}
	f.dir = filepath.Join(f.root, filepath.FromSlash(f.path))
	copyTree(t, filepath.Join("..", "ruleset", "testdata", "legacy-shop"), f.dir)

	euckr, err := korean.EUCKR.NewEncoder().String("# 데이터베이스 설정\r\ndb.password=" + euckrValue + "\r\n")
	if err != nil {
		t.Fatalf("encoding the EUC-KR file: %v", err)
	}
	write(t, f.dir, euckrFile, euckr)
	write(t, f.dir, localFile, "local.db.password="+localValue+"\n")
	write(t, f.dir, untrackLog, "2026-11-20 INFO 연락처="+phone+"\n")
	write(t, f.dir, notesFile, "# 메모\n파트너 키:\n-----BEGIN RSA PRIVATE KEY-----\n"+pemLine+"\n-----END RSA PRIVATE KEY-----\n")
	write(t, f.dir, "data/blob.bin", "\x00\x01not text\x00")
	write(t, f.dir, vendorFile, "vendor.password="+vendorValue+"\n")

	f.opts = apply.Options{WorkerID: "worker-1", Root: f.root, Git: git}
	switch l {
	case inRepository, inSubfolder:
		worktreetest.Init(t, filepath.Join(f.dir, "vendor", "lib"))
		worktreetest.Init(t, f.repo)
	case noRepository:
	}
	return f
}

// git runs git in the repository's folder.
func (f *fixture) git(args ...string) {
	f.t.Helper()
	worktreetest.Git(f.t, f.repo, args...)
}

// commit commits every change in the repository.
func (f *fixture) commit() {
	f.t.Helper()
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "fake")
}

// write writes content to rel under dir, making its folders.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("making the folder of %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
}

// read returns the bytes of rel under dir.
func read(t *testing.T, dir, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) //nolint:gosec // G304: the path is under t.TempDir
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return b
}

// copyTree copies the regular files and folders under src to dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o750)
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: a file of the example project in testdata
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600) //nolint:gosec // G703: rel comes from walking testdata
	})
	if err != nil {
		t.Fatalf("copying %s: %v", src, err)
	}
}

// snapshot returns every entry under root, .git folders included, with
// the hash of each regular file.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			out[rel] = "folder"
		case d.Type().IsRegular():
			b, err := os.ReadFile(p) //nolint:gosec // G304: the path is under t.TempDir
			if err != nil {
				return err
			}
			out[rel] = sha(b)
		default:
			out[rel] = d.Type().String()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listing %s: %v", root, err)
	}
	return out
}

// position returns the line and the column, counted in characters, of the
// byte offset off in text.
func position(text []byte, off int) (line, col int) {
	start := bytes.LastIndexByte(text[:off], '\n') + 1
	return bytes.Count(text[:off], []byte("\n")) + 1, utf8.RuneCount(text[start:off]) + 1
}

// locate returns the location of the first val in file, as a scan of the
// file as it is now would report it.
func (f *fixture) locate(file, val string) finding.Location {
	f.t.Helper()
	tf, err := textfile.Decode(read(f.t, f.dir, file))
	if err != nil {
		f.t.Fatalf("decoding %s: %v", file, err)
	}
	i := bytes.Index(tf.Text, []byte(val))
	if i < 0 {
		f.t.Fatalf("%s holds no %q", file, val)
	}
	_, size := utf8.DecodeLastRuneInString(val)
	l := finding.Location{FileHash: tf.Hash}
	l.StartLine, l.StartCol = position(tf.Text, i)
	l.EndLine, l.EndCol = position(tf.Text, i+len(val)-size)
	return l
}

// add adds b with a new ID and finding IDs to the scan's bundles and
// returns the key of each of its locations, as outcomes names it.
func (f *fixture) add(b triage.Bundle) []string {
	f.nextID++
	b.ID = triage.BundleID(f.nextID)
	b.ProjectID = testProject
	b.Preview = "****"
	var keys []string
	for i := range b.Members {
		f.nextID++
		b.Members[i].FindingID = finding.ID(f.nextID)
		for _, l := range b.Members[i].Locations {
			keys = append(keys, key(b.Members[i].File, b.Kind, l.StartLine, l.StartCol))
		}
	}
	f.bundles = append(f.bundles, b)
	if b.Decision.Verdict == triage.VerdictExternalize {
		if f.storeSums == nil {
			f.storeSums = map[string]string{}
		}
		f.storeSums[b.Decision.Name.Config()] = b.Fingerprint
	}
	return keys
}

// secret adds a password externalized as name at the first val in each of
// files.
func (f *fixture) secret(name, val string, files ...string) []string {
	f.t.Helper()
	b := triage.Bundle{
		Fingerprint: sha([]byte(val)), Category: finding.CategoryStorage, Kind: finding.KindPassword,
		Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: triage.Name(name)},
	}
	for _, file := range files {
		b.Members = append(b.Members, triage.Member{File: file, Locations: []finding.Location{f.locate(file, val)}})
	}
	f.values = append(f.values, val)
	return f.add(b)
}

// privateKey adds a private key externalized as name at the first line of
// the PEM block in file.
func (f *fixture) privateKey(name, file string) string {
	f.t.Helper()
	val := pemLine
	f.values = append(f.values, val)
	return f.add(triage.Bundle{
		Fingerprint: sha([]byte(val)), Category: finding.CategorySSH, Kind: finding.KindPrivateKey,
		Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: triage.Name(name)},
		Members:  []triage.Member{{File: file, Locations: []finding.Location{f.locate(file, val)}}},
	})[0]
}

// keyFile adds file as a key file externalized as name, located as the
// scan locates a whole file.
func (f *fixture) keyFile(name, file string) string {
	f.t.Helper()
	raw := read(f.t, f.dir, file)
	l := finding.Location{StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 1, FileHash: sha(raw)}
	if tf, err := textfile.Decode(raw); err == nil && len(tf.Text) > 0 {
		_, size := utf8.DecodeLastRune(tf.Text)
		l.EndLine, l.EndCol = position(tf.Text, len(tf.Text)-size)
	}
	f.values = append(f.values, pemLine)
	return f.add(triage.Bundle{
		Fingerprint: sha(raw), Category: finding.CategorySSH, Kind: finding.KindKeyFile,
		Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: triage.Name(name)},
		Members:  []triage.Member{{File: file, Locations: []finding.Location{l}}},
	})[0]
}

// phoneNumber adds the phone number to redact at its first place in file.
func (f *fixture) phoneNumber(file string) string {
	f.t.Helper()
	val := phone
	l := f.locate(file, val)
	f.values = append(f.values, val)
	f.customer = append(f.customer, file)
	f.spans = append(f.spans, apply.CustomerSpan{
		File: file, StartLine: l.StartLine, StartCol: l.StartCol, EndLine: l.EndLine, EndCol: l.EndCol,
	})
	return f.add(triage.Bundle{
		Fingerprint: sha([]byte(val)), Category: finding.CategoryCustomerData, Kind: finding.KindPhone,
		Decision: triage.Decision{Verdict: triage.VerdictRedact},
		Members:  []triage.Member{{File: file, Locations: []finding.Location{l}}},
	})[0]
}

// input returns what the store gives the runner: run 1 of the project on
// scan 3, requested for the plan of the bundles added.
func (f *fixture) input() apply.Input {
	customer := slices.Clone(f.customer)
	slices.Sort(customer)
	in := apply.Input{
		Run: apply.Run{
			ID: cmpOr(f.run, testRun), ProjectID: testProject, ScanID: testScan, State: apply.StateChecking,
			AllowIrreversible: f.allow, IrreversibleFiles: slices.Clone(f.irreversible),
		},
		Project:           project.Project{ID: testProject, Name: "shop", Path: f.path},
		Scan:              scan.Scan{ID: testScan, ProjectID: testProject, Status: scan.StatusSucceeded},
		NewestScan:        cmpOr(f.newest, testScan),
		Bundles:           f.bundles,
		CustomerDataFiles: slices.Compact(customer),
		CustomerDataSpans: f.spans,
	}
	in.Run.PlanDigest = cmpOr(f.digest, apply.NewPlan(in).Digest)
	return in
}

// cmpOr returns v, or def when v is zero.
func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// check runs Check on the apply-check job of the run under ctx.
func (f *fixture) check(ctx context.Context) error {
	f.t.Helper()
	f.store.in = f.input()
	r := apply.NewRunner(slog.New(slog.NewJSONHandler(&f.logs, nil)), f.store, f.vault, f.opts)
	run := cmpOr(f.run, testRun)
	return r.Check(ctx, job.Job{ID: testJob + int64(run), Kind: job.KindApplyCheck, ApplyID: int64(run), Attempts: 1})
}

// mustCheck runs Check and fails the test unless the check was saved.
func (f *fixture) mustCheck() *savedCheck {
	f.t.Helper()
	err := f.check(f.t.Context())
	if err != nil {
		f.t.Fatalf("Check() error = %v, want nil", err)
	}
	if f.store.failed != nil {
		f.t.Fatalf("Check() failed the run with %q, want the check saved", f.store.failed.Reason)
	}
	if f.store.saved == nil {
		f.t.Fatalf("Check() saved no check; calls %v", f.store.calls)
	}
	f.assertNoValue(err)
	return f.store.saved
}

// assertNoValue fails the test if a value of the example appears in the
// log, in an argument the store received, in an item it keeps, in what the
// vault's calls print, in the manifest, or in err.
func (f *fixture) assertNoValue(err error) {
	f.t.Helper()
	places := map[string]string{
		"log":             f.logs.String(),
		"store arguments": strings.Join(f.store.args, "\n"),
		"stored items":    fmt.Sprintf("%+v", f.store.items),
		"vault arguments": strings.Join(f.vault.calls, "\n"),
	}
	if b, readErr := os.ReadFile(filepath.Join(f.dir, "secrets-manifest.yml")); readErr == nil {
		places["manifest"] = string(b)
	}
	if err != nil {
		places["error"] = err.Error()
	}
	for _, v := range f.values {
		forms := []string{v}
		if b, jsonErr := json.Marshal(v); jsonErr == nil && string(b[1:len(b)-1]) != v {
			forms = append(forms, string(b[1:len(b)-1]))
		}
		for where, text := range places {
			for _, form := range forms {
				if strings.Contains(text, form) {
					f.t.Errorf("the %s holds a value of the example (%d bytes)", where, len(v))
				}
			}
		}
	}
	// The vault receives values only where they belong: each value given to
	// PutStored is the one value of its store key.
	for _, st := range f.vault.stores {
		if want, ok := f.storeSums[st.key]; !ok || st.sum != want {
			f.t.Errorf("PutStored(%s) got bytes that are not the value of that key", st.key)
		}
	}
}

// key names a location in outcomes.
func key(file string, k finding.Kind, line, col int) string {
	return fmt.Sprintf("%s %s %d:%d", file, k, line, col)
}

// outcomes returns, by location, "planned" or "skipped <reason>".
func outcomes(items []apply.Item) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		v := string(it.Status)
		if it.Reason != "" {
			v += " " + string(it.Reason)
		}
		out[key(it.File, it.Kind, it.StartLine, it.StartCol)] = v
	}
	return out
}

const planned = "planned"

// skippedFor returns the outcome of a location skipped for reason.
func skippedFor(reason apply.Reason) string { return "skipped " + string(reason) }

func TestCheck_ChangesNothing(t *testing.T) {
	f := newShop(t, inRepository)
	want := map[string]string{}
	for _, k := range f.secret("db.yaml.password", ymlValue, ymlFile) {
		want[k] = planned
	}
	for _, k := range f.secret("db.xml.password", xmlValue, xmlFile) {
		want[k] = planned
	}
	for _, k := range f.secret("db.euckr.password", euckrValue, euckrFile) {
		want[k] = planned
	}
	for _, k := range f.secret("deploy.ssh.password", shValue, shFile) {
		want[k] = planned
	}
	want[f.phoneNumber(trackedLog)] = planned
	want[f.keyFile("ssh.partner.key", pemFile)] = planned
	want[f.keyFile("ssh.notes.key", notesFile)] = skippedFor(apply.ReasonKeyInSourceFile)
	before := snapshot(t, f.root)

	saved := f.mustCheck()

	if diff := cmp.Diff(before, snapshot(t, f.root)); diff != "" {
		t.Errorf("Check() changed the workspace (-before +after):\n%s", diff)
	}
	if f.vault.reads != 0 || f.vault.puts != 0 || f.vault.deletes != 0 {
		t.Errorf("Check() called ReadStored %d, PutStored %d and DeletePending %d times, want none",
			f.vault.reads, f.vault.puts, f.vault.deletes)
	}
	if len(saved.Blockers) != 0 || len(saved.Dirty) != 0 || len(saved.Irreversible) != 0 {
		t.Errorf("SaveCheck got blockers %v, dirty %v, irreversible %v, want none", saved.Blockers, saved.Dirty, saved.Irreversible)
	}
	if diff := cmp.Diff(want, outcomes(saved.Items)); diff != "" {
		t.Errorf("SaveCheck items (-want +got):\n%s", diff)
	}
	for _, it := range saved.Items {
		if !it.Tracked {
			t.Errorf("SaveCheck item in %s Tracked = false, want true", it.File)
		}
	}
	for _, c := range f.store.calls {
		if c == "BeginFile" || c == "EndFile" || c == "FinishApply" || c == "MarkInManifest" {
			t.Errorf("Check() called %s", c)
		}
	}
}

func TestCheck_Blockers(t *testing.T) {
	tests := []struct {
		name   string
		layout layout
		setup  func(f *fixture)
		want   apply.Blocker
	}{
		{"not a repository", noRepository, func(*fixture) {}, apply.BlockerNotRepository},
		{"no commit", noRepository, func(f *fixture) { worktreetest.Git(f.t, f.dir, "init", "-q") }, apply.BlockerNoCommit},
		{"unsafe configuration", inRepository, func(f *fixture) { f.git("config", "filter.fake.clean", "cat") }, apply.BlockerUnsafeGitConfig},
		{"no git", inRepository, func(f *fixture) { f.opts.Git = "" }, apply.BlockerGitMissing},
		{"no read-write root", inRepository, func(f *fixture) { f.opts.Root = "" }, apply.BlockerWorkspaceReadOnly},
		{"newer scan", inRepository, func(f *fixture) { f.newest = testScan + 1 }, apply.BlockerScanNotLatest},
		{"manifest with a comment", inRepository, func(f *fixture) {
			write(t, f.dir, "secrets-manifest.yml", "# edited by hand\nversion: 1\nentries: []\n")
			f.commit()
		}, apply.BlockerManifestUnreadable},
		{"project folder gone", inRepository, func(f *fixture) {
			if err := os.RemoveAll(f.dir); err != nil {
				t.Fatalf("removing the project folder: %v", err)
			}
		}, apply.BlockerProjectPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, tt.layout)
			f.secret("db.yaml.password", ymlValue, ymlFile)
			tt.setup(f)

			saved := f.mustCheck()

			if !slices.Contains(saved.Blockers, tt.want) {
				t.Errorf("SaveCheck blockers = %v, want %q among them", saved.Blockers, tt.want)
			}
		})
	}
}

// TestCheck_NothingPlanned checks that a check whose every location turns
// out skipped once the files are read is blocked like one with no verdict
// to apply, and that customer data in an untracked file, which an apply
// redacts with the user's consent, counts as something to apply.
func TestCheck_NothingPlanned(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture)
		want  bool
	}{
		{"every location skipped", func(f *fixture) {
			f.secret("db.vendor.password", vendorValue, vendorFile)
			f.keyFile("ssh.notes.key", notesFile)
		}, true},
		{"a skipped and a planned location", func(f *fixture) {
			f.secret("db.vendor.password", vendorValue, vendorFile)
			f.secret("db.yaml.password", ymlValue, ymlFile)
		}, false},
		{"customer data in an untracked file", func(f *fixture) { f.phoneNumber(untrackLog) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			tt.setup(f)

			saved := f.mustCheck()

			if got := slices.Contains(saved.Blockers, apply.BlockerNothingToApply); got != tt.want {
				t.Errorf("SaveCheck blockers = %v, items %v; want %q among them: %v",
					saved.Blockers, outcomes(saved.Items), apply.BlockerNothingToApply, tt.want)
			}
		})
	}
}

func TestCheck_DirtyTree(t *testing.T) {
	t.Run("sixty tracked files", func(t *testing.T) {
		f := newShop(t, inRepository)
		var want []string
		for i := range 60 {
			rel := fmt.Sprintf("gen/file%02d.txt", i)
			write(t, f.dir, rel, "generated\n")
			want = append(want, rel)
		}
		f.commit()
		for _, rel := range want {
			write(t, f.dir, rel, "changed\n")
		}
		f.secret("db.yaml.password", ymlValue, ymlFile)

		saved := f.mustCheck()

		if !slices.Contains(saved.Blockers, apply.BlockerDirty) {
			t.Errorf("SaveCheck blockers = %v, want dirty", saved.Blockers)
		}
		if diff := cmp.Diff(want, saved.Dirty); diff != "" {
			t.Errorf("SaveCheck dirty (-want +got):\n%s", diff)
		}
	})
	t.Run("a file that is no target", func(t *testing.T) {
		f := newShop(t, inRepository)
		f.secret("db.yaml.password", ymlValue, ymlFile)
		write(t, f.dir, "pom.xml", "<project/>\n")

		saved := f.mustCheck()

		if !slices.Contains(saved.Blockers, apply.BlockerDirty) {
			t.Errorf("SaveCheck blockers = %v, want dirty", saved.Blockers)
		}
		if diff := cmp.Diff([]string{"pom.xml"}, saved.Dirty); diff != "" {
			t.Errorf("SaveCheck dirty (-want +got):\n%s", diff)
		}
	})
	t.Run("a change outside the project folder", func(t *testing.T) {
		f := newShop(t, inSubfolder)
		f.secret("db.yaml.password", ymlValue, ymlFile)
		write(t, f.repo, "outside.txt", "changed\n")

		saved := f.mustCheck()

		if slices.Contains(saved.Blockers, apply.BlockerDirty) || len(saved.Dirty) != 0 {
			t.Errorf("SaveCheck blockers = %v, dirty = %v, want no dirty", saved.Blockers, saved.Dirty)
		}
	})
}

func TestCheck_SkipReasons(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture) string // returns the key of the location
		want  apply.Reason
		// notSent is a value that must not reach Fingerprints.
		notSent func(f *fixture) []byte
	}{
		{name: "changed and committed", want: apply.ReasonFileChanged, setup: func(f *fixture) string {
			k := f.secret("db.yaml.password", ymlValue, ymlFile)[0]
			write(f.t, f.dir, ymlFile, string(read(f.t, f.dir, ymlFile))+"# changed\n")
			f.commit()
			return k
		}},
		{name: "removed and committed", want: apply.ReasonFileMissing, setup: func(f *fixture) string {
			k := f.secret("db.jdbc.password", jdbcValue, jdbcFile)[0]
			f.git("rm", "-q", jdbcFile)
			f.commit()
			return k
		}},
		{name: "not text", want: apply.ReasonNotText, setup: func(f *fixture) string {
			raw := read(f.t, f.dir, "data/blob.bin")
			return f.add(triage.Bundle{
				Fingerprint: "fp-blob", Category: finding.CategoryStorage, Kind: finding.KindPassword,
				Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.blob.password"},
				Members: []triage.Member{{File: "data/blob.bin", Locations: []finding.Location{
					{StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 1, FileHash: sha(raw)},
				}}},
			})[0]
		}},
		{name: "fingerprint differs", want: apply.ReasonValueMismatch, setup: func(f *fixture) string {
			f.vault.lie[sha([]byte(ymlValue))] = "fp-other"
			return f.secret("db.yaml.password", ymlValue, ymlFile)[0]
		}},
		{name: "nested repository", want: apply.ReasonNestedRepository, setup: func(f *fixture) string {
			return f.secret("db.vendor.password", vendorValue, vendorFile)[0]
		}},
		{
			name: "key file too large", want: apply.ReasonTooLarge,
			setup: func(f *fixture) string {
				f.opts.MaxStoredBytes = 100
				return f.keyFile("ssh.partner.key", pemFile)
			},
			notSent: func(f *fixture) []byte { return read(f.t, f.dir, pemFile) },
		},
		{name: "key in a note", want: apply.ReasonKeyInSourceFile, setup: func(f *fixture) string {
			return f.keyFile("ssh.notes.key", notesFile)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			k := tt.setup(f)

			saved := f.mustCheck()

			if got := outcomes(saved.Items)[k]; got != skippedFor(tt.want) {
				t.Errorf("SaveCheck outcome of %s = %q, want %q", k, got, skippedFor(tt.want))
			}
			if tt.notSent != nil && slices.Contains(f.vault.seen, sha(tt.notSent(f))) {
				t.Errorf("Fingerprints got the value of %s, want it not sent", k)
			}
		})
	}
}

func TestCheck_KeyFileSkipTakesFile(t *testing.T) {
	tests := []struct {
		name       string
		file       string
		setup      func(f *fixture)
		wantKey    string
		wantOthers string
	}{
		{
			name: "too large", file: pemFile, setup: func(f *fixture) { f.opts.MaxStoredBytes = 100 },
			wantKey: skippedFor(apply.ReasonTooLarge), wantOthers: skippedFor(apply.ReasonTooLarge),
		},
		{
			name: "fingerprint differs", file: pemFile,
			setup:   func(f *fixture) { f.vault.lie[sha(read(f.t, f.dir, pemFile))] = "fp-other" },
			wantKey: skippedFor(apply.ReasonValueMismatch), wantOthers: skippedFor(apply.ReasonValueMismatch),
		},
		{
			name: "key in a note", file: notesFile, setup: func(*fixture) {},
			wantKey: skippedFor(apply.ReasonKeyInSourceFile), wantOthers: planned,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			tt.setup(f)
			whole := f.keyFile("ssh.partner.key", tt.file)
			block := f.privateKey("ssh.partner.block", tt.file)

			saved := f.mustCheck()

			want := map[string]string{whole: tt.wantKey, block: tt.wantOthers}
			if diff := cmp.Diff(want, outcomes(saved.Items)); diff != "" {
				t.Errorf("SaveCheck items (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCheck_IrreversibleFiles(t *testing.T) {
	f := newShop(t, inRepository)
	untracked := f.phoneNumber(untrackLog)
	tracked := f.phoneNumber(trackedLog)
	secret := f.secret("db.local.password", localValue, localFile)[0]

	saved := f.mustCheck()

	if diff := cmp.Diff([]string{untrackLog}, saved.Irreversible); diff != "" {
		t.Errorf("SaveCheck irreversible files (-want +got):\n%s", diff)
	}
	want := map[string]string{untracked: planned, tracked: planned, secret: planned}
	if diff := cmp.Diff(want, outcomes(saved.Items)); diff != "" {
		t.Errorf("SaveCheck items (-want +got):\n%s", diff)
	}
	wantTracked := map[string]bool{untrackLog: false, trackedLog: true, localFile: false}
	for _, it := range saved.Items {
		if it.Tracked != wantTracked[it.File] {
			t.Errorf("SaveCheck item in %s Tracked = %v, want %v", it.File, it.Tracked, wantTracked[it.File])
		}
	}
}

func TestCheck_ReconcilesOrphans(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("deploy.ssh.password", shValue, shFile)

	// The earlier run rewrote the YAML file and removed the key file before
	// it failed; it announced a change of the properties file that did not
	// happen.
	write(t, f.dir, ymlFile, "spring: {}\n")
	if err := os.Remove(filepath.Join(f.dir, pemFile)); err != nil {
		t.Fatalf("removing the key file: %v", err)
	}
	tempYML := ymlFile + ".se-apply-0123456789abcdef.tmp"
	tempJDBC := jdbcFile + ".se-apply-fedcba9876543210.tmp"
	stray := "pom.xml.se-apply-0011223344556677.tmp"
	for _, tmp := range []string{tempYML, tempJDBC, stray} {
		write(t, f.dir, tmp, "temporary\n")
	}
	wrote := apply.OpenFile{Run: 9, File: ymlFile, AfterHash: sha(read(t, f.dir, ymlFile)), TempName: tempYML}
	notWritten := apply.OpenFile{Run: 9, File: jdbcFile, AfterHash: strings.Repeat("a", 64), TempName: tempJDBC}
	removed := apply.OpenFile{Run: 9, File: pemFile}
	f.store.open = []apply.OpenFile{wrote, notWritten, removed}

	saved := f.mustCheck()

	want := []resolvedFile{{wrote, true}, {notWritten, false}, {removed, true}}
	if diff := cmp.Diff(want, f.store.resolved); diff != "" {
		t.Errorf("ResolveFile calls (-want +got):\n%s", diff)
	}
	for _, tmp := range []string{tempYML, tempJDBC} {
		if _, err := os.Lstat(filepath.Join(f.dir, tmp)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(%s) error = %v, want the recorded temporary file removed", tmp, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(f.dir, stray)); err != nil {
		t.Errorf("Lstat(%s) error = %v, want the unrecorded temporary file kept", stray, err)
	}
	if slices.Contains(saved.Blockers, apply.BlockerDirty) || len(saved.Dirty) != 0 {
		t.Errorf("SaveCheck blockers = %v, dirty = %v, want the reconciled files not dirty", saved.Blockers, saved.Dirty)
	}
	if i := slices.Index(f.store.calls, "ResolveFile"); i < 0 || slices.Index(f.store.calls[i:], "ApplyInput") < 0 {
		t.Errorf("calls = %v, want ApplyInput read again after ResolveFile", f.store.calls)
	}
}

func TestCheck_PlanChanged(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.digest = strings.Repeat("0", 64)

	err := f.check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailurePlanChanged {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailurePlanChanged)
	}
	if f.store.saved != nil {
		t.Errorf("SaveCheck was called, want the run failed")
	}
	f.assertNoValue(err)
}

func TestCheck_TimeLimit(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.opts.CheckLimit = 50 * time.Millisecond
	f.store.hooks["WrittenFiles"] = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	write(t, f.dir, "pom.xml", "<project/>\n") // so that the check asks for written files

	err := f.check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureTimeLimit {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailureTimeLimit)
	}
	if f.store.saved != nil {
		t.Errorf("SaveCheck was called, want the run failed")
	}
	f.assertNoValue(err)
}

func TestCheck_Canceled(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.store.hooks["OpenFiles"] = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}

	err := f.check(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Check() error = %v, want context.Canceled", err)
	}
	if f.store.failed != nil || f.store.saved != nil {
		t.Errorf("Check() failed %+v and saved %+v, want the run left as it is", f.store.failed, f.store.saved)
	}
	f.assertNoValue(err)
}

func TestCheck_LeaseLost(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.store.errs["SaveCheck"] = fmt.Errorf("closing the job: %w", job.ErrLeaseLost)

	err := f.check(t.Context())
	if !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("Check() error = %v, want job.ErrLeaseLost", err)
	}
	if f.store.failed != nil {
		t.Errorf("FailApply = %+v, want the run left as it is", f.store.failed)
	}
}

func TestCheck_SecretStoreUnreachable(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.vault.err = errors.New("fake vault refused the request")

	err := f.check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureSecretStore {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailureSecretStore)
	}
	f.assertNoValue(err)
}

func TestCheck_NoValueOutside(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture)
	}{
		{"every kind of target", func(f *fixture) {
			f.secret("db.yaml.password", ymlValue, ymlFile)
			f.secret("db.xml.password", xmlValue, xmlFile)
			f.secret("db.euckr.password", euckrValue, euckrFile)
			f.secret("db.local.password", localValue, localFile)
			f.secret("db.vendor.password", vendorValue, vendorFile)
			f.phoneNumber(untrackLog)
			f.phoneNumber(trackedLog)
			f.keyFile("ssh.partner.key", pemFile)
			f.privateKey("ssh.partner.block", pemFile)
			f.keyFile("ssh.notes.key", notesFile)
		}},
		{"fingerprints differ", func(f *fixture) {
			f.vault.lie[sha([]byte(ymlValue))] = "fp-other"
			f.vault.lie[sha([]byte(euckrValue))] = "fp-other"
			f.secret("db.yaml.password", ymlValue, ymlFile)
			f.secret("db.euckr.password", euckrValue, euckrFile)
		}},
		{"values too large", func(f *fixture) {
			f.opts.MaxStoredBytes = 10
			f.secret("db.yaml.password", ymlValue, ymlFile)
			f.keyFile("ssh.partner.key", pemFile)
		}},
		{"secret store unreachable", func(f *fixture) {
			f.vault.err = errors.New("fake vault refused the request")
			f.secret("db.yaml.password", ymlValue, ymlFile)
		}},
		{"files changed", func(f *fixture) {
			f.secret("db.yaml.password", ymlValue, ymlFile)
			f.secret("db.jdbc.password", jdbcValue, jdbcFile)
			write(f.t, f.dir, ymlFile, "password: "+ymlValue+"-changed\n")
			f.git("rm", "-q", jdbcFile)
			f.commit()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			tt.setup(f)

			err := f.check(t.Context())

			if f.store.saved == nil && f.store.failed == nil {
				t.Errorf("Check() = %v, saved nothing and failed nothing", err)
			}
			f.assertNoValue(err)
		})
	}
}

func TestCheck_ReconcileRemovesTempFirst(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("deploy.ssh.password", shValue, shFile)
	tmp := jdbcFile + ".se-apply-0123456789abcdef.tmp"
	write(t, f.dir, tmp, "temporary\n")
	record := apply.OpenFile{Run: 9, File: jdbcFile, AfterHash: strings.Repeat("a", 64), TempName: tmp}
	f.store.open = []apply.OpenFile{record}
	f.store.errs["ResolveFile"] = errors.New("fake database refused the request")

	err := f.check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureOther {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailureOther)
	}
	if _, err := os.Lstat(filepath.Join(f.dir, tmp)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(%s) error = %v, want the temporary file removed before the record is closed", tmp, err)
	}
	if diff := cmp.Diff([]apply.OpenFile{record}, f.store.open); diff != "" {
		t.Errorf("open records (-want +got):\n%s", diff)
	}
	f.assertNoValue(err)
}

func TestCheck_ReconcileLeavesOddTemp(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("deploy.ssh.password", shValue, shFile)
	linkTemp := jdbcFile + ".se-apply-0123456789abcdef.tmp"
	folderTemp := ymlFile + ".se-apply-fedcba9876543210.tmp"
	if err := os.Symlink("jdbc.properties", filepath.Join(f.dir, linkTemp)); err != nil {
		t.Fatalf("making a link: %v", err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, folderTemp), 0o750); err != nil {
		t.Fatalf("making a folder: %v", err)
	}
	link := apply.OpenFile{Run: 9, File: jdbcFile, AfterHash: strings.Repeat("a", 64), TempName: linkTemp}
	folder := apply.OpenFile{Run: 9, File: ymlFile, AfterHash: strings.Repeat("b", 64), TempName: folderTemp}
	f.store.open = []apply.OpenFile{link, folder}

	f.mustCheck()

	if diff := cmp.Diff([]resolvedFile{{link, false}, {folder, false}}, f.store.resolved); diff != "" {
		t.Errorf("ResolveFile calls (-want +got):\n%s", diff)
	}
	for _, tmp := range []string{linkTemp, folderTemp} {
		if _, err := os.Lstat(filepath.Join(f.dir, tmp)); err != nil {
			t.Errorf("Lstat(%s) error = %v, want it left in place", tmp, err)
		}
	}
}

func TestCheck_ReconcileNotDone(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("deploy.ssh.password", shValue, shFile)
	if err := os.Remove(filepath.Join(f.dir, jdbcFile)); err != nil {
		t.Fatalf("removing %s: %v", jdbcFile, err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, jdbcFile), 0o750); err != nil {
		t.Fatalf("making a folder: %v", err)
	}
	if err := os.Remove(filepath.Join(f.dir, ymlFile)); err != nil {
		t.Fatalf("removing %s: %v", ymlFile, err)
	}
	if err := os.Symlink("jdbc.properties", filepath.Join(f.dir, ymlFile)); err != nil {
		t.Fatalf("making a link: %v", err)
	}
	present := apply.OpenFile{Run: 9, File: pemFile}
	folder := apply.OpenFile{Run: 9, File: jdbcFile, AfterHash: strings.Repeat("a", 64)}
	link := apply.OpenFile{Run: 9, File: ymlFile, AfterHash: strings.Repeat("b", 64)}
	f.store.open = []apply.OpenFile{present, folder, link}

	f.mustCheck()

	want := []resolvedFile{{present, false}, {folder, false}, {link, false}}
	if diff := cmp.Diff(want, f.store.resolved); diff != "" {
		t.Errorf("ResolveFile calls (-want +got):\n%s", diff)
	}
}

func TestCheck_DirtyDespiteRecords(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("deploy.ssh.password", shValue, shFile)
	write(t, f.dir, ymlFile, "spring: {}\n")
	f.store.written[ymlFile] = []apply.Written{{AfterHash: strings.Repeat("a", 64)}}
	write(t, f.dir, pemFile, "-----BEGIN RSA PRIVATE KEY-----\n-----END RSA PRIVATE KEY-----\n")
	f.store.written[pemFile] = []apply.Written{{AfterHash: ""}}

	saved := f.mustCheck()

	if !slices.Contains(saved.Blockers, apply.BlockerDirty) {
		t.Errorf("SaveCheck blockers = %v, want dirty", saved.Blockers)
	}
	if diff := cmp.Diff([]string{pemFile, ymlFile}, saved.Dirty); diff != "" {
		t.Errorf("SaveCheck dirty (-want +got):\n%s", diff)
	}
}

func TestCheck_DirtyUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file without permission")
	}
	f := newShop(t, inRepository)
	f.secret("deploy.ssh.password", shValue, shFile)
	full := filepath.Join(f.dir, filepath.FromSlash(ymlFile))
	write(t, f.dir, ymlFile, "spring: {}\n")
	f.store.written[ymlFile] = []apply.Written{{AfterHash: sha(read(t, f.dir, ymlFile))}}
	if err := os.Chmod(full, 0); err != nil {
		t.Fatalf("taking away read permission: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(full, 0o600) }) // lets t.TempDir remove it; nothing to report if it fails

	saved := f.mustCheck()

	if diff := cmp.Diff([]string{ymlFile}, saved.Dirty); diff != "" {
		t.Errorf("SaveCheck dirty (-want +got):\n%s", diff)
	}
}

func TestCheck_DirtySubtractedInSubfolder(t *testing.T) {
	f := newShop(t, inSubfolder)
	f.secret("deploy.ssh.password", shValue, shFile)
	write(t, f.dir, ymlFile, "spring: {}\n")
	f.store.written[ymlFile] = []apply.Written{{AfterHash: sha(read(t, f.dir, ymlFile))}}

	saved := f.mustCheck()

	if slices.Contains(saved.Blockers, apply.BlockerDirty) || len(saved.Dirty) != 0 {
		t.Errorf("SaveCheck blockers = %v, dirty = %v, want the file the apply wrote not dirty", saved.Blockers, saved.Dirty)
	}
}

func TestCheck_SpanPastEnd(t *testing.T) {
	f := newShop(t, inRepository)
	l := f.locate(ymlFile, ymlValue)
	l.EndLine, l.EndCol = 999, 1
	f.values = append(f.values, ymlValue)
	k := f.add(triage.Bundle{
		Fingerprint: sha([]byte(ymlValue)), Category: finding.CategoryStorage, Kind: finding.KindPassword,
		Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.yaml.password"},
		Members:  []triage.Member{{File: ymlFile, Locations: []finding.Location{l}}},
	})[0]

	saved := f.mustCheck()

	if got := outcomes(saved.Items)[k]; got != skippedFor(apply.ReasonValueMismatch) {
		t.Errorf("SaveCheck outcome of %s = %q, want %q", k, got, skippedFor(apply.ReasonValueMismatch))
	}
}

func TestCheck_ReconcileTempElsewhere(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("deploy.ssh.password", shValue, shFile)
	elsewhere := "pom.xml.se-apply-0123456789abcdef.tmp"
	write(t, f.dir, elsewhere, "not beside the recorded file\n")
	record := apply.OpenFile{Run: 9, File: jdbcFile, AfterHash: strings.Repeat("a", 64), TempName: elsewhere}
	f.store.open = []apply.OpenFile{record}

	f.mustCheck()

	if _, err := os.Lstat(filepath.Join(f.dir, elsewhere)); err != nil {
		t.Errorf("Lstat(%s) error = %v, want a temporary file outside the record's folder left in place", elsewhere, err)
	}
	if diff := cmp.Diff([]resolvedFile{{record, false}}, f.store.resolved); diff != "" {
		t.Errorf("ResolveFile calls (-want +got):\n%s", diff)
	}
}

func TestCheck_ReconcileBadTempName(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.jdbc.password", jdbcValue, jdbcFile)
	bad := jdbcFile + ".bak"
	write(t, f.dir, bad, "kept\n")
	record := apply.OpenFile{Run: 9, File: jdbcFile, AfterHash: strings.Repeat("a", 64), TempName: bad}
	f.store.open = []apply.OpenFile{record}

	err := f.check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureOther {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailureOther)
	}
	if f.store.saved != nil || len(f.store.resolved) != 0 {
		t.Errorf("Check() saved %+v and resolved %+v, want nothing planned or settled", f.store.saved, f.store.resolved)
	}
	if diff := cmp.Diff([]apply.OpenFile{record}, f.store.open); diff != "" {
		t.Errorf("open records (-want +got):\n%s", diff)
	}
	if _, err := os.Lstat(filepath.Join(f.dir, bad)); err != nil {
		t.Errorf("Lstat(%s) error = %v, want it left in place", bad, err)
	}
	f.assertNoValue(err)
}
