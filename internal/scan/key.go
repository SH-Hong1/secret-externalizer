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
// climbing to shallower lines, then the key on the value's own line. A list
// item adds the value of its name or id key, when it has one, between the
// list's key and the keys inside the item, so that two items of one list
// get different keys.
func yamlPieces(text []byte, ls, at int) []finding.Span {
	leaf, sym, ok := leafPiece(text, ls, at)
	if !ok || sym != ':' {
		return nil
	}
	spans := []finding.Span{leaf}
	cur, content := yamlLine(text, ls)
	// atList is set when cur is the column of a list item's dash. The key of
	// the list may then sit at the same column as the dash.
	atList := false
	if isDashItem(content) {
		if s, ok := itemName(text, ls, cur, at); ok {
			spans = append(spans, s)
		}
		atList = true
	}

	pos := ls
	for (cur > 0 || atList) && pos > 0 {
		end := pos - 1
		start := bytes.LastIndexByte(text[:end], '\n') + 1
		pos = start
		ind, content := yamlLine(text, start)
		if len(content) == 0 || content[0] == '#' || ind > cur {
			continue
		}
		dash := isDashItem(content)
		if ind == cur && (!atList || dash) {
			continue
		}
		if dash {
			// The list item that holds the keys found so far. A key on the
			// dash's line is their parent when they are deeper than it.
			if k, ok := yamlKey(content, 1); ok && k.End == len(content) && cur > k.Start+ind {
				spans = append(spans, finding.Span{Start: start + ind + k.Start, End: start + ind + k.End - 1})
			}
			if s, ok := itemName(text, start, ind, at); ok {
				spans = append(spans, s)
			}
			cur, atList = ind, true
			continue
		}
		k, ok := yamlKey(content, 0)
		if !ok || k.Start != 0 {
			return nil
		}
		spans = append(spans, finding.Span{Start: start + ind, End: start + ind + k.End - 1})
		cur, atList = ind, false
	}
	slices.Reverse(spans)
	return spans
}

// yamlLine returns the indentation of the line that starts at ls and the
// rest of that line, without its trailing blanks.
func yamlLine(text []byte, ls int) (int, []byte) {
	le := len(text)
	if i := bytes.IndexByte(text[ls:], '\n'); i >= 0 {
		le = ls + i
	}
	line := bytes.TrimRight(text[ls:le], "\r \t")
	ind := 0
	for ind < len(line) && isBlank(line[ind]) {
		ind++
	}
	return ind, line[ind:]
}

// isDashItem reports whether a line's content starts a list item.
func isDashItem(content []byte) bool {
	return len(content) > 0 && content[0] == '-' && (len(content) == 1 || isBlank(content[1]))
}

// yamlKey reads a mapping key in content after skipping blanks from byte
// from. The span starts at the key and ends after its colon, which is
// followed by a blank or by the end of content.
func yamlKey(content []byte, from int) (finding.Span, bool) {
	i := from
	for i < len(content) && isBlank(content[i]) {
		i++
	}
	k := i
	for k < len(content) && isKeyChar(content[k]) {
		k++
	}
	if k == i || k >= len(content) || content[k] != ':' ||
		(k+1 < len(content) && !isBlank(content[k+1])) {
		return finding.Span{}, false
	}
	return finding.Span{Start: i, End: k + 1}, true
}

// itemName returns the value of the first name or id key of the list item
// whose dash is at column ind of the line that starts at ls. Only keys of the
// item itself count, not keys nested deeper in it. The value must be made of
// key characters, and must not hold the byte at, which is the value whose
// key is being read.
func itemName(text []byte, ls, ind, at int) (finding.Span, bool) {
	keyCol := -1
	for pos := ls; pos < len(text); {
		next := len(text)
		if i := bytes.IndexByte(text[pos:], '\n'); i >= 0 {
			next = pos + i + 1
		}
		lineInd, content := yamlLine(text, pos)
		from := 0
		switch {
		case pos == ls:
			from = 1
		case len(content) == 0 || content[0] == '#':
			pos = next
			continue
		case lineInd <= ind:
			return finding.Span{}, false
		}
		k, ok := yamlKey(content, from)
		col := lineInd + k.Start
		if ok && keyCol < 0 {
			keyCol = col
		}
		if ok && col == keyCol {
			name := string(content[k.Start : k.End-1])
			if name == "name" || name == "id" {
				return nameValue(content, k.End, pos+lineInd, at)
			}
		}
		if !ok && pos != ls && keyCol < 0 {
			keyCol = lineInd
		}
		pos = next
	}
	return finding.Span{}, false
}

// nameValue returns the plain scalar after byte from of content, which
// starts at byte base of the text. A quoted scalar is returned without its
// quotes and a trailing comment is dropped.
func nameValue(content []byte, from, base, at int) (finding.Span, bool) {
	rest := content[from:]
	if i := bytes.Index(rest, []byte(" #")); i >= 0 {
		rest = rest[:i]
	}
	s := from
	for len(rest) > 0 && isBlank(rest[0]) {
		rest = rest[1:]
		s++
	}
	rest = bytes.TrimRight(rest, " \t")
	if len(rest) >= 2 && isQuote(rest[0]) && rest[len(rest)-1] == rest[0] {
		rest = rest[1 : len(rest)-1]
		s++
	}
	if len(rest) == 0 {
		return finding.Span{}, false
	}
	for _, b := range rest {
		if !isKeyChar(b) {
			return finding.Span{}, false
		}
	}
	span := finding.Span{Start: base + s, End: base + s + len(rest)}
	if span.Start <= at && at < span.End {
		return finding.Span{}, false
	}
	return span, true
}

// xmlPieces returns the key of the value at byte at: the leaf name, prefixed
// by the id of the enclosing bean when there is one. The tag's name attribute
// is the leaf only when the value is the tag's value attribute or its text,
// as in <property name="password" value="…"/>. A value in any other
// attribute takes that attribute's name, because there the name attribute
// names the element, not the value. For an element other than a bean, that
// name then goes before the leaf, split at each slash, so that
// <Resource name="jdbc/live" password="…"/> has the key jdbc.live.password
// and two resources of one file get different keys. A bean is named by its
// id instead.
func xmlPieces(text []byte, ls, at int) []finding.Span {
	lt := ownTag(text, at)
	leaf, ok := finding.Span{}, false
	if s, sym, found := leafPiece(text, ls, at); found && sym == '=' {
		leaf, ok = s, true
	}
	var named []finding.Span
	if lt >= 0 {
		name, found := xmlAttr(text, lt, at, "name")
		switch {
		case !found:
		case !ok || string(text[leaf.Start:leaf.End]) == "value":
			leaf, ok = name, true
		case !isBeanTag(text, lt+len("<")):
			named = nameSpans(text, name)
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

	spans := make([]finding.Span, 0, len(named)+2)
	spans = append(spans, named...)
	spans = append(spans, leaf)
	b, tagEnd, found := openBean(text, at)
	if !found {
		return spans
	}
	if id, found := xmlAttr(text, b, tagEnd, "id"); found {
		return slices.Insert(spans, 0, id)
	}
	return spans
}

// nameSpans splits the name attribute value at s into its slash-separated
// pieces, so that a JNDI name such as jdbc/live gives the pieces jdbc and
// live. It returns nil when a piece is empty or holds a byte that is not a
// key character, and the key then goes without the name.
func nameSpans(text []byte, s finding.Span) []finding.Span {
	var spans []finding.Span
	start := s.Start
	for i := s.Start; i <= s.End; i++ {
		if i < s.End && text[i] != '/' {
			if !isKeyChar(text[i]) {
				return nil
			}
			continue
		}
		if i == start {
			return nil
		}
		spans = append(spans, finding.Span{Start: start, End: i})
		start = i + 1
	}
	return spans
}

// ownTag returns the offset of the '<' of the tag that holds byte at, or
// that ends right before at so that the value is its element's text. The
// tag may start on an earlier line. It returns -1 when there is no such tag.
func ownTag(text []byte, at int) int {
	lt := bytes.LastIndexByte(text[:at], '<')
	if lt < 0 {
		return -1
	}
	if end := tagClose(text, lt); end < at && end != at-1 {
		return -1
	}
	return lt
}

// openBean finds the nearest bean element that is still open at byte at:
// the start tag that holds at, or else the last start tag before at whose
// element has not been closed by then. It walks the tags backwards,
// counting each </bean> against the next start tag, and passes over a
// self-closed <bean .../> that ends before at. A '>' inside a quoted
// attribute value does not end a tag, and a tag inside a comment
// (<!-- ... -->) that ends before at is not counted. It returns the offset of the
// start tag's '<' and of its '>', or of the end of text when the tag is not
// closed. A bean may carry a namespace prefix, as in <beans:bean>.
func openBean(text []byte, at int) (start, tagEnd int, ok bool) {
	depth := 0
	commentEnd := bytes.LastIndex(text[:at], []byte("-->"))
	for pos := at; pos > 0; {
		lt := bytes.LastIndexByte(text[:pos], '<')
		if lt < 0 {
			break
		}
		if commentEnd > lt {
			// The tags between lt and the comment's end are inside the
			// comment: go on from the comment's start.
			c := bytes.LastIndex(text[:commentEnd], []byte("<!--"))
			commentEnd = -1
			if c >= 0 {
				pos = c
				commentEnd = bytes.LastIndex(text[:c], []byte("-->"))
			}
			continue
		}
		pos = lt
		switch {
		case lt+1 < len(text) && text[lt+1] == '/' && isBeanTag(text, lt+len("</")):
			depth++
		case isBeanTag(text, lt+len("<")):
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

// isBeanTag reports whether the element name that starts at byte i of text
// is bean, with or without a namespace prefix such as beans:, followed by a
// byte that ends an element name.
func isBeanTag(text []byte, i int) bool {
	j := i
	for j < len(text) && isKeyChar(text[j]) {
		j++
	}
	if j < len(text) && text[j] == ':' && j > i {
		i = j + 1
	}
	after := i + len("bean")
	return bytes.HasPrefix(text[i:], []byte("bean")) && after < len(text) && endsTagName(text[after])
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
