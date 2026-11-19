package opengrep_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/opengrep"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
)

func TestParse_Opengrep(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, counts, err := opengrep.Parse(data, "/scan")
	if err != nil {
		t.Fatalf("Parse(report.json) error = %v", err)
	}
	want := []scan.Raw{{
		Tool: "opengrep", Rule: "java-password-field",
		Category: finding.CategoryStorage, Kind: finding.KindPassword,
		File: "src/main/java/shop/Db.java", Offset: 68, EndOffset: 91,
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Parse(report.json) mismatch (-want +got):\n%s", diff)
	}
	if want := (scan.Counts{Errors: 1}); counts != want {
		t.Errorf("Parse(report.json) counts = %+v, want %+v", counts, want)
	}
	// The report holds the matched value in extra.lines and extra.metavars,
	// and source text in the error message. None of it is kept.
	for _, r := range got {
		if len(r.Value) != 0 {
			t.Errorf("Parse(report.json) Value has %d bytes, want none", len(r.Value))
		}
		for _, s := range []string{"dev-only-fake", "비밀번호", "password\"", "Broken"} {
			if strings.Contains(fmt.Sprintf("%+v %s", r, r.Value), s) {
				t.Errorf("Parse(report.json) result %+v contains %q", r, s)
			}
		}
	}
}

// result returns a report with one result that has the given metadata,
// path, and offsets.
func result(metadata, path, start, end string) []byte {
	return []byte(`{"version":"2.0.0","results":[{"check_id":"fake-rule","path":"` + path +
		`","start":` + start + `,"end":` + end +
		`,"extra":{"metadata":` + metadata + `,"lines":"dev-only-fake-leak"}}],"errors":[]}`)
}

const (
	goodMetadata = `{"category":"storage-credential","kind":"password"}`
	start10      = `{"line":1,"col":11,"offset":10}`
	end20        = `{"line":1,"col":21,"offset":20}`
)

func TestParse_Opengrep_MissingMetadata(t *testing.T) {
	for _, metadata := range []string{
		`{}`,
		`{"kind":"password"}`,
		`{"category":"storage-credential"}`,
		`{"category":"dev-only-fake-leak","kind":"password"}`,
		`{"category":"storage-credential","kind":"dev-only-fake-leak"}`,
		`{"category":1,"kind":"password"}`,
	} {
		_, _, err := opengrep.Parse(result(metadata, "/scan/a.java", start10, end20), "/scan")
		if !errors.Is(err, scan.ErrScannerFailed) {
			t.Errorf("Parse(metadata %s) error = %v, want ErrScannerFailed", metadata, err)
			continue
		}
		if strings.Contains(err.Error(), "dev-only-fake-leak") {
			t.Errorf("Parse(metadata %s) error = %q, contains the input", metadata, err)
		}
	}
}

func TestParse_Opengrep_Dropped(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		start, end string
	}{
		{"FileOutsideDir", "/elsewhere/a.java", start10, end20},
		{"FileEscapesDir", "/scan/../a.java", start10, end20},
		{"EndBeforeStart", "/scan/a.java", end20, start10},
		{"NegativeOffset", "/scan/a.java", `{"offset":-1}`, end20},
		{"NoOffset", "/scan/a.java", `{"line":1,"col":1}`, end20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, counts, err := opengrep.Parse(result(goodMetadata, tt.path, tt.start, tt.end), "/scan")
			if err != nil || len(got) != 0 || counts.Dropped != 1 {
				t.Errorf("Parse(%s) = %d results, %+v, %v, want none, dropped 1, no error", tt.name, len(got), counts, err)
			}
		})
	}
}

func TestParse_Opengrep_EmptyMatch(t *testing.T) {
	got, _, err := opengrep.Parse(result(goodMetadata, "/scan/a.java", start10, start10), "/scan")
	if err != nil || len(got) != 1 || got[0].Offset != 10 || got[0].EndOffset != 10 {
		t.Errorf("Parse(empty match) = %+v, %v, want one result at 10..10", got, err)
	}
}

func TestParse_Truncated(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	checkParseFails(t, data[:len(data)/2])
}

func TestParse_NotJSON(t *testing.T) {
	checkParseFails(t, []byte("dev-only-fake-leak is not JSON"))
	checkParseFails(t, []byte(`[{"lines": "dev-only-fake-leak"}]`))
	checkParseFails(t, []byte(`{"results":[{"start":{"offset":"dev-only-fake-leak"}}]}`))
	checkParseFails(t, []byte(`{"version":"2.0.0","errors":[]}`))
	checkParseFails(t, nil)
}

func checkParseFails(t *testing.T, data []byte) {
	t.Helper()
	_, _, err := opengrep.Parse(data, "/scan")
	if !errors.Is(err, scan.ErrScannerFailed) {
		t.Fatalf("Parse(%.40q) error = %v, want ErrScannerFailed", data, err)
	}
	for _, s := range []string{"dev-only-fake", "password", "lines", "offset"} {
		if strings.Contains(fmt.Sprintf("%+v", err), s) {
			t.Errorf("Parse(%.40q) error = %q, contains %q from the input", data, err, s)
		}
	}
}

// fakeScanner writes a shell script that stands in for opengrep and returns
// its path.
func fakeScanner(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake scanners are shell scripts")
	}
	bin := filepath.Join(t.TempDir(), "opengrep")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // G306: the fake scanner must be executable
		t.Fatal(err)
	}
	return bin
}

func testEnv(t *testing.T) scanexec.Env {
	t.Helper()
	return scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
}

func TestScan_NonZeroExit(t *testing.T) {
	for _, code := range []int{1, 2, 3, 7} {
		bin := fakeScanner(t, fmt.Sprintf(`echo '{"results":[],"errors":[{"message":"dev-only-fake-leak"}]}'; echo dev-only-fake-leak >&2; exit %d`, code))
		s := opengrep.New(bin, "/rules/opengrep", nil, testEnv(t))
		_, err := s.Scan(t.Context(), t.TempDir())
		if !errors.Is(err, scan.ErrScannerFailed) {
			t.Errorf("Scan() with exit code %d error = %v, want ErrScannerFailed", code, err)
			continue
		}
		if msg := fmt.Sprintf("%+v", err); strings.Contains(msg, "dev-only-fake-leak") {
			t.Errorf("Scan() error = %q, contains the scanner output", msg)
		}
		if !strings.Contains(err.Error(), "opengrep") || !strings.Contains(err.Error(), fmt.Sprint(code)) {
			t.Errorf("Scan() error = %q, want the tool name and exit code %d", err, code)
		}
	}
}

func TestScan_Args(t *testing.T) {
	env := testEnv(t)
	bin := fakeScanner(t, `for a in "$@"; do printf '%s\n' "$a"; done > "$HOME/args"; echo '{"results":[],"errors":[{},{}]}'`)
	dir := t.TempDir()
	excludes := []string{"vendor/lib", "we*ird [x]?", `back\slash`, "-starts-with-dash"}
	s := opengrep.New(bin, "/rules/opengrep", excludes, env)
	excludes[0] = "changed-after-new"
	got, counts, err := s.ScanWithCounts(t.Context(), dir)
	if err != nil || len(got) != 0 || counts != (scan.Counts{Errors: 2}) {
		t.Fatalf("ScanWithCounts() = %v, %+v, %v, want no results, 2 errors, no error", got, counts, err)
	}
	args, err := os.ReadFile(filepath.Join(env.Home, "args"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"scan", "--config=/rules/opengrep", "--json", "--quiet",
		"--no-git-ignore", "--x-ignore-semgrepignore-files",
		"--no-rewrite-rule-ids", "--disable-nosem", "--project-root=" + dir,
		"--max-target-bytes=20MB", "--timeout=10",
		"--exclude=.git/", "--exclude=target/", "--exclude=build/", "--exclude=node_modules/",
		"--exclude=/vendor/lib", `--exclude=/we\*ird\ \[x\]\?`, `--exclude=/back\\slash`, "--exclude=/-starts-with-dash",
		"--", dir,
	}
	if diff := cmp.Diff(want, strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")); diff != "" {
		t.Errorf("opengrep arguments mismatch (-want +got):\n%s", diff)
	}
}

func TestScan_RelativeDir(t *testing.T) {
	bin := fakeScanner(t, `echo '{"results":[],"errors":[]}'`)
	s := opengrep.New(bin, "/rules/opengrep", nil, testEnv(t))
	if _, err := s.Scan(t.Context(), "relative/dir"); err == nil {
		t.Errorf("Scan(relative/dir) error = nil, want an error")
	}
}

func TestVersion(t *testing.T) {
	bin := fakeScanner(t, `[ "$1" = --version ] && echo 2.0.0`)
	s := opengrep.New(bin, "/rules/opengrep", nil, testEnv(t))
	got, err := s.Version(t.Context())
	if err != nil || got != "2.0.0" {
		t.Errorf("Version() = %q, %v, want 2.0.0", got, err)
	}
	if s.Name() != "opengrep" {
		t.Errorf("Name() = %q, want opengrep", s.Name())
	}
}

// realBinary returns the opengrep executable that make scanners installs, or
// skips the test when it is not there.
func realBinary(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin", "opengrep"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no opengrep executable at %s; run make scanners", bin)
	}
	return bin
}

func TestScan_RealOpengrep(t *testing.T) {
	bin := realBinary(t)
	rules, err := filepath.Abs(filepath.Join("testdata", "rules"))
	if err != nil {
		t.Fatal(err)
	}
	const value = "dev-only-fake-real-run"
	source := "class Db {\n    /* 비밀번호 */ String password = \"" + value + "\"; // nosemgrep\n}\n"
	dir := t.TempDir()
	// test/ and vendor/ are skipped by opengrep unless told otherwise, and
	// .gitignore and .semgrepignore would hide the others; all are scanned.
	// build/ is a default exclude and skip/ a project exclude.
	files := []string{"src/Db.java", "test/Db.java", "vendor/Db.java", "ignored/Db.java", "build/Db.java", "skip/Db.java"}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{".gitignore": "ignored/\n", ".semgrepignore": "src/\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := opengrep.New(bin, rules, []string{"skip"}, testEnv(t)).Scan(t.Context(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	slices.SortFunc(got, func(a, b scan.Raw) int { return strings.Compare(a.File, b.File) })
	start := strings.Index(source, value)
	var want []scan.Raw
	for _, f := range []string{"ignored/Db.java", "src/Db.java", "test/Db.java", "vendor/Db.java"} {
		want = append(want, scan.Raw{
			Tool: "opengrep", Rule: "test-password-field",
			Category: finding.CategoryStorage, Kind: finding.KindPassword, File: f,
			Offset: start, EndOffset: start + len(value),
		})
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Scan() mismatch (-want +got):\n%s", diff)
	}
}
