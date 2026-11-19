package finding

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

const (
	contextRadius   = 2
	maxContextLines = 2*contextRadius + 1
	maxLineRunes    = 200
)

// Context returns the lines around target in f, and the file line number of
// the first of them. Every span in all, and target itself, is replaced by
// "****" before the lines are cut, and a line is cut to 200 characters only
// after that, so no part of a detected value can survive. Between masking
// and cutting, each line goes through Printable. Spans may be
// unsorted, overlapping, adjacent or outside the text; they are clamped to the
// text and widened to whole characters, and touching spans become one "****".
//
// The window runs from two lines before the first line of target to two lines
// after its last line. When target covers more than five lines the window
// ends two lines after its first line instead. Line breaks inside a masked
// span are kept, so the returned lines are real file lines: the first line of
// a multi-line value shows the text before it, the lines inside it are empty,
// and the last shows the text after it.
func Context(f textfile.File, target Span, all []Span) (lines []string, first int) {
	spans := make([]Span, 0, len(all)+1)
	spans = append(spans, all...)
	spans = append(spans, target)
	merged := mergeSpans(f.Text, spans)

	t := widen(f.Text, target)
	firstLine, _ := f.Position(t.Start)
	lastLine := firstLine
	if t.Start < t.End {
		lastLine, _ = f.Position(t.End - 1)
	}
	if lastLine-firstLine+1 > maxContextLines {
		lastLine = firstLine
	}
	first = max(firstLine-contextRadius, 1)
	last := lastLine + contextRadius

	masked := textfile.File{Text: applyMask(f.Text, merged)}
	lines = masked.Lines(first, last)
	for i, ln := range lines {
		lines[i] = truncateRunes(Printable(ln), maxLineRunes)
	}
	return lines, first
}

// Printable returns s with every byte that is not part of valid UTF-8, and
// every control character except tab, replaced by U+FFFD. postgres refuses
// NUL and invalid UTF-8 in text, and the other control characters are not
// meant to be shown. Context applies it after masking, so it only ever
// replaces characters and cannot bring back a masked byte.
func Printable(s string) string {
	clean := true
	for _, r := range s {
		if r == utf8.RuneError || (unicode.IsControl(r) && r != '\t') {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == utf8.RuneError || (unicode.IsControl(r) && r != '\t') {
			r = utf8.RuneError
		}
		b.WriteRune(r)
	}
	return b.String()
}

// widen clamps s to text and extends it to whole characters. A span that is
// empty or reversed after clamping comes back with End equal to Start.
func widen(text []byte, s Span) Span {
	start := max(0, min(s.Start, len(text)))
	end := max(0, min(s.End, len(text)))
	if end <= start {
		return Span{Start: start, End: start}
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	return Span{Start: start, End: end}
}

// mergeSpans returns the non-empty, widened spans in start order, with
// overlapping and adjacent spans joined.
func mergeSpans(text []byte, spans []Span) []Span {
	ws := make([]Span, 0, len(spans))
	for _, s := range spans {
		if w := widen(text, s); w.Start < w.End {
			ws = append(ws, w)
		}
	}
	slices.SortFunc(ws, func(a, b Span) int { return a.Start - b.Start })
	var out []Span
	for _, s := range ws {
		if n := len(out); n > 0 && s.Start <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, s.End)
			continue
		}
		out = append(out, s)
	}
	return out
}

// applyMask returns a copy of text in which each span is replaced by "****"
// followed by the line breaks the span contained.
func applyMask(text []byte, spans []Span) []byte {
	out := make([]byte, 0, len(text))
	pos := 0
	for _, s := range spans {
		out = append(out, text[pos:s.Start]...)
		out = append(out, maskText...)
		for _, b := range text[s.Start:s.End] {
			if b == '\n' {
				out = append(out, '\n')
			}
		}
		pos = s.End
	}
	return append(out, text[pos:]...)
}

// truncateRunes returns at most n characters of s.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}
