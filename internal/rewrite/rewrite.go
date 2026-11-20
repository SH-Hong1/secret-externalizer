// Package rewrite replaces ranges of a source file's text while keeping every
// other byte of the file as it is, its encoding, byte order mark and line
// endings included, and writes files inside a project folder without
// following symbolic links.
package rewrite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

var (
	// ErrOverlap means two edits share a byte of the text.
	ErrOverlap = errors.New("edits overlap")
	// ErrChanged means a file no longer looks as it did when it was read.
	ErrChanged = errors.New("file changed since it was read")
	// ErrNotRegular means a path is a symbolic link or anything else that is
	// not a regular file, or passes through something that is not a folder.
	ErrNotRegular = errors.New("not a regular file")
	// ErrTooLarge means a file is larger than the size limit.
	ErrTooLarge = errors.New("file is too large")
	// ErrHardLinked means a file has more than one link. Replacing or
	// removing one name would leave its bytes under the others.
	ErrHardLinked = errors.New("file has more than one link")
	// ErrAfterCommit means a change is already on disk and a step after it
	// failed. It is returned wrapped together with that step's error.
	ErrAfterCommit = errors.New("change is on disk but a following step failed")
)

// Edit replaces one range of a file's text. With is printable ASCII
// without a line break.
type Edit struct {
	Span finding.Span
	With string
}

// Output is a file after its edits: the bytes to write, their hex SHA-256,
// and for each edit, in the order given, the 1-based line its replacement
// starts on in Content.
type Output struct {
	Content []byte
	Hash    string
	Lines   []int
}

// Span returns the range of l in f.Text, from its first to its last
// character. The end of l names the last character, which may be the line
// feed one column past the last character of its line.
func Span(f textfile.File, l finding.Location) (finding.Span, error) {
	start, err := f.CharOffset(l.StartLine, l.StartCol)
	if err != nil {
		return finding.Span{}, fmt.Errorf("start of location: %w", err)
	}
	last, err := f.CharOffset(l.EndLine, l.EndCol)
	if err != nil {
		return finding.Span{}, fmt.Errorf("end of location: %w", err)
	}
	if last >= len(f.Text) {
		return finding.Span{}, fmt.Errorf("end of location after the last character: %w", textfile.ErrOutOfRange)
	}
	_, size := utf8.DecodeRune(f.Text[last:])
	if last+size <= start {
		return finding.Span{}, fmt.Errorf("location ends before it starts: %w", textfile.ErrOutOfRange)
	}
	return finding.Span{Start: start, End: last + size}, nil
}

// Replace applies edits to raw, the bytes f was decoded from. Every byte
// outside the edited ranges, the byte order mark included, stays as it is.
// It fails with ErrOverlap when two ranges share a byte, and with another
// error when a range is empty or does not lie on character boundaries of the
// text, when a replacement is not printable ASCII or is empty, or when raw is
// not what f was decoded from. On failure the Output is empty.
func Replace(raw []byte, f textfile.File, edits []Edit) (Output, error) {
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != f.Hash {
		return Output{}, textfile.ErrRawMismatch
	}
	order := make([]int, len(edits))
	for i, e := range edits {
		if err := checkEdit(f.Text, e); err != nil {
			return Output{}, fmt.Errorf("edit %d: %w", i, err)
		}
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return edits[a].Span.Start - edits[b].Span.Start })
	for k := 1; k < len(order); k++ {
		if edits[order[k]].Span.Start < edits[order[k-1]].Span.End {
			return Output{}, fmt.Errorf("edits %d and %d: %w", order[k-1], order[k], ErrOverlap)
		}
	}

	content := make([]byte, 0, len(raw))
	lines := make([]int, len(edits))
	newlines, prev := 0, 0
	for _, i := range order {
		e := edits[i]
		start, err := f.DiskOffset(raw, e.Span.Start)
		if err != nil {
			return Output{}, fmt.Errorf("edit %d: %w", i, err)
		}
		end, err := f.DiskOffset(raw, e.Span.End)
		if err != nil {
			return Output{}, fmt.Errorf("edit %d: %w", i, err)
		}
		// Neither EUC-KR nor UTF-8 has 0x0A inside a character, so counting
		// that byte on disk counts lines.
		newlines += bytes.Count(raw[prev:start], []byte{'\n'})
		lines[i] = 1 + newlines
		content = append(content, raw[prev:start]...)
		content = append(content, e.With...)
		prev = end
	}
	content = append(content, raw[prev:]...)
	out := sha256.Sum256(content)
	return Output{Content: content, Hash: hex.EncodeToString(out[:]), Lines: lines}, nil
}

// checkEdit confirms that e replaces a non-empty range of text that starts
// and ends on character boundaries with printable ASCII.
func checkEdit(text []byte, e Edit) error {
	s := e.Span
	if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
		return fmt.Errorf("range %d-%d: %w", s.Start, s.End, textfile.ErrOutOfRange)
	}
	if !utf8.RuneStart(text[s.Start]) || (s.End < len(text) && !utf8.RuneStart(text[s.End])) {
		return fmt.Errorf("range %d-%d is not on character boundaries: %w", s.Start, s.End, textfile.ErrOutOfRange)
	}
	if e.With == "" {
		return errors.New("replacement is empty")
	}
	for i := range len(e.With) {
		if c := e.With[i]; c < 0x20 || c > 0x7e {
			return errors.New("replacement is not printable ASCII")
		}
	}
	return nil
}
