// Package textfile reads legacy source files as UTF-8, converts between byte
// offsets and line/column positions, and walks a project folder for the files
// worth scanning.
package textfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"
)

// Encoding is the encoding a file had on disk.
type Encoding int

// The encodings Read recognises. The zero value means "not set".
const (
	UTF8 Encoding = iota + 1
	EUCKR
)

// String returns the name of the encoding.
func (e Encoding) String() string {
	switch e {
	case UTF8:
		return "UTF-8"
	case EUCKR:
		return "EUC-KR"
	default:
		return fmt.Sprintf("Encoding(%d)", int(e))
	}
}

var (
	// ErrTooLarge means a file is larger than the size limit.
	ErrTooLarge = errors.New("file is too large")
	// ErrBinary means a file contains a NUL byte near its start.
	ErrBinary = errors.New("file is binary")
	// ErrUnknownEncoding means a file is neither UTF-8 nor EUC-KR.
	ErrUnknownEncoding = errors.New("file encoding is not recognised")
	// ErrNotRegular means a path is not a regular file.
	ErrNotRegular = errors.New("not a regular file")
	// ErrOutOfRange means a line or column lies outside the text.
	ErrOutOfRange = errors.New("position is outside the text")
)

// probeLen is the number of leading bytes searched for a NUL byte.
const probeLen = 8000

// File is a source file converted to UTF-8.
type File struct {
	// Encoding is the encoding the file had on disk.
	Encoding Encoding
	// Text is the file content as UTF-8 without a byte order mark. Callers
	// must not modify it.
	Text []byte
	// Hash is the hex-encoded SHA-256 of the bytes exactly as they are on
	// disk, including any byte order mark and the original line endings.
	Hash string
}

// String returns the encoding, the size of the text and the hash. The text
// can hold secret values, so it is never printed.
func (f File) String() string {
	return fmt.Sprintf("{encoding:%s text:[redacted %d bytes] hash:%s}", f.Encoding, len(f.Text), f.Hash)
}

// Format prints the form String returns for every fmt verb, including %#v
// and %x, which would otherwise print the text.
func (f File) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, f.String()) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns the encoding, the size of the text and the hash.
func (f File) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("encoding", f.Encoding.String()),
		slog.Int("text_bytes", len(f.Text)),
		slog.String("hash", f.Hash),
	)
}

// MarshalJSON encodes the encoding, the size of the text and the hash.
func (f File) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Encoding  string `json:"encoding"`
		TextBytes int    `json:"text_bytes"`
		Hash      string `json:"hash"`
	}{f.Encoding.String(), len(f.Text), f.Hash})
}

// Read reads the regular file at path and returns it as UTF-8. It never
// reads more than maxBytes+1 bytes. It fails with ErrNotRegular for anything
// but a regular file (a symbolic link included), ErrTooLarge when the file is
// longer than maxBytes, ErrBinary when a NUL byte appears in the first 8,000
// bytes after any UTF-8 byte order mark, and ErrUnknownEncoding for UTF-16,
// for a byte order mark followed by bytes that are not UTF-8, and for bytes
// that are neither UTF-8 nor EUC-KR. A NUL byte further into the file does
// not make it binary.
func Read(path string, maxBytes int64) (File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return File{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return File{}, fmt.Errorf("reading %s: %w", path, ErrNotRegular)
	}
	if info.Size() > maxBytes {
		return File{}, fmt.Errorf("reading %s: %w", path, ErrTooLarge)
	}
	raw, err := readLimited(path, maxBytes)
	if err != nil {
		return File{}, err
	}
	sum := sha256.Sum256(raw)
	text, enc, err := decode(raw)
	if err != nil {
		return File{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return File{Encoding: enc, Text: text, Hash: hex.EncodeToString(sum[:])}, nil
}

// readLimited reads the file at path, which must still be a regular file once
// opened, and fails with ErrTooLarge when it holds more than maxBytes.
func readLimited(path string, maxBytes int64) (_ []byte, err error) {
	f, err := os.Open(path) //nolint:gosec // G304: the caller has confirmed the path is inside the walk
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() {
		// The file was only read, so a close failure loses nothing.
		_ = f.Close()
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reading %s: %w", path, ErrNotRegular)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("reading %s: %w", path, ErrTooLarge)
	}
	return raw, nil
}

// decode converts raw to UTF-8 and reports the encoding it had. The bytes
// after a UTF-8 byte order mark are probed for NUL and must be valid UTF-8.
func decode(raw []byte) ([]byte, Encoding, error) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		body := raw[3:]
		if hasNUL(body) {
			return nil, 0, ErrBinary
		}
		if !utf8.Valid(body) {
			return nil, 0, fmt.Errorf("%w: not valid UTF-8 after a byte order mark", ErrUnknownEncoding)
		}
		return body, UTF8, nil
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}), bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return nil, 0, fmt.Errorf("%w: UTF-16", ErrUnknownEncoding)
	}
	if hasNUL(raw) {
		return nil, 0, ErrBinary
	}
	if utf8.Valid(raw) {
		return raw, UTF8, nil
	}
	text, err := korean.EUCKR.NewDecoder().Bytes(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: decoding EUC-KR: %w", ErrUnknownEncoding, err)
	}
	if bytes.ContainsRune(text, utf8.RuneError) {
		return nil, 0, fmt.Errorf("%w: not valid EUC-KR", ErrUnknownEncoding)
	}
	return text, EUCKR, nil
}

// hasNUL reports whether a NUL byte is in the first probeLen bytes of b.
func hasNUL(b []byte) bool {
	if len(b) > probeLen {
		b = b[:probeLen]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// Position converts a byte offset in Text to a 1-based line and a 1-based
// column that counts characters. A line ends at "\n"; a "\r" before it is an
// ordinary character of the line. An offset inside a multi-byte character
// gives the column of that character. An offset before the start or past the
// end is clamped to the start or the end of the text.
func (f File) Position(offset int) (line, col int) {
	offset = max(0, min(offset, len(f.Text)))
	for offset > 0 && offset < len(f.Text) && !utf8.RuneStart(f.Text[offset]) {
		offset--
	}
	before := f.Text[:offset]
	line = 1 + bytes.Count(before, []byte{'\n'})
	start := bytes.LastIndexByte(before, '\n') + 1
	return line, 1 + utf8.RuneCount(before[start:])
}

// Offset converts a 1-based line and a 1-based byte column within that line
// to a byte offset in Text. The column may point one past the last byte of
// the line, which is where its line break or the end of the text is. A line
// after a final "\n" counts as an empty line. Offset fails with
// ErrOutOfRange for a line or column outside the text.
func (f File) Offset(line, byteCol int) (int, error) {
	if line < 1 || byteCol < 1 {
		return 0, fmt.Errorf("line %d, column %d: %w", line, byteCol, ErrOutOfRange)
	}
	start := 0
	for i := 1; i < line; i++ {
		n := bytes.IndexByte(f.Text[start:], '\n')
		if n < 0 {
			return 0, fmt.Errorf("line %d: %w", line, ErrOutOfRange)
		}
		start += n + 1
	}
	end := len(f.Text)
	if n := bytes.IndexByte(f.Text[start:], '\n'); n >= 0 {
		end = start + n
	}
	if byteCol-1 > end-start {
		return 0, fmt.Errorf("line %d, column %d: %w", line, byteCol, ErrOutOfRange)
	}
	return start + byteCol - 1, nil
}

// Lines returns lines first through last, 1-based and inclusive, without
// their line endings; a "\r" before the "\n" is removed. Lines outside the
// text are left out, so the result may be shorter than requested or nil. A
// final "\n" does not start another line.
func (f File) Lines(first, last int) []string {
	first = max(first, 1)
	var out []string
	rest := f.Text
	for n := 1; len(rest) > 0 && n <= last; n++ {
		ln := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			ln, rest = rest[:i], rest[i+1:]
		} else {
			rest = nil
		}
		if n >= first {
			out = append(out, strings.TrimSuffix(string(ln), "\r"))
		}
	}
	return out
}
