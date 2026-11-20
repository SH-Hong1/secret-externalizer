package apply_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/text/encoding/korean"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/manifest"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// mainValue is the one password three files of TestApply_ReplacesAndStores
// share.
const mainValue = "dev-only-fake-db-main-password"

// errCrash is what a call answers with when the worker stops during it.
var errCrash = errors.New("fake: the worker stopped")

// errRefused is what a vault or store call refused by a test answers with.
var errRefused = errors.New("fake: the request was refused")

// apply runs Apply on the apply job of the run under ctx.
func (f *fixture) apply(ctx context.Context) error {
	f.t.Helper()
	in := f.input()
	in.Run.State = apply.StateRunning
	f.store.in = in
	r := apply.NewRunner(slog.New(slog.NewJSONHandler(&f.logs, nil)), f.store, f.vault, f.opts)
	run := cmpOr(f.run, testRun)
	return r.Apply(ctx, job.Job{ID: testJob + int64(run), Kind: job.KindApply, ApplyID: int64(run), Attempts: 1})
}

// mustApply runs Apply and fails the test unless the run succeeded.
func (f *fixture) mustApply() {
	f.t.Helper()
	err := f.apply(f.t.Context())
	if err != nil {
		f.t.Fatalf("Apply() error = %v, want nil", err)
	}
	if f.store.failed != nil {
		f.t.Fatalf("Apply() failed the run with %q, want it finished", f.store.failed.Reason)
	}
	if got := f.store.state[cmpOr(f.run, testRun)]; got != apply.StateSucceeded {
		f.t.Fatalf("Apply() left the run %q, want succeeded; calls %v", got, f.store.calls)
	}
	f.assertNoValue(err)
}

// items returns the outcome of each location of the run as the store keeps
// it.
func (f *fixture) items() map[string]string {
	f.t.Helper()
	return outcomes(f.store.runItems(cmpOr(f.run, testRun)))
}

// crash makes the n-th call of method stop the worker: it ends the run's
// context through cancel and fails, before the call takes effect when
// before is set and after it otherwise.
func (f *fixture) crash(cancel context.CancelFunc, method string, n int, before bool) {
	calls := 0
	hook := func(context.Context) error {
		calls++
		if calls != n {
			return nil
		}
		cancel()
		return errCrash
	}
	if before {
		f.store.hooks[method] = hook
	} else {
		f.store.after[method] = hook
	}
}

// crashThenApply runs Apply under a context that crash ends, expects it to
// stop with an error and leave the run running, and runs Apply again.
func (f *fixture) crashThenApply(setup func(cancel context.CancelFunc)) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(f.t.Context())
	defer cancel()
	setup(cancel)
	if err := f.apply(ctx); err == nil {
		f.t.Fatalf("Apply() with the worker stopping = nil, want an error")
	}
	if f.store.failed != nil || f.store.state[cmpOr(f.run, testRun)] != apply.StateRunning {
		f.t.Fatalf("Apply() with the worker stopping left the run %q (failed %+v), want running",
			f.store.state[cmpOr(f.run, testRun)], f.store.failed)
	}
	f.store.hooks = map[string]func(context.Context) error{}
	f.store.after = map[string]func(context.Context) error{}
	f.mustApply()
}

// valueBundle adds a bundle of kind k in category c with decision d at the
// first val in each of files.
func (f *fixture) valueBundle(c finding.Category, k finding.Kind, d triage.Decision, val string, files ...string) []string {
	f.t.Helper()
	b := triage.Bundle{Fingerprint: sha([]byte(val)), Category: c, Kind: k, Decision: d}
	for _, file := range files {
		b.Members = append(b.Members, triage.Member{File: file, Locations: []finding.Location{f.locate(file, val)}})
	}
	return f.add(b)
}

// customerData adds customer data of kind k at the first val in file, with
// decision d.
func (f *fixture) customerData(k finding.Kind, d triage.Decision, val, file string) string {
	f.t.Helper()
	l := f.locate(file, val)
	f.values = append(f.values, val)
	f.customer = append(f.customer, file)
	if d.Verdict != triage.VerdictFalsePositive {
		f.spans = append(f.spans, apply.CustomerSpan{
			File: file, StartLine: l.StartLine, StartCol: l.StartCol, EndLine: l.EndLine, EndCol: l.EndCol,
		})
	}
	return f.valueBundle(finding.CategoryCustomerData, k, d, val, file)[0]
}

// replaceIn writes file with every old replaced by with.
func (f *fixture) replaceIn(file, old, with string) {
	f.t.Helper()
	b := read(f.t, f.dir, file)
	if !bytes.Contains(b, []byte(old)) {
		f.t.Fatalf("%s holds no %q", file, old)
	}
	write(f.t, f.dir, file, string(bytes.ReplaceAll(b, []byte(old), []byte(with))))
}

// readManifest parses the manifest in the project folder.
func (f *fixture) readManifest() manifest.Manifest {
	f.t.Helper()
	m, err := manifest.Parse(read(f.t, f.dir, manifest.FileName))
	if err != nil {
		f.t.Fatalf("manifest.Parse() error = %v", err)
	}
	return m
}

// exists reports whether rel is in the project folder.
func (f *fixture) exists(rel string) bool {
	f.t.Helper()
	_, err := os.Lstat(filepath.Join(f.dir, filepath.FromSlash(rel)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		f.t.Fatalf("Lstat(%s) error = %v", rel, err)
	}
	return err == nil
}

// storePath is the store path of key in the test project.
func storePath(key string) string { return bao.StorePath(int64(testProject), key) }

// change is a range of a file's bytes and what replaces it.
type change struct{ old, new string }

// assertOutside fails the test unless after is before with, for each of
// spans in order, its first old bytes after the previous one replaced by its
// new text: every byte outside the spans is the same.
func assertOutside(t *testing.T, before, after []byte, spans []change) {
	t.Helper()
	var want []byte
	rest := before
	for _, s := range spans {
		i := bytes.Index(rest, []byte(s.old))
		if i < 0 {
			t.Fatalf("assertOutside: the file before holds no %q in order", s.old)
		}
		want = append(want, rest[:i]...)
		want = append(want, s.new...)
		rest = rest[i+len(s.old):]
	}
	want = append(want, rest...)
	if diff := cmp.Diff(string(want), string(after)); diff != "" {
		t.Errorf("file after the apply (-want +got):\n%s", diff)
	}
}

const applied = "applied"

// failedFor returns the outcome of a location that failed for reason.
func failedFor(reason apply.Reason) string { return "failed " + string(reason) }

// all returns want with every key in keys set to outcome.
func all(want map[string]string, outcome string, keys ...string) map[string]string {
	for _, k := range keys {
		want[k] = outcome
	}
	return want
}

// lineOf returns the line val starts on in file now.
func (f *fixture) lineOf(file, val string) int {
	f.t.Helper()
	return f.locate(file, val).StartLine
}

func TestApply_ReplacesAndStores(t *testing.T) {
	f := newShop(t, inRepository)
	f.replaceIn(ymlFile, ymlValue, mainValue)
	f.replaceIn(xmlFile, xmlValue, mainValue)
	f.replaceIn(shFile, shValue, mainValue)
	f.commit()
	before := map[string][]byte{}
	lines := map[string]int{}
	for _, file := range []string{ymlFile, xmlFile, shFile} {
		before[file] = read(t, f.dir, file)
		lines[file] = f.lineOf(file, mainValue)
	}
	keys := f.secret("db.main.password", mainValue, ymlFile, xmlFile, shFile)

	f.mustApply()

	assertOutside(t, before[ymlFile], read(t, f.dir, ymlFile), []change{{mainValue, "${ext.db.main.password}"}})
	assertOutside(t, before[xmlFile], read(t, f.dir, xmlFile), []change{{mainValue, "${ext.db.main.password}"}})
	assertOutside(t, before[shFile], read(t, f.dir, shFile), []change{{mainValue, "${EXT_DB_MAIN_PASSWORD}"}})
	if len(f.vault.stored) != 1 || string(f.vault.value("ext.db.main.password")) != mainValue {
		t.Errorf("vault holds %d paths, ext.db.main.password holding the value: %v; want that one path with the value",
			len(f.vault.stored), string(f.vault.value("ext.db.main.password")) == mainValue)
	}
	if diff := cmp.Diff(all(map[string]string{}, applied, keys...), f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	store := storePath("ext.db.main.password")
	want := []manifest.Entry{
		{
			Name: "EXT_DB_MAIN_PASSWORD", Category: finding.CategoryStorage, Action: manifest.ActionExternalized, Store: store,
			Locations: []manifest.Location{{File: shFile, Line: lines[shFile]}},
		},
		{
			Name: "ext.db.main.password", Category: finding.CategoryStorage, Action: manifest.ActionExternalized, Store: store,
			Locations: []manifest.Location{{File: ymlFile, Line: lines[ymlFile]}, {File: xmlFile, Line: lines[xmlFile]}},
		},
	}
	if diff := cmp.Diff(want, f.readManifest().Entries); diff != "" {
		t.Errorf("manifest entries (-want +got):\n%s", diff)
	}
	for _, it := range f.store.runItems(testRun) {
		if it.StoreVersion != 1 || it.Line != lines[it.File] {
			t.Errorf("item in %s has store version %d, line %d; want 1, %d", it.File, it.StoreVersion, it.Line, lines[it.File])
		}
	}
}

func TestApply_UndecidedBytesUntouched(t *testing.T) {
	const (
		file   = "conf/line.properties"
		chosen = "dev-only-fake-aaaa"
	)
	f := newShop(t, inRepository)
	write(t, f.dir, file, "a="+chosen+" b=dev-only-fake-bbbb c=dev-only-fake-cccc d=dev-only-fake-dddd\n")
	f.commit()
	before := read(t, f.dir, file)
	ext := triage.Decision{Verdict: triage.VerdictExternalize, Name: "line.a.password"}
	k := f.valueBundle(finding.CategoryStorage, finding.KindPassword, ext, chosen, file)[0]
	f.valueBundle(finding.CategoryStorage, finding.KindPassword, triage.Decision{}, "dev-only-fake-bbbb", file)
	f.valueBundle(finding.CategoryStorage, finding.KindPassword,
		triage.Decision{Verdict: triage.VerdictFalsePositive}, "dev-only-fake-cccc", file)
	f.valueBundle(finding.CategoryStorage, finding.KindPassword,
		triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm}, "dev-only-fake-dddd", file)

	f.mustApply()

	assertOutside(t, before, read(t, f.dir, file), []change{{chosen, "${ext.line.a.password}"}})
	if diff := cmp.Diff(map[string]string{k: applied}, f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
}

func TestApply_PartialFile(t *testing.T) {
	const (
		file    = "conf/db.properties"
		host    = "db.invalid"
		port    = "15210"
		account = "dev-only-fake-user"
		dbValue = "dev-only-fake-db-pw"
	)
	f := newShop(t, inRepository)
	write(t, f.dir, file, "url=jdbc:oracle:thin:"+account+"/"+dbValue+"@"+host+":"+port+"/SHOP\n")
	f.commit()
	before := read(t, f.dir, file)
	ext := func(name string) triage.Decision {
		return triage.Decision{Verdict: triage.VerdictExternalize, Name: triage.Name(name)}
	}
	kh := f.valueBundle(finding.CategoryStorage, finding.KindHost, ext("db.main.host"), host, file)[0]
	kp := f.valueBundle(finding.CategoryStorage, finding.KindPort, ext("db.main.port"), port, file)[0]
	ka := f.valueBundle(finding.CategoryStorage, finding.KindAccount, ext("db.main.account"), account, file)[0]
	kw := f.secret("db.main.password", dbValue, file)[0]
	f.vault.putErrs["ext.db.main.password"] = []error{errRefused}

	f.mustApply()

	assertOutside(t, before, read(t, f.dir, file), []change{
		{account, "${ext.db.main.account}"}, {host, "${ext.db.main.host}"}, {port, "${ext.db.main.port}"},
	})
	want := map[string]string{kh: applied, kp: applied, ka: applied, kw: failedFor(apply.ReasonStoreFailed)}
	if diff := cmp.Diff(want, f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	if f.vault.value("ext.db.main.password") != nil {
		t.Errorf("vault holds ext.db.main.password, want nothing stored")
	}
}

func TestApply_StoreFailureLeavesSource(t *testing.T) {
	f := newShop(t, inRepository)
	var keys []string
	keys = append(keys, f.secret("db.yaml.password", ymlValue, ymlFile)...)
	keys = append(keys, f.secret("db.xml.password", xmlValue, xmlFile)...)
	keys = append(keys, f.secret("db.euckr.password", euckrValue, euckrFile)...)
	keys = append(keys, f.secret("deploy.ssh.password", shValue, shFile)...)
	keys = append(keys, f.keyFile("ssh.partner.key", pemFile))
	for _, key := range []string{
		"ext.db.yaml.password", "ext.db.xml.password", "ext.db.euckr.password",
		"ext.deploy.ssh.password", "ext.ssh.partner.key",
	} {
		f.vault.putErrs[key] = []error{errRefused}
	}
	before := snapshot(t, f.root)

	f.mustApply()

	if diff := cmp.Diff(before, snapshot(t, f.root)); diff != "" {
		t.Errorf("Apply() changed the workspace (-before +after):\n%s", diff)
	}
	if f.exists(manifest.FileName) {
		t.Errorf("Apply() wrote a manifest, want none")
	}
	if diff := cmp.Diff(all(map[string]string{}, failedFor(apply.ReasonStoreFailed), keys...), f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	if n := f.store.begins(); n != 0 {
		t.Errorf("BeginFile was called %d times, want none", n)
	}
}

func TestApply_VerifyMismatchLeavesSource(t *testing.T) {
	const key = "ext.db.yaml.password"
	other := []byte("dev-only-fake-other-value")
	tests := []struct {
		name   string
		setup  func(f *fixture)
		before int // the version at the path before the apply
	}{
		{name: "nothing read back", setup: func(f *fixture) {
			f.vault.onRead = func(_ string, call int, st bao.Stored, err error) (bao.Stored, error) {
				if call == 2 {
					return bao.Stored{}, fmt.Errorf("fake vault: %w", bao.ErrNotStored)
				}
				return st, err
			}
		}},
		{name: "another version read back", setup: func(f *fixture) {
			f.vault.onRead = func(_ string, call int, st bao.Stored, err error) (bao.Stored, error) {
				if call == 2 {
					st.Version++
				}
				return st, err
			}
		}},
		{name: "other bytes read back", setup: func(f *fixture) {
			f.vault.onRead = func(_ string, call int, st bao.Stored, err error) (bao.Stored, error) {
				if call == 2 {
					st.Value = bao.Value(slices.Clone(other))
				}
				return st, err
			}
		}},
		{
			// The record's fingerprint field would name the bundle's
			// fingerprint; only the bytes count, so the apply writes a new
			// version and reads the same record back.
			name: "a record that only claims the value", before: 1,
			setup: func(f *fixture) {
				f.vault.stored[key] = &fakeRecord{versions: [][]byte{slices.Clone(other)}, file: []bool{false}}
				f.vault.onRead = func(_ string, _ int, st bao.Stored, err error) (bao.Stored, error) {
					if err == nil {
						st.Value = bao.Value(slices.Clone(other))
					}
					return st, err
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			k := f.secret("db.yaml.password", ymlValue, ymlFile)[0]
			tt.setup(f)
			before := read(t, f.dir, ymlFile)

			f.mustApply()

			if !bytes.Equal(before, read(t, f.dir, ymlFile)) {
				t.Errorf("Apply() changed %s, want it as it was", ymlFile)
			}
			if got := f.items()[k]; got != failedFor(apply.ReasonStoreFailed) {
				t.Errorf("item %s = %q, want %q", k, got, failedFor(apply.ReasonStoreFailed))
			}
			if diff := cmp.Diff([]int{tt.before}, f.vault.cas[key]); diff != "" {
				t.Errorf("PutStored check-and-set versions (-want +got):\n%s", diff)
			}
		})
	}
}

func TestApply_OtherValueAtPath(t *testing.T) {
	const key = "ext.db.yaml.password"
	tests := []struct {
		name        string
		setup       func(f *fixture)
		wantCAS     []int
		wantVersion int // 0: not stored
	}{
		{name: "another value", wantCAS: []int{1}, wantVersion: 2, setup: func(f *fixture) {
			f.vault.stored[key] = &fakeRecord{versions: [][]byte{[]byte("dev-only-fake-older")}, file: []bool{false}}
		}},
		{name: "the same value", wantCAS: nil, wantVersion: 1, setup: func(f *fixture) {
			f.vault.stored[key] = &fakeRecord{versions: [][]byte{[]byte(ymlValue)}, file: []bool{false}}
		}},
		{name: "deleted", wantCAS: []int{1}, wantVersion: 2, setup: func(f *fixture) {
			f.vault.stored[key] = &fakeRecord{versions: [][]byte{[]byte(ymlValue)}, file: []bool{false}, deleted: true}
		}},
		{name: "one conflict", wantCAS: []int{0, 0}, wantVersion: 1, setup: func(f *fixture) {
			f.vault.putErrs[key] = []error{fmt.Errorf("fake vault: %w", bao.ErrVersionConflict)}
		}},
		{name: "two conflicts", wantCAS: []int{0, 0}, setup: func(f *fixture) {
			f.vault.putErrs[key] = []error{
				fmt.Errorf("fake vault: %w", bao.ErrVersionConflict), fmt.Errorf("fake vault: %w", bao.ErrVersionConflict),
			}
		}},
		{name: "a write whose answer was lost", wantCAS: []int{0}, wantVersion: 1, setup: func(f *fixture) {
			f.vault.putErrs[key] = []error{errSavedButLost}
		}},
		{name: "a refused write", wantCAS: []int{0}, setup: func(f *fixture) {
			f.vault.putErrs[key] = []error{errRefused}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			k := f.secret("db.yaml.password", ymlValue, ymlFile)[0]
			tt.setup(f)
			before := read(t, f.dir, ymlFile)

			f.mustApply()

			if diff := cmp.Diff(tt.wantCAS, f.vault.cas[key]); diff != "" {
				t.Errorf("PutStored check-and-set versions (-want +got):\n%s", diff)
			}
			items := f.store.runItems(testRun)
			if len(items) != 1 {
				t.Fatalf("items = %+v, want one", items)
			}
			if tt.wantVersion == 0 {
				if items[0].Status != apply.StatusFailed || items[0].Reason != apply.ReasonStoreFailed {
					t.Errorf("item %s = %s %s, want failed store-failed", k, items[0].Status, items[0].Reason)
				}
				if !bytes.Equal(before, read(t, f.dir, ymlFile)) {
					t.Errorf("Apply() changed %s, want it as it was", ymlFile)
				}
				return
			}
			if items[0].Status != apply.StatusApplied || items[0].StoreVersion != tt.wantVersion {
				t.Errorf("item %s = %s with store version %d, want applied with %d", k, items[0].Status,
					items[0].StoreVersion, tt.wantVersion)
			}
			if string(f.vault.value(key)) != ymlValue {
				t.Errorf("vault path %s does not hold the value", key)
			}
		})
	}
}

func TestApply_DirtyTreeRefused(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.replaceIn("pom.xml", "<project", "<project ")
	before := snapshot(t, f.root)

	err := f.apply(t.Context())
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}

	if f.vault.reads != 0 || f.vault.puts != 0 {
		t.Errorf("Apply() called ReadStored %d and PutStored %d times, want none", f.vault.reads, f.vault.puts)
	}
	if diff := cmp.Diff(before, snapshot(t, f.root)); diff != "" {
		t.Errorf("Apply() changed the workspace (-before +after):\n%s", diff)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureDirty ||
		!slices.Contains(f.store.failed.Blockers, apply.BlockerDirty) {
		t.Errorf("FailApply = %+v, want reason %q with blocker dirty", f.store.failed, apply.FailureDirty)
	}
	f.assertNoValue(err)
}

func TestApply_ChangedFileSkipped(t *testing.T) {
	f := newShop(t, inRepository)
	ky := f.secret("db.yaml.password", ymlValue, ymlFile)[0]
	kx := f.secret("db.xml.password", xmlValue, xmlFile)[0]
	f.replaceIn(ymlFile, "spring:", "spring: # changed")
	f.commit()
	before := read(t, f.dir, ymlFile)

	f.mustApply()

	want := map[string]string{ky: skippedFor(apply.ReasonFileChanged), kx: applied}
	if diff := cmp.Diff(want, f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	if !bytes.Equal(before, read(t, f.dir, ymlFile)) {
		t.Errorf("Apply() changed %s, want it as it was", ymlFile)
	}
}

func TestApply_CustomerDataNotStored(t *testing.T) {
	const rrn = "000000-3000000"
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	redact := triage.Decision{Verdict: triage.VerdictRedact}
	kp := f.phoneNumber(trackedLog)
	kr := f.customerData(finding.KindRRN, redact, rrn, trackedLog)
	before := read(t, f.dir, trackedLog)

	f.mustApply()

	after := read(t, f.dir, trackedLog)
	enc := func(s string) string {
		b, err := korean.EUCKR.NewEncoder().String(s)
		if err != nil {
			t.Fatalf("encoding %q: %v", s, err)
		}
		return b
	}
	assertOutside(t, before, after, []change{{enc(phone), "[REDACTED:phone]"}, {enc(rrn), "[REDACTED:rrn]"}})
	if bytes.Count(before, []byte("\n")) != bytes.Count(after, []byte("\n")) {
		t.Errorf("the log has %d lines after the apply, want %d", bytes.Count(after, []byte("\n")), bytes.Count(before, []byte("\n")))
	}
	for _, v := range []string{phone, rrn} {
		if slices.Contains(f.vault.put, sha([]byte(v))) {
			t.Errorf("PutStored got customer data, want it never stored")
		}
	}
	for _, it := range f.vault.calls {
		if strings.Contains(it, "phone") || strings.Contains(it, "rrn") {
			t.Errorf("a vault call names customer data: %s", it)
		}
	}
	for _, e := range f.readManifest().Entries {
		for _, l := range e.Locations {
			if l.File == trackedLog {
				t.Errorf("manifest entry %s lists %s", e.Name, trackedLog)
			}
		}
	}
	if got := f.items(); got[kp] != applied || got[kr] != applied {
		t.Errorf("items = %v, want the phone number and the RRN applied", got)
	}
}

func TestApply_UntrackedCustomerDataNeedsConsent(t *testing.T) {
	tests := []struct {
		name         string
		allow        bool
		irreversible []string
		want         string
	}{
		{"not confirmed", false, []string{untrackLog}, skippedFor(apply.ReasonUntrackedIrreversible)},
		{"confirmed", true, []string{untrackLog}, applied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			ku := f.phoneNumber(untrackLog)
			kt := f.phoneNumber(trackedLog)
			f.allow, f.irreversible = tt.allow, tt.irreversible
			before := read(t, f.dir, untrackLog)

			f.mustApply()

			want := map[string]string{ku: tt.want, kt: applied}
			if diff := cmp.Diff(want, f.items()); diff != "" {
				t.Errorf("items (-want +got):\n%s", diff)
			}
			changed := !bytes.Equal(before, read(t, f.dir, untrackLog))
			if changed != (tt.want == applied) {
				t.Errorf("Apply() changed %s: %v, want %v", untrackLog, changed, tt.want == applied)
			}
			if bytes.Contains(read(t, f.dir, trackedLog), []byte("REDACTED:phone")) == false {
				t.Errorf("Apply() left the phone number in %s, want it redacted", trackedLog)
			}
		})
	}
}

func TestApply_ConsentCoversShownFilesOnly(t *testing.T) {
	f := newShop(t, inRepository)
	ku := f.phoneNumber(untrackLog)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.allow, f.irreversible = true, []string{"local/other.log"}
	before := read(t, f.dir, untrackLog)

	f.mustApply()

	if got := f.items()[ku]; got != skippedFor(apply.ReasonUntrackedIrreversible) {
		t.Errorf("item %s = %q, want %q", ku, got, skippedFor(apply.ReasonUntrackedIrreversible))
	}
	if !bytes.Equal(before, read(t, f.dir, untrackLog)) {
		t.Errorf("Apply() changed %s, want it as it was", untrackLog)
	}
}

func TestApply_UntrackedSecretApplied(t *testing.T) {
	f := newShop(t, inRepository)
	k := f.secret("db.local.password", localValue, localFile)[0]
	before := read(t, f.dir, localFile)

	f.mustApply()

	assertOutside(t, before, read(t, f.dir, localFile), []change{{localValue, "${ext.db.local.password}"}})
	items := f.store.runItems(testRun)
	if len(items) != 1 || items[0].Status != apply.StatusApplied || items[0].Tracked {
		t.Errorf("items = %+v, want %s applied and not tracked", items, k)
	}
}

func TestApply_KeyFileRemoved(t *testing.T) {
	const (
		store   = "conf/store.jks"
		bareKey = "conf/partner-key"
	)
	f := newShop(t, inRepository)
	write(t, f.dir, store, "\x00\x01dev-only-fake-keystore\x00\xff")
	write(t, f.dir, bareKey, string(read(t, f.dir, pemFile)))
	f.commit()
	raw := map[string][]byte{store: read(t, f.dir, store), bareKey: read(t, f.dir, bareKey)}
	ks := f.keyFile("ssh.store.key", store)
	kb := f.keyFile("ssh.bare.key", bareKey)

	f.mustApply()

	for file, key := range map[string]string{store: "ext.ssh.store.key", bareKey: "ext.ssh.bare.key"} {
		if f.exists(file) {
			t.Errorf("%s exists after the apply, want it removed", file)
		}
		rec := f.vault.stored[key]
		if rec == nil || !bytes.Equal(f.vault.value(key), raw[file]) || !rec.file[len(rec.file)-1] {
			t.Errorf("vault path %s does not hold the bytes of %s as a file", key, file)
		}
	}
	if diff := cmp.Diff(map[string]string{ks: applied, kb: applied}, f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	want := []manifest.Entry{
		{
			Name: "ext.ssh.bare.key", Category: finding.CategorySSH, Action: manifest.ActionFileRemoved,
			Store: storePath("ext.ssh.bare.key"), Locations: []manifest.Location{{File: bareKey}},
		},
		{
			Name: "ext.ssh.store.key", Category: finding.CategorySSH, Action: manifest.ActionFileRemoved,
			Store: storePath("ext.ssh.store.key"), Locations: []manifest.Location{{File: store}},
		},
	}
	if diff := cmp.Diff(want, f.readManifest().Entries); diff != "" {
		t.Errorf("manifest entries (-want +got):\n%s", diff)
	}
}

func TestApply_KeyFileNotStoredKeepsFile(t *testing.T) {
	const (
		fileKey  = "ext.ssh.partner.key"
		blockKey = "ext.ssh.partner.block"
	)
	refuse := func(key string) func(f *fixture) {
		return func(f *fixture) { f.vault.putErrs[key] = []error{errRefused} }
	}
	tests := []struct {
		name      string
		setup     func(f *fixture)
		removed   bool
		wantFile  string
		wantBlock string
		listed    []string
	}{
		{
			name: "the key file not stored", setup: refuse(fileKey),
			wantFile: failedFor(apply.ReasonStoreFailed), wantBlock: failedFor(apply.ReasonStoreFailed),
		},
		{
			name: "the key file read back differs",
			setup: func(f *fixture) {
				f.vault.onRead = func(key string, call int, st bao.Stored, err error) (bao.Stored, error) {
					if key == fileKey && call == 2 {
						st.Value = bao.Value("dev-only-fake-other-bytes")
					}
					return st, err
				}
			},
			wantFile: failedFor(apply.ReasonStoreFailed), wantBlock: failedFor(apply.ReasonStoreFailed),
		},
		{
			name: "the block not stored", setup: refuse(blockKey), removed: true,
			wantFile: applied, wantBlock: skippedFor(apply.ReasonFileRemoved), listed: []string{"ext.ssh.partner.key"},
		},
		{
			name: "both stored", setup: func(*fixture) {}, removed: true,
			wantFile: applied, wantBlock: applied, listed: []string{"ext.ssh.partner.block", "ext.ssh.partner.key"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			kf := f.keyFile("ssh.partner.key", pemFile)
			kb := f.privateKey("ssh.partner.block", pemFile)
			tt.setup(f)
			before := read(t, f.dir, pemFile)

			f.mustApply()

			if diff := cmp.Diff(map[string]string{kf: tt.wantFile, kb: tt.wantBlock}, f.items()); diff != "" {
				t.Errorf("items (-want +got):\n%s", diff)
			}
			if tt.removed {
				if f.exists(pemFile) {
					t.Errorf("%s exists, want it removed", pemFile)
				}
				var names []string
				for _, e := range f.readManifest().Entries {
					names = append(names, e.Name)
					if e.Action != manifest.ActionFileRemoved || len(e.Locations) != 1 || e.Locations[0].Line != 0 {
						t.Errorf("manifest entry %+v, want file-removed with one location without a line", e)
					}
				}
				if diff := cmp.Diff(tt.listed, names); diff != "" {
					t.Errorf("manifest names (-want +got):\n%s", diff)
				}
				return
			}
			if !bytes.Equal(before, read(t, f.dir, pemFile)) {
				t.Errorf("%s changed, want it as it was", pemFile)
			}
		})
	}
}

func TestApply_KeyFileWithCustomerDataKept(t *testing.T) {
	const email = "user@example.invalid"
	f := newShop(t, inRepository)
	write(t, f.dir, pemFile, string(read(t, f.dir, pemFile))+"contact: "+email+" "+phone+"\n")
	f.commit()
	before := read(t, f.dir, pemFile)
	kf := f.keyFile("ssh.partner.key", pemFile)
	f.customerData(finding.KindEmail, triage.Decision{}, email, pemFile)
	kp := f.phoneNumber(pemFile)

	f.mustApply()

	if f.vault.stored["ext.ssh.partner.key"] != nil || f.vault.puts != 0 {
		t.Errorf("Apply() stored %d values, want the key file never stored", f.vault.puts)
	}
	assertOutside(t, before, read(t, f.dir, pemFile), []change{{phone, "[REDACTED:phone]"}})
	want := map[string]string{kf: skippedFor(apply.ReasonKeyHoldsCustomerData), kp: applied}
	if diff := cmp.Diff(want, f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
}

func TestApply_EUCKRAndCRLFPreserved(t *testing.T) {
	const (
		file  = "conf/korean.properties"
		value = "dev-only-fake-비밀번호"
	)
	f := newShop(t, inRepository)
	enc, err := korean.EUCKR.NewEncoder().String("# 데이터베이스 설정\r\ndb.password=" + value + "\r\n# 끝\r\n")
	if err != nil {
		t.Fatalf("encoding the file: %v", err)
	}
	write(t, f.dir, file, enc)
	f.commit()
	encValue, err := korean.EUCKR.NewEncoder().String(value)
	if err != nil {
		t.Fatalf("encoding the value: %v", err)
	}
	ke := f.secret("db.korean.password", value, file)[0]
	k2 := f.secret("db.euckr.password", euckrValue, euckrFile)[0]
	before := map[string][]byte{file: read(t, f.dir, file), euckrFile: read(t, f.dir, euckrFile)}

	f.mustApply()

	assertOutside(t, before[file], read(t, f.dir, file), []change{{encValue, "${ext.db.korean.password}"}})
	assertOutside(t, before[euckrFile], read(t, f.dir, euckrFile), []change{{euckrValue, "${ext.db.euckr.password}"}})
	if string(f.vault.value("ext.db.korean.password")) != value {
		t.Errorf("vault path ext.db.korean.password does not hold the value in UTF-8")
	}
	if diff := cmp.Diff(map[string]string{ke: applied, k2: applied}, f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
}

func TestApply_StoresLiteral(t *testing.T) {
	const (
		xmlLiteral  = "a&amp;b-dev-only-fake"
		propLiteral = `a\=b-dev-only-fake`
	)
	f := newShop(t, inRepository)
	write(t, f.dir, "conf/literal.xml", `<bean><property name="password" value="`+xmlLiteral+`"/></bean>`+"\n")
	write(t, f.dir, "conf/literal.properties", "pw="+propLiteral+"\n")
	f.commit()
	f.secret("literal.xml.password", xmlLiteral, "conf/literal.xml")
	f.secret("literal.props.password", propLiteral, "conf/literal.properties")

	f.mustApply()

	for key, want := range map[string]string{"ext.literal.xml.password": xmlLiteral, "ext.literal.props.password": propLiteral} {
		if string(f.vault.value(key)) != want {
			t.Errorf("vault path %s does not hold the text as written in the source", key)
		}
	}
}

func TestApply_CodeChange(t *testing.T) {
	const (
		javaFile  = "src/main/java/com/example/shop/dao/OrderDao.java"
		javaValue = "dev-only-fake-java-password"
	)
	f := newShop(t, inRepository)
	f.secret("db.java.password", javaValue, javaFile)
	f.secret("db.yaml.password", ymlValue, ymlFile)

	f.mustApply()

	for _, it := range f.store.runItems(testRun) {
		if want := it.File == javaFile; it.CodeChange != want || it.Status != apply.StatusApplied {
			t.Errorf("item in %s: %s, CodeChange = %v; want applied, %v", it.File, it.Status, it.CodeChange, want)
		}
	}
}

func TestApply_ExistingManifestExtended(t *testing.T) {
	f := newShop(t, inRepository)
	kept := manifest.Entry{
		Name: "ext.other.thing", Category: finding.CategoryOther, Action: manifest.ActionExternalized,
		Store: storePath("ext.other.thing"), Locations: []manifest.Location{{File: "conf/other.yml", Line: 4}},
	}
	content, err := manifest.Manifest{Entries: []manifest.Entry{kept}}.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	write(t, f.dir, manifest.FileName, string(content))
	f.commit()
	line := f.lineOf(ymlFile, ymlValue)
	f.secret("db.yaml.password", ymlValue, ymlFile)

	f.mustApply()

	want := []manifest.Entry{
		{
			Name: "ext.db.yaml.password", Category: finding.CategoryStorage, Action: manifest.ActionExternalized,
			Store: storePath("ext.db.yaml.password"), Locations: []manifest.Location{{File: ymlFile, Line: line}},
		},
		kept,
	}
	if diff := cmp.Diff(want, f.readManifest().Entries); diff != "" {
		t.Errorf("manifest entries (-want +got):\n%s", diff)
	}
	var order []string
	for _, a := range f.store.args {
		if strings.Contains(a, manifest.FileName) {
			order = append(order, strings.Fields(a)[0])
		}
	}
	if diff := cmp.Diff([]string{"BeginFile", "EndFile"}, order); diff != "" {
		t.Errorf("calls naming the manifest (-want +got):\n%s", diff)
	}
}

func TestApply_UnreadableManifestBlocks(t *testing.T) {
	f := newShop(t, inRepository)
	write(t, f.dir, manifest.FileName, "# edited by hand\nversion: 1\nentries: []\n")
	f.commit()
	f.secret("db.yaml.password", ymlValue, ymlFile)

	err := f.apply(t.Context())
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureManifest ||
		!slices.Contains(f.store.failed.Blockers, apply.BlockerManifestUnreadable) {
		t.Errorf("FailApply = %+v, want reason %q with blocker manifest-unreadable", f.store.failed, apply.FailureManifest)
	}
	if f.vault.puts != 0 || f.vault.reads != 0 {
		t.Errorf("Apply() called ReadStored %d and PutStored %d times, want none", f.vault.reads, f.vault.puts)
	}
}

// beginsOf returns how many times the store was told that file is about to
// change.
func (f *fixture) beginsOf(file string) int {
	n := 0
	prefix := fmt.Sprintf("BeginFile [%d %s ", cmpOr(f.run, testRun), file)
	for _, a := range f.store.args {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

// twoFiles adds the YAML and XML passwords and returns their keys and what
// the files hold before and should hold after an apply.
func (f *fixture) twoFiles() (keys []string, want map[string]string) {
	f.t.Helper()
	want = map[string]string{
		ymlFile: strings.Replace(string(read(f.t, f.dir, ymlFile)), ymlValue, "${ext.db.yaml.password}", 1),
		xmlFile: strings.Replace(string(read(f.t, f.dir, xmlFile)), xmlValue, "${ext.db.xml.password}", 1),
	}
	keys = append(f.secret("db.yaml.password", ymlValue, ymlFile), f.secret("db.xml.password", xmlValue, xmlFile)...)
	return keys, want
}

// assertApplied checks that both files hold what want says, that every key
// is applied, and that the manifest lists each name once.
func (f *fixture) assertApplied(keys []string, want map[string]string) {
	f.t.Helper()
	for file, content := range want {
		if got := string(read(f.t, f.dir, file)); got != content {
			f.t.Errorf("%s after the apply (-want +got):\n%s", file, cmp.Diff(content, got))
		}
	}
	if diff := cmp.Diff(all(map[string]string{}, applied, keys...), f.items()); diff != "" {
		f.t.Errorf("items (-want +got):\n%s", diff)
	}
	var got []string
	for _, e := range f.readManifest().Entries {
		got = append(got, fmt.Sprintf("%s %d", e.Name, len(e.Locations)))
	}
	if diff := cmp.Diff([]string{"ext.db.xml.password 1", "ext.db.yaml.password 1"}, got); diff != "" {
		f.t.Errorf("manifest names and location counts (-want +got):\n%s", diff)
	}
}

func TestApply_CrashAfterStore(t *testing.T) {
	f := newShop(t, inRepository)
	keys, want := f.twoFiles()

	f.crashThenApply(func(cancel context.CancelFunc) { f.crash(cancel, "BeginFile", 1, true) })

	f.assertApplied(keys, want)
	if f.vault.puts != 2 {
		t.Errorf("PutStored was called %d times, want once for each of the two keys", f.vault.puts)
	}
	if f.vault.version("ext.db.yaml.password") != 1 || f.vault.version("ext.db.xml.password") != 1 {
		t.Errorf("vault versions %d and %d, want 1 and 1", f.vault.version("ext.db.yaml.password"),
			f.vault.version("ext.db.xml.password"))
	}
	if n := f.beginsOf(ymlFile); n != 2 {
		t.Errorf("BeginFile(%s) was called %d times, want 2 (the first refused)", ymlFile, n)
	}
}

func TestApply_CrashAfterRename(t *testing.T) {
	f := newShop(t, inRepository)
	keys, want := f.twoFiles()

	f.crashThenApply(func(cancel context.CancelFunc) { f.crash(cancel, "EndFile", 1, true) })

	f.assertApplied(keys, want)
	if n := f.beginsOf(ymlFile); n != 1 {
		t.Errorf("BeginFile(%s) was called %d times, want once", ymlFile, n)
	}
	if len(f.store.resolved) != 1 || f.store.resolved[0].File.File != ymlFile || !f.store.resolved[0].Done {
		t.Errorf("ResolveFile calls %+v, want %s settled as done", f.store.resolved, ymlFile)
	}
	line := 0
	for _, it := range f.store.runItems(testRun) {
		if it.File == ymlFile {
			line = it.Line
		}
	}
	if want := f.lineOf(ymlFile, "${ext.db.yaml.password}"); line != want {
		t.Errorf("item in %s has line %d, want %d", ymlFile, line, want)
	}
}

func TestApply_CrashBeforeRename(t *testing.T) {
	f := newShop(t, inRepository)
	keys, want := f.twoFiles()
	var temp string

	f.crashThenApply(func(cancel context.CancelFunc) {
		f.store.after["BeginFile"] = func(context.Context) error {
			temp = f.store.open[0].TempName
			write(t, f.dir, temp, "half of the new content\n")
			cancel()
			return errCrash
		}
	})

	f.assertApplied(keys, want)
	if temp == "" || f.exists(temp) {
		t.Errorf("temporary file %q exists after the retry, want it removed", temp)
	}
	if len(f.store.resolved) != 1 || f.store.resolved[0].Done || f.store.resolved[0].File.TempName != temp {
		t.Errorf("ResolveFile calls %+v, want the record of %s settled as not done", f.store.resolved, temp)
	}
	if n := f.beginsOf(ymlFile); n != 2 {
		t.Errorf("BeginFile(%s) was called %d times, want 2", ymlFile, n)
	}
}

func TestApply_CrashBetweenFiles(t *testing.T) {
	f := newShop(t, inRepository)
	keys, want := f.twoFiles()
	f.crashThenApply(func(cancel context.CancelFunc) { f.crash(cancel, "EndFile", 1, false) })
	f.assertApplied(keys, want)
}

func TestApply_CrashBeforeManifest(t *testing.T) {
	f := newShop(t, inRepository)
	keys, want := f.twoFiles()
	f.crashThenApply(func(cancel context.CancelFunc) { f.crash(cancel, "BeginFile", 3, true) })
	f.assertApplied(keys, want)
}

func TestApply_CrashAfterManifestRename(t *testing.T) {
	tests := []struct {
		name   string
		method string
		n      int
	}{
		{"before the record is written", "EndFile", 3},
		{"before the items are marked", "MarkInManifest", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			keys, want := f.twoFiles()
			f.crashThenApply(func(cancel context.CancelFunc) { f.crash(cancel, tt.method, tt.n, true) })
			f.assertApplied(keys, want)
			if n := f.beginsOf(manifest.FileName); n != 1 {
				t.Errorf("BeginFile(%s) was called %d times, want once: the retry finds it as it would write it", manifest.FileName, n)
			}
		})
	}
}

func TestApply_AbandonedRunIsReconciled(t *testing.T) {
	f := newShop(t, inRepository)
	keys, want := f.twoFiles()
	ks := f.secret("deploy.ssh.password", shValue, shFile)[0]
	want[shFile] = strings.Replace(string(read(t, f.dir, shFile)), shValue, "${EXT_DEPLOY_SSH_PASSWORD}", 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Files are changed in path order: the script, the YAML file, then the
	// XML file. The first run stops after renaming the YAML file, before
	// recording it.
	f.crash(cancel, "EndFile", 2, true)
	if err := f.apply(ctx); err == nil {
		t.Fatalf("Apply() with the worker stopping = nil, want an error")
	}
	f.store.hooks = map[string]func(context.Context) error{}
	f.store.state[testRun] = apply.StateFailed

	f.run = 2
	saved := f.mustCheck()
	if len(saved.Dirty) != 0 || slices.Contains(saved.Blockers, apply.BlockerDirty) {
		t.Errorf("SaveCheck dirty %v, blockers %v, want the files of the first run not dirty", saved.Dirty, saved.Blockers)
	}
	if len(f.store.open) != 0 {
		t.Errorf("open records after the check = %+v, want none", f.store.open)
	}
	f.mustApply()

	for file, content := range want {
		if got := string(read(t, f.dir, file)); got != content {
			t.Errorf("%s after the apply (-want +got):\n%s", file, cmp.Diff(content, got))
		}
	}
	first := outcomes(f.store.runItems(testRun))
	if diff := cmp.Diff(map[string]string{ks: applied, keys[0]: applied}, first); diff != "" {
		t.Errorf("items of the first run (-want +got):\n%s", diff)
	}
	if got := f.items()[keys[1]]; got != applied {
		t.Errorf("item %s of the second run = %q, want applied", keys[1], got)
	}
	var names []string
	for _, e := range f.readManifest().Entries {
		names = append(names, e.Name)
	}
	if diff := cmp.Diff([]string{"EXT_DEPLOY_SSH_PASSWORD", "ext.db.xml.password", "ext.db.yaml.password"}, names); diff != "" {
		t.Errorf("manifest names (-want +got):\n%s", diff)
	}
}

func TestApply_ChangedAfterOwnWrite(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.mustApply()
	f.replaceIn(ymlFile, "spring:", "spring: # edited after the apply")

	f.run = 2
	err := f.apply(t.Context())
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureDirty {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailureDirty)
	}
}

func TestApply_SecondRunIsNoOp(t *testing.T) {
	f := newShop(t, inRepository)
	keys, _ := f.twoFiles()
	f.mustApply()
	puts, begins := f.vault.puts, f.store.begins()
	before := snapshot(t, f.root)

	f.run = 2
	f.mustApply()

	if diff := cmp.Diff(all(map[string]string{}, skippedFor(apply.ReasonFileChanged), keys...), f.items()); diff != "" {
		t.Errorf("items of the second run (-want +got):\n%s", diff)
	}
	if f.vault.puts != puts || f.store.begins() != begins {
		t.Errorf("the second run called PutStored %d and BeginFile %d times, want none",
			f.vault.puts-puts, f.store.begins()-begins)
	}
	if diff := cmp.Diff(before, snapshot(t, f.root)); diff != "" {
		t.Errorf("the second run changed the workspace (-before +after):\n%s", diff)
	}
}

func TestApply_StopsBetweenFiles(t *testing.T) {
	f := newShop(t, inRepository)
	f.twoFiles()
	before := read(t, f.dir, xmlFile)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.store.after["EndFile"] = func(context.Context) error {
		cancel()
		return nil
	}

	err := f.apply(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Apply() error = %v, want context.Canceled", err)
	}
	if f.store.failed != nil || f.store.state[testRun] != apply.StateRunning {
		t.Errorf("run %q, failed %+v; want it left running", f.store.state[testRun], f.store.failed)
	}
	items := f.store.runItems(testRun)
	if len(items) != 1 || items[0].File != ymlFile || items[0].Status != apply.StatusApplied {
		t.Errorf("items = %+v, want the YAML file's item applied and nothing else", items)
	}
	if !bytes.Equal(before, read(t, f.dir, xmlFile)) {
		t.Errorf("Apply() changed %s after it was stopped", xmlFile)
	}
	f.assertNoValue(err)
}

func TestApply_TimeLimit(t *testing.T) {
	f := newShop(t, inRepository)
	f.twoFiles()
	f.opts.ApplyLimit = 3 * time.Second
	f.store.after["BeginFile"] = func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}

	err := f.apply(t.Context())
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureTimeLimit {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailureTimeLimit)
	}
	items := f.store.runItems(testRun)
	if len(items) != 1 || items[0].File != ymlFile || items[0].Status != apply.StatusApplied {
		t.Errorf("items = %+v, want the YAML file's item applied", items)
	}
	f.assertNoValue(err)
}

func TestApply_DropsFalsePositiveOriginals(t *testing.T) {
	settled := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	t.Run("succeeded", func(t *testing.T) {
		f := newShop(t, inRepository)
		f.secret("db.yaml.password", ymlValue, ymlFile)
		f.store.settled = settled
		f.mustApply()
		if diff := cmp.Diff(settled, f.vault.dropped); diff != "" {
			t.Errorf("DeletePending calls (-want +got):\n%s", diff)
		}
	})
	t.Run("deleting fails", func(t *testing.T) {
		f := newShop(t, inRepository)
		f.secret("db.yaml.password", ymlValue, ymlFile)
		f.store.settled = settled
		f.vault.deleteErr = errRefused
		f.mustApply()
		if f.vault.deletes != len(settled) {
			t.Errorf("DeletePending was called %d times, want %d", f.vault.deletes, len(settled))
		}
		for _, fp := range settled {
			if strings.Contains(f.logs.String(), fp) {
				t.Errorf("the log holds a fingerprint")
			}
		}
	})
	t.Run("refused", func(t *testing.T) {
		f := newShop(t, inRepository)
		f.secret("db.yaml.password", ymlValue, ymlFile)
		f.store.settled = settled
		f.replaceIn("pom.xml", "<project", "<project ")
		if err := f.apply(t.Context()); err != nil {
			t.Fatalf("Apply() error = %v, want nil", err)
		}
		if f.store.failed == nil || f.vault.deletes != 0 {
			t.Errorf("failed %+v, DeletePending called %d times; want the run failed and no call", f.store.failed, f.vault.deletes)
		}
	})
}

func TestApply_ValuesAreCleared(t *testing.T) {
	const (
		file   = "conf/two.properties"
		first  = "dev-only-fake-first-value"
		second = "dev-only-fake-second-value"
	)
	f := newShop(t, inRepository)
	write(t, f.dir, file, "a="+first+" b="+second+"\n")
	f.commit()
	keyRaw := read(t, f.dir, pemFile)
	f.secret("two.first.password", first, file)
	f.secret("two.second.password", second, file)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.keyFile("ssh.partner.key", pemFile)
	f.privateKey("ssh.partner.pem", pemFile)
	// Once the store stage is over, before the first file changes, no value
	// is needed any more.
	cleared := true
	f.store.hooks["BeginFile"] = func(context.Context) error {
		for _, b := range f.vault.held {
			cleared = cleared && len(bytes.Trim(b, "\x00")) == 0
		}
		return nil
	}

	f.mustApply()

	if !cleared {
		t.Errorf("a value given to the vault held bytes when the first file changed, want every value zeroed by then")
	}

	if len(f.vault.held) == 0 {
		t.Fatalf("the vault was given no value")
	}
	for i, b := range f.vault.held {
		if len(bytes.Trim(b, "\x00")) != 0 {
			t.Errorf("value %d given to the vault holds bytes after Apply, want zeros", i)
		}
	}
	want := map[string][]byte{
		"ext.two.first.password": []byte(first), "ext.two.second.password": []byte(second),
		"ext.db.yaml.password": []byte(ymlValue), "ext.ssh.partner.key": keyRaw, "ext.ssh.partner.pem": []byte(pemLine),
	}
	for key, v := range want {
		if !bytes.Equal(f.vault.value(key), v) {
			t.Errorf("vault path %s does not hold its value", key)
		}
	}
	assertOutside(t, []byte("a="+first+" b="+second+"\n"), read(t, f.dir, file), []change{
		{first, "${ext.two.first.password}"}, {second, "${ext.two.second.password}"},
	})
}

func TestApply_NoValueOutsideVault(t *testing.T) {
	everything := func(f *fixture) {
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
	}
	tests := []struct {
		name  string
		setup func(f *fixture, cancel context.CancelFunc)
	}{
		{"succeeded", func(f *fixture, _ context.CancelFunc) { everything(f) }},
		{"store failed", func(f *fixture, _ context.CancelFunc) {
			everything(f)
			f.vault.putErrs["ext.db.yaml.password"] = []error{errRefused}
			f.vault.putErrs["ext.ssh.partner.key"] = []error{errRefused}
		}},
		{"read back differs", func(f *fixture, _ context.CancelFunc) {
			everything(f)
			f.vault.onRead = func(_ string, call int, st bao.Stored, err error) (bao.Stored, error) {
				if call == 2 {
					st.Value = bao.Value("dev-only-fake-other-bytes")
				}
				return st, err
			}
		}},
		{"stopped", func(f *fixture, cancel context.CancelFunc) {
			everything(f)
			f.crash(cancel, "EndFile", 2, true)
		}},
		{"refused", func(f *fixture, _ context.CancelFunc) {
			everything(f)
			f.replaceIn("pom.xml", "<project", "<project ")
		}},
		{"manifest not written", func(f *fixture, _ context.CancelFunc) {
			everything(f)
			f.store.errs["MarkInManifest"] = errRefused
		}},
		{"write failed", func(f *fixture, _ context.CancelFunc) {
			everything(f)
			f.store.after["BeginFile"] = func(context.Context) error {
				write(f.t, f.dir, ymlFile, string(read(f.t, f.dir, ymlFile))+"# changed while writing\n")
				return nil
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShop(t, inRepository)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tt.setup(f, cancel)

			err := f.apply(ctx)

			f.assertNoValue(err)
		})
	}
}

func TestApply_WriteFailed(t *testing.T) {
	f := newShop(t, inRepository)
	keys, _ := f.twoFiles()
	f.store.after["BeginFile"] = func(context.Context) error {
		if len(f.store.open) == 1 && f.store.open[0].File == ymlFile {
			write(t, f.dir, ymlFile, string(read(t, f.dir, ymlFile))+"# changed while writing\n")
		}
		return nil
	}

	f.mustApply()

	want := map[string]string{keys[0]: failedFor(apply.ReasonWriteFailed), keys[1]: applied}
	if diff := cmp.Diff(want, f.items()); diff != "" {
		t.Errorf("items (-want +got):\n%s", diff)
	}
	if len(f.store.open) != 0 {
		t.Errorf("open records = %+v, want none", f.store.open)
	}
	if !bytes.Contains(read(t, f.dir, ymlFile), []byte(ymlValue)) {
		t.Errorf("%s lost its value, want it kept", ymlFile)
	}
}

func TestApply_ManifestNotWritten(t *testing.T) {
	f := newShop(t, inRepository)
	f.secret("db.yaml.password", ymlValue, ymlFile)
	f.store.errs["MarkInManifest"] = errRefused

	err := f.apply(t.Context())
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if f.store.failed == nil || f.store.failed.Reason != apply.FailureManifestWrite {
		t.Errorf("FailApply = %+v, want reason %q", f.store.failed, apply.FailureManifestWrite)
	}
	if items := f.store.runItems(testRun); len(items) != 1 || items[0].Status != apply.StatusApplied {
		t.Errorf("items = %+v, want the applied item kept", items)
	}
}

func TestCheckAgreesWithApply(t *testing.T) {
	f := newShop(t, inRepository)
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
	f.vault.lie[sha([]byte(xmlValue))] = "fp-other"

	saved := f.mustCheck()
	f.allow, f.irreversible = true, saved.Irreversible
	f.mustApply()

	var checked, done []string
	for k, v := range outcomes(saved.Items) {
		if v == planned {
			checked = append(checked, k)
		}
	}
	for k, v := range f.items() {
		if v == applied {
			done = append(done, k)
		}
	}
	slices.Sort(checked)
	slices.Sort(done)
	if diff := cmp.Diff(checked, done); diff != "" {
		t.Errorf("planned by Check (-) and applied by Apply (+):\n%s", diff)
	}
}
