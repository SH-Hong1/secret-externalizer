package scan_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/korean"

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
