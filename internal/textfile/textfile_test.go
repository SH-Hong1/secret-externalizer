package textfile_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/text/encoding/korean"

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

// readPath reads the file at path through the folder that holds it.
func readPath(path string, maxBytes int64) (textfile.File, error) {
	return textfile.Read(filepath.Dir(path), filepath.Base(path), maxBytes)
}

// linkedDir returns a folder holding the regular file sub/a.txt, the symbolic
// link link.txt to it, the FIFO pipe, the folder sub, and the symbolic link
// out to a folder outside that holds x.txt.
func linkedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("pw=dev-only-fake-pw\n"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if err := os.Symlink(filepath.Join("sub", "a.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatalf("Mkfifo error = %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	return dir
}

// readers are the three ways to read a file, reduced to their error.
var readers = []struct {
	name string
	read func(dir, rel string) error
}{
	{"Read", func(dir, rel string) error { _, err := textfile.Read(dir, rel, 1<<20); return err }},
	{"ReadRaw", func(dir, rel string) error { _, err := textfile.ReadRaw(dir, rel, 1<<20); return err }},
	{"ReadHead", func(dir, rel string) error { _, err := textfile.ReadHead(dir, rel, 16); return err }},
}

func TestReaders_RefuseWhatIsNotARegularFile(t *testing.T) {
	dir := linkedDir(t)
	for _, r := range readers {
		for _, rel := range []string{"link.txt", "pipe", "sub"} {
			if err := r.read(dir, rel); !errors.Is(err, textfile.ErrNotRegular) {
				t.Errorf("%s(%q) error = %v, want ErrNotRegular", r.name, rel, err)
			}
		}
		if err := r.read(dir, "sub/a.txt"); err != nil {
			t.Errorf("%s(sub/a.txt) error = %v, want nil", r.name, err)
		}
		if err := r.read(dir, "sub/missing.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s(sub/missing.txt) error = %v, want fs.ErrNotExist", r.name, err)
		}
	}
}

func TestReaders_StayInsideTheFolder(t *testing.T) {
	dir := linkedDir(t)
	abs := filepath.Join(dir, "sub", "a.txt")
	for _, r := range readers {
		for _, rel := range []string{"out/x.txt", "../x.txt", "sub/../../x.txt", abs, ""} {
			if err := r.read(dir, rel); err == nil {
				t.Errorf("%s(%q) error = nil, want an error", r.name, rel)
			}
		}
	}
}

func TestReadRaw(t *testing.T) {
	dir := t.TempDir()
	content := "\xEF\xBB\xBFkey=\x00value\r\n"
	if err := os.WriteFile(filepath.Join(dir, "f.bin"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	got, err := textfile.ReadRaw(dir, "f.bin", int64(len(content)))
	if err != nil || string(got) != content {
		t.Errorf("ReadRaw(f.bin, %d) = %q, %v, want the bytes on disk", len(content), got, err)
	}
	if _, err := textfile.ReadRaw(dir, "f.bin", int64(len(content))-1); !errors.Is(err, textfile.ErrTooLarge) {
		t.Errorf("ReadRaw(f.bin, %d) error = %v, want ErrTooLarge", len(content)-1, err)
	}
}

func TestReadHead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("abcdef"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	for _, tt := range []struct {
		n    int64
		want string
	}{{4, "abcd"}, {6, "abcdef"}, {100, "abcdef"}, {0, ""}} {
		got, err := textfile.ReadHead(dir, "f.txt", tt.n)
		if err != nil || string(got) != tt.want {
			t.Errorf("ReadHead(f.txt, %d) = %q, %v, want %q", tt.n, got, err, tt.want)
		}
	}
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
			got, err := readPath(path, 1<<20)
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
			_, err := readPath(path, tt.max)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Read(%q, %d) error = %v, want %v", path, tt.max, err, tt.wantErr)
			}
		})
	}
}

func TestRead_SizeLimitIsInclusive(t *testing.T) {
	p := writeFile(t, []byte("abcde"))
	if _, err := readPath(p, 5); err != nil {
		t.Errorf("Read(5-byte file, 5) error = %v, want nil", err)
	}
	if _, err := readPath(p, 4); !errors.Is(err, textfile.ErrTooLarge) {
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
	if _, err := readPath(p, 1<<20); !errors.Is(err, textfile.ErrTooLarge) {
		t.Errorf("Read(1 TiB sparse file, 1 MiB) error = %v, want ErrTooLarge", err)
	}
}

func TestRead_EmptyFile(t *testing.T) {
	got, err := readPath(writeFile(t, nil), 10)
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
		if _, err := readPath(p, 10); !errors.Is(err, textfile.ErrNotRegular) {
			t.Errorf("Read(%q) error = %v, want ErrNotRegular", filepath.Base(p), err)
		}
	}
}

func TestRead_MultiByteAcrossProbeBoundary(t *testing.T) {
	// A three-byte character straddles byte 8,000, where the NUL probe ends.
	body := strings.Repeat("a", 7999) + "한" + strings.Repeat("b", 100)
	got, err := readPath(writeFile(t, []byte(body)), 1<<20)
	if err != nil || got.Encoding != textfile.UTF8 || string(got.Text) != body {
		t.Errorf("Read(text with character at the probe boundary) = %v, %v, want UTF8 and the same text", got.Encoding, err)
	}
}

func TestRead_NULProbeWindow(t *testing.T) {
	inside := []byte(strings.Repeat("a", 7999) + "\x00")
	if _, err := readPath(writeFile(t, inside), 1<<20); !errors.Is(err, textfile.ErrBinary) {
		t.Errorf("Read(NUL at byte 8000) error = %v, want ErrBinary", err)
	}
	outside := []byte(strings.Repeat("a", 8000) + "\x00")
	if _, err := readPath(writeFile(t, outside), 1<<20); err != nil {
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
	f, err := readPath(filepath.Join("testdata", "euckr-crlf.properties"), 1<<20)
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

func TestRead_BOMFileIsValidated(t *testing.T) {
	bom := "\xef\xbb\xbf"
	tests := []struct {
		name    string
		content string
		wantErr error
	}{
		{"invalid UTF-8 after the mark", bom + "key=\xff\xfe\x80\n", textfile.ErrUnknownEncoding},
		{"EUC-KR after the mark", bom + "key=\xb0\xa1\n", textfile.ErrUnknownEncoding},
		{"NUL in the first 8,000 bytes", bom + "key=a\x00b\n", textfile.ErrBinary},
		{"NUL at the last probed byte", bom + strings.Repeat("a", 7999) + "\x00", textfile.ErrBinary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readPath(writeFile(t, []byte(tt.content)), 1<<20)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Read(%q) = %v, %v, want error %v", tt.content, got.Encoding, err, tt.wantErr)
			}
		})
	}
}

func TestDecode(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("ReadDir(testdata) error = %v", err)
	}
	for _, e := range entries {
		path := filepath.Join("testdata", e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(path) //nolint:gosec // G304: the path is a fixture under testdata
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", path, err)
			}
			want, wantErr := readPath(path, 1<<20)
			got, gotErr := textfile.Decode(raw)
			if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && !sameSentinel(gotErr, wantErr)) {
				t.Fatalf("Decode(%s) error = %v, want the error Read gives, %v", e.Name(), gotErr, wantErr)
			}
			if diff := cmp.Diff(want.Text, got.Text); diff != "" || got.Encoding != want.Encoding || got.Hash != want.Hash {
				t.Errorf("Decode(%s) = %v, want %v (text diff %s)", e.Name(), got, want, diff)
			}
		})
	}
}

// sameSentinel reports whether a and b wrap the same textfile error.
func sameSentinel(a, b error) bool {
	for _, s := range []error{textfile.ErrBinary, textfile.ErrUnknownEncoding, textfile.ErrTooLarge} {
		if errors.Is(a, s) && errors.Is(b, s) {
			return true
		}
	}
	return false
}

// charBoundaries returns every offset in text that starts a character, and
// the end of the text.
func charBoundaries(text []byte) []int {
	var out []int
	for i := range text {
		if utf8.RuneStart(text[i]) {
			out = append(out, i)
		}
	}
	return append(out, len(text))
}

func TestCharOffset(t *testing.T) {
	for _, name := range []string{"utf8.txt", "euckr-crlf.properties", "cp949-extended.txt"} {
		t.Run(name, func(t *testing.T) {
			f, err := readPath(filepath.Join("testdata", name), 1<<20)
			if err != nil {
				t.Fatalf("Read(%s) error = %v", name, err)
			}
			for _, off := range charBoundaries(f.Text) {
				line, col := f.Position(off)
				got, err := f.CharOffset(line, col)
				if err != nil || got != off {
					t.Errorf("CharOffset(Position(%d) = %d, %d) = %d, %v, want %d", off, line, col, got, err, off)
				}
			}
		})
	}
	f := file("ab\r\n키=값\nlast")
	tests := []struct {
		name      string
		line, col int
	}{
		{"line zero", 0, 1},
		{"column zero", 1, 0},
		{"missing line", 4, 1},
		{"column past the line end", 2, 5},
		{"column past the CR", 1, 5},
		{"column past the text end", 3, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := f.CharOffset(tt.line, tt.col); !errors.Is(err, textfile.ErrOutOfRange) {
				t.Errorf("CharOffset(%d, %d) = %d, %v, want ErrOutOfRange", tt.line, tt.col, got, err)
			}
		})
	}
	for _, tt := range []struct{ line, col, want int }{
		{2, 3, len("ab\r\n키=")},
		{2, 4, len("ab\r\n키=값")},
		{1, 3, 2},
		{3, 5, len("ab\r\n키=값\nlast")},
	} {
		if got, err := f.CharOffset(tt.line, tt.col); err != nil || got != tt.want {
			t.Errorf("CharOffset(%d, %d) = %d, %v, want %d", tt.line, tt.col, got, err, tt.want)
		}
	}
}

// eucKR encodes s as EUC-KR (CP949).
func eucKR(t *testing.T, s string) []byte {
	t.Helper()
	b, err := korean.EUCKR.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encoding %q as EUC-KR error = %v", s, err)
	}
	return b
}

// asciiTrailChars returns CP949 characters whose second byte is an ASCII
// letter: for lead bytes from across the range that allows such a second
// byte, the first upper-case and the first lower-case letter that decode.
func asciiTrailChars(t *testing.T) []string {
	t.Helper()
	var out []string
	dec := korean.EUCKR.NewDecoder()
	for _, lead := range []byte{0x81, 0x8c, 0x9f, 0xa1, 0xb0, 0xc6} {
		for _, letters := range []string{"ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz"} {
			for i := range len(letters) {
				r, err := dec.Bytes([]byte{lead, letters[i]})
				if err == nil && !strings.ContainsRune(string(r), utf8.RuneError) {
					out = append(out, string(r))
					break
				}
			}
		}
	}
	if len(out) < 6 {
		t.Fatalf("found %d CP949 characters with a letter as second byte, want at least 6", len(out))
	}
	return out
}

func TestDiskOffset(t *testing.T) {
	t.Run("utf-8 is the same offset", func(t *testing.T) {
		raw := []byte("키=값\nx")
		f, err := textfile.Decode(raw)
		if err != nil {
			t.Fatalf("Decode error = %v", err)
		}
		for _, off := range charBoundaries(f.Text) {
			if got, err := f.DiskOffset(raw, off); err != nil || got != off {
				t.Errorf("DiskOffset(%d) = %d, %v, want %d", off, got, err, off)
			}
		}
	})
	t.Run("utf-8 with BOM adds three", func(t *testing.T) {
		raw := []byte("\xef\xbb\xbfkey=값\r\n")
		f, err := textfile.Decode(raw)
		if err != nil {
			t.Fatalf("Decode error = %v", err)
		}
		for _, off := range charBoundaries(f.Text) {
			if got, err := f.DiskOffset(raw, off); err != nil || got != off+3 {
				t.Errorf("DiskOffset(%d) = %d, %v, want %d", off, got, err, off+3)
			}
		}
	})

	euckrFiles := map[string][]byte{}
	for _, name := range []string{"euckr.txt", "euckr-crlf.properties", "cp949-extended.txt"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // G304: the path is a fixture under testdata
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", name, err)
		}
		euckrFiles[name] = raw
	}
	for i, c := range asciiTrailChars(t) {
		euckrFiles[fmt.Sprintf("constructed %d", i)] = eucKR(t, "# 설정\r\n"+c+"pw=dev-only-fake-"+c+"Az\r\n"+c+c+"aZ\n"+c)
	}
	for name, raw := range euckrFiles {
		t.Run("euc-kr "+name, func(t *testing.T) {
			f, err := textfile.Decode(raw)
			if err != nil || f.Encoding != textfile.EUCKR {
				t.Fatalf("Decode(%s) = %v, %v, want EUC-KR", name, f.Encoding, err)
			}
			bounds := charBoundaries(f.Text)
			for ai, a := range bounds {
				da, err := f.DiskOffset(raw, a)
				if err != nil {
					t.Fatalf("DiskOffset(%d) error = %v", a, err)
				}
				if want := len(eucKR(t, string(f.Text[:a]))); da != want {
					t.Errorf("DiskOffset(%d) = %d, want %d", a, da, want)
				}
				for _, b := range bounds[ai:] {
					db, err := f.DiskOffset(raw, b)
					if err != nil {
						t.Fatalf("DiskOffset(%d) error = %v", b, err)
					}
					got, err := korean.EUCKR.NewDecoder().Bytes(raw[da:db])
					if err != nil || string(got) != string(f.Text[a:b]) {
						t.Errorf("decoding raw[DiskOffset(%d):DiskOffset(%d)] = %q, %v, want %q", a, b, got, err, f.Text[a:b])
					}
				}
			}
		})
	}

	t.Run("refuses", func(t *testing.T) {
		raw := eucKR(t, "키=값")
		f, err := textfile.Decode(raw)
		if err != nil {
			t.Fatalf("Decode error = %v", err)
		}
		for _, off := range []int{1, 2, -1, len(f.Text) + 1} {
			if got, err := f.DiskOffset(raw, off); err == nil {
				t.Errorf("DiskOffset(%d) = %d, nil, want an error", off, got)
			}
		}
		u := []byte("키=값")
		uf, err := textfile.Decode(u)
		if err != nil {
			t.Fatalf("Decode error = %v", err)
		}
		for _, off := range []int{1, -1, len(u) + 1} {
			if got, err := uf.DiskOffset(u, off); err == nil {
				t.Errorf("DiskOffset(utf-8, %d) = %d, nil, want an error", off, got)
			}
		}
	})
}

// TestDiskOffset_EUCKRWidths decodes every two-byte combination that starts
// with a lead byte. Where the result is not the replacement character, the
// pair is one character at or above U+0080, so walking a file from its start
// can take a byte below 0x80 at a lead position as one character and any
// other byte as the first of two. A pair whose second byte is a line feed
// never decodes, so a line feed is never inside a character.
func TestDiskOffset_EUCKRWidths(t *testing.T) {
	dec := korean.EUCKR.NewDecoder()
	asciiTrail := 0
	for lead := 0x80; lead <= 0xff; lead++ {
		for trail := 0; trail <= 0xff; trail++ {
			pair := []byte{byte(lead), byte(trail)}
			got, err := dec.Bytes(pair)
			if err != nil {
				t.Fatalf("decoding % x error = %v", pair, err)
			}
			if strings.ContainsRune(string(got), utf8.RuneError) {
				continue
			}
			if lead == 0x80 || lead == 0xff {
				t.Errorf("decoding % x = %q, want the replacement character for a byte that cannot lead", pair, got)
			}
			if trail == '\n' {
				t.Errorf("decoding % x = %q, want the replacement character for a line feed", pair, got)
			}
			r, size := utf8.DecodeRune(got)
			if size != len(got) || r < 0x80 {
				t.Errorf("decoding % x = %q, want one character at or above U+0080", pair, got)
			}
			if trail < 0x80 {
				asciiTrail++
			}
		}
	}
	if asciiTrail == 0 {
		t.Errorf("found no CP949 character with an ASCII second byte, want some")
	}
}
