package manifest_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/manifest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

const store = "openbao:secret/data/projects/7/"

// specManifest is the design's example with the categories and names the
// code uses.
func specManifest() manifest.Manifest {
	return manifest.Manifest{Entries: []manifest.Entry{
		{
			Name:      "EXT_DB_MAIN_PASSWORD",
			Category:  finding.CategoryStorage,
			Action:    manifest.ActionExternalized,
			Store:     store + "ext.db.main.password",
			Locations: []manifest.Location{{File: "deploy/backup.sh", Line: 12}},
		},
		{
			Name:     "ext.db.main.password",
			Category: finding.CategoryStorage,
			Action:   manifest.ActionExternalized,
			Store:    store + "ext.db.main.password",
			Locations: []manifest.Location{
				{File: "src/main/resources/application-prod.yml", Line: 14},
				{File: "src/main/webapp/WEB-INF/spring/root-context.xml", Line: 31},
			},
		},
		{
			Name:      "ext.deploy.ssh.key",
			Category:  finding.CategorySSH,
			Action:    manifest.ActionFileRemoved,
			Store:     store + "ext.deploy.ssh.key",
			Locations: []manifest.Location{{File: "deploy/id_rsa"}},
		},
	}}
}

func readSpec(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "spec.yml"))
	if err != nil {
		t.Fatalf("reading spec example: %v", err)
	}
	return data
}

// encode is Encode for a manifest the test expects to be writable.
func encode(t testing.TB, m manifest.Manifest) []byte {
	t.Helper()
	data, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return data
}

func TestEncode_SpecExample(t *testing.T) {
	want := readSpec(t)
	got := encode(t, specManifest())
	if !bytes.Equal(got, want) {
		t.Errorf("Encode() differs from testdata/spec.yml (-want +got):\n%s", cmp.Diff(string(want), string(got)))
	}
}

func TestParse_RoundTrip(t *testing.T) {
	spec := readSpec(t)
	crlf := bytes.ReplaceAll(spec, []byte("\n"), []byte("\r\n"))
	tests := []struct {
		name string
		data []byte
		want manifest.Manifest
	}{
		{name: "spec", data: spec, want: specManifest()},
		{name: "crlf", data: crlf, want: func() manifest.Manifest {
			m := specManifest()
			m.CRLF = true
			return m
		}()},
		{name: "empty file", data: nil, want: manifest.Manifest{}},
		{name: "no entries", data: []byte("version: 1\nentries: []\n"), want: manifest.Manifest{}},
		{name: "no final newline", data: []byte("version: 1\nentries: []"), want: manifest.Manifest{}},
		{name: "no locations", data: []byte("version: 1\nentries:\n  - name: a\n    category: other-secret\n    action: externalized\n    store: s\n    locations: []\n"), want: manifest.Manifest{Entries: []manifest.Entry{
			{Name: "a", Category: finding.CategoryOther, Action: manifest.ActionExternalized, Store: "s"},
		}}},
		{name: "escapes", data: []byte("version: 1\nentries:\n  - name: \"a\\u00e9\\n\\U0001f600\"\n    category: other-secret\n    action: externalized\n    store: \"\\U000e0001\"\n    locations: []\n"), want: manifest.Manifest{Entries: []manifest.Entry{
			{Name: "a\u00e9\n\U0001F600", Category: finding.CategoryOther, Action: manifest.ActionExternalized, Store: "\U000E0001"},
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := manifest.Parse(tt.data)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.data, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Parse(%q) mismatch (-want +got):\n%s", tt.data, diff)
			}
			again, err := manifest.Parse(encode(t, got))
			if err != nil {
				t.Fatalf("Parse(Encode()) error = %v", err)
			}
			if diff := cmp.Diff(got, again); diff != "" {
				t.Errorf("Parse(Encode()) mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("crlf is written back", func(t *testing.T) {
		m, err := manifest.Parse(crlf)
		if err != nil {
			t.Fatalf("Parse(crlf) error = %v", err)
		}
		if got := encode(t, m); !bytes.Equal(got, crlf) {
			t.Errorf("Encode() of a CRLF manifest = %q, want %q", got, crlf)
		}
	})
	t.Run("empty manifest is written", func(t *testing.T) {
		got := string(encode(t, manifest.Manifest{}))
		if want := "version: 1\nentries: []\n"; got != want {
			t.Errorf("Manifest{}.Encode() = %q, want %q", got, want)
		}
	})
}

func TestParse_Rejects(t *testing.T) {
	// Lines 1-2 are head, 3-6 the entry's name, category, action and store,
	// and 7-9 its locations, file and line.
	const head = "version: 1\nentries:\n"
	const entry = "  - name: ext.a\n    category: other-secret\n    action: externalized\n    store: s\n"
	const locs = "    locations:\n      - file: a.yml\n        line: 3\n"
	valid := head + entry + locs
	if _, err := manifest.Parse([]byte(valid)); err != nil {
		t.Fatalf("Parse(%q) error = %v, the base case must parse", valid, err)
	}
	store := func(v string) string { return head + strings.Replace(entry, "store: s", "store: "+v, 1) + locs }
	file := func(v string) string { return head + entry + strings.Replace(locs, "file: a.yml", "file: "+v, 1) }
	line := func(v string) string { return head + entry + strings.Replace(locs, "line: 3", "line: "+v, 1) }
	tests := []struct {
		name string
		data string
		line int // 0 for an error that names no line
	}{
		{"version 2", "version: 2\nentries: []\n", 1},
		{"version quoted", "version: \"1\"\nentries: []\n", 1},
		{"no version", "entries: []\n", 1},
		{"no entries key", "version: 1\n", 2},
		{"entries without items", "version: 1\nentries:\n", 3},
		{"unknown top-level key", "version: 1\nowner: me\nentries: []\n", 2},
		{"top-level key after entries", "version: 1\nentries: []\nowner: me\n", 3},
		{"unknown entry key", head + entry + "    owner: me\n" + locs, 7},
		{"unknown location key", head + entry + locs + "        owner: me\n", 10},
		{"comment line", "# secrets\n" + valid, 1},
		{"comment between entries", head + entry + locs + "  # more\n" + strings.Replace(entry, "ext.a", "ext.b", 1) + locs, 10},
		{"end of line comment", store("s # where"), 6},
		{"end of line comment after quotes", store("\"s\" # where"), 6},
		{"tab indentation", head + strings.Replace(entry, "    category", "\tcategory", 1) + locs, 4},
		{"tab after colon", head + strings.Replace(entry, "store: s", "store:\ts", 1) + locs, 6},
		{"other indentation", head + strings.ReplaceAll(entry, "    ", "     ") + locs, 4},
		{"item indentation", "version: 1\nentries:\n- name: ext.a\n  category: other-secret\n  action: externalized\n  store: s\n  locations: []\n", 3},
		{"two spaces after colon", store(" s"), 6},
		{"duplicate name", valid + entry + locs, 10},
		{"no name", head + strings.Replace(entry, "  - name: ext.a\n    category", "  - category", 1) + locs, 3},
		{"empty name", head + strings.Replace(entry, "name: ext.a", "name: \"\"", 1) + locs, 3},
		{"no store", head + strings.Replace(entry, "    store: s\n", "", 1) + locs, 6},
		{"empty store", store("\"\""), 6},
		{"no locations", head + entry, 7},
		{"repeated key", head + entry + "    store: t\n" + locs, 7},
		{"unknown action", head + strings.Replace(entry, "externalized", "rotated", 1) + locs, 5},
		{"unknown category", head + strings.Replace(entry, "other-secret", "ssh-key-file", 1) + locs, 4},
		{"customer data", head + strings.Replace(entry, "other-secret", "customer-data", 1) + locs, 4},
		{"line not a number", line("three"), 9},
		{"line zero", line("0"), 9},
		{"line negative", line("-3"), 9},
		{"line leading zero", line("03"), 9},
		{"line quoted", line("\"3\""), 9},
		{"line too large", line("1000000000"), 9},
		{"line far too large", line("99999999999999999999999"), 9},
		{"empty file name", file("\"\""), 8},
		{"anchor", store("&st s"), 6},
		{"alias", store("*st"), 6},
		{"anchor on item", head + strings.Replace(entry, "  - name", "  - &e name", 1) + locs, 3},
		{"tag", store("!!str s"), 6},
		{"flow mapping", "version: 1\nentries: {}\n", 2},
		{"flow sequence", "version: 1\nentries: [a]\n", 2},
		{"flow locations", head + entry + "    locations: [{file: a.yml}]\n", 7},
		{"document start", "---\n" + valid, 1},
		{"second document", valid + "---\n" + valid, 10},
		{"document end", valid + "...\n", 10},
		{"block scalar literal", store("|\n      s"), 6},
		{"block scalar folded", store(">\n      s"), 6},
		{"single quotes", store("'s'"), 6},
		{"plain null", store("null"), 6},
		{"plain bool", file("true"), 8},
		{"plain number", file("1.5"), 8},
		{"plain leading dash", file("-a"), 8},
		{"unterminated quote", store("\"s"), 6},
		{"unknown escape", store("\"s\\q\""), 6},
		{"slash escape", store("\"s\\/\""), 6},
		{"lone surrogate", store("\"\\ud800\""), 6},
		{"surrogate pair", store("\"\\ud83d\\ude00\""), 6},
		{"short unicode escape", store("\"\\u00e\""), 6},
		{"long escape below the astral planes", store("\"\\U000000e9\""), 6},
		{"long escape beyond unicode", store("\"\\U00110000\""), 6},
		{"raw control in quotes", store("\"s\x01\""), 6},
		{"blank line", head + entry + "\n" + locs, 7},
		{"trailing space", store("s "), 6},
		{"bare carriage return", head + strings.Replace(entry, "store: s\n", "store: s\r", 1) + locs, 6},
		{"mixed line endings", strings.Replace(valid, "\n", "\r\n", 1), 2},
		{"byte order mark", "\ufeff" + valid, 1},
		{"not utf-8", file("\"a\xff.yml\""), 8},
		{"over 1 MiB", valid + "#" + strings.Repeat("x", manifest.MaxSize), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := manifest.Parse([]byte(tt.data))
			if !errors.Is(err, manifest.ErrUnsupported) {
				t.Fatalf("Parse(%.80q) = %+v, %v, want %v", tt.data, m, err, manifest.ErrUnsupported)
			}
			msg := err.Error()
			if tt.line == 0 {
				if strings.Contains(msg, "line ") {
					t.Errorf("Parse(%.80q) error = %q, want no line number", tt.data, msg)
				}
				return
			}
			if want := fmt.Sprintf("line %d: ", tt.line); !strings.HasPrefix(msg, want) {
				t.Errorf("Parse(%.80q) error = %q, want prefix %q", tt.data, msg, want)
			}
		})
	}

	t.Run("error names the line only", func(t *testing.T) {
		data := file("dev-only-fake-marker # x")
		_, err := manifest.Parse([]byte(data))
		if !errors.Is(err, manifest.ErrUnsupported) {
			t.Fatalf("Parse(%q) error = %v, want %v", data, err, manifest.ErrUnsupported)
		}
		if msg := err.Error(); !strings.Contains(msg, "line 8") || strings.Contains(msg, "dev-only-fake-marker") {
			t.Errorf("Parse(%q) error = %q, want line 8 and no line content", data, msg)
		}
	})
	t.Run("exactly 1 MiB is read", func(t *testing.T) {
		pad := manifest.MaxSize - len(valid)
		data := []byte(store("s" + strings.Repeat("s", pad)))
		if len(data) != manifest.MaxSize {
			t.Fatalf("test input is %d bytes, want %d", len(data), manifest.MaxSize)
		}
		if _, err := manifest.Parse(data); err != nil {
			t.Errorf("Parse(1 MiB manifest) error = %v, want nil", err)
		}
	})
	t.Run("largest line is read", func(t *testing.T) {
		m, err := manifest.Parse([]byte(line("999999999")))
		if err != nil {
			t.Fatalf("Parse(line 999999999) error = %v", err)
		}
		if got := m.Entries[0].Locations[0].Line; got != manifest.MaxLine {
			t.Errorf("Parse(line 999999999) line = %d, want %d", got, manifest.MaxLine)
		}
	})
}

func entry(name string, locs ...manifest.Location) manifest.Entry {
	return manifest.Entry{
		Name:      name,
		Category:  finding.CategoryStorage,
		Action:    manifest.ActionExternalized,
		Store:     store + name,
		Locations: locs,
	}
}

func TestEncode_Refuses(t *testing.T) {
	one := func(change func(e *manifest.Entry)) manifest.Manifest {
		e := entry("ext.a", manifest.Location{File: "a.yml", Line: 1})
		change(&e)
		return manifest.Manifest{Entries: []manifest.Entry{entry("ext.0"), e}}
	}
	tests := []struct {
		name  string
		m     manifest.Manifest
		field string
	}{
		{"empty name", one(func(e *manifest.Entry) { e.Name = "" }), "entry 1 name: empty"},
		{"empty store", one(func(e *manifest.Entry) { e.Store = "" }), "entry 1 store: empty"},
		{"empty file", one(func(e *manifest.Entry) { e.Locations[0].File = "" }), "entry 1 location 0 file: empty"},
		{"name not utf-8", one(func(e *manifest.Entry) { e.Name = "ext.\xff" }), "entry 1 name: not UTF-8"},
		{"store not utf-8", one(func(e *manifest.Entry) { e.Store = "s\xff" }), "entry 1 store: not UTF-8"},
		{"file not utf-8", one(func(e *manifest.Entry) { e.Locations[0].File = "a\xff\n- file: b" }), "entry 1 location 0 file: not UTF-8"},
		{"unknown action", one(func(e *manifest.Entry) { e.Action = "rotated" }), "entry 1 action: unknown"},
		{"empty action", one(func(e *manifest.Entry) { e.Action = "" }), "entry 1 action: unknown"},
		{"unknown category", one(func(e *manifest.Entry) { e.Category = "ssh-key-file" }), "entry 1 category: unknown"},
		{"customer data", one(func(e *manifest.Entry) { e.Category = finding.CategoryCustomerData }), "entry 1 category: unknown"},
		{"negative line", one(func(e *manifest.Entry) { e.Locations[0].Line = -1 }), "entry 1 location 0 line: out of range"},
		{"line above the largest", one(func(e *manifest.Entry) { e.Locations[0].Line = manifest.MaxLine + 1 }), "entry 1 location 0 line: out of range"},
		{"name listed twice", one(func(e *manifest.Entry) { e.Name = "ext.0" }), "entry 1 name: listed twice"},
		{"names that differ only in invalid bytes", manifest.Manifest{Entries: []manifest.Entry{entry("ext.\xfe"), entry("ext.\xff")}}, "entry 0 name: not UTF-8"},
		{"larger than 1 MiB", one(func(e *manifest.Entry) { e.Store = strings.Repeat("s", manifest.MaxSize) }), "manifest is larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.m.Encode()
			if !errors.Is(err, manifest.ErrInvalid) {
				t.Fatalf("Encode() = %d bytes, %v, want %v", len(data), err, manifest.ErrInvalid)
			}
			if !strings.HasPrefix(err.Error(), tt.field) {
				t.Errorf("Encode() error = %q, want prefix %q", err, tt.field)
			}
			if data != nil {
				t.Errorf("Encode() = %d bytes with an error, want none", len(data))
			}
		})
	}

	t.Run("error holds no value", func(t *testing.T) {
		m := one(func(e *manifest.Entry) { e.Store = "dev-only-fake-marker\xff" })
		_, err := m.Encode()
		if err == nil || strings.Contains(err.Error(), "dev-only-fake-marker") {
			t.Errorf("Encode() error = %v, want an error without the store", err)
		}
	})
	t.Run("largest line is written", func(t *testing.T) {
		m := one(func(e *manifest.Entry) { e.Locations[0].Line = manifest.MaxLine })
		got, err := manifest.Parse(encode(t, m))
		if err != nil {
			t.Fatalf("Parse(Encode()) error = %v", err)
		}
		if diff := cmp.Diff(m, got); diff != "" {
			t.Errorf("Parse(Encode()) mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestMerge(t *testing.T) {
	base := manifest.Manifest{Entries: []manifest.Entry{
		entry("ext.b", manifest.Location{File: "b.yml", Line: 5}, manifest.Location{File: "c.yml", Line: 1}),
		entry("ext.d", manifest.Location{File: "d.yml", Line: 2}),
	}, CRLF: true}

	tests := []struct {
		name  string
		merge []manifest.Entry
		want  manifest.Manifest
	}{
		{
			name:  "new names go in name order",
			merge: []manifest.Entry{entry("ext.e", manifest.Location{File: "e.yml", Line: 1}), entry("ext.a"), entry("ext.c", manifest.Location{File: "c.yml", Line: 9})},
			want: manifest.Manifest{Entries: []manifest.Entry{
				entry("ext.a"),
				base.Entries[0],
				entry("ext.c", manifest.Location{File: "c.yml", Line: 9}),
				base.Entries[1],
				entry("ext.e", manifest.Location{File: "e.yml", Line: 1}),
			}, CRLF: true},
		},
		{
			name: "known name joins locations and takes the new fields",
			merge: []manifest.Entry{{
				Name:     "ext.b",
				Category: finding.CategoryOther,
				Action:   manifest.ActionFileRemoved,
				Store:    "openbao:elsewhere",
				Locations: []manifest.Location{
					{File: "c.yml", Line: 1},
					{File: "a.yml", Line: 7},
					{File: "b.yml", Line: 2},
					{File: "a.yml", Line: 7},
				},
			}},
			want: manifest.Manifest{Entries: []manifest.Entry{
				{
					Name:     "ext.b",
					Category: finding.CategoryOther,
					Action:   manifest.ActionFileRemoved,
					Store:    "openbao:elsewhere",
					Locations: []manifest.Location{
						{File: "a.yml", Line: 7},
						{File: "b.yml", Line: 2},
						{File: "b.yml", Line: 5},
						{File: "c.yml", Line: 1},
					},
				},
				base.Entries[1],
			}, CRLF: true},
		},
		{
			name:  "nothing to merge",
			merge: nil,
			want:  base,
		},
		{
			name:  "same name twice in one call",
			merge: []manifest.Entry{entry("ext.a", manifest.Location{File: "x", Line: 2}), entry("ext.a", manifest.Location{File: "x", Line: 1})},
			want: manifest.Manifest{Entries: []manifest.Entry{
				entry("ext.a", manifest.Location{File: "x", Line: 1}, manifest.Location{File: "x", Line: 2}),
				base.Entries[0],
				base.Entries[1],
			}, CRLF: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneManifest(base)
			got := base.Merge(tt.merge)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Merge(%v) mismatch (-want +got):\n%s", tt.merge, diff)
			}
			if diff := cmp.Diff(before, base); diff != "" {
				t.Errorf("Merge(%v) changed its receiver (-before +after):\n%s", tt.merge, diff)
			}
			if diff := cmp.Diff(got, got.Merge(tt.merge)); diff != "" {
				t.Errorf("Merge(%v) twice differs from once (-once +twice):\n%s", tt.merge, diff)
			}
		})
	}

	t.Run("hand-ordered entries keep their order", func(t *testing.T) {
		m := manifest.Manifest{Entries: []manifest.Entry{entry("ext.z"), entry("ext.b")}}
		got := m.Merge([]manifest.Entry{entry("ext.c")})
		want := manifest.Manifest{Entries: []manifest.Entry{entry("ext.c"), entry("ext.z"), entry("ext.b")}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Merge(ext.c) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("result does not share the caller's slices", func(t *testing.T) {
		in := []manifest.Entry{entry("ext.a", manifest.Location{File: "a", Line: 1})}
		got := manifest.Manifest{}.Merge(in)
		in[0].Locations[0].Line = 99
		if got.Entries[0].Locations[0].Line != 1 {
			t.Errorf("Merge kept the caller's location slice")
		}
	})
}

func TestRelocate(t *testing.T) {
	base := manifest.Manifest{Entries: []manifest.Entry{
		entry("ext.a", manifest.Location{File: "a.yml", Line: 2}, manifest.Location{File: "b.yml", Line: 9}),
		entry("ext.b", manifest.Location{File: "a.yml", Line: 0}),
		entry("ext.c", manifest.Location{File: "a.yml", Line: 5}),
		entry("ext.a.x", manifest.Location{File: "c.yml", Line: 1}),
	}, CRLF: true}

	tests := []struct {
		name    string
		file    string
		content string
		want    manifest.Manifest
	}{
		{
			name:    "moved placeholders get their current lines",
			file:    "a.yml",
			content: "x: 1\r\ny: 2\r\na: ${ext.a}\r\n\r\nc: \"${ext.c}\"\r\n",
			want: manifest.Manifest{Entries: []manifest.Entry{
				entry("ext.a", manifest.Location{File: "a.yml", Line: 3}, manifest.Location{File: "b.yml", Line: 9}),
				base.Entries[1],
				entry("ext.c", manifest.Location{File: "a.yml", Line: 5}),
				base.Entries[3],
			}, CRLF: true},
		},
		{
			name:    "every line that holds the placeholder is listed once",
			file:    "a.yml",
			content: "a: ${ext.a} ${ext.a}\nb: x\nc: ${ext.c}\nd: '\"${ext.a}\"'\n",
			want: manifest.Manifest{Entries: []manifest.Entry{
				entry("ext.a", manifest.Location{File: "a.yml", Line: 1}, manifest.Location{File: "a.yml", Line: 4}, manifest.Location{File: "b.yml", Line: 9}),
				base.Entries[1],
				entry("ext.c", manifest.Location{File: "a.yml", Line: 3}),
				base.Entries[3],
			}, CRLF: true},
		},
		{
			name:    "a placeholder no longer found keeps its line",
			file:    "a.yml",
			content: "a: ${ext.a.x}\nb: ${ext.a\n}\n",
			want:    base,
		},
		{
			name:    "an entry with no location in the file is not added to it",
			file:    "c.yml",
			content: "a: ${ext.a}\nb: x\nc: ${ext.a.x}\n",
			want: manifest.Manifest{Entries: []manifest.Entry{
				base.Entries[0],
				base.Entries[1],
				base.Entries[2],
				entry("ext.a.x", manifest.Location{File: "c.yml", Line: 3}),
			}, CRLF: true},
		},
		{
			name:    "a removed file keeps line 0",
			file:    "a.yml",
			content: "b: ${ext.b}\n",
			want:    base,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneManifest(base)
			got := base.Relocate(tt.file, []byte(tt.content))
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Relocate(%q, %q) mismatch (-want +got):\n%s", tt.file, tt.content, diff)
			}
			if diff := cmp.Diff(before, base); diff != "" {
				t.Errorf("Relocate(%q, %q) changed its receiver (-before +after):\n%s", tt.file, tt.content, diff)
			}
		})
	}
}

func cloneManifest(m manifest.Manifest) manifest.Manifest {
	out := manifest.Manifest{CRLF: m.CRLF}
	for _, e := range m.Entries {
		e.Locations = append([]manifest.Location(nil), e.Locations...)
		out.Entries = append(out.Entries, e)
	}
	return out
}

func TestEncode_QuotesScalars(t *testing.T) {
	files := []string{
		"src/my file.yml",
		"src/#hash.yml",
		"src/a: b.yml",
		"src/설정.yml",
		"-leading.yml",
		"*star.yml",
		"&amp.yml",
		"!bang.yml",
		"say \"hi\".yml",
		"back\\slash.yml",
		"line\nbreak.yml",
		"carriage\rreturn.yml",
		"tab\there.yml",
		"nul\x00.yml",
		"del\x7f.yml",
		"c1\u0085\u009b.yml",
		"rtl\u202eoverride.yml",
		"bom\ufeff.yml",
		"sep\u2028.yml",
		"replacement\ufffd.yml",
		"emoji\U0001F600.yml",
		"\U000E0001tag.yml",
		"private\U0010FFFD.yml",
		"'single'.yml",
		"{flow}.yml",
		"[seq].yml",
		"| block",
		"> folded",
		"%directive",
		"@at",
		"`tick",
		"null",
		"True",
		"no",
		"~",
		"123",
		"0x1F",
		"2026-11-20",
		".inf",
		"-",
		"---",
		"...",
		" leading space",
		"trailing space ",
		"\n      - file: injected.yml\n        line: 1",
		"x\n  - name: injected\n    category: other-secret",
	}
	var locs []manifest.Location
	for i, f := range files {
		locs = append(locs, manifest.Location{File: f, Line: i + 1})
	}
	m := manifest.Manifest{Entries: []manifest.Entry{{
		Name:      "ext.a: b # c",
		Category:  finding.CategoryOther,
		Action:    manifest.ActionExternalized,
		Store:     "openbao:secret/data/projects/7/\"x\"\n",
		Locations: locs,
	}}}
	data := encode(t, m)
	got, err := manifest.Parse(data)
	if err != nil {
		t.Fatalf("Parse(Encode()) error = %v\n%s", err, data)
	}
	if diff := cmp.Diff(m, got); diff != "" {
		t.Errorf("Parse(Encode()) mismatch (-want +got):\n%s", diff)
	}
	if n := bytes.Count(data, []byte("\n")); n != 7+2*len(files) {
		t.Errorf("Encode() wrote %d lines, want %d:\n%s", n, 7+2*len(files), data)
	}
	for _, want := range []string{`\U000e0001`, `\U0010fffd`, `\u202e`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("Encode() does not contain the escape %s:\n%s", want, data)
		}
	}
}

func TestEncode_HoldsNoValue(t *testing.T) {
	fieldNames := func(v any) []string {
		var names []string
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			names = append(names, typ.Field(i).Name)
		}
		return names
	}
	if diff := cmp.Diff([]string{"Name", "Category", "Action", "Store", "Locations"}, fieldNames(manifest.Entry{})); diff != "" {
		t.Errorf("Entry fields mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"File", "Line"}, fieldNames(manifest.Location{})); diff != "" {
		t.Errorf("Location fields mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"Entries", "CRLF"}, fieldNames(manifest.Manifest{})); diff != "" {
		t.Errorf("Manifest fields mismatch (-want +got):\n%s", diff)
	}
}

func FuzzParse(f *testing.F) {
	spec := readSpec(f)
	f.Add(spec)
	f.Add(bytes.ReplaceAll(spec, []byte("\n"), []byte("\r\n")))
	f.Add([]byte("version: 1\nentries: []\n"))
	f.Add([]byte("version: 1\nentries:\n  - name: \"a\\u00e9\\n\"\n    category: other-secret\n    action: externalized\n    store: \"\\U0001f600\"\n    locations: []\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := manifest.Parse(data)
		if err != nil {
			return
		}
		out, err := m.Encode()
		if err != nil {
			t.Fatalf("Parse(%q) succeeded but Encode() error = %v", data, err)
		}
		again, err := manifest.Parse(out)
		if err != nil {
			t.Fatalf("Parse(%q) succeeded but Parse(Encode()) error = %v\n%s", data, err, out)
		}
		if diff := cmp.Diff(m, again); diff != "" {
			t.Errorf("Parse(Encode(Parse(%q))) mismatch (-want +got):\n%s", data, diff)
		}
	})
}

// generator builds a manifest from fuzz input bytes. It returns zero values
// once the input runs out.
type generator struct {
	b []byte
}

func (g *generator) byte() byte {
	if len(g.b) == 0 {
		return 0
	}
	c := g.b[0]
	g.b = g.b[1:]
	return c
}

func (g *generator) str() string {
	n := min(int(g.byte()%32), len(g.b))
	s := string(g.b[:n])
	g.b = g.b[n:]
	return s
}

func (g *generator) line() int {
	switch g.byte() % 4 {
	case 0:
		return 0
	case 1:
		return int(g.byte()) + 1
	case 2:
		return manifest.MaxLine + int(g.byte()) - 128
	}
	var raw [4]byte
	for i := range raw {
		raw[i] = g.byte()
	}
	return int(int32(binary.BigEndian.Uint32(raw[:]))) //nolint:gosec // reinterpreting four fuzz bytes as a signed number is the intent
}

func (g *generator) manifest() manifest.Manifest {
	categories := []finding.Category{
		finding.CategoryStorage, finding.CategorySSH, finding.CategoryPartnerSession,
		finding.CategoryOther, finding.CategoryCustomerData, "", "ssh-key-file",
	}
	actions := []manifest.Action{manifest.ActionExternalized, manifest.ActionFileRemoved, "", "rotated"}
	m := manifest.Manifest{CRLF: g.byte()%2 == 1}
	for range g.byte() % 5 {
		e := manifest.Entry{
			Name:     g.str(),
			Category: categories[int(g.byte())%len(categories)],
			Action:   actions[int(g.byte())%len(actions)],
			Store:    g.str(),
		}
		if len(m.Entries) > 0 && g.byte()%4 == 0 {
			e.Name = m.Entries[len(m.Entries)-1].Name
		}
		for range g.byte() % 4 {
			e.Locations = append(e.Locations, manifest.Location{File: g.str(), Line: g.line()})
		}
		m.Entries = append(m.Entries, e)
	}
	return m
}

func FuzzEncode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("\x00\x01\x05ext.a\x00\x00\x01s\x01\x05a.yml\x01\x03"))
	f.Add([]byte("\x01\x02\x05ext.a\x03\x01\x02\"\n\x00\x02\x04a\xffb\x02\x80\x05ext.a\x04\x00\x01s\x03"))
	f.Fuzz(func(t *testing.T, data []byte) {
		g := generator{b: data}
		m := g.manifest()
		out, err := m.Encode()
		if err != nil {
			if !errors.Is(err, manifest.ErrInvalid) {
				t.Fatalf("Encode(%+v) error = %v, want %v", m, err, manifest.ErrInvalid)
			}
			return
		}
		got, err := manifest.Parse(out)
		if err != nil {
			t.Fatalf("Encode(%+v) wrote what Parse rejects: %v\n%s", m, err, out)
		}
		if diff := cmp.Diff(m, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("Parse(Encode(%+v)) mismatch (-want +got):\n%s", m, diff)
		}
	})
}
