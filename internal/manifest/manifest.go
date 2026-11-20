// Package manifest reads, merges and writes secrets-manifest.yml, the file at
// a project's root that lists which placeholders the tool wrote, where, and
// where each value is stored. It never holds a value.
//
// The file is a small fixed subset of YAML. Encode writes only that subset
// and Parse accepts only that subset, so that extending a manifest a person
// edited by hand never drops or reinterprets what they wrote: anything else
// is ErrUnsupported.
package manifest

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// FileName is the manifest's name in the project folder.
const FileName = "secrets-manifest.yml"

// MaxSize is the largest manifest Parse reads, in bytes.
const MaxSize = 1 << 20

// ErrUnsupported means the file is not a manifest this package wrote.
var ErrUnsupported = errors.New("manifest is not in the supported form")

// Action says what was done to the source where a placeholder is listed.
type Action string

// The actions an entry can have.
const (
	ActionExternalized Action = "externalized"
	ActionFileRemoved  Action = "file-removed"
)

// Location is one place a placeholder was written. Line is 0 for a file
// that was removed.
type Location struct {
	File string
	Line int
}

// Entry describes one placeholder. It has no field for a value.
type Entry struct {
	Name      string
	Category  finding.Category
	Action    Action
	Store     string
	Locations []Location
}

// Manifest is the content of a manifest file. CRLF records the line
// ending the file had, which Encode writes again.
type Manifest struct {
	Entries []Entry
	CRLF    bool
}

// The fixed lines and line prefixes of the format.
const (
	lineVersion     = "version: 1"
	lineEntries     = "entries:"
	lineNoEntries   = "entries: []"
	prefixName      = "  - name: "
	prefixCategory  = "    category: "
	prefixAction    = "    action: "
	prefixStore     = "    store: "
	lineLocations   = "    locations:"
	lineNoLocations = "    locations: []"
	prefixFile      = "      - file: "
	prefixLine      = "        line: "
	maxLineDigits   = 9
)

// plainScalar is the set of strings Encode writes without quotes, before the
// checks in plain.
var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// yamlWords are the plain scalars a YAML 1.1 or 1.2 reader resolves to a
// boolean or null rather than a string, compared case-insensitively.
var yamlWords = []string{"null", "true", "false", "yes", "no", "on", "off", "y", "n"}

// Parse reads a manifest. An empty file is a manifest with no entries. Any
// input that is not exactly the form Encode writes, apart from the final line
// ending being optional, fails with an error that wraps ErrUnsupported and
// names the line but not its content.
func Parse(data []byte) (Manifest, error) {
	if len(data) > MaxSize {
		return Manifest{}, fmt.Errorf("manifest is larger than %d bytes: %w", MaxSize, ErrUnsupported)
	}
	if len(data) == 0 {
		return Manifest{}, nil
	}
	if !utf8.Valid(data) {
		return Manifest{}, unsupported(invalidUTF8Line(data), "not UTF-8")
	}
	lines, crlf, err := splitLines(data)
	if err != nil {
		return Manifest{}, err
	}
	p := parser{lines: lines}
	entries, err := p.parse()
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{Entries: entries, CRLF: crlf}, nil
}

// unsupported returns an error naming the 1-based line and a fixed reason.
func unsupported(line int, reason string) error {
	return fmt.Errorf("line %d: %s: %w", line, reason, ErrUnsupported)
}

// invalidUTF8Line returns the line of the first byte that is not UTF-8.
func invalidUTF8Line(data []byte) int {
	i := 0
	for i < len(data) {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		i += size
	}
	return bytes.Count(data[:i], []byte("\n")) + 1
}

// splitLines splits data into lines. The first line ending decides whether
// the file uses CRLF; every line must then end the same way, and no carriage
// return may appear anywhere else.
func splitLines(data []byte) ([]string, bool, error) {
	text := string(data)
	first := strings.IndexByte(text, '\n')
	crlf := first > 0 && text[first-1] == '\r'
	sep := "\n"
	if crlf {
		sep = "\r\n"
	}
	lines := strings.Split(text, sep)
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		if strings.ContainsAny(l, "\r\n") {
			return nil, false, unsupported(i+1, "mixed line endings")
		}
	}
	return lines, crlf, nil
}

// parser walks the lines of a manifest in the one order Encode writes them.
type parser struct {
	lines []string
	next  int
}

// peek returns the next line without consuming it, and false at the end.
func (p *parser) peek() (string, bool) {
	if p.next >= len(p.lines) {
		return "", false
	}
	return p.lines[p.next], true
}

// lineNo is the 1-based number of the next line, which is one past the last
// line at the end of the input.
func (p *parser) lineNo() int {
	return p.next + 1
}

// fail reports the next line as the one that is not understood.
func (p *parser) fail(reason string) error {
	return unsupported(p.lineNo(), reason)
}

// exact consumes the next line if it is want.
func (p *parser) exact(want string) bool {
	l, ok := p.peek()
	if !ok || l != want {
		return false
	}
	p.next++
	return true
}

// field consumes the next line, which must start with prefix, and returns the
// scalar after it.
func (p *parser) field(prefix, reason string) (string, error) {
	l, ok := p.peek()
	if !ok || !strings.HasPrefix(l, prefix) {
		return "", p.fail(reason)
	}
	s, err := scalar(l[len(prefix):])
	if err != nil {
		return "", p.fail(err.Error())
	}
	p.next++
	return s, nil
}

func (p *parser) parse() ([]Entry, error) {
	if !p.exact(lineVersion) {
		return nil, p.fail("expected version 1")
	}
	if p.exact(lineNoEntries) {
		if _, more := p.peek(); more {
			return nil, p.fail("unexpected line after entries")
		}
		return nil, nil
	}
	if !p.exact(lineEntries) {
		return nil, p.fail("expected entries")
	}
	var entries []Entry
	seen := make(map[string]bool)
	for {
		if _, more := p.peek(); !more {
			break
		}
		start := p.lineNo()
		e, err := p.entry()
		if err != nil {
			return nil, err
		}
		if seen[e.Name] {
			return nil, unsupported(start, "name listed twice")
		}
		seen[e.Name] = true
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, p.fail("expected an entry")
	}
	return entries, nil
}

func (p *parser) entry() (Entry, error) {
	var e Entry
	var err error
	if e.Name, err = p.nonEmpty(prefixName, "expected name"); err != nil {
		return Entry{}, err
	}
	category, err := p.field(prefixCategory, "expected category")
	if err != nil {
		return Entry{}, err
	}
	if e.Category, err = parseCategory(category); err != nil {
		return Entry{}, unsupported(p.lineNo()-1, "unknown category")
	}
	action, err := p.field(prefixAction, "expected action")
	if err != nil {
		return Entry{}, err
	}
	if e.Action, err = parseAction(action); err != nil {
		return Entry{}, unsupported(p.lineNo()-1, "unknown action")
	}
	if e.Store, err = p.nonEmpty(prefixStore, "expected store"); err != nil {
		return Entry{}, err
	}
	if p.exact(lineNoLocations) {
		return e, p.endOfEntry()
	}
	if !p.exact(lineLocations) {
		return Entry{}, p.fail("expected locations")
	}
	for {
		l, ok := p.peek()
		if !ok || !strings.HasPrefix(l, prefixFile) {
			break
		}
		loc, err := p.location()
		if err != nil {
			return Entry{}, err
		}
		e.Locations = append(e.Locations, loc)
	}
	if len(e.Locations) == 0 {
		return Entry{}, p.fail("expected a location")
	}
	return e, p.endOfEntry()
}

// endOfEntry checks that the next line, if any, starts another entry.
func (p *parser) endOfEntry() error {
	l, ok := p.peek()
	if ok && !strings.HasPrefix(l, prefixName) {
		return p.fail("expected the next entry")
	}
	return nil
}

func (p *parser) location() (Location, error) {
	var loc Location
	var err error
	if loc.File, err = p.nonEmpty(prefixFile, "expected file"); err != nil {
		return Location{}, err
	}
	l, ok := p.peek()
	if !ok || !strings.HasPrefix(l, prefixLine) {
		return loc, nil
	}
	if loc.Line, err = parseLine(l[len(prefixLine):]); err != nil {
		return Location{}, p.fail("line is not a positive number")
	}
	p.next++
	return loc, nil
}

// nonEmpty is field for a scalar that must not be empty.
func (p *parser) nonEmpty(prefix, reason string) (string, error) {
	s, err := p.field(prefix, reason)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", unsupported(p.lineNo()-1, "empty value")
	}
	return s, nil
}

// parseCategory accepts the categories of a secret. Customer data never
// enters a manifest.
func parseCategory(s string) (finding.Category, error) {
	c, err := finding.ParseCategory(s)
	if err != nil {
		return "", err
	}
	if c == finding.CategoryCustomerData {
		return "", finding.ErrUnknown
	}
	return c, nil
}

func parseAction(s string) (Action, error) {
	switch a := Action(s); a {
	case ActionExternalized, ActionFileRemoved:
		return a, nil
	}
	return "", ErrUnsupported
}

// parseLine accepts a positive decimal number without leading zeros, the only
// form Encode writes.
func parseLine(s string) (int, error) {
	if s == "" || len(s) > maxLineDigits || s[0] == '0' {
		return 0, ErrUnsupported
	}
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, ErrUnsupported
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, ErrUnsupported
	}
	return n, nil
}

// scalar reads a value written by quote: a plain scalar or a double-quoted
// one that ends the line.
func scalar(s string) (string, error) {
	if !strings.HasPrefix(s, `"`) {
		if !plain(s) {
			return "", errors.New("value is not a plain scalar")
		}
		return s, nil
	}
	return unquote(s)
}

// plain reports whether Encode writes s without quotes. Such a string reads
// as the same string in any YAML reader: it holds no indicator, space or
// comment, does not start with "-", and is not a word or number that YAML
// resolves to another type.
func plain(s string) bool {
	if !plainScalar.MatchString(s) || strings.HasPrefix(s, "-") {
		return false
	}
	if c := s[0]; c == '.' || ('0' <= c && c <= '9') {
		return false
	}
	return !slices.ContainsFunc(yamlWords, func(w string) bool { return strings.EqualFold(w, s) })
}

// unquote decodes a double-quoted scalar that takes up the rest of the line.
// It accepts the JSON escapes and, unescaped, only the characters quote
// writes unescaped.
func unquote(s string) (string, error) {
	var b strings.Builder
	i := 1
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			if i+size != len(s) {
				return "", errors.New("text after the closing quote")
			}
			return b.String(), nil
		case r == '\\':
			r, n, err := unescape(s[i:])
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
			i += n
			continue
		case escaped(r):
			return "", errors.New("unescaped control character")
		}
		b.WriteRune(r)
		i += size
	}
	return "", errors.New("missing closing quote")
}

// unescape decodes the escape at the start of s, which begins with a
// backslash, and returns the rune and the number of bytes it took.
func unescape(s string) (rune, int, error) {
	if len(s) < 2 {
		return 0, 0, errors.New("unknown escape")
	}
	switch s[1] {
	case '"':
		return '"', 2, nil
	case '\\':
		return '\\', 2, nil
	case '/':
		return '/', 2, nil
	case 'b':
		return '\b', 2, nil
	case 'f':
		return '\f', 2, nil
	case 'n':
		return '\n', 2, nil
	case 'r':
		return '\r', 2, nil
	case 't':
		return '\t', 2, nil
	case 'u':
	default:
		return 0, 0, errors.New("unknown escape")
	}
	r, ok := hex4(s[2:])
	if !ok {
		return 0, 0, errors.New("unknown escape")
	}
	if !utf16.IsSurrogate(r) {
		return r, 6, nil
	}
	if len(s) < 12 || s[6:8] != `\u` {
		return 0, 0, errors.New("unpaired surrogate")
	}
	low, ok := hex4(s[8:])
	if !ok {
		return 0, 0, errors.New("unpaired surrogate")
	}
	pair := utf16.DecodeRune(r, low)
	if pair == unicode.ReplacementChar {
		return 0, 0, errors.New("unpaired surrogate")
	}
	return pair, 12, nil
}

// hex4 decodes four hexadecimal digits at the start of s.
func hex4(s string) (rune, bool) {
	if len(s) < 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(s[:4], 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(n), true
}

// escaped reports whether quote writes r as an escape: every character YAML
// does not allow unescaped in a double-quoted scalar, and every character a
// reader could not see, such as a line separator or a direction override.
func escaped(r rune) bool {
	return r == '"' || r == '\\' || r == utf8.RuneError || !unicode.IsPrint(r)
}

// quote returns s as Encode writes it: plain when plain allows it, and
// otherwise double-quoted with JSON escapes. Bytes that are not UTF-8 are
// written as U+FFFD, so the result is always one line of valid UTF-8.
func quote(s string) string {
	if plain(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if !escaped(r) {
			b.WriteRune(r)
			continue
		}
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r1, r2 := utf16.EncodeRune(r); r1 != unicode.ReplacementChar {
				fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
				continue
			}
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Encode writes m in the manifest format, with the line ending m.CRLF names
// and one line ending at the end. It writes entries and locations in the
// order m holds them.
func (m Manifest) Encode() []byte {
	eol := "\n"
	if m.CRLF {
		eol = "\r\n"
	}
	var b strings.Builder
	line := func(parts ...string) {
		for _, s := range parts {
			b.WriteString(s)
		}
		b.WriteString(eol)
	}
	line(lineVersion)
	if len(m.Entries) == 0 {
		line(lineNoEntries)
		return []byte(b.String())
	}
	line(lineEntries)
	for _, e := range m.Entries {
		line(prefixName, quote(e.Name))
		line(prefixCategory, quote(string(e.Category)))
		line(prefixAction, quote(string(e.Action)))
		line(prefixStore, quote(e.Store))
		if len(e.Locations) == 0 {
			line(lineNoLocations)
			continue
		}
		line(lineLocations)
		for _, loc := range e.Locations {
			line(prefixFile, quote(loc.File))
			if loc.Line != 0 {
				line(prefixLine, strconv.Itoa(loc.Line))
			}
		}
	}
	return []byte(b.String())
}

// Merge returns m with entries added: a new name is inserted in name
// order, and a known name takes the new category, action and store and the
// union of the locations.
//
// A new name goes before the first entry whose name sorts after it, so the
// order of a hand-edited file is kept. The locations of every entry Merge
// touches are sorted by file and line with duplicates removed; entries it
// does not touch are returned unchanged. Merging the same entries again
// gives the same result. Merge copies what it keeps and changes neither m
// nor entries.
func (m Manifest) Merge(entries []Entry) Manifest {
	out := Manifest{CRLF: m.CRLF}
	for _, e := range m.Entries {
		out.Entries = append(out.Entries, cloneEntry(e))
	}
	for _, e := range entries {
		i := slices.IndexFunc(out.Entries, func(o Entry) bool { return o.Name == e.Name })
		if i >= 0 {
			known := out.Entries[i]
			e.Locations = append(known.Locations, e.Locations...)
			out.Entries[i] = withLocations(e)
			continue
		}
		at := slices.IndexFunc(out.Entries, func(o Entry) bool { return o.Name > e.Name })
		if at < 0 {
			at = len(out.Entries)
		}
		out.Entries = slices.Insert(out.Entries, at, withLocations(e))
	}
	return out
}

// cloneEntry returns e with its own copy of the locations.
func cloneEntry(e Entry) Entry {
	e.Locations = slices.Clone(e.Locations)
	return e
}

// withLocations returns e with a new, sorted and deduplicated copy of its
// locations, nil when there are none.
func withLocations(e Entry) Entry {
	locs := slices.Clone(e.Locations)
	slices.SortFunc(locs, func(a, b Location) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	locs = slices.Compact(locs)
	if len(locs) == 0 {
		locs = nil
	}
	e.Locations = locs
	return e
}
