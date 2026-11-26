package scan_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// logRecords returns the JSON log records with message msg.
func logRecords(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	s := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	for s.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(s.Bytes(), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", s.Text(), err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	return out
}

func TestHandle_LogsSummary(t *testing.T) {
	const binary = "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"
	e := newEnv(t, map[string]string{
		propsFile:             "jdbc.password=" + password + "\n",
		"img/logo.png":        binary,
		"img/bad\x01name.png": binary,
	})
	og := passwordOG()
	og.errors = 2
	// A result in a file that is not text: the scan does not use it.
	og.extra = []scan.Raw{{
		Tool: "opengrep", Rule: "properties-password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
		File: "img/logo.png", Offset: 0, EndOffset: 4,
	}}
	if err := e.handle(t.Context(), og, passwordBL()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := logRecords(t, e.logs, "scan finished")
	if len(got) != 1 {
		t.Fatalf("Handle() logged %d scan finished records, want 1; log:\n%s", len(got), e.logs)
	}
	want := map[string]any{
		"level": "INFO", "msg": "scan finished", "scan_id": 5.0, "job_id": 1.0,
		"files": 3.0, "skipped": 2.0, "dropped": 0.0, "findings": 1.0,
		"results_in_skipped_files": 1.0,
		"scanner_errors":           map[string]any{"betterleaks": 0.0, "keyfile": 0.0, "opengrep": 2.0},
	}
	delete(got[0], "time")
	if diff := cmp.Diff(want, got[0]); diff != "" {
		t.Errorf("Handle() scan finished record (-want +got):\n%s", diff)
	}

	skipped := logRecords(t, e.logs, "scan skipped files")
	if len(skipped) != 1 {
		t.Fatalf("Handle() logged %d scan skipped files records, want 1; log:\n%s", len(skipped), e.logs)
	}
	wantSkipped := map[string]any{
		"level": "INFO", "msg": "scan skipped files", "scan_id": 5.0, "job_id": 1.0,
		"total": 2.0, "paths": []any{"img/bad�name.png", "img/logo.png"},
	}
	delete(skipped[0], "time")
	if diff := cmp.Diff(wantSkipped, skipped[0]); diff != "" {
		t.Errorf("Handle() scan skipped files record (-want +got):\n%s", diff)
	}
}

func TestHandle_LogsAtMost50SkippedPaths(t *testing.T) {
	files := map[string]string{}
	for i := range 52 {
		files[fmt.Sprintf("bin/f%02d.bin", i)] = "\x00"
	}
	e := newEnv(t, files)
	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := logRecords(t, e.logs, "scan skipped files")
	if len(got) != 1 {
		t.Fatalf("Handle() logged %d scan skipped files records, want 1; log:\n%s", len(got), e.logs)
	}
	paths, _ := got[0]["paths"].([]any)
	if got[0]["total"] != 52.0 || len(paths) != 50 || paths[0] != "bin/f00.bin" || paths[49] != "bin/f49.bin" {
		t.Errorf("Handle() scan skipped files record = %v, want total 52 and the first 50 paths", got[0])
	}
}

func TestHandle_NoSkippedFilesRecordWithoutSkippedFiles(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := logRecords(t, e.logs, "scan skipped files"); len(got) != 0 {
		t.Errorf("Handle() logged %v, want no skipped files record", got)
	}
}

func TestHandle_KeepsSkippedFileNames(t *testing.T) {
	files := map[string]string{"conf/ok.properties": "x=1\n"}
	for i := range 250 {
		files[fmt.Sprintf("bin/f%03d.bin", i)] = "\x00"
	}
	e := newEnv(t, files)
	// Paths that are not valid UTF-8 or hold a control character, a format
	// character, or a line or paragraph separator are counted but not kept.
	// A file system may refuse such a name.
	unkept := 0
	for _, rel := range []string{"bin/a\x01.bin", "bin/a\t.bin", "bin/a\xff.bin", "bin/a\u202e.bin", "bin/a\u200b.bin", "bin/a\u2028.bin", "bin/a\u2029.bin"} {
		if err := os.WriteFile(filepath.Join(e.dir, filepath.FromSlash(rel)), []byte("\x00"), 0o600); err == nil {
			unkept++
		}
	}
	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Skipped != 250+unkept {
		t.Errorf("Handle() Skipped = %d, want %d", got.Skipped, 250+unkept)
	}
	want := make([]string, 0, 200)
	for i := range 200 {
		want = append(want, fmt.Sprintf("bin/f%03d.bin", i))
	}
	if diff := cmp.Diff(want, got.SkippedFiles); diff != "" {
		t.Errorf("Handle() SkippedFiles (-want +got):\n%s", diff)
	}
}

func TestHandle_NoSkippedFileNames(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := e.result(t); len(got.SkippedFiles) != 0 || len(got.ScannerErrors) != 0 {
		t.Errorf("Handle() SkippedFiles = %q, ScannerErrors = %v, want none", got.SkippedFiles, got.ScannerErrors)
	}
}

func TestHandle_KeepsScannerErrors(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	og := passwordOG()
	og.errors = 3
	if err := e.handle(t.Context(), og, passwordBL()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	want := map[string]int{"opengrep": 3}
	if diff := cmp.Diff(want, e.result(t).ScannerErrors); diff != "" {
		t.Errorf("Handle() ScannerErrors (-want +got):\n%s", diff)
	}
}
