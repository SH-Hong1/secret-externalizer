package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

// bomLen is the length of the UTF-8 byte order mark, which the scanners
// count in offsets and columns and textfile.File.Text leaves out.
const bomLen = 3

// spot is one scanner result placed in a file.
type spot struct {
	raw Raw
	// span is the value's range in the file's Text. It is empty for a
	// whole-file result.
	span finding.Span
	// value is the bytes that are fingerprinted and kept as the original:
	// the span of Text, or the file's bytes on disk for a whole-file result.
	value Value
}

// placedFile is a file that has at least one placed result.
type placedFile struct {
	rel string
	// file is the file as text. Its Text is empty for a file that is not
	// text, which only whole-file results refer to.
	file textfile.File
	// hash is the SHA-256 of the file's bytes on disk.
	hash  string
	spots []spot
}

// locate re-reads every file that raws refer to and places each result in
// it. A result whose value is not where the scanner reported it, or whose
// file changed since it was listed or can no longer be read, is dropped and
// counted. Files are returned in path order.
func locate(ctx context.Context, dir string, files fileList, raws []Raw, maxBytes int64) ([]*placedFile, int, error) {
	byFile := map[string][]Raw{}
	for _, raw := range raws {
		byFile[raw.File] = append(byFile[raw.File], raw)
	}
	var placed []*placedFile
	dropped := 0
	for _, rel := range slices.Sorted(maps.Keys(byFile)) {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		pf, d := placeFile(dir, rel, files.files[rel], byFile[rel], maxBytes)
		dropped += d
		if len(pf.spots) > 0 {
			placed = append(placed, pf)
		}
	}
	return placed, dropped, nil
}

// placeFile places the results raws of the listed file rel.
func placeFile(dir, rel string, src sourceFile, raws []Raw, maxBytes int64) (*placedFile, int) {
	pf := &placedFile{rel: rel, hash: src.hash}
	bom := 0
	if src.text {
		f, err := textfile.Read(filepath.Join(dir, filepath.FromSlash(rel)), maxBytes)
		if err != nil || f.Hash != src.hash {
			return pf, len(raws)
		}
		pf.file = f
		if f.Encoding == textfile.UTF8 && hexSHA256(f.Text) != f.Hash {
			bom = bomLen
		}
	}

	dropped := 0
	var original []byte
	for _, raw := range raws {
		if raw.wholeFile() {
			if original == nil {
				data, hash, err := readOriginal(dir, rel, maxBytes)
				if err != nil || (src.text && hash != src.hash) {
					dropped++
					continue
				}
				original, pf.hash = data, hash
			}
			pf.spots = append(pf.spots, spot{raw: raw, value: original})
			continue
		}
		span, ok := place(pf.file, bom, raw)
		if !ok {
			dropped++
			continue
		}
		pf.spots = append(pf.spots, spot{raw: raw, span: span, value: Value(pf.file.Text[span.Start:span.End])})
	}
	return pf, dropped
}

// place returns the range of raw's value in f.Text. bom is the length of
// the byte order mark the file had on disk, which the scanner counted. ok is
// false when the range is outside the text, does not fall on character
// boundaries, is empty, or does not hold the value the scanner reported.
func place(f textfile.File, bom int, raw Raw) (_ finding.Span, ok bool) {
	var start, end int
	if raw.Offset >= 0 {
		start, end = raw.Offset-bom, raw.EndOffset-bom
	} else {
		col := raw.ByteCol
		if raw.Line == 1 {
			col -= bom
		}
		off, err := f.Offset(raw.Line, col)
		if err != nil {
			return finding.Span{}, false
		}
		start, end = off, off+len(raw.Value)
	}
	text := f.Text
	if start < 0 || end <= start || end > len(text) ||
		!utf8.RuneStart(text[start]) || (end < len(text) && !utf8.RuneStart(text[end])) {
		return finding.Span{}, false
	}
	if raw.Offset < 0 && !bytes.Equal(text[start:end], raw.Value) {
		return finding.Span{}, false
	}
	return finding.Span{Start: start, End: end}, true
}

// position returns where s is in the file: the line and column of its first
// character and of its last character, columns counting characters. A
// whole-file spot runs from the first to the last character of the text, or
// is line 1, column 1, for a file that is not text.
func (pf *placedFile) position(s spot) (startLine, startCol, endLine, endCol int) {
	span := s.span
	if s.raw.wholeFile() {
		span = finding.Span{Start: 0, End: len(pf.file.Text)}
	}
	if span.End <= span.Start {
		return 1, 1, 1, 1
	}
	startLine, startCol = pf.file.Position(span.Start)
	endLine, endCol = pf.file.Position(span.End - 1)
	return startLine, startCol, endLine, endCol
}

// spans returns the spans of every text result in the file.
func (pf *placedFile) spans() []finding.Span {
	out := make([]finding.Span, 0, len(pf.spots))
	for _, s := range pf.spots {
		if !s.raw.wholeFile() {
			out = append(out, s.span)
		}
	}
	return out
}

// forget drops the values held by placed, and zeroes the bytes of the
// whole-file originals, which no other value shares.
func forget(placed []*placedFile) {
	for _, pf := range placed {
		for i := range pf.spots {
			if pf.spots[i].raw.wholeFile() {
				clear(pf.spots[i].value)
			}
			pf.spots[i].value = nil
			pf.spots[i].raw.Value = nil
		}
		pf.file = textfile.File{}
	}
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
