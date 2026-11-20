package manifest_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/manifest"

	"github.com/google/go-cmp/cmp"
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

func readSpec(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "spec.yml"))
	if err != nil {
		t.Fatalf("reading spec example: %v", err)
	}
	return data
}

func TestEncode_SpecExample(t *testing.T) {
	want := readSpec(t)
	got := specManifest().Encode()
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
			again, err := manifest.Parse(got.Encode())
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
		if got := m.Encode(); !bytes.Equal(got, crlf) {
			t.Errorf("Encode() of a CRLF manifest = %q, want %q", got, crlf)
		}
	})
	t.Run("empty manifest is written", func(t *testing.T) {
		got := string(manifest.Manifest{}.Encode())
		if want := "version: 1\nentries: []\n"; got != want {
			t.Errorf("Manifest{}.Encode() = %q, want %q", got, want)
		}
	})
}

func TestParse_Rejects(t *testing.T) {
	const head = "version: 1\nentries:\n"
	const entry = "  - name: ext.a\n    category: other-secret\n    action: externalized\n    store: s\n"
	const locs = "    locations:\n      - file: a.yml\n        line: 3\n"
	valid := head + entry + locs
	if _, err := manifest.Parse([]byte(valid)); err != nil {
		t.Fatalf("Parse(%q) error = %v, the base case must parse", valid, err)
	}
	tests := []struct {
		name string
		data string
	}{
		{"version 2", "version: 2\nentries: []\n"},
		{"version quoted", "version: \"1\"\nentries: []\n"},
		{"no version", "entries: []\n"},
		{"no entries key", "version: 1\n"},
		{"entries without items", "version: 1\nentries:\n"},
		{"unknown top-level key", "version: 1\nowner: me\nentries: []\n"},
		{"top-level key after entries", "version: 1\nentries: []\nowner: me\n"},
		{"unknown entry key", head + entry + "    owner: me\n" + locs},
		{"unknown location key", head + entry + locs + "        owner: me\n"},
		{"comment line", "# secrets\n" + valid},
		{"comment between entries", head + entry + locs + "  # more\n" + strings.Replace(entry, "ext.a", "ext.b", 1) + locs},
		{"end of line comment", head + strings.Replace(entry, "store: s", "store: s # where", 1) + locs},
		{"end of line comment after quotes", head + strings.Replace(entry, "store: s", "store: \"s\" # where", 1) + locs},
		{"tab indentation", head + strings.Replace(entry, "    category", "\tcategory", 1) + locs},
		{"tab after colon", head + strings.Replace(entry, "store: s", "store:\ts", 1) + locs},
		{"other indentation", head + strings.ReplaceAll(entry, "    ", "     ") + locs},
		{"item indentation", "version: 1\nentries:\n- name: ext.a\n  category: other-secret\n  action: externalized\n  store: s\n  locations: []\n"},
		{"two spaces after colon", head + strings.Replace(entry, "store: s", "store:  s", 1) + locs},
		{"duplicate name", valid + entry + locs},
		{"no name", head + strings.Replace(entry, "  - name: ext.a\n    category", "  - category", 1) + locs},
		{"empty name", head + strings.Replace(entry, "name: ext.a", "name: \"\"", 1) + locs},
		{"no store", head + strings.Replace(entry, "    store: s\n", "", 1) + locs},
		{"empty store", head + strings.Replace(entry, "store: s", "store: \"\"", 1) + locs},
		{"no locations", head + entry},
		{"repeated key", head + entry + "    store: t\n" + locs},
		{"unknown action", head + strings.Replace(entry, "externalized", "rotated", 1) + locs},
		{"unknown category", head + strings.Replace(entry, "other-secret", "ssh-key-file", 1) + locs},
		{"customer data", head + strings.Replace(entry, "other-secret", "customer-data", 1) + locs},
		{"line not a number", head + entry + strings.Replace(locs, "line: 3", "line: three", 1)},
		{"line zero", head + entry + strings.Replace(locs, "line: 3", "line: 0", 1)},
		{"line negative", head + entry + strings.Replace(locs, "line: 3", "line: -3", 1)},
		{"line leading zero", head + entry + strings.Replace(locs, "line: 3", "line: 03", 1)},
		{"line quoted", head + entry + strings.Replace(locs, "line: 3", "line: \"3\"", 1)},
		{"line too large", head + entry + strings.Replace(locs, "line: 3", "line: 99999999999999999999999", 1)},
		{"empty file name", head + entry + strings.Replace(locs, "file: a.yml", "file: \"\"", 1)},
		{"anchor", head + strings.Replace(entry, "store: s", "store: &st s", 1) + locs},
		{"alias", head + strings.Replace(entry, "store: s", "store: *st", 1) + locs},
		{"anchor on item", head + strings.Replace(entry, "  - name", "  - &e name", 1) + locs},
		{"tag", head + strings.Replace(entry, "store: s", "store: !!str s", 1) + locs},
		{"flow mapping", "version: 1\nentries: {}\n"},
		{"flow sequence", "version: 1\nentries: [a]\n"},
		{"flow locations", head + entry + "    locations: [{file: a.yml}]\n"},
		{"document start", "---\n" + valid},
		{"second document", valid + "---\n" + valid},
		{"document end", valid + "...\n"},
		{"block scalar literal", head + strings.Replace(entry, "store: s", "store: |\n      s", 1) + locs},
		{"block scalar folded", head + strings.Replace(entry, "store: s", "store: >\n      s", 1) + locs},
		{"single quotes", head + strings.Replace(entry, "store: s", "store: 's'", 1) + locs},
		{"plain null", head + strings.Replace(entry, "store: s", "store: null", 1) + locs},
		{"plain bool", head + entry + strings.Replace(locs, "file: a.yml", "file: true", 1)},
		{"plain number", head + entry + strings.Replace(locs, "file: a.yml", "file: 1.5", 1)},
		{"plain leading dash", head + entry + strings.Replace(locs, "file: a.yml", "file: -a", 1)},
		{"unterminated quote", head + strings.Replace(entry, "store: s", "store: \"s", 1) + locs},
		{"bad escape", head + strings.Replace(entry, "store: s", "store: \"s\\q\"", 1) + locs},
		{"lone surrogate", head + strings.Replace(entry, "store: s", "store: \"\\ud800\"", 1) + locs},
		{"raw control in quotes", head + strings.Replace(entry, "store: s", "store: \"s\x01\"", 1) + locs},
		{"blank line", head + entry + "\n" + locs},
		{"trailing space", head + strings.Replace(entry, "store: s", "store: s ", 1) + locs},
		{"bare carriage return", head + strings.Replace(entry, "store: s\n", "store: s\r", 1) + locs},
		{"mixed line endings", strings.Replace(valid, "\n", "\r\n", 1)},
		{"byte order mark", "\ufeff" + valid},
		{"not utf-8", head + entry + strings.Replace(locs, "a.yml", "\"a\xff.yml\"", 1)},
		{"over 1 MiB", valid + "#" + strings.Repeat("x", 1<<20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := manifest.Parse([]byte(tt.data))
			if !errors.Is(err, manifest.ErrUnsupported) {
				t.Fatalf("Parse(%.80q) = %+v, %v, want %v", tt.data, m, err, manifest.ErrUnsupported)
			}
		})
	}

	t.Run("error names the line only", func(t *testing.T) {
		data := head + entry + strings.Replace(locs, "file: a.yml", "file: dev-only-fake-marker # x", 1)
		_, err := manifest.Parse([]byte(data))
		if !errors.Is(err, manifest.ErrUnsupported) {
			t.Fatalf("Parse(%q) error = %v, want %v", data, err, manifest.ErrUnsupported)
		}
		if msg := err.Error(); !strings.Contains(msg, "line 8") || strings.Contains(msg, "dev-only-fake-marker") {
			t.Errorf("Parse(%q) error = %q, want line 8 and no line content", data, msg)
		}
	})
	t.Run("exactly 1 MiB is read", func(t *testing.T) {
		data := []byte(valid)
		pad := (1 << 20) - len(data)
		data = []byte(strings.Replace(valid, "store: s", "store: s"+strings.Repeat("s", pad), 1))
		if len(data) != 1<<20 {
			t.Fatalf("test input is %d bytes, want %d", len(data), 1<<20)
		}
		if _, err := manifest.Parse(data); err != nil {
			t.Errorf("Parse(1 MiB manifest) error = %v, want nil", err)
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
		"emoji\U0001F600.yml",
		"\U000E0001tag.yml",
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
	data := m.Encode()
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

	t.Run("non-UTF-8 name stays one location", func(t *testing.T) {
		m := manifest.Manifest{Entries: []manifest.Entry{entry("ext.a", manifest.Location{File: "a\xff\n- file: b", Line: 1})}}
		got, err := manifest.Parse(m.Encode())
		if err != nil {
			t.Fatalf("Parse(Encode()) error = %v", err)
		}
		want := manifest.Location{File: "a\ufffd\n- file: b", Line: 1}
		if diff := cmp.Diff([]manifest.Location{want}, got.Entries[0].Locations); diff != "" {
			t.Errorf("Parse(Encode()) locations mismatch (-want +got):\n%s", diff)
		}
	})
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
	spec, err := os.ReadFile(filepath.Join("testdata", "spec.yml"))
	if err != nil {
		f.Fatalf("reading spec example: %v", err)
	}
	f.Add(spec)
	f.Add(bytes.ReplaceAll(spec, []byte("\n"), []byte("\r\n")))
	f.Add([]byte("version: 1\nentries: []\n"))
	f.Add([]byte("version: 1\nentries:\n  - name: \"a\\u00e9\\n\"\n    category: other-secret\n    action: externalized\n    store: \"\\ud83d\\ude00\"\n    locations: []\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := manifest.Parse(data)
		if err != nil {
			return
		}
		again, err := manifest.Parse(m.Encode())
		if err != nil {
			t.Fatalf("Parse(%q) succeeded but Parse(Encode()) error = %v\n%s", data, err, m.Encode())
		}
		if diff := cmp.Diff(m, again); diff != "" {
			t.Errorf("Parse(Encode(Parse(%q))) mismatch (-want +got):\n%s", data, diff)
		}
	})
}
