// Package manifest reads, merges and writes secrets-manifest.yml, the file at
// a project's root that lists which placeholders the tool wrote, where, and
// where each value is stored. It never holds a value.
//
// The file is a small fixed subset of YAML. Encode writes only that subset
// and Parse accepts only that subset, so that extending a manifest a person
// edited by hand never drops or reinterprets what they wrote: anything else
// is ErrUnsupported. Encode refuses, with ErrInvalid, any manifest that Parse
// would not read back as the same manifest.
package manifest

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
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

// MaxSize is the largest manifest Parse reads and Encode writes, in bytes.
const MaxSize = 1 << 20

// MaxLine is the largest line number a location can have.
const MaxLine = 999_999_999

// ErrUnsupported means the file is not a manifest this package wrote.
var ErrUnsupported = errors.New("manifest is not in the supported form")

// ErrInvalid means a manifest holds something Encode cannot write in a form
// Parse reads back as the same manifest.
var ErrInvalid = errors.New("manifest cannot be written")

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
)

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
	s, ok := scalar(l[len(prefix):])
	if !ok {
		return "", p.fail("value is not in the supported form")
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
	e.Category = finding.Category(category)
	if !knownCategory(e.Category) {
		return Entry{}, unsupported(p.lineNo()-1, "unknown category")
	}
	action, err := p.field(prefixAction, "expected action")
	if err != nil {
		return Entry{}, err
	}
	e.Action = Action(action)
	if !knownAction(e.Action) {
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
	n, ok := parseLine(l[len(prefixLine):])
	if !ok {
		return Location{}, p.fail("line is not a positive number")
	}
	loc.Line = n
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

// knownCategory reports whether c is the category of a secret. Customer data
// never enters a manifest.
func knownCategory(c finding.Category) bool {
	switch c {
	case finding.CategoryPartnerSession, finding.CategoryStorage, finding.CategorySSH, finding.CategoryOther:
		return true
	case finding.CategoryCustomerData:
		return false
	}
	return false
}

// knownAction reports whether a is one of the actions an entry can have.
func knownAction(a Action) bool {
	return a == ActionExternalized || a == ActionFileRemoved
}

// parseLine reads a number from 1 to MaxLine written without leading zeros,
// the only form Encode writes.
func parseLine(s string) (int, bool) {
	if s == "" || len(s) > len(strconv.Itoa(MaxLine)) || s[0] == '0' {
		return 0, false
	}
	n := 0
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// scalar reads a value written by quote: a plain scalar or a double-quoted
// one that ends the line.
func scalar(s string) (string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return s, plain(s)
	}
	return unquote(s)
}

// plain reports whether Encode writes s without quotes: s is made only of
// ASCII letters, digits and "_./-", does not start with "-", "." or a digit,
// and is not a word YAML resolves to a boolean or null. Such a string reads as
// the same string in any YAML reader.
func plain(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		ok := ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			c == '_' || c == '.' || c == '/' || c == '-'
		if !ok {
			return false
		}
	}
	if c := s[0]; c == '-' || c == '.' || ('0' <= c && c <= '9') {
		return false
	}
	switch strings.ToLower(s) {
	case "null", "true", "false", "yes", "no", "on", "off", "y", "n":
		return false
	}
	return true
}

// unquote decodes a double-quoted scalar that takes up the rest of the line.
// It accepts the escapes quote writes and, unescaped, only the characters
// quote writes unescaped.
func unquote(s string) (string, bool) {
	var b strings.Builder
	i := 1
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			return b.String(), i+size == len(s)
		case r == '\\':
			r, n, ok := unescape(s[i:])
			if !ok {
				return "", false
			}
			b.WriteRune(r)
			i += n
			continue
		case escaped(r):
			return "", false
		}
		b.WriteRune(r)
		i += size
	}
	return "", false
}

// unescape decodes the escape at the start of s, which begins with a
// backslash, and returns the rune and the number of bytes it took. It accepts
// the escapes quote writes: \" \\ \n \r \t, \uXXXX for a character in the
// basic plane that is not a surrogate, and \UXXXXXXXX for a character above
// it.
func unescape(s string) (rune, int, bool) {
	if len(s) < 2 {
		return 0, 0, false
	}
	switch s[1] {
	case '"':
		return '"', 2, true
	case '\\':
		return '\\', 2, true
	case 'n':
		return '\n', 2, true
	case 'r':
		return '\r', 2, true
	case 't':
		return '\t', 2, true
	case 'u':
		r, ok := hexRune(s[2:], 4)
		if !ok || utf16.IsSurrogate(r) {
			return 0, 0, false
		}
		return r, 6, true
	case 'U':
		r, ok := hexRune(s[2:], 8)
		if !ok || r <= 0xFFFF || !utf8.ValidRune(r) {
			return 0, 0, false
		}
		return r, 10, true
	}
	return 0, 0, false
}

// hexRune decodes n hexadecimal digits at the start of s. It fails for a
// value above unicode.MaxRune.
func hexRune(s string, n int) (rune, bool) {
	if len(s) < n {
		return 0, false
	}
	var r rune
	for _, c := range []byte(s[:n]) {
		var d rune
		switch {
		case '0' <= c && c <= '9':
			d = rune(c - '0')
		case 'a' <= c && c <= 'f':
			d = rune(c-'a') + 10
		case 'A' <= c && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, false
		}
		r = r<<4 | d
		if r > unicode.MaxRune {
			return 0, false
		}
	}
	return r, true
}

// escaped reports whether quote writes r as an escape: every character YAML
// does not allow unescaped in a double-quoted scalar, and every character a
// reader could not see, such as a line separator or a direction override.
func escaped(r rune) bool {
	return r == '"' || r == '\\' || r == utf8.RuneError || !unicode.IsPrint(r)
}

// quote returns s as Encode writes it: plain when plain allows it, and
// otherwise double-quoted with YAML escapes. s must be valid UTF-8, which
// Encode checks.
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
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r > 0xFFFF:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Encode writes m in the manifest format, with the line ending m.CRLF names
// and one line ending at the end. It writes entries and locations in the
// order m holds them.
//
// Encode fails with an error wrapping ErrInvalid, naming the entry and field
// but not the value, when Parse would not read the result back as m: an
// empty name, store or file; a string that is not UTF-8; an unknown action or
// category, or customer data; a line below 0 or above MaxLine; a name listed
// twice; or a result larger than MaxSize. Empty and nil slices read back
// as nil.
func (m Manifest) Encode() ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
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
		return []byte(b.String()), nil
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
	if b.Len() > MaxSize {
		return nil, fmt.Errorf("manifest is larger than %d bytes: %w", MaxSize, ErrInvalid)
	}
	return []byte(b.String()), nil
}

// validate checks every field Parse checks, so that Encode never writes a
// file Parse rejects.
func (m Manifest) validate() error {
	seen := make(map[string]bool, len(m.Entries))
	for i, e := range m.Entries {
		if err := text(e.Name); err != "" {
			return invalid(i, "name", err)
		}
		if seen[e.Name] {
			return invalid(i, "name", "listed twice")
		}
		seen[e.Name] = true
		if !knownCategory(e.Category) {
			return invalid(i, "category", "unknown")
		}
		if !knownAction(e.Action) {
			return invalid(i, "action", "unknown")
		}
		if err := text(e.Store); err != "" {
			return invalid(i, "store", err)
		}
		for j, loc := range e.Locations {
			if err := text(loc.File); err != "" {
				return fmt.Errorf("entry %d location %d file: %s: %w", i, j, err, ErrInvalid)
			}
			if loc.Line < 0 || loc.Line > MaxLine {
				return fmt.Errorf("entry %d location %d line: out of range: %w", i, j, ErrInvalid)
			}
		}
	}
	return nil
}

// text returns why s cannot be written as a name, store or file, or "".
func text(s string) string {
	switch {
	case s == "":
		return "empty"
	case !utf8.ValidString(s):
		return "not UTF-8"
	}
	return ""
}

// invalid returns an error naming the entry and field but not the value.
func invalid(entry int, field, reason string) error {
	return fmt.Errorf("entry %d %s: %s: %w", entry, field, reason, ErrInvalid)
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
// nor entries. It does not check the entries; Encode does.
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
