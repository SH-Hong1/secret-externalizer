package scan_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/korean"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/betterleaks"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/opengrep"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
)

// realBin returns the scanner executable named by the environment variable
// env or, failing that, the one in the repository's bin/ folder. It skips
// the test when there is none.
func realBin(t *testing.T, env, name string) string {
	t.Helper()
	p := os.Getenv(env)
	if p == "" {
		p = filepath.Join("..", "..", "bin", name)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("Abs(%q) error = %v", p, err)
	}
	if _, err := os.Stat(abs); err != nil { //nolint:gosec // G703: the path is the developer's own setting or the repository's bin/
		t.Skipf("%s is not installed (%v); run make scanners", name, err)
	}
	return abs
}

// TestHandle_RealScanners checks the offset and column arithmetic against
// what betterleaks and opengrep really report for a file with a byte order
// mark and for an EUC-KR file with CRLF line endings.
func TestHandle_RealScanners(t *testing.T) {
	blBin := realBin(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := realBin(t, "OPENGREP_BIN", "opengrep")
	rules, err := filepath.Abs(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatalf("Abs(rules) error = %v", err)
	}

	euckr, err := korean.EUCKR.NewEncoder().String("# 주문 설정\r\njdbc.password=" + password + "\r\n")
	if err != nil {
		t.Fatalf("encoding EUC-KR: %v", err)
	}
	// Hangul before the value on the same line: betterleaks reports a byte
	// column, opengrep a byte offset.
	javaLine := `String 설명 = "주문"; String password = "` + password + `";` + "\r\n"
	javaEUCKR, err := korean.EUCKR.NewEncoder().String(javaLine)
	if err != nil {
		t.Fatalf("encoding EUC-KR: %v", err)
	}
	e := newEnv(t, map[string]string{
		"conf/euckr.properties": euckr,
		"conf/bom.properties":   "\xef\xbb\xbf# 설정\r\njdbc.password=" + password + "\r\n",
		"src/A.java":            javaLine,
		"src/B.java":            javaEUCKR,
	})
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
	bl := betterleaks.New(blBin, filepath.Join(rules, "betterleaks.toml"), env)
	og := opengrep.New(ogBin, filepath.Join(rules, "opengrep"), nil, env)

	if err := e.handle(t.Context(), bl, og); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 0 || len(got.Findings) != 4 {
		t.Fatalf("Handle() = %+v, want four findings and nothing dropped", got)
	}
	javaCol := len([]rune(`String 설명 = "주문"; String password = "`)) + 1
	for i, file := range []string{"src/A.java", "src/B.java"} {
		f := got.Findings[2+i]
		if f.File != file || f.Fingerprint != fingerprintOf(password) || len(f.Locations) != 1 {
			t.Errorf("finding %d = %+v, want the password in %s", 2+i, f, file)
			continue
		}
		l := f.Locations[0]
		if l.StartLine != 1 || l.StartCol != javaCol || l.EndLine != 1 || l.EndCol != javaCol+len(password)-1 || len(l.Detections) != 2 {
			t.Errorf("location in %s = %+v, want line 1 columns %d to %d found by both tools", file, l, javaCol, javaCol+len(password)-1)
		}
	}
	for i, file := range []string{"conf/bom.properties", "conf/euckr.properties"} {
		f := got.Findings[i]
		if f.File != file || f.Fingerprint != fingerprintOf(password) || f.Category != finding.CategoryStorage || len(f.Locations) != 1 {
			t.Errorf("finding %d = %+v, want the password in %s", i, f, file)
			continue
		}
		l := f.Locations[0]
		if l.StartLine != 2 || l.StartCol != 15 || l.EndCol != 14+len(password) || len(l.Detections) != 2 {
			t.Errorf("location in %s = %+v, want line 2 column 15 found by both tools", file, l)
		}
	}
	if len(e.tempEntries(t)) != 0 {
		t.Errorf("temporary folder holds %v after the scan, want it empty", e.tempEntries(t))
	}
}

// TestHandle_RealScannersContextMasksEveryValue scans the example project
// of the rule tests and checks that no value the scan parked, four bytes or
// longer, appears in any stored surrounding line, wherever it was found.
func TestHandle_RealScannersContextMasksEveryValue(t *testing.T) {
	blBin := realBin(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := realBin(t, "OPENGREP_BIN", "opengrep")
	rules, err := filepath.Abs(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatalf("Abs(rules) error = %v", err)
	}
	e := newEnv(t, nil)
	if err := os.CopyFS(e.dir, os.DirFS(filepath.Join("..", "ruleset", "testdata", "legacy-shop"))); err != nil {
		t.Fatalf("copying legacy-shop: %v", err)
	}
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
	bl := betterleaks.New(blBin, filepath.Join(rules, "betterleaks.toml"), env)
	og := opengrep.New(ogBin, filepath.Join(rules, "opengrep"), nil, env)

	if err := e.handle(t.Context(), bl, og); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	var values []string
	for _, v := range e.orig.pending {
		if len(v) >= 4 {
			values = append(values, string(v))
		}
	}
	lines := 0
	for _, f := range got.Findings {
		for _, l := range f.Locations {
			for _, line := range l.Context {
				lines++
				if v := containsAny(line, values...); v != "" {
					t.Errorf("%s line %d: surrounding line %q holds a parked value", f.File, l.StartLine, line)
				}
			}
		}
	}
	if len(values) == 0 || lines == 0 {
		t.Fatalf("Handle(legacy-shop) parked %d values and stored %d surrounding lines, want some of both", len(values), lines)
	}
}

// TestHandle_RealScannersEncodedValue checks a value betterleaks finds only
// after decoding base64: its decoded form appears nowhere in the file, so it
// cannot be located, and the encoded text must not be stored in a
// neighbour's surrounding lines.
func TestHandle_RealScannersEncodedValue(t *testing.T) {
	blBin := realBin(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := realBin(t, "OPENGREP_BIN", "opengrep")
	rules, err := filepath.Abs(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatalf("Abs(rules) error = %v", err)
	}
	blob := base64.StdEncoding.EncodeToString([]byte(`password = "dev-only-fake-encoded-pw-7"`))
	// The same password in plain text and base64-encoded: the decoded value
	// is found in the text, but not at the place betterleaks reports.
	mixedBlob := base64.StdEncoding.EncodeToString([]byte(`password = "` + password + `"`))
	e := newEnv(t, map[string]string{
		"conf/app.properties":   "a.user=dev\nblob=" + blob + "\njdbc.password=" + password + "\n",
		"conf/mixed.properties": "a.user=dev\nblob=" + mixedBlob + "\njdbc.password=" + password + "\nx=1\n",
	})
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
	bl := betterleaks.New(blBin, filepath.Join(rules, "betterleaks.toml"), env)
	og := opengrep.New(ogBin, filepath.Join(rules, "opengrep"), nil, env)

	if err := e.handle(t.Context(), bl, og); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 2 || len(got.Findings) == 0 {
		t.Fatalf("Handle() = %+v, want the passwords found and both decoded values dropped", got)
	}
	for _, f := range got.Findings {
		for _, l := range f.Locations {
			if l.Context != nil {
				t.Errorf("%s location = %+v, want no surrounding lines in a file with a value that cannot be located", f.Kind, l)
			}
		}
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	for _, dump := range []string{fmt.Sprintf("%+v", got), string(encoded)} {
		if v := containsAny(dump, blob, mixedBlob, "dev-only-fake-encoded-pw-7"); v != "" {
			t.Errorf("Handle() saved a result that holds %q:\n%s", v, dump)
		}
	}
}

// TestHandle_RealScannersSplitURL checks that the password betterleaks
// reports inside a JDBC URL and the password split from opengrep's URL are
// one location of one finding.
func TestHandle_RealScannersSplitURL(t *testing.T) {
	blBin := realBin(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := realBin(t, "OPENGREP_BIN", "opengrep")
	rules, err := filepath.Abs(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatalf("Abs(rules) error = %v", err)
	}
	const urlPassword = "dev-only-fake-url-pw-8" //nolint:gosec // G101: an obviously fake fixture value
	e := newEnv(t, map[string]string{
		"src/main/resources/application.yml": "spring:\n  datasource:\n    url: jdbc:mysql://shop_app:" + urlPassword + "@db.invalid:3306/shop\n",
	})
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
	bl := betterleaks.New(blBin, filepath.Join(rules, "betterleaks.toml"), env)
	og := opengrep.New(ogBin, filepath.Join(rules, "opengrep"), nil, env)

	if err := e.handle(t.Context(), bl, og); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	kinds := map[finding.Kind]int{}
	for _, f := range got.Findings {
		kinds[f.Kind]++
	}
	wantKinds := map[finding.Kind]int{finding.KindAccount: 1, finding.KindPassword: 1, finding.KindHost: 1, finding.KindPort: 1}
	if got.Dropped != 0 || fmt.Sprint(kinds) != fmt.Sprint(wantKinds) {
		t.Fatalf("Handle() = %+v, want one finding of each component and nothing dropped", got)
	}
	for _, f := range got.Findings {
		if f.Kind != finding.KindPassword {
			continue
		}
		want := []finding.Detection{{Tool: "betterleaks", Rule: "generic-credential-uri"}, {Tool: "opengrep", Rule: "yaml-jdbc-url"}}
		if f.Fingerprint != fingerprintOf(urlPassword) || f.Category != finding.CategoryStorage ||
			len(f.Locations) != 1 || fmt.Sprint(f.Locations[0].Detections) != fmt.Sprint(want) {
			t.Errorf("password finding = %+v, want the password at one location found by %v", f, want)
		}
	}
}

// TestHandle_RealScannersKeysOfOneFile scans the rule cases and checks that
// the same account and the same password under three environment keys of one
// file are six findings, each with the key it is assigned to.
func TestHandle_RealScannersKeysOfOneFile(t *testing.T) {
	blBin := realBin(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := realBin(t, "OPENGREP_BIN", "opengrep")
	rules, err := filepath.Abs(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatalf("Abs(rules) error = %v", err)
	}
	e := newEnv(t, nil)
	if err := os.CopyFS(e.dir, os.DirFS(filepath.Join("..", "ruleset", "testdata", "rule-cases"))); err != nil {
		t.Fatalf("copying rule-cases: %v", err)
	}
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
	bl := betterleaks.New(blBin, filepath.Join(rules, "betterleaks.toml"), env)
	og := opengrep.New(ogBin, filepath.Join(rules, "opengrep"), nil, env)

	if err := e.handle(t.Context(), bl, og); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	const file = "conf/multi-env.properties"
	type found struct {
		Kind finding.Kind
		Line int
	}
	got := map[string]found{}
	for _, f := range e.result(t).Findings {
		if f.File != file || f.Category != finding.CategoryStorage {
			continue
		}
		if _, dup := got[f.Key]; dup {
			t.Errorf("Handle() has two storage findings under key %q in %s, want one", f.Key, file)
		}
		got[f.Key] = found{f.Kind, f.Locations[0].StartLine}
	}
	want := map[string]found{
		"local.db.username": {finding.KindAccount, 1},
		"local.db.password": {finding.KindPassword, 2},
		"dev.db.username":   {finding.KindAccount, 3},
		"dev.db.password":   {finding.KindPassword, 4},
		"live.db.username":  {finding.KindAccount, 5},
		"live.db.password":  {finding.KindPassword, 6},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("storage findings of %s by key mismatch (-want +got):\n%s", file, diff)
	}
}
