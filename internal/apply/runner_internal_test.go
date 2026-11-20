package apply

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/rewrite"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

const (
	fakeKey   = "-----BEGIN RSA PRIVATE KEY-----\nZGV2LW9ubHktZmFrZS1rZXk=\n-----END RSA PRIVATE KEY-----\n" //nolint:gosec // G101: an obviously fake key
	fakeValue = "dev-only-fake-internal-password"
)

// readFile returns a sourceFile for content as Dir.Read would give it, with
// a whole-file target and a target at fakeValue on its second line.
func readFile(t *testing.T, content string) *sourceFile {
	t.Helper()
	dir := t.TempDir()
	d, err := rewrite.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s) error = %v", dir, err)
	}
	t.Cleanup(func() { _ = d.Close() }) // only read through; nothing to lose
	if err := writeFile(dir, "key.pem", content); err != nil {
		t.Fatalf("writing key.pem: %v", err)
	}
	raw, seen, err := d.Read("key.pem", 1<<20)
	if err != nil {
		t.Fatalf("Read(key.pem) error = %v", err)
	}
	tf, err := textfile.Decode(raw)
	if err != nil {
		t.Fatalf("Decode(key.pem) error = %v", err)
	}
	whole := &target{Target: Target{WholeFile: true, Location: finding.Location{FileHash: tf.Hash}}}
	line := &target{Target: Target{Location: finding.Location{
		StartLine: 2, StartCol: 1, EndLine: 2, EndCol: len(fakeValue), FileHash: tf.Hash,
	}}}
	return &sourceFile{rel: "key.pem", raw: value(raw), seen: seen, targets: []*target{whole, line}}
}

func TestValueAt_WholeFileIsACopy(t *testing.T) {
	f := readFile(t, "first\n"+fakeValue+"\n")
	for _, tg := range f.targets {
		if reason, v := f.valueAt(tg, 1<<20); reason != "" {
			t.Fatalf("valueAt() reason = %q, want none", reason)
		} else {
			tg.value = v
		}
	}
	clear(f.targets[0].value)
	if got := string(f.raw); got != "first\n"+fakeValue+"\n" {
		t.Errorf("file buffer after zeroing the whole-file value = %q, want it unchanged", got)
	}
	text, err := f.decode()
	if err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	if !bytes.Contains(text.Text, []byte(fakeValue)) || string(f.targets[1].value) != fakeValue {
		t.Errorf("text or line value changed after zeroing the whole-file value")
	}
}

func TestPrepared_CloseZeroes(t *testing.T) {
	f := readFile(t, "first\n"+fakeValue+"\n")
	raw := f.raw
	text, err := f.decode()
	if err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	var values []value
	for _, tg := range f.targets {
		_, tg.value = f.valueAt(tg, 1<<20)
		values = append(values, tg.value)
	}
	p := &prepared{files: []*sourceFile{f}}

	p.close()

	for i, buf := range append([]value{raw, value(text.Text)}, values...) {
		if bytes.ContainsFunc(buf, func(r rune) bool { return r != 0 }) {
			t.Errorf("buffer %d after close() = %q, want only zero bytes", i, buf)
		}
	}
}

func TestPrepared_Redacted(t *testing.T) {
	f := readFile(t, fakeKey)
	for _, tg := range f.targets {
		_, tg.value = f.valueAt(tg, 1<<20)
	}
	if _, err := f.decode(); err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	p := &prepared{files: []*sourceFile{f}}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("prepared", "prepared", p, "file", f)
	for _, out := range []string{
		fmt.Sprintf("%v %+v %#v %s %x", p, p, p, p, p),
		fmt.Sprintf("%v %+v %#v %s %x", f, f, f, f, f),
		logs.String(),
	} {
		for _, v := range []string{"ZGV2LW9ubHktZmFrZS1rZXk", "BEGIN RSA", "5a4756324c5739"} {
			if strings.Contains(out, v) {
				t.Errorf("printed form %q holds %q", out, v)
			}
		}
	}
}

// writeFile writes content to rel under dir.
func writeFile(dir, rel, content string) error {
	return os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o600)
}
