package scan

import (
	"bytes"
	"path"
	"slices"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// maxKeyBytes is the longest configuration key that configKey returns.
const maxKeyBytes = 200

// configKey reads the configuration key that the value starting at byte at of
// text is assigned to. rel is the slash-separated path of the file and selects
// the reading rules by extension. The spans are byte ranges of text, one per
// piece of the key in key order, and joining the pieces with "." gives the
// key. It returns "", nil when the value has no readable key.
func configKey(rel string, text []byte, at int) (string, []finding.Span) {
	if at < 0 || at > len(text) {
		return "", nil
	}
	ls := bytes.LastIndexByte(text[:at], '\n') + 1
	le := len(text)
	if i := bytes.IndexByte(text[at:], '\n'); i >= 0 {
		le = at + i
	}
	if le > ls && text[le-1] == '\r' {
		le--
	}
	if at > le {
		return "", nil
	}

	var spans []finding.Span
	switch strings.ToLower(path.Ext(rel)) {
	case ".yml", ".yaml":
		spans = yamlPieces(text, ls, at)
	case ".xml":
		spans = xmlPieces(text, ls, at)
	default:
		if s, _, ok := leafPiece(text, ls, at); ok {
			spans = []finding.Span{s}
		}
	}
	return assembleKey(text, spans)
}

// assembleKey joins the pieces and rejects a key that is empty, too long, or
// not made of key characters.
func assembleKey(text []byte, spans []finding.Span) (string, []finding.Span) {
	if len(spans) == 0 {
		return "", nil
	}
	var b strings.Builder
	for i, s := range spans {
		if s.Start >= s.End {
			return "", nil
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.Write(text[s.Start:s.End])
	}
	key := b.String()
	if len(key) > maxKeyBytes {
		return "", nil
	}
	for i := 0; i < len(key); i++ {
		if !isKeyChar(key[i]) {
			return "", nil
		}
	}
	return key, spans
}

// isKeyChar reports whether b may appear in a configuration key.
func isKeyChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
		b == '.' || b == '_' || b == '-'
}

func isBlank(b byte) bool { return b == ' ' || b == '\t' }

func isSpace(b byte) bool { return isBlank(b) || b == '\n' || b == '\r' }

// endsTagName reports whether b may follow an element name.
func endsTagName(b byte) bool { return isSpace(b) || b == '>' || b == '/' }

func isQuote(b byte) bool { return b == '"' || b == '\'' }

// leafPiece reads the key run that ends at the assignment symbol before the
// value at byte at, looking only at the line that starts at ls. It also
// returns the symbol.
func leafPiece(text []byte, ls, at int) (finding.Span, byte, bool) {
	i := at
	if i > ls && isQuote(text[i-1]) {
		i--
	}
	for i > ls && isBlank(text[i-1]) {
		i--
	}
	if i <= ls {
		return finding.Span{}, 0, false
	}
	sym := text[i-1]
	i--
	switch sym {
	case '=', ':':
		for i > ls && isBlank(text[i-1]) {
			i--
		}
		if i > ls && isQuote(text[i-1]) {
			i--
		}
	case '(':
		// A call's first argument: the key is the name of the method called.
	default:
		return finding.Span{}, 0, false
	}
	end := i
	for i > ls && isKeyChar(text[i-1]) {
		i--
	}
	if i == end {
		return finding.Span{}, 0, false
	}
	return finding.Span{Start: i, End: end}, sym, true
}

// yamlPieces returns the key of the value at byte at: the parents found by
// climbing to shallower lines, then the key on the value's own line.
func yamlPieces(text []byte, ls, at int) []finding.Span {
	leaf, sym, ok := leafPiece(text, ls, at)
	if !ok || sym != ':' {
		return nil
	}
	indent := 0
	for ls+indent < len(text) && isBlank(text[ls+indent]) {
		indent++
	}
	if bytes.HasPrefix(text[ls+indent:], []byte("- ")) {
		return []finding.Span{leaf}
	}

	spans := []finding.Span{leaf}
	cur, pos := indent, ls
	for cur > 0 && pos > 0 {
		end := pos - 1
		start := bytes.LastIndexByte(text[:end], '\n') + 1
		pos = start
		line := bytes.TrimRight(text[start:end], "\r \t")
		ind := 0
		for ind < len(line) && isBlank(line[ind]) {
			ind++
		}
		content := line[ind:]
		if len(content) == 0 || content[0] == '#' || ind >= cur {
			continue
		}
		if bytes.HasPrefix(content, []byte("- ")) || bytes.Equal(content, []byte("-")) {
			break
		}
		k := 0
		for k < len(content) && isKeyChar(content[k]) {
			k++
		}
		if k == 0 || k >= len(content) || content[k] != ':' ||
			(k+1 < len(content) && !isBlank(content[k+1])) {
			return nil
		}
		spans = append(spans, finding.Span{Start: start + ind, End: start + ind + k})
		cur = ind
	}
	slices.Reverse(spans)
	return spans
}

// xmlPieces returns the key of the value at byte at: the leaf name, prefixed
// by the id of the enclosing bean when there is one. The tag's name attribute
// is the leaf only when the value is the tag's value attribute or its text,
// as in <property name="password" value="…"/>. A value in any other
// attribute takes that attribute's name, because there the name attribute
// names the element, not the value.
func xmlPieces(text []byte, ls, at int) []finding.Span {
	lt := -1
	if i := bytes.LastIndexByte(text[ls:at], '<'); i >= 0 {
		lt = ls + i
	}
	leaf, ok := finding.Span{}, false
	if s, sym, found := leafPiece(text, ls, at); found && sym == '=' {
		leaf, ok = s, true
	}
	if lt >= 0 && (!ok || string(text[leaf.Start:leaf.End]) == "value") {
		if s, found := xmlAttr(text, lt, at, "name"); found {
			leaf, ok = s, true
		}
	}
	if !ok && lt >= 0 && at > ls && text[at-1] == '>' {
		s := lt + 1
		e := s
		for e < at && isKeyChar(text[e]) {
			e++
		}
		if e > s && e < at && endsTagName(text[e]) {
			leaf, ok = finding.Span{Start: s, End: e}, true
		}
	}
	if !ok {
		return nil
	}

	b, tagEnd, found := openBean(text, at)
	if !found {
		return []finding.Span{leaf}
	}
	if id, found := xmlAttr(text, b, tagEnd, "id"); found {
		return []finding.Span{id, leaf}
	}
	return []finding.Span{leaf}
}

// openBean finds the nearest bean element that is still open at byte at:
// the start tag that holds at, or else the last start tag before at whose
// element has not been closed by then. It walks the tags backwards,
// counting each </bean> against the next start tag, and passes over a
// self-closed <bean .../> that ends before at. A '>' inside a quoted
// attribute value does not end a tag. It returns the offset of the
// start tag's '<' and of its '>', or of the end of text when the tag is not
// closed. A prefixed element such as <beans:bean> is not a bean here.
func openBean(text []byte, at int) (start, tagEnd int, ok bool) {
	depth := 0
	for pos := at; pos > 0; {
		lt := bytes.LastIndexByte(text[:pos], '<')
		if lt < 0 {
			break
		}
		pos = lt
		switch {
		case isTag(text, lt, "</bean"):
			depth++
		case isTag(text, lt, "<bean"):
			end := tagClose(text, lt)
			if end < at && text[end-1] == '/' {
				continue
			}
			if depth == 0 {
				return lt, end, true
			}
			depth--
		}
	}
	return 0, 0, false
}

// tagClose returns the offset of the '>' that closes the tag starting at
// byte lt, passing over a '>' inside a quoted attribute value, or the end of
// text when the tag is not closed.
func tagClose(text []byte, lt int) int {
	var quote byte
	for i := lt; i < len(text); i++ {
		switch b := text[i]; {
		case quote != 0:
			if b == quote {
				quote = 0
			}
		case isQuote(b):
			quote = b
		case b == '>':
			return i
		}
	}
	return len(text)
}

// isTag reports whether text holds the tag opening name at byte i, followed
// by a byte that ends an element name.
func isTag(text []byte, i int, name string) bool {
	after := i + len(name)
	return bytes.HasPrefix(text[i:], []byte(name)) && after < len(text) && endsTagName(text[after])
}

// xmlAttr finds the attribute name="value" in text[from:to] and returns the
// span of its quoted value. The attribute name must follow white space.
func xmlAttr(text []byte, from, to int, name string) (finding.Span, bool) {
	tag := text[from:to]
	needle := []byte(name + "=")
	for off := 0; off < len(tag); {
		j := bytes.Index(tag[off:], needle)
		if j < 0 {
			break
		}
		j += off
		off = j + len(needle)
		if j == 0 || !isSpace(tag[j-1]) || off >= len(tag) || !isQuote(tag[off]) {
			continue
		}
		n := bytes.IndexByte(tag[off+1:], tag[off])
		if n < 0 {
			break
		}
		return finding.Span{Start: from + off + 1, End: from + off + 1 + n}, true
	}
	return finding.Span{}, false
}
