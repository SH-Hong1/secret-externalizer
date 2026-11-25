package apply_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// workerLog returns what a failure message shows of the worker log: the
// digest built by logDigest, or the whole log when SHOW_WORKER_LOG is 1.
// The tests that use it run the real scanners, and the public sample's
// values are real text, so a regression that logs a value and a failure
// that prints the log together would put the value in the CI log.
func workerLog(t *testing.T, logs *bytes.Buffer) string {
	t.Helper()
	if os.Getenv("SHOW_WORKER_LOG") == "1" {
		return logs.String()
	}
	known := sourceLiterals(t, filepath.Join("..", "..", "cmd"), filepath.Join("..", "..", "internal"))
	return logDigest(logs.String(), known) + "(attribute values withheld; set SHOW_WORKER_LOG=1 to see the whole log)\n"
}

// sourceLiterals returns every string literal of the program's Go files
// under dirs, leaving out test files and testdata folders. A log message
// or attribute name that is one of them was written by the program, not
// read from a scanned file.
func sourceLiterals(t *testing.T, dirs ...string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out[s] = true
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("reading the string literals under %s: %v", dir, err)
		}
	}
	return out
}

// logDigest returns one line for each JSON record of log: its level, its
// message, and the names of its attributes, never their values. A message
// or name that is not in known, a level slog does not write, and a line
// that is not a record are withheld.
func logDigest(log string, known map[string]bool) string {
	var b strings.Builder
	for line := range strings.Lines(log) {
		var rec map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			b.WriteString("(a line that is not a JSON log record)\n")
			continue
		}
		var level, msg string
		if err := json.Unmarshal(rec["level"], &level); err != nil || !slices.Contains([]string{"DEBUG", "INFO", "WARN", "ERROR"}, level) {
			level = "(level withheld)"
		}
		if err := json.Unmarshal(rec["msg"], &msg); err != nil || !known[msg] {
			msg = "(message withheld)"
		}
		keys := make([]string, 0, len(rec))
		for k := range rec {
			if k == "time" || k == "level" || k == "msg" {
				continue
			}
			if !known[k] {
				k = "(name withheld)"
			}
			keys = append(keys, k)
		}
		slices.Sort(keys)
		fmt.Fprintf(&b, "%s %s [%s]\n", level, msg, strings.Join(keys, " "))
	}
	return b.String()
}

func TestLogDigest(t *testing.T) {
	known := map[string]bool{
		"scan started": true, "scan failed": true, "apply file skipped": true, "done": true,
		"scan_id": true, "error": true, "item": true,
	}
	tests := []struct {
		name string
		log  string
		want string
	}{
		{
			name: "empty",
			log:  "",
			want: "",
		},
		{
			name: "records keep level, message and attribute names",
			log: `{"time":"2026-11-25T10:00:00Z","level":"INFO","msg":"scan started","scan_id":7}` + "\n" +
				`{"time":"2026-11-25T10:00:01Z","level":"ERROR","msg":"scan failed","error":"dev-only-fake-secret-value","scan_id":7}` + "\n",
			want: "INFO scan started [scan_id]\n" +
				"ERROR scan failed [error scan_id]\n",
		},
		{
			name: "a group shows its name, not its content",
			log:  `{"level":"WARN","msg":"apply file skipped","item":{"file":"a.yml","value":"dev-only-fake-value"}}` + "\n",
			want: "WARN apply file skipped [item]\n",
		},
		{
			name: "a line that is not a record is withheld",
			log:  "dev-only-fake-plain-line\n" + `{"level":"INFO","msg":"done"}` + "\n",
			want: "(a line that is not a JSON log record)\n" +
				"INFO done []\n",
		},
		{
			name: "a message the program does not write is withheld",
			log:  `{"level":"INFO","msg":"scan failed: dev-only-fake-value"}` + "\n",
			want: "INFO (message withheld) []\n",
		},
		{
			name: "a non-string message is withheld",
			log:  `{"level":"INFO","msg":{"value":"dev-only-fake-value"}}` + "\n",
			want: "INFO (message withheld) []\n",
		},
		{
			name: "an attribute name the program does not write is withheld",
			log:  `{"level":"INFO","msg":"done","dev-only-fake-value":1}` + "\n",
			want: "INFO done [(name withheld)]\n",
		},
		{
			name: "a level slog does not write is withheld",
			log:  `{"level":"dev-only-fake-value","msg":"done"}` + "\n",
			want: "(level withheld) done []\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logDigest(tt.log, known); got != tt.want {
				t.Errorf("logDigest(%q) = %q, want %q", tt.log, got, tt.want)
			}
		})
	}
}

func TestSourceLiterals(t *testing.T) {
	got := sourceLiterals(t, filepath.Join("..", "..", "cmd"), filepath.Join("..", "..", "internal"))
	for _, s := range []string{"scan finished", "apply finished", "scan_id"} {
		if !got[s] {
			t.Errorf("sourceLiterals(cmd, internal)[%q] = false, want true: the program writes it", s)
		}
	}
	// A literal only a test file holds is not the program's.
	if s := "dev-only-fake-plain-line"; got[s] {
		t.Errorf("sourceLiterals(cmd, internal)[%q] = true, want false: only a test file holds it", s)
	}
}
