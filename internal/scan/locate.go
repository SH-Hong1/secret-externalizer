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

	"github.com/SH-Hong1/secret-externalizer/internal/connstr"
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
	// noContext is true when no surrounding lines may be shown for any
	// finding in the file: the file is text and a scanner reported the whole
	// file as a key, so every line of it is part of a value, or a result in
	// the file could not be placed, so the place of its value is not known
	// (betterleaks reports a value it decoded from base64, for example,
	// which the text holds only in encoded form), or a scanner dropped a
	// result in the file or a result whose file it could not name, or the
	// file may hold customer data that no rule reported: it is on a
	// customer data path (finding.CustomerDataPath), or a rule reported
	// customer data in it. It is only ever set.
	noContext bool
	// unplaced holds the ranges where the values of results that could not
	// be placed may be. They are masked as well, in case surrounding lines
	// are ever shown for such a file.
	unplaced []finding.Span
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

// dropContext sets noContext on each placed text file that a scanner
// dropped a result in, and on every placed file when a scanner dropped a
// result whose file it could not name: the value of such a result may be in
// any line of that file.
func dropContext(placed []*placedFile, files fileList, d drops) {
	for _, pf := range placed {
		if d.unknown || (d.files[pf.rel] && files.files[pf.rel].text) {
			pf.noContext = true
		}
	}
}

// placeFile places the results raws of the listed file rel.
func placeFile(dir, rel string, src sourceFile, raws []Raw, maxBytes int64) (*placedFile, int) {
	pf := &placedFile{rel: rel, hash: src.hash, noContext: finding.CustomerDataPath(rel)}
	for _, raw := range raws {
		if raw.Category == finding.CategoryCustomerData {
			pf.noContext = true
		}
	}
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
			pf.noContext = pf.noContext || src.text
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
			// Where the value is, or in what form, cannot be told, so no
			// line of the file is known to be free of it. The masks are a
			// second layer.
			pf.noContext = true
			pf.unplaced = append(pf.unplaced, unplacedSpans(pf.file.Text, bom, raw)...)
			dropped++
			continue
		}
		pf.spots = append(pf.spots, spot{raw: raw, span: span, value: Value(pf.file.Text[span.Start:span.End])})
	}
	return pf, dropped
}

// splitConnections replaces each placed result of kind url whose value
// connstr.Split knows with one result for each component: the same tool,
// rule, and category, the component's kind, and the component's range in
// the file. A URL it does not know stays one result. Parts of the URL that
// are not components, such as the database name, are then neither a value
// nor masked.
func splitConnections(placed []*placedFile) {
	for _, pf := range placed {
		var spots []spot
		for _, s := range pf.spots {
			var parts []connstr.Part
			if s.raw.Kind == finding.KindURL && !s.raw.wholeFile() {
				parts = connstr.Split(s.value)
			}
			if parts == nil {
				spots = append(spots, s)
				continue
			}
			for _, p := range parts {
				c := s
				c.raw.Kind = p.Kind
				c.span = finding.Span{Start: s.span.Start + p.Span.Start, End: s.span.Start + p.Span.End}
				c.value = s.value[p.Span.Start:p.Span.End]
				if c.raw.Value != nil {
					c.raw.Value = c.value
				}
				spots = append(spots, c)
			}
		}
		pf.spots = spots
	}
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

// unplacedSpans returns where the value of raw, which could not be placed,
// may be in text. For a value the scanner gave (betterleaks), that is every
// place the value appears. For a byte range (opengrep), it is the range
// itself clamped to the text; finding.Context widens it to whole
// characters. It returns nil when no such place is in the text.
func unplacedSpans(text []byte, bom int, raw Raw) []finding.Span {
	if raw.Offset >= 0 {
		start := max(0, raw.Offset-bom)
		end := min(len(text), raw.EndOffset-bom)
		if end <= start {
			return nil
		}
		return []finding.Span{{Start: start, End: end}}
	}
	if len(raw.Value) == 0 {
		return nil
	}
	var out []finding.Span
	for off := 0; off < len(text); {
		i := bytes.Index(text[off:], raw.Value)
		if i < 0 {
			break
		}
		out = append(out, finding.Span{Start: off + i, End: off + i + len(raw.Value)})
		off += i + 1
	}
	return out
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

// showsContext reports whether some finding in the file gets surrounding
// lines: the file is not marked noContext and has a result that is not a
// whole-file result.
func (pf *placedFile) showsContext() bool {
	if pf.noContext {
		return false
	}
	for _, s := range pf.spots {
		if !s.raw.wholeFile() {
			return true
		}
	}
	return false
}

// spans returns the ranges to mask in the file's surrounding lines: every
// placed text result and every place a result that could not be placed may
// have its value.
func (pf *placedFile) spans() []finding.Span {
	out := make([]finding.Span, 0, len(pf.spots)+len(pf.unplaced))
	for _, s := range pf.spots {
		if !s.raw.wholeFile() {
			out = append(out, s.span)
		}
	}
	return append(out, pf.unplaced...)
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
