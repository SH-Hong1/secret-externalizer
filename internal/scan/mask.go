package scan

import (
	"bytes"
	"encoding/binary"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// minMaskLen is the length in bytes of the shortest value that surrounding
// lines mask wherever it occurs. A shorter value, such as a three-letter
// account name, would also match many words that are not that value.
const minMaskLen = 4

// maskValues returns the distinct values that the surrounding lines of every
// file mask wherever they occur: the value of every result placed in a text
// file, of any category, and every value a scanner reported that could not
// be placed. Whole-file values and values shorter than minMaskLen bytes are
// left out. The returned slices share the bytes of placed and raws.
func maskValues(placed []*placedFile, raws []Raw) [][]byte {
	seen := map[string]bool{}
	var out [][]byte
	add := func(v []byte) {
		if len(v) < minMaskLen || seen[string(v)] {
			return
		}
		seen[string(v)] = true
		out = append(out, v)
	}
	for _, pf := range placed {
		for _, s := range pf.spots {
			if !s.raw.wholeFile() {
				add(s.value)
			}
		}
	}
	for _, raw := range raws {
		if !raw.wholeFile() {
			add(raw.Value)
		}
	}
	return out
}

// valueIndex finds every place of a set of values in a text. It groups the
// values by their first four bytes, so one pass over a text finds them all:
// a text of n bytes costs n lookups plus a comparison for each value that
// shares the four bytes at a position.
type valueIndex map[uint32][][]byte

// newValueIndex returns the index of values, each at least minMaskLen bytes
// long. It shares the bytes of values.
func newValueIndex(values [][]byte) valueIndex {
	ix := valueIndex{}
	for _, v := range values {
		k := binary.LittleEndian.Uint32(v)
		ix[k] = append(ix[k], v)
	}
	return ix
}

// find returns the range of every occurrence of every value in text,
// overlapping occurrences included.
func (ix valueIndex) find(text []byte) []finding.Span {
	if len(ix) == 0 {
		return nil
	}
	var out []finding.Span
	for i := 0; i+minMaskLen <= len(text); i++ {
		for _, v := range ix[binary.LittleEndian.Uint32(text[i:])] {
			if bytes.HasPrefix(text[i:], v) {
				out = append(out, finding.Span{Start: i, End: i + len(v)})
			}
		}
	}
	return out
}
