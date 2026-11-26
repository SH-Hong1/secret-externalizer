// Package literal reads the value a source file gives to a range of its
// text: the text with the escapes of the file's syntax undone, as the
// program that reads the file sees it. A properties, XML, YAML, Java, shell
// or JSON file is read by its own rules; any other file gives its text as it
// is.
package literal

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// ErrUnclear means the text alone does not settle the value: it holds an
// escape the syntax does not define, a reference to something defined
// elsewhere, an expansion the shell would carry out, a quote that ends the
// string it is in, or a line break.
var ErrUnclear = errors.New("value cannot be read from the text alone")

// unclear returns ErrUnclear with why, a fixed text that never holds a
// character of the value.
func unclear(why string) error {
	return fmt.Errorf("%w: %s", ErrUnclear, why)
}

// syntax is the set of rules a file's text is read by.
type syntax int

const (
	syntaxOther syntax = iota + 1
	syntaxProperties
	syntaxXML
	syntaxYAML
	syntaxJava
	syntaxShell
	syntaxJSON
)

// syntaxOf returns the syntax of file, by its extension, ignoring case.
func syntaxOf(file string) syntax {
	if triage.ShellFile(file) {
		return syntaxShell
	}
	switch strings.ToLower(path.Ext(file)) {
	case ".properties":
		return syntaxProperties
	case ".xml":
		return syntaxXML
	case ".yml", ".yaml":
		return syntaxYAML
	case ".java":
		return syntaxJava
	case ".json":
		return syntaxJSON
	}
	return syntaxOther
}

// Value returns the value that file gives to the range sp of text, its
// decoded content. The context of the range is read from its line up to
// the range, and for XML from the text before it: a range inside a comment,
// an XML CDATA section, a YAML plain scalar or single-quoted shell string,
// or Java code outside a string, holds its text as it is. Value fails with
// ErrUnclear when the text alone does not settle the value, and in a file
// with a known syntax when the range holds a line break. The result is a
// new slice that shares no bytes with text, so the caller may zero it.
func Value(file string, text []byte, sp finding.Span) ([]byte, error) {
	if sp.Start < 0 || sp.End > len(text) || sp.Start > sp.End {
		return nil, errors.New("range outside the text")
	}
	raw := text[sp.Start:sp.End]
	s := syntaxOf(file)
	if s == syntaxOther {
		return clone(raw), nil
	}
	if bytes.ContainsAny(raw, "\r\n") {
		return nil, unclear("range holds a line break")
	}
	line := text[bytes.LastIndexByte(text[:sp.Start], '\n')+1 : sp.Start]
	switch s {
	case syntaxOther:
		return clone(raw), nil
	case syntaxProperties:
		return properties(line, raw)
	case syntaxXML:
		return xml(text[:sp.Start], raw)
	case syntaxYAML:
		return yaml(line, raw)
	case syntaxJava:
		return java(line, raw)
	case syntaxShell:
		return shell(line, raw)
	case syntaxJSON:
		return json(line, raw)
	}
	return nil, errors.New("unknown syntax")
}

// clone returns a copy of b that is never nil.
func clone(b []byte) []byte {
	return append(make([]byte, 0, len(b)), b...)
}

// simpleEscape returns the byte of to at the index c has in from.
func simpleEscape(c byte, from, to string) (byte, bool) {
	i := strings.IndexByte(from, c)
	if i < 0 {
		return 0, false
	}
	return to[i], true
}

// digitsRune reads b, which is all digits of base 10 or 16, as a Unicode
// code point. It fails on an empty b, a byte that is not such a digit, and
// a number above the last code point.
func digitsRune(b []byte, base rune) (rune, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var v rune
	for _, c := range b {
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case base == 16 && c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case base == 16 && c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, false
		}
		v = v*base + d
		if v > utf8.MaxRune {
			return 0, false
		}
	}
	return v, true
}

// hexRune reads exactly n hex digits at the start of b as a code point.
func hexRune(b []byte, n int) (rune, bool) {
	if len(b) < n {
		return 0, false
	}
	return digitsRune(b[:n], 16)
}

// utf16Escape reads what follows "\u" at the start of b: four hex digits,
// and when they name a high surrogate, a second "\u" escape naming the low
// one. With manyU, as in Java, each "u" may be repeated. It returns the
// character and the number of bytes of b it read.
func utf16Escape(b []byte, manyU bool) (rune, int, error) {
	n := 0
	if manyU {
		for n < len(b) && b[n] == 'u' {
			n++
		}
	}
	r, ok := hexRune(b[n:], 4)
	if !ok {
		return 0, 0, unclear("unicode escape without four hex digits")
	}
	n += 4
	if !utf16.IsSurrogate(r) {
		return r, n, nil
	}
	rest := b[n:]
	if len(rest) < 2 || rest[0] != '\\' || rest[1] != 'u' {
		return 0, 0, unclear("unpaired surrogate")
	}
	m := 2
	if manyU {
		for m < len(rest) && rest[m] == 'u' {
			m++
		}
	}
	low, ok := hexRune(rest[m:], 4)
	if !ok {
		return 0, 0, unclear("unicode escape without four hex digits")
	}
	pair := utf16.DecodeRune(r, low)
	if pair == utf8.RuneError {
		return 0, 0, unclear("unpaired surrogate")
	}
	return pair, n + m + 4, nil
}

// properties reads raw as java.util.Properties reads a key or value: a
// backslash escapes the character after it, and \t, \n, \r, \f and \uXXXX
// stand for characters. A comment line holds its text as it is.
func properties(line, raw []byte) ([]byte, error) {
	if first := bytes.TrimLeft(line, " \t\f"); len(first) > 0 && (first[0] == '#' || first[0] == '!') {
		return clone(raw), nil
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			out = append(out, raw[i])
			continue
		}
		i++
		if i == len(raw) {
			return nil, unclear("escape at the end of the range")
		}
		if raw[i] == 'u' {
			r, n, err := utf16Escape(raw[i+1:], false)
			if err != nil {
				return nil, err
			}
			out = utf8.AppendRune(out, r)
			i += n
			continue
		}
		if e, ok := simpleEscape(raw[i], "tnrf", "\t\n\r\f"); ok {
			out = append(out, e)
			continue
		}
		out = append(out, raw[i])
	}
	return out, nil
}

// inside reports whether the end of before lies between open and the next
// end.
func inside(before []byte, open, end string) bool {
	o := bytes.LastIndex(before, []byte(open))
	return o >= 0 && bytes.LastIndex(before, []byte(end)) < o
}

// xml reads raw as an XML parser reads text or an attribute value: the five
// predefined entities and character references stand for characters. Raw
// inside a comment or a CDATA section, by the text before it, holds its
// text as it is. A tab, which a parser changes to a space in an attribute
// but not in text, is unclear.
func xml(before, raw []byte) ([]byte, error) {
	if inside(before, "<![CDATA[", "]]>") || inside(before, "<!--", "-->") {
		return clone(raw), nil
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\t' {
			return nil, unclear("tab in XML text")
		}
		if raw[i] != '&' {
			out = append(out, raw[i])
			continue
		}
		end := bytes.IndexByte(raw[i:], ';')
		if end < 0 {
			return nil, unclear("ampersand without a reference")
		}
		r, ok := entity(raw[i+1 : i+end])
		if !ok {
			return nil, unclear("reference to an entity defined elsewhere")
		}
		out = utf8.AppendRune(out, r)
		i += end
	}
	return out, nil
}

// entity returns the character name, the text between "&" and ";", stands
// for: a predefined entity, or a character reference made of "#" and
// decimal digits or "#x" and hex digits.
func entity(name []byte) (rune, bool) {
	switch string(name) {
	case "amp":
		return '&', true
	case "lt":
		return '<', true
	case "gt":
		return '>', true
	case "quot":
		return '"', true
	case "apos":
		return '\'', true
	}
	var (
		r  rune
		ok bool
	)
	switch {
	case bytes.HasPrefix(name, []byte("#x")):
		r, ok = digitsRune(name[2:], 16)
	case bytes.HasPrefix(name, []byte("#")):
		r, ok = digitsRune(name[1:], 10)
	}
	return r, ok && r != 0 && utf8.ValidRune(r)
}

// quoting is where a range starts within the quoting of its line.
type quoting int

const (
	inPlain quoting = iota + 1
	inSingle
	inDouble
	inComment
	inUnclear
)

// yamlQuoting returns where the end of line lies: in a plain scalar, a
// single- or double-quoted scalar, or a comment. A quote opens a scalar
// only where a scalar can start: at the start of the line, or after
// indentation, "- ", ": ", "? ", "[", "{" or ",".
func yamlQuoting(line []byte) quoting {
	at := inPlain
	canStart := true
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch at {
		case inDouble:
			switch c {
			case '\\':
				i++
			case '"':
				at, canStart = inPlain, false
			}
		case inSingle:
			switch {
			case c != '\'':
			case i+1 < len(line) && line[i+1] == '\'':
				i++
			default:
				at, canStart = inPlain, false
			}
		case inPlain, inComment, inUnclear:
			spaceAfter := i+1 == len(line) || line[i+1] == ' ' || line[i+1] == '\t'
			switch {
			case c == ' ' || c == '\t':
			case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
				return inComment
			case c == '"' && canStart:
				at = inDouble
			case c == '\'' && canStart:
				at = inSingle
			case c == '[' || c == '{' || c == ',':
				canStart = true
			case (c == '-' || c == ':' || c == '?') && spaceAfter:
				canStart = true
			default:
				canStart = false
			}
		}
	}
	return at
}

// yaml reads raw as a YAML parser reads a scalar on one line: a plain
// scalar or a comment as it is, a single-quoted scalar with "”" for a
// quote, and a double-quoted scalar with its escapes.
func yaml(line, raw []byte) ([]byte, error) {
	switch yamlQuoting(line) {
	case inSingle:
		return singleQuoted(raw)
	case inDouble:
		return yamlDouble(raw)
	case inPlain, inComment, inUnclear:
	}
	return clone(raw), nil
}

// singleQuoted reads raw inside a YAML single-quoted scalar.
func singleQuoted(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\'' {
			if i+1 == len(raw) || raw[i+1] != '\'' {
				return nil, unclear("quote that ends the string")
			}
			i++
		}
		out = append(out, raw[i])
	}
	return out, nil
}

// yamlSimpleEscape returns what the character after a backslash in a YAML
// double-quoted scalar stands for, for every escape without hex digits.
func yamlSimpleEscape(c byte) (string, bool) {
	switch c {
	case '0':
		return "\x00", true
	case 'e':
		return "\x1b", true
	case 'N':
		return string(rune(0x0085)), true
	case '_':
		return string(rune(0x00a0)), true
	case 'L':
		return string(rune(0x2028)), true
	case 'P':
		return string(rune(0x2029)), true
	}
	b, ok := simpleEscape(c, "abt\tnvfr \"/\\", "\a\b\t\t\n\v\f\r \"/\\")
	return string(b), ok
}

// yamlHexDigits returns how many hex digits follow the character c after
// a backslash in a YAML double-quoted scalar, or 0 when c starts no hex
// escape.
func yamlHexDigits(c byte) int {
	switch c {
	case 'x':
		return 2
	case 'u':
		return 4
	case 'U':
		return 8
	}
	return 0
}

// yamlDouble reads raw inside a YAML double-quoted scalar.
func yamlDouble(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			return nil, unclear("quote that ends the string")
		}
		if raw[i] != '\\' {
			out = append(out, raw[i])
			continue
		}
		i++
		if i == len(raw) {
			return nil, unclear("escape at the end of the range")
		}
		if s, ok := yamlSimpleEscape(raw[i]); ok {
			out = append(out, s...)
			continue
		}
		n := yamlHexDigits(raw[i])
		if n == 0 {
			return nil, unclear("escape the syntax does not define")
		}
		r, ok := hexRune(raw[i+1:], n)
		if !ok || !utf8.ValidRune(r) {
			return nil, unclear("hex escape without a valid character")
		}
		out = utf8.AppendRune(out, r)
		i += n
	}
	return out, nil
}

// javaQuoting returns where the end of line lies: in a string, in a
// comment, or in code. A text block is unclear, since its content is read
// with the indentation of lines this one does not show.
func javaQuoting(line []byte) quoting {
	at := inPlain
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch at {
		case inDouble, inSingle:
			quote := byte('"')
			if at == inSingle {
				quote = '\''
			}
			switch c {
			case '\\':
				i++
			case quote:
				at = inPlain
			}
		case inComment:
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				at = inPlain
				i++
			}
		case inPlain, inUnclear:
			switch {
			case bytes.HasPrefix(line[i:], []byte(`"""`)):
				return inUnclear
			case bytes.HasPrefix(line[i:], []byte("//")):
				return inComment
			case bytes.HasPrefix(line[i:], []byte("/*")):
				at = inComment
				i++
			case c == '"':
				at = inDouble
			case c == '\'':
				at = inSingle
			}
		}
	}
	return at
}

// java reads raw as the Java compiler reads a string literal; outside a
// string it is as it is.
func java(line, raw []byte) ([]byte, error) {
	switch javaQuoting(line) {
	case inUnclear:
		return nil, unclear("text block")
	case inDouble:
		return javaString(raw)
	case inPlain, inSingle, inComment:
	}
	return clone(raw), nil
}

// javaString reads raw inside a Java string literal.
func javaString(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			return nil, unclear("quote that ends the string")
		}
		if raw[i] != '\\' {
			out = append(out, raw[i])
			continue
		}
		i++
		if i == len(raw) {
			return nil, unclear("escape at the end of the range")
		}
		c := raw[i]
		if e, ok := simpleEscape(c, "btnfrs\"'\\", "\b\t\n\f\r \"'\\"); ok {
			out = append(out, e)
			continue
		}
		switch {
		case c == 'u':
			r, n, err := utf16Escape(raw[i:], true)
			if err != nil {
				return nil, err
			}
			out = utf8.AppendRune(out, r)
			i += n - 1
		case c >= '0' && c <= '7':
			// An octal escape has up to three digits when it starts with
			// 0 to 3, and up to two otherwise, so it stays below 0400.
			most := 2
			if c <= '3' {
				most = 3
			}
			var v rune
			n := 0
			for n < most && i+n < len(raw) && raw[i+n] >= '0' && raw[i+n] <= '7' {
				v = v*8 + rune(raw[i+n]-'0')
				n++
			}
			out = utf8.AppendRune(out, v)
			i += n - 1
		default:
			return nil, unclear("escape the syntax does not define")
		}
	}
	return out, nil
}

// shellWordBreak reports whether c ends a shell word, so that a "#" after
// it starts a comment.
func shellWordBreak(c byte) bool {
	return strings.IndexByte(" \t;&|()<>", c) >= 0
}

// shellQuoting returns where the end of line lies: in a single- or
// double-quoted string, in a comment, or unquoted. An ANSI-C string
// ($'...') is unclear.
func shellQuoting(line []byte) quoting {
	at := inPlain
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch at {
		case inSingle:
			if c == '\'' {
				at = inPlain
			}
		case inDouble:
			switch c {
			case '\\':
				i++
			case '"':
				at = inPlain
			}
		case inPlain, inComment, inUnclear:
			switch {
			case c == '\\':
				i++
			case c == '$' && i+1 < len(line) && line[i+1] == '\'':
				return inUnclear
			case c == '\'':
				at = inSingle
			case c == '"':
				at = inDouble
			case c == '#' && (i == 0 || shellWordBreak(line[i-1])):
				return inComment
			}
		}
	}
	return at
}

// shell reads raw as the shell reads a word: a single-quoted string as it
// is, a double-quoted string with "\$", "\`", "\"" and "\\" escaped, and an
// unquoted word with a backslash escaping any character. An expansion,
// pattern, quote or blank the shell would act on is unclear.
func shell(line, raw []byte) ([]byte, error) {
	switch shellQuoting(line) {
	case inUnclear:
		return nil, unclear("ANSI-C string")
	case inComment:
		return clone(raw), nil
	case inSingle:
		if bytes.IndexByte(raw, '\'') >= 0 {
			return nil, unclear("quote that ends the string")
		}
		return clone(raw), nil
	case inDouble:
		return shellDouble(raw)
	case inPlain:
	}
	return shellUnquoted(raw)
}

// shellDouble reads raw inside a double-quoted shell string.
func shellDouble(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			return nil, unclear("quote that ends the string")
		case '$', '`':
			return nil, unclear("expansion")
		case '\\':
			if i+1 == len(raw) {
				return nil, unclear("escape at the end of the range")
			}
			if strings.IndexByte("$`\"\\", raw[i+1]) >= 0 {
				i++
			}
		}
		out = append(out, raw[i])
	}
	return out, nil
}

// shellUnquoted reads raw as an unquoted shell word.
func shellUnquoted(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\':
			i++
			if i == len(raw) {
				return nil, unclear("escape at the end of the range")
			}
			out = append(out, raw[i])
		case strings.IndexByte("$`\"'*?[~{ \t;&|<>()", c) >= 0:
			return nil, unclear("character the shell acts on")
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

// jsonInString reports whether the end of line lies in a JSON string.
func jsonInString(line []byte) bool {
	in := false
	for i := 0; i < len(line); i++ {
		switch {
		case in && line[i] == '\\':
			i++
		case line[i] == '"':
			in = !in
		}
	}
	return in
}

// json reads raw as a JSON parser reads a string; outside a string it is as
// it is.
func json(line, raw []byte) ([]byte, error) {
	if !jsonInString(line) {
		return clone(raw), nil
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			return nil, unclear("quote that ends the string")
		}
		if raw[i] != '\\' {
			out = append(out, raw[i])
			continue
		}
		i++
		if i == len(raw) {
			return nil, unclear("escape at the end of the range")
		}
		if e, ok := simpleEscape(raw[i], "\"\\/bfnrt", "\"\\/\b\f\n\r\t"); ok {
			out = append(out, e)
			continue
		}
		if raw[i] != 'u' {
			return nil, unclear("escape the syntax does not define")
		}
		r, n, err := utf16Escape(raw[i+1:], false)
		if err != nil {
			return nil, err
		}
		out = utf8.AppendRune(out, r)
		i += n
	}
	return out, nil
}

// ShellSingleQuoted reports whether the range sp of text, in a shell file,
// starts inside a single-quoted string, where the shell does not expand a
// placeholder such as ${EXT_NAME}. The string is read from the line of the
// range, as Value reads it.
func ShellSingleQuoted(file string, text []byte, sp finding.Span) bool {
	if syntaxOf(file) != syntaxShell || sp.Start < 0 || sp.Start > len(text) {
		return false
	}
	line := text[bytes.LastIndexByte(text[:sp.Start], '\n')+1 : sp.Start]
	return shellQuoting(line) == inSingle
}
