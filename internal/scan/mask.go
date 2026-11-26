package scan

import (
	"bytes"
	"encoding/binary"

	"github.com/SH-Hong1/secret-externalizer/internal/connstr"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// minMaskLen is the length in bytes of the shortest value that surrounding
// lines mask wherever it occurs. A shorter value, such as a three-letter
// account name, would also match many words that are not that value.
const minMaskLen = 4

// pattern is a text that surrounding lines mask wherever it occurs. When
// parts is empty the whole text is masked; otherwise only parts, ranges
// relative to the start of the text.
type pattern struct {
	text  []byte
	parts []finding.Span
}

// maskPatterns returns what the surrounding lines of every file mask
// wherever it occurs: the value of every result placed in a text file, of
// any category, and every value a scanner reported that could not be
// placed, each masked whole; and every split URL that has a component
// shorter than minMaskLen bytes, masked only at those components, so that a
// short port or account stays hidden where the same URL occurs unreported.
// Whole-file values and texts shorter than minMaskLen bytes are left out.
// The returned slices share the bytes of placed and raws.
func maskPatterns(placed []*placedFile, raws []Raw) []pattern {
	seen := map[string]bool{}
	var out []pattern
	add := func(v []byte) {
		if len(v) < minMaskLen || seen[string(v)] {
			return
		}
		seen[string(v)] = true
		out = append(out, pattern{text: v})
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
	seenURL := map[string]bool{}
	for _, pf := range placed {
		for _, u := range pf.urls {
			text := pf.file.Text[u.Start:u.End]
			if len(text) < minMaskLen || seen[string(text)] || seenURL[string(text)] {
				continue
			}
			seenURL[string(text)] = true
			parts, _ := connstr.Split(text)
			var short []finding.Span
			for _, p := range parts {
				if p.Span.End-p.Span.Start < minMaskLen {
					short = append(short, p.Span)
				}
			}
			if len(short) > 0 {
				out = append(out, pattern{text: text, parts: short})
			}
		}
	}
	return out
}

// valueIndex finds every place of a set of patterns in a text. It groups
// the patterns by their first four bytes, so one pass over a text finds them
// all: a text of n bytes costs n lookups plus a comparison for each pattern
// that shares the four bytes at a position.
type valueIndex map[uint32][]pattern

// newValueIndex returns the index of patterns, each at least minMaskLen
// bytes long. It shares the bytes of patterns.
func newValueIndex(patterns []pattern) valueIndex {
	ix := valueIndex{}
	for _, p := range patterns {
		k := binary.LittleEndian.Uint32(p.text)
		ix[k] = append(ix[k], p)
	}
	return ix
}

// find returns the ranges to mask of every occurrence of every pattern in
// text, overlapping occurrences included.
func (ix valueIndex) find(text []byte) []finding.Span {
	if len(ix) == 0 {
		return nil
	}
	var out []finding.Span
	for i := 0; i+minMaskLen <= len(text); i++ {
		for _, p := range ix[binary.LittleEndian.Uint32(text[i:])] {
			if !bytes.HasPrefix(text[i:], p.text) {
				continue
			}
			if len(p.parts) == 0 {
				out = append(out, finding.Span{Start: i, End: i + len(p.text)})
				continue
			}
			for _, s := range p.parts {
				out = append(out, finding.Span{Start: i + s.Start, End: i + s.End})
			}
		}
	}
	return out
}
