package apply_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// workerLog returns what a failure message shows of the worker log: the
// digest built by logDigest, or the whole log when SHOW_WORKER_LOG is 1.
// The tests that use it run the real scanners, and the public sample's
// values are real text, so a regression that logs a value and a failure
// that prints the log together would put the value in the CI log.
func workerLog(logs *bytes.Buffer) string {
	if os.Getenv("SHOW_WORKER_LOG") == "1" {
		return logs.String()
	}
	return logDigest(logs.String()) + "(attribute values withheld; set SHOW_WORKER_LOG=1 to see the whole log)\n"
}

// logDigest returns one line for each JSON record of log: its level, its
// message, and the names of its attributes, never their values. A line
// that is not a record, and a message that is not a string, is withheld.
func logDigest(log string) string {
	var b strings.Builder
	for line := range strings.Lines(log) {
		var rec map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			b.WriteString("(a line that is not a JSON log record)\n")
			continue
		}
		var level, msg string
		if err := json.Unmarshal(rec["level"], &level); err != nil {
			level = "(a level that is not a string)"
		}
		if err := json.Unmarshal(rec["msg"], &msg); err != nil {
			msg = "(a message that is not a string)"
		}
		keys := make([]string, 0, len(rec))
		for k := range rec {
			if k != "time" && k != "level" && k != "msg" {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		fmt.Fprintf(&b, "%s %s [%s]\n", level, msg, strings.Join(keys, " "))
	}
	return b.String()
}

func TestLogDigest(t *testing.T) {
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
			name: "a non-string message is withheld",
			log:  `{"level":"INFO","msg":{"value":"dev-only-fake-value"}}` + "\n",
			want: "INFO (a message that is not a string) []\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logDigest(tt.log); got != tt.want {
				t.Errorf("logDigest(%q) = %q, want %q", tt.log, got, tt.want)
			}
		})
	}
}
