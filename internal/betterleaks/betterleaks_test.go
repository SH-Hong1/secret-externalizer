package betterleaks_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/betterleaks"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
)

// leak is one result in the shape betterleaks writes, reduced to the fields
// the parser reads.
type leak struct {
	RuleID      string
	StartLine   int
	StartColumn int
	Match       string
	Secret      string
	File        string
	Tags        []string
}

func report(t *testing.T, leaks ...leak) []byte {
	t.Helper()
	out, err := json.Marshal(leaks) //nolint:gosec // G117: the report holds fake values only
	if err != nil {
		t.Fatalf("json.Marshal(leaks) = %v", err)
	}
	return out
}

// bytesComparer compares Values by their bytes; cmp would otherwise format
// them, and Value formats as [redacted].
var bytesComparer = cmp.Comparer(func(a, b scan.Value) bool { return string(a) == string(b) })

func TestParse_Betterleaks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, counts, err := betterleaks.Parse(data, "/scan")
	if err != nil {
		t.Fatalf("Parse(report.json) error = %v", err)
	}
	raw := func(rule string, c finding.Category, k finding.Kind, file string, line, col int, value string) scan.Raw {
		return scan.Raw{
			Tool: "betterleaks", Rule: rule, Category: c, Kind: k, File: file,
			Offset: -1, EndOffset: -1, Line: line, ByteCol: col, Value: scan.Value(value),
		}
	}
	want := []scan.Raw{
		raw("fake-multiline", finding.CategoryOther, finding.KindPassword, "config/multi.yml", 3, 5, "dev-only-fake-multiline-pw"),
		raw("fake-password-assign", finding.CategoryStorage, finding.KindPassword, "src/main/java/shop/Db.java", 4, 43, "dev-only-fake-hangul-pw"),
		raw("fake-password-assign", finding.CategoryStorage, finding.KindPassword, "src/main/resources/application.properties", 2, 13, "dev-only-fake-db-password"),
		raw("fake-generic", finding.CategoryOther, finding.KindOther, "src/main/resources/application.properties", 3, 8, "dev-only-fake-api-key"),
		raw("fake-private-key", finding.CategorySSH, finding.KindPrivateKey, "config/fake.key", 1, 1, "-----BEGIN FAKE PRIVATE KEY-----"),
		raw("fake-password-assign", finding.CategoryStorage, finding.KindPassword, "src/main/java/shop/Broken.java", 2, 22, "dev-only-fake-broken-pw"),
	}
	if diff := cmp.Diff(want, got, bytesComparer); diff != "" {
		t.Errorf("Parse(report.json) mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(scan.Counts{}, counts); diff != "" {
		t.Errorf("Parse(report.json) counts mismatch (-want +got):\n%s", diff)
	}
}

// parseOne parses a report holding l and returns its only result.
func parseOne(t *testing.T, l leak) scan.Raw {
	t.Helper()
	got, counts, err := betterleaks.Parse(report(t, l), "/scan")
	if err != nil {
		t.Fatalf("Parse(%+v) error = %v", l, err)
	}
	if len(got) != 1 || counts.Dropped != 0 {
		t.Fatalf("Parse(%+v) = %d results, %d dropped, want 1 and 0", l, len(got), counts.Dropped)
	}
	return got[0]
}

func TestParse_Betterleaks_SecretInsideMatch(t *testing.T) {
	got := parseOne(t, leak{
		RuleID: "fake-rule", StartLine: 7, StartColumn: 5,
		Match: `password = "abc"`, Secret: "abc", File: "/scan/a.properties",
		Tags: []string{"category:storage-credential", "kind:password"},
	})
	if got.Line != 7 || got.ByteCol != 17 {
		t.Errorf("Parse(secret inside match) = line %d col %d, want line 7 col 17", got.Line, got.ByteCol)
	}
}

// betterleaks runs in the scanned folder with the target ".", so it reports
// paths relative to that folder.
func TestParse_Betterleaks_RelativeFile(t *testing.T) {
	got := parseOne(t, leak{
		RuleID: "fake-rule", StartLine: 1, StartColumn: 1,
		Match: "abc", Secret: "abc", File: "logs/app.log",
		Tags: []string{"category:customer-data", "kind:phone"},
	})
	if got.File != "logs/app.log" {
		t.Errorf("Parse(relative file) file = %q, want logs/app.log", got.File)
	}
}

func TestParse_Betterleaks_MultiLineMatch(t *testing.T) {
	got := parseOne(t, leak{
		RuleID: "fake-rule", StartLine: 7, StartColumn: 5,
		Match: "password: |\r\n  abc", Secret: "abc", File: "/scan/a.yml",
		Tags: []string{"category:storage-credential", "kind:password"},
	})
	if got.Line != 8 || got.ByteCol != 3 {
		t.Errorf("Parse(multi-line match) = line %d col %d, want line 8 col 3", got.Line, got.ByteCol)
	}
}

func TestParse_Betterleaks_FirstOccurrence(t *testing.T) {
	got := parseOne(t, leak{
		RuleID: "fake-rule", StartLine: 1, StartColumn: 1,
		Match: "abc=abc", Secret: "abc", File: "/scan/a.properties",
		Tags: []string{"category:storage-credential", "kind:password"},
	})
	if got.Line != 1 || got.ByteCol != 1 {
		t.Errorf("Parse(secret twice in match) = line %d col %d, want line 1 col 1", got.Line, got.ByteCol)
	}
}

func TestParse_Betterleaks_Dropped(t *testing.T) {
	tags := []string{"category:storage-credential", "kind:password"}
	tests := []struct {
		name string
		leak leak
	}{
		{"SecretNotInMatch", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "password = xyz", Secret: "abc", File: "/scan/a", Tags: tags}},
		{"EmptySecret", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "password = ", Secret: "", File: "/scan/a", Tags: tags}},
		{"FileOutsideDir", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "abc", Secret: "abc", File: "/elsewhere/a", Tags: tags}},
		{"FileEscapesDir", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "abc", Secret: "abc", File: "/scan/../a", Tags: tags}},
		{"RelativeFileEscapesDir", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "abc", Secret: "abc", File: "../a", Tags: tags}},
		{"NoLine", leak{RuleID: "r", StartLine: 0, StartColumn: 1, Match: "abc", Secret: "abc", File: "/scan/a", Tags: tags}},
		{"NoColumn", leak{RuleID: "r", StartLine: 1, StartColumn: 0, Match: "abc", Secret: "abc", File: "/scan/a", Tags: tags}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept := leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "abc", Secret: "abc", File: "/scan/b", Tags: tags}
			got, counts, err := betterleaks.Parse(report(t, tt.leak, kept), "/scan")
			if err != nil {
				t.Fatalf("Parse(%+v) error = %v", tt.leak, err)
			}
			if len(got) != 1 || got[0].File != "b" || counts.Dropped != 1 {
				t.Errorf("Parse(%+v) = %d results, dropped %d, want only b and dropped 1", tt.leak, len(got), counts.Dropped)
			}
		})
	}
}

func TestParse_Betterleaks_DroppedPaths(t *testing.T) {
	tags := []string{"category:storage-credential", "kind:password"}
	tests := []struct {
		name string
		leak leak
		want []string
	}{
		{"SecretNotInMatch", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "password = xyz", Secret: "abc", File: "/scan/conf/a.properties", Tags: tags}, []string{"conf/a.properties"}},
		{"RelativeFile", leak{RuleID: "r", StartLine: 0, StartColumn: 1, Match: "abc", Secret: "abc", File: "conf/a.properties", Tags: tags}, []string{"conf/a.properties"}},
		{"NoColumn", leak{RuleID: "r", StartLine: 1, StartColumn: 0, Match: "abc", Secret: "abc", File: "/scan/a", Tags: tags}, []string{"a"}},
		{"FileEscapesDir", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "x", Secret: "abc", File: "/scan/../a", Tags: tags}, nil},
		{"NoFile", leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "x", Secret: "abc", File: "", Tags: tags}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept := leak{RuleID: "r", StartLine: 1, StartColumn: 1, Match: "abc", Secret: "abc", File: "/scan/b", Tags: tags}
			got, counts, err := betterleaks.Parse(report(t, tt.leak, kept), "/scan")
			if err != nil {
				t.Fatalf("Parse(%+v) error = %v", tt.leak, err)
			}
			if len(got) != 1 || counts.Dropped != 1 {
				t.Errorf("Parse(%+v) = %d results, dropped %d, want 1 and 1", tt.leak, len(got), counts.Dropped)
			}
			if diff := cmp.Diff(tt.want, counts.DroppedFiles); diff != "" {
				t.Errorf("Parse(%+v) DroppedFiles mismatch (-want +got):\n%s", tt.leak, diff)
			}
		})
	}
}

func TestParse_Betterleaks_SecretNotInMatch(t *testing.T) {
	got, counts, err := betterleaks.Parse(report(t, leak{
		RuleID: "r", StartLine: 1, StartColumn: 1, Match: `password = "xyz"`, Secret: "abc", File: "/scan/a",
	}), "/scan")
	if err != nil || len(got) != 0 || counts.Dropped != 1 {
		t.Errorf("Parse(secret not in match) = %d results, %+v, %v, want 0 results, dropped 1, no error", len(got), counts, err)
	}
}

func TestParse_Betterleaks_Classification(t *testing.T) {
	tests := []struct {
		name     string
		rule     string
		tags     []string
		category finding.Category
		kind     finding.Kind
		wantErr  bool
	}{
		{"Tags", "r", []string{"kind:token", "other", "category:partner-session"}, finding.CategoryPartnerSession, finding.KindToken, false},
		{"NoTags", "generic-api-key", nil, finding.CategoryOther, finding.KindOther, false},
		{"NoTagsPrivateKey", "private-key", nil, finding.CategorySSH, finding.KindPrivateKey, false},
		{"NoTagsPrivateKeyInID", "pkcs8-private-key-file", nil, finding.CategorySSH, finding.KindPrivateKey, false},
		{"OnlyCategory", "r", []string{"category:partner-session"}, "", "", true},
		{"OnlyKind", "r", []string{"kind:token"}, "", "", true},
		{"UnknownCategory", "r", []string{"category:dev-only-fake-leak", "kind:token"}, "", "", true},
		{"UnknownKind", "r", []string{"category:partner-session", "kind:dev-only-fake-leak"}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := leak{RuleID: tt.rule, StartLine: 1, StartColumn: 1, Match: "abc", Secret: "abc", File: "/scan/a", Tags: tt.tags}
			got, _, err := betterleaks.Parse(report(t, l), "/scan")
			if tt.wantErr {
				if !errors.Is(err, scan.ErrScannerFailed) || strings.Contains(err.Error(), "dev-only-fake-leak") {
					t.Errorf("Parse(tags %q) error = %v, want ErrScannerFailed without the tag text", tt.tags, err)
				}
				return
			}
			if err != nil || len(got) != 1 {
				t.Fatalf("Parse(tags %q) = %d results, %v, want 1 result", tt.tags, len(got), err)
			}
			if got[0].Category != tt.category || got[0].Kind != tt.kind {
				t.Errorf("Parse(rule %q, tags %q) = %s/%s, want %s/%s", tt.rule, tt.tags, got[0].Category, got[0].Kind, tt.category, tt.kind)
			}
		})
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
	checkParseFails(t, []byte(`{"dev-only-fake-leak": 1}`))
	checkParseFails(t, []byte(`[{"StartLine": "dev-only-fake-leak"}]`))
	checkParseFails(t, nil)
}

func checkParseFails(t *testing.T, data []byte) {
	t.Helper()
	_, _, err := betterleaks.Parse(data, "/scan")
	if !errors.Is(err, scan.ErrScannerFailed) {
		t.Fatalf("Parse(%.40q) error = %v, want ErrScannerFailed", data, err)
	}
	for _, s := range []string{"dev-only-fake", "password", "Secret", "StartLine"} {
		if strings.Contains(fmt.Sprintf("%+v", err), s) {
			t.Errorf("Parse(%.40q) error = %q, contains %q from the input", data, err, s)
		}
	}
}

// fakeScanner writes a shell script that stands in for betterleaks and
// returns its path.
func fakeScanner(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake scanners are shell scripts")
	}
	bin := filepath.Join(t.TempDir(), "betterleaks")
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
	bin := fakeScanner(t, `echo '[{"Secret": "dev-only-fake-leak"}]'; echo dev-only-fake-leak >&2; exit 1`)
	s := betterleaks.New(bin, "/rules/betterleaks.toml", testEnv(t))
	_, err := s.Scan(t.Context(), t.TempDir())
	if !errors.Is(err, scan.ErrScannerFailed) {
		t.Fatalf("Scan() error = %v, want ErrScannerFailed", err)
	}
	if msg := fmt.Sprintf("%+v", err); strings.Contains(msg, "dev-only-fake-leak") {
		t.Errorf("Scan() error = %q, contains the scanner output", msg)
	}
	if !strings.Contains(err.Error(), "betterleaks") || !strings.Contains(err.Error(), "1") {
		t.Errorf("Scan() error = %q, want the tool name and the exit code", err)
	}
}

func TestScan_Betterleaks_ExitCode3(t *testing.T) {
	dir := t.TempDir()
	out := report(t, leak{
		RuleID: "r", StartLine: 1, StartColumn: 1, Match: "abc", Secret: "abc",
		File: filepath.Join(dir, "a"), Tags: []string{"category:other-secret", "kind:other"},
	})
	bin := fakeScanner(t, "cat <<'EOF'\n"+string(out)+"\nEOF\nexit 3")
	s := betterleaks.New(bin, "/rules/betterleaks.toml", testEnv(t))
	got, err := s.Scan(t.Context(), dir)
	if err != nil {
		t.Fatalf("Scan() with exit code 3 error = %v, want nil", err)
	}
	if len(got) != 1 || got[0].File != "a" {
		t.Errorf("Scan() = %+v, want one result in a", got)
	}
}

func TestScan_Args(t *testing.T) {
	env := testEnv(t)
	bin := fakeScanner(t, `pwd > "$HOME/pwd"; for a in "$@"; do printf '%s\n' "$a"; done > "$HOME/args"; echo '[]'`)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := betterleaks.New(bin, "/rules/betterleaks.toml", env)
	got, counts, err := s.ScanWithCounts(t.Context(), dir)
	if err != nil || len(got) != 0 || !cmp.Equal(counts, scan.Counts{}) {
		t.Fatalf("ScanWithCounts() = %v, %+v, %v, want no results and no error", got, counts, err)
	}
	args, err := os.ReadFile(filepath.Join(env.Home, "args"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dir", "--no-banner", "--config=/rules/betterleaks.toml",
		"--report-format=json", "--report-path=-", "--exit-code=3",
		"--max-target-megabytes=20", "--timeout=1500", "--log-level=warn",
		"--ignore-gitleaks-allow", "--", ".",
	}
	if diff := cmp.Diff(want, strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")); diff != "" {
		t.Errorf("betterleaks arguments mismatch (-want +got):\n%s", diff)
	}
	pwd, err := os.ReadFile(filepath.Join(env.Home, "pwd"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(pwd)); got != dir {
		t.Errorf("betterleaks working directory = %s, want the scanned folder %s", got, dir)
	}
}

func TestScan_RelativeDir(t *testing.T) {
	bin := fakeScanner(t, `echo '[]'`)
	s := betterleaks.New(bin, "/rules/betterleaks.toml", testEnv(t))
	if _, err := s.Scan(t.Context(), "relative/dir"); err == nil {
		t.Errorf("Scan(relative/dir) error = nil, want an error")
	}
}

func TestVersion(t *testing.T) {
	bin := fakeScanner(t, `[ "$1" = version ] && echo 1.10.1`)
	s := betterleaks.New(bin, "/rules/betterleaks.toml", testEnv(t))
	got, err := s.Version(t.Context())
	if err != nil || got != "1.10.1" {
		t.Errorf("Version() = %q, %v, want 1.10.1", got, err)
	}
	if s.Name() != "betterleaks" {
		t.Errorf("Name() = %q, want betterleaks", s.Name())
	}
}

// realBinary returns the betterleaks executable that make scanners installs,
// or skips the test when it is not there.
func realBinary(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin", "betterleaks"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no betterleaks executable at %s; run make scanners", bin)
	}
	return bin
}

func TestScan_RealBetterleaks(t *testing.T) {
	bin := realBinary(t)
	config, err := filepath.Abs(filepath.Join("testdata", "rules.toml"))
	if err != nil {
		t.Fatal(err)
	}
	const value = "dev-only-fake-real-run"
	line := `    /* 비밀번호 */ String password = "` + value + `"; // gitleaks:allow`
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "Db.java"), []byte("class Db {\n"+line+"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := betterleaks.New(bin, config, testEnv(t)).Scan(t.Context(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	want := []scan.Raw{{
		Tool: "betterleaks", Rule: "test-password-assign",
		Category: finding.CategoryStorage, Kind: finding.KindPassword, File: "src/Db.java",
		Offset: -1, EndOffset: -1, Line: 2, ByteCol: strings.Index(line, value) + 1, Value: scan.Value(value),
	}}
	if diff := cmp.Diff(want, got, bytesComparer); diff != "" {
		t.Errorf("Scan() mismatch (-want +got):\n%s", diff)
	}
}
