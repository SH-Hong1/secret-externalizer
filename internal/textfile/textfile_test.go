package textfile_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

func hashOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is a fixture name from this test file
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", p, err)
	}
	return p
}

func TestRead(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		wantEnc  textfile.Encoding
		wantText string
	}{
		{"utf-8", "utf8.txt", textfile.UTF8, "비밀번호=dev-only-fake-value\n"},
		{"utf-8 with BOM", "utf8-bom.txt", textfile.UTF8, "key=값\r\n"},
		{"BOM only", "bom-only.txt", textfile.UTF8, ""},
		{"euc-kr", "euckr.txt", textfile.EUCKR, "비밀번호=dev-only-fake-value\n"},
		{"euc-kr with CRLF", "euckr-crlf.properties", textfile.EUCKR, "# 데이터베이스 설정\r\ndb.password=dev-only-fake-password\r\nadmin.phone=010-0000-0000"},
		{"cp949 extended hangul", "cp949-extended.txt", textfile.EUCKR, "이름=똠방각하\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("testdata", tt.file)
			got, err := textfile.Read(path, 1<<20)
			if err != nil {
				t.Fatalf("Read(%q) error = %v", path, err)
			}
			if got.Encoding != tt.wantEnc {
				t.Errorf("Read(%q).Encoding = %v, want %v", path, got.Encoding, tt.wantEnc)
			}
			if diff := cmp.Diff(tt.wantText, string(got.Text)); diff != "" {
				t.Errorf("Read(%q).Text mismatch (-want +got):\n%s", path, diff)
			}
			if want := hashOf(t, path); got.Hash != want {
				t.Errorf("Read(%q).Hash = %q, want %q (SHA-256 of the bytes on disk)", path, got.Hash, want)
			}
		})
	}
}

func TestRead_Errors(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		max     int64
		wantErr error
	}{
		{"binary", "binary.bin", 1 << 20, textfile.ErrBinary},
		{"utf-16 with BOM", "utf16.txt", 1 << 20, textfile.ErrUnknownEncoding},
		{"invalid in every encoding", "garbage.txt", 1 << 20, textfile.ErrUnknownEncoding},
		{"too large", "utf8.txt", 10, textfile.ErrTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("testdata", tt.file)
			_, err := textfile.Read(path, tt.max)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Read(%q, %d) error = %v, want %v", path, tt.max, err, tt.wantErr)
			}
		})
	}
}

func TestRead_SizeLimitIsInclusive(t *testing.T) {
	p := writeFile(t, []byte("abcde"))
	if _, err := textfile.Read(p, 5); err != nil {
		t.Errorf("Read(5-byte file, 5) error = %v, want nil", err)
	}
	if _, err := textfile.Read(p, 4); !errors.Is(err, textfile.ErrTooLarge) {
		t.Errorf("Read(5-byte file, 4) error = %v, want ErrTooLarge", err)
	}
}

func TestRead_HugeSparseFileIsRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "huge")
	f, err := os.Create(p) //nolint:gosec // G304: the path is under t.TempDir
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	if err := f.Truncate(1 << 40); err != nil {
		_ = f.Close() // the file is abandoned; the skip below reports the real reason
		t.Skipf("file system cannot make a sparse file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := textfile.Read(p, 1<<20); !errors.Is(err, textfile.ErrTooLarge) {
		t.Errorf("Read(1 TiB sparse file, 1 MiB) error = %v, want ErrTooLarge", err)
	}
}

func TestRead_EmptyFile(t *testing.T) {
	got, err := textfile.Read(writeFile(t, nil), 10)
	if err != nil {
		t.Fatalf("Read(empty file) error = %v", err)
	}
	if got.Encoding != textfile.UTF8 || len(got.Text) != 0 {
		t.Errorf("Read(empty file) = %v, %q, want UTF8 and empty text", got.Encoding, got.Text)
	}
	if line, col := got.Position(0); line != 1 || col != 1 {
		t.Errorf("Position(0) of empty text = %d, %d, want 1, 1", line, col)
	}
}

func TestRead_NotRegular(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo error = %v", err)
	}
	for _, p := range []string{dir, link, fifo} {
		if _, err := textfile.Read(p, 10); !errors.Is(err, textfile.ErrNotRegular) {
			t.Errorf("Read(%q) error = %v, want ErrNotRegular", filepath.Base(p), err)
		}
	}
}

func TestRead_MultiByteAcrossProbeBoundary(t *testing.T) {
	// A three-byte character straddles byte 8,000, where the NUL probe ends.
	body := strings.Repeat("a", 7999) + "한" + strings.Repeat("b", 100)
	got, err := textfile.Read(writeFile(t, []byte(body)), 1<<20)
	if err != nil || got.Encoding != textfile.UTF8 || string(got.Text) != body {
		t.Errorf("Read(text with character at the probe boundary) = %v, %v, want UTF8 and the same text", got.Encoding, err)
	}
}

func TestRead_NULProbeWindow(t *testing.T) {
	inside := []byte(strings.Repeat("a", 7999) + "\x00")
	if _, err := textfile.Read(writeFile(t, inside), 1<<20); !errors.Is(err, textfile.ErrBinary) {
		t.Errorf("Read(NUL at byte 8000) error = %v, want ErrBinary", err)
	}
	outside := []byte(strings.Repeat("a", 8000) + "\x00")
	if _, err := textfile.Read(writeFile(t, outside), 1<<20); err != nil {
		t.Errorf("Read(NUL at byte 8001) error = %v, want nil", err)
	}
}

func file(text string) textfile.File {
	return textfile.File{Encoding: textfile.UTF8, Text: []byte(text)}
}

func TestPosition(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		offset   int
		wantLine int
		wantCol  int
	}{
		{"start", "abc", 0, 1, 1},
		{"inside first line", "abc", 2, 1, 3},
		{"end of text", "abc", 3, 1, 4},
		{"second line", "ab\ncd", 3, 2, 1},
		{"column counts characters after hangul", "키=값x", len("키=값"), 1, 4},
		{"column of the character itself", "키=값x", len("키="), 1, 3},
		{"CRLF second line", "a\r\nb\r\nc", len("a\r\nb\r\n"), 3, 1},
		{"CR belongs to its line", "a\r\nb", 1, 1, 2},
		{"lone CR is not a break", "a\rb", 2, 1, 3},
		{"no trailing newline", "a\nbc", 4, 2, 3},
		{"after trailing newline", "a\n", 2, 2, 1},
		{"inside a multi-byte character", "한", 1, 1, 1},
		{"negative clamps to start", "abc", -5, 1, 1},
		{"past end clamps to end", "ab\ncd", 99, 2, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, col := file(tt.text).Position(tt.offset)
			if line != tt.wantLine || col != tt.wantCol {
				t.Errorf("Position(%q, %d) = %d, %d, want %d, %d", tt.text, tt.offset, line, col, tt.wantLine, tt.wantCol)
			}
		})
	}
}

func TestOffset(t *testing.T) {
	const text = "ab\r\n키=값\nlast"
	tests := []struct {
		name    string
		line    int
		byteCol int
		want    int
		wantErr bool
	}{
		{"first byte", 1, 1, 0, false},
		{"end of first line is the CR", 1, 3, 2, false},
		{"one past the CR is outside", 1, 5, 0, true},
		{"second line start", 2, 1, 4, false},
		{"byte column inside hangul line", 2, 5, 8, false},
		{"last line", 3, 2, len(text) - 3, false},
		{"end of text", 3, 5, len(text), false},
		{"missing line", 4, 1, 0, true},
		{"line zero", 0, 1, 0, true},
		{"column zero", 1, 0, 0, true},
		{"column past line end", 3, 6, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := file(text).Offset(tt.line, tt.byteCol)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Offset(%d, %d) error = %v, wantErr %v", tt.line, tt.byteCol, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("Offset(%d, %d) = %d, want %d", tt.line, tt.byteCol, got, tt.want)
			}
			if err != nil && !errors.Is(err, textfile.ErrOutOfRange) {
				t.Errorf("Offset(%d, %d) error = %v, want ErrOutOfRange", tt.line, tt.byteCol, err)
			}
		})
	}
}

func TestOffset_RoundTrip(t *testing.T) {
	f, err := textfile.Read(filepath.Join("testdata", "euckr-crlf.properties"), 1<<20)
	if err != nil {
		t.Fatalf("Read error = %v", err)
	}
	lines := strings.Split(string(f.Text), "\n")
	for i, ln := range lines {
		for byteCol := 1; byteCol <= len(ln)+1; byteCol++ {
			off, err := f.Offset(i+1, byteCol)
			if err != nil {
				t.Fatalf("Offset(%d, %d) error = %v", i+1, byteCol, err)
			}
			gotLine, _ := f.Position(off)
			if gotLine != i+1 {
				t.Errorf("Position(Offset(%d, %d)) line = %d, want %d", i+1, byteCol, gotLine, i+1)
			}
		}
	}
	if _, err := f.Offset(len(lines)+1, 1); err == nil {
		t.Errorf("Offset(%d, 1) error = nil, want an error for a missing line", len(lines)+1)
	}
}

func TestLines(t *testing.T) {
	const text = "one\r\ntwo\r\nthree"
	tests := []struct {
		name        string
		text        string
		first, last int
		want        []string
	}{
		{"strips CR", text, 1, 2, []string{"one", "two"}},
		{"last line without newline", text, 3, 3, []string{"three"}},
		{"clamps both ends", text, -3, 99, []string{"one", "two", "three"}},
		{"entirely past the end", text, 7, 9, nil},
		{"reversed range", text, 3, 1, nil},
		{"lone CR is kept", "a\rb\n", 1, 1, []string{"a\rb"}},
		{"trailing newline adds no line", "a\n", 1, 5, []string{"a"}},
		{"empty text", "", 1, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := file(tt.text).Lines(tt.first, tt.last)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Lines(%d, %d) mismatch (-want +got):\n%s", tt.first, tt.last, diff)
			}
		})
	}
}
