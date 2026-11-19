package ruleset_test

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	gocmp "github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/betterleaks"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/keyfile"
	"github.com/SH-Hong1/secret-externalizer/internal/opengrep"
	"github.com/SH-Hong1/secret-externalizer/internal/ruleset"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

// maxFileBytes is the largest example file the tests read.
const maxFileBytes = 1 << 20

// located is a result reduced to what expected.json lists. Line is the
// 1-based line where the value starts and Value is the exact text of the
// value in the UTF-8 copy; a result for a whole file is on line 1 with an
// empty Value.
type located struct {
	File     string           `json:"file"`
	Line     int              `json:"line"`
	Category finding.Category `json:"category"`
	Kind     finding.Kind     `json:"kind"`
	Value    string           `json:"value"`
}

// result is one scanner result placed in the scanned copy. Span is the
// value's bytes, or nil for a result that covers a whole file; Before and
// After hold the byte just before and just after it, and are empty at the
// start or end of the file.
type result struct {
	located
	Tool          string
	Rule          string
	Span          []byte
	Before, After []byte
}

// repoPath returns the absolute path of rel, a path relative to the
// repository root.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// scannerBin returns the scanner executable named by the environment
// variable env, or the one in the repository's bin/ folder, and skips the
// test when neither exists.
func scannerBin(t *testing.T, env, name string) string {
	t.Helper()
	bin := os.Getenv(env)
	if bin == "" {
		bin = repoPath(t, "bin/"+name)
	}
	if _, err := os.Stat(bin); err != nil { //nolint:gosec // G703: the path names a scanner executable chosen by the developer
		t.Skipf("%s not found at %s: run make scanners, or set %s to the executable", name, bin, env)
	}
	return bin
}

// utf8Copy copies the example project into a new folder at parent, a
// slash-separated path inside a temporary folder, with every file converted
// to UTF-8, the way the scan runner hands files to the scanners. It returns
// the folder with its symbolic links resolved.
func utf8Copy(t *testing.T, parent string) string {
	t.Helper()
	src, err := filepath.EvalSymlinks(filepath.Join("testdata", "legacy-shop"))
	if err != nil {
		t.Fatal(err)
	}
	src, err = filepath.Abs(src)
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, filepath.FromSlash(parent), "legacy-shop")
	_, err = textfile.Walk(src, nil, func(rel string, _ fs.DirEntry) error {
		f, err := textfile.Read(filepath.Join(src, filepath.FromSlash(rel)), maxFileBytes)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return err
		}
		return os.WriteFile(out, f.Text, 0o600)
	})
	if err != nil {
		t.Fatalf("copying legacy-shop: %v", err)
	}
	return dst
}

// scanExample scans a UTF-8 copy of the example project, placed under
// parent, with the three scanners and the rules in the repository's rules/
// folder.
func scanExample(t *testing.T, parent string) []result {
	t.Helper()
	blBin := scannerBin(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := scannerBin(t, "OPENGREP_BIN", "opengrep")
	dir := utf8Copy(t, parent)
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}

	var raws []scan.Raw
	bl, counts, err := betterleaks.New(blBin, repoPath(t, "rules/betterleaks.toml"), env).ScanWithCounts(t.Context(), dir)
	if err != nil {
		t.Fatalf("betterleaks ScanWithCounts(legacy-shop) error = %v", err)
	}
	if !gocmp.Equal(counts, scan.Counts{}) {
		t.Errorf("betterleaks ScanWithCounts(legacy-shop) counts = %+v, want zero", counts)
	}
	raws = append(raws, bl...)
	og, counts, err := opengrep.New(ogBin, repoPath(t, "rules/opengrep"), nil, env).ScanWithCounts(t.Context(), dir)
	if err != nil {
		t.Fatalf("opengrep ScanWithCounts(legacy-shop) error = %v", err)
	}
	if !gocmp.Equal(counts, scan.Counts{}) {
		t.Errorf("opengrep ScanWithCounts(legacy-shop) counts = %+v, want zero", counts)
	}
	raws = append(raws, og...)
	kf, err := keyfile.New().Scan(t.Context(), dir)
	if err != nil {
		t.Fatalf("keyfile Scan(legacy-shop) error = %v", err)
	}
	raws = append(raws, kf...)

	results := make([]result, 0, len(raws))
	for _, r := range raws {
		results = append(results, place(t, dir, r))
	}
	return results
}

// place finds where r is in the scanned copy and which bytes it covers.
func place(t *testing.T, dir string, r scan.Raw) result {
	t.Helper()
	f, err := textfile.Read(filepath.Join(dir, filepath.FromSlash(r.File)), maxFileBytes)
	if err != nil {
		t.Fatalf("reading %s for %s %s: %v", r.File, r.Tool, r.Rule, err)
	}
	res := result{located: located{File: r.File, Category: r.Category, Kind: r.Kind}, Tool: r.Tool, Rule: r.Rule}
	var start, end int
	switch {
	case r.Offset == 0 && r.EndOffset == -1:
		res.Line = 1
		return res
	case r.Offset >= 0:
		if r.EndOffset > len(f.Text) {
			t.Fatalf("%s %s in %s: end offset %d is past the end of the file (%d bytes)", r.Tool, r.Rule, r.File, r.EndOffset, len(f.Text))
		}
		start, end = r.Offset, r.EndOffset
		res.Line, _ = f.Position(start)
	default:
		off, err := f.Offset(r.Line, r.ByteCol)
		if err != nil {
			t.Fatalf("%s %s in %s: line %d column %d: %v", r.Tool, r.Rule, r.File, r.Line, r.ByteCol, err)
		}
		if !bytes.HasPrefix(f.Text[off:], r.Value) {
			t.Fatalf("%s %s in %s: the value is not at line %d column %d", r.Tool, r.Rule, r.File, r.Line, r.ByteCol)
		}
		start, end = off, off+len(r.Value)
		res.Line = r.Line
	}
	res.Span = f.Text[start:end]
	res.Value = string(res.Span)
	res.Before = f.Text[max(0, start-1):start]
	res.After = f.Text[end:min(len(f.Text), end+1)]
	return res
}

func readExpected(t *testing.T) []located {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want []located
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("reading expected.json: %v", err)
	}
	return want
}

func compareLocated(a, b located) int {
	return cmp.Or(
		strings.Compare(a.File, b.File),
		cmp.Compare(a.Line, b.Line),
		strings.Compare(string(a.Category), string(b.Category)),
		strings.Compare(string(a.Kind), string(b.Kind)),
		strings.Compare(a.Value, b.Value),
	)
}

// checkExpected compares results with expected.json, as lists: a finding
// reported twice is a difference. It also checks every span.
func checkExpected(t *testing.T, results []result) {
	t.Helper()
	got := make([]located, 0, len(results))
	for _, r := range results {
		got = append(got, r.located)
	}
	slices.SortFunc(got, compareLocated)
	want := readExpected(t)
	slices.SortFunc(want, compareLocated)
	if diff := gocmp.Diff(want, got); diff != "" {
		t.Errorf("scanning legacy-shop: findings mismatch (-want +got):\n%s", diff)
	}
	for _, r := range results {
		checkSpan(t, r)
	}
}

func TestRules_ExpectedFindings(t *testing.T) {
	checkExpected(t, scanExample(t, "copy"))
}

// The scanned folder's own path must not decide which rules apply: rules
// limited to log and data folders or to file names such as build.xml and
// *.sh look only at paths inside the project.
func TestRules_ParentFoldersDoNotMatter(t *testing.T) {
	checkExpected(t, scanExample(t, "data/logs/samples/dump/build.xml/settings.xml/pom.xml/deploy.sh/app.properties/application.yml"))
}

// boundary holds the bytes that may surround a value: white space and the
// quoting and separator characters of the example files' formats.
const boundary = " \t\r\n\"'<>=:@;,()"

// checkSpan fails when the bytes reported for r are empty, start or end
// with a boundary byte, or are not surrounded by boundary bytes (or the
// start or end of the file). Any of these means the span takes in a quote or
// a key name, or cuts the value short. A value may end with "=", the padding
// of a base64 token. expected.json pins each value exactly; this check
// catches the same mistakes in results that are not listed there.
func checkSpan(t *testing.T, r result) {
	t.Helper()
	if r.Span == nil {
		return
	}
	if len(r.Span) == 0 {
		t.Errorf("%s %s in %s line %d: the span is empty", r.Tool, r.Rule, r.File, r.Line)
		return
	}
	first, last := r.Span[0], r.Span[len(r.Span)-1]
	if strings.IndexByte(boundary, first) >= 0 || (last != '=' && strings.IndexByte(boundary, last) >= 0) {
		t.Errorf("%s %s in %s line %d: the span %q starts or ends with a quote, a separator, or white space", r.Tool, r.Rule, r.File, r.Line, r.Span)
	}
	isBoundary := func(b []byte) bool { return len(b) == 0 || strings.IndexByte(boundary, b[0]) >= 0 }
	if !isBoundary(r.Before) || !isBoundary(r.After) {
		t.Errorf("%s %s in %s line %d: the span %q is not surrounded by a quote, a separator, or white space", r.Tool, r.Rule, r.File, r.Line, r.Span)
	}
}

func TestRules_IgnoredAndTestDirsAreScanned(t *testing.T) {
	ignore, err := os.ReadFile(filepath.Join("testdata", "legacy-shop", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(string(ignore), "\n"), "local/") {
		t.Fatalf("legacy-shop/.gitignore does not list local/, so the test checks nothing")
	}
	results := scanExample(t, "copy")
	for _, dir := range []string{"test/", "local/"} {
		tools := map[string]bool{}
		for _, r := range results {
			if strings.HasPrefix(r.File, dir) {
				tools[r.Tool] = true
			}
		}
		for _, tool := range []string{"betterleaks", "opengrep"} {
			if !tools[tool] {
				t.Errorf("scanning legacy-shop: %s found nothing under %s, want a finding", tool, dir)
			}
		}
	}
}

var tomlPath = regexp.MustCompile(`^path = '''(.*)'''$`)

// TestRules_CustomerDataPathMatchesGo checks that every customer-data rule
// in betterleaks.toml limits itself to the same files that
// finding.CustomerDataPath names, which the scan uses to leave out the
// surrounding lines of every finding in such a file.
func TestRules_CustomerDataPathMatchesGo(t *testing.T) {
	var id string
	patterns := map[string]string{}
	scanLines(t, repoPath(t, "rules/betterleaks.toml"), func(line string) {
		if line == "[[rules]]" {
			id = ""
		}
		if m := tomlID.FindStringSubmatch(line); m != nil {
			id = m[1]
		}
		if m := tomlPath.FindStringSubmatch(line); m != nil {
			patterns[id] = m[1]
		}
	})
	var customer []string
	for _, r := range readRules(t) {
		if r.file == "betterleaks.toml" && r.category == string(finding.CategoryCustomerData) {
			customer = append(customer, r.id)
		}
	}
	if len(customer) == 0 {
		t.Fatal("betterleaks.toml has no customer-data rule")
	}
	paths := []string{
		"app.log", "logs/app.log", "export/orders.CSV", "a.tsv", "readme.txt", "db/init.sql", "x.dat",
		"conf/settings.json", "log/x.properties", "logs/x.java", "a/b/Sample/x.xml", "samples/x.yml",
		"data/x.properties", "deep/er/dump/x", "LOGS/x", "src/main/java/A.java", "conf/db.properties",
		"web.xml", "app.log.1", "catalina.out", "logs", "data", "logsx/a.java", "mydata/a.java",
		"data.properties", "src/log.java", "a.jsonp", "a.LoG", "dumps/x.java", "x/datas/y.java",
	}
	for _, rule := range customer {
		p, ok := patterns[rule]
		if !ok {
			t.Errorf("betterleaks.toml: customer-data rule %q has no path line", rule)
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			t.Errorf("betterleaks.toml: rule %q path %q: %v", rule, p, err)
			continue
		}
		for _, rel := range paths {
			if got, want := re.MatchString(rel), finding.CustomerDataPath(rel); got != want {
				t.Errorf("betterleaks.toml: rule %q path matches %q = %v, but CustomerDataPath(%q) = %v", rule, rel, got, rel, want)
			}
		}
	}
}

func TestRules_CustomerDataOnlyInDataFiles(t *testing.T) {
	results := scanExample(t, "copy")
	var logPhone bool
	for _, r := range results {
		if r.Category != finding.CategoryCustomerData {
			continue
		}
		if !finding.CustomerDataPath(r.File) {
			t.Errorf("scanning legacy-shop: %s %s reported customer data %s in %s line %d, want none outside log and sample data", r.Tool, r.Rule, r.Kind, r.File, r.Line)
		}
		if r.File == "logs/app-2026-11-01.log" && r.Kind == finding.KindPhone {
			logPhone = true
		}
	}
	if !logPhone {
		t.Errorf("scanning legacy-shop: no phone number reported in logs/app-2026-11-01.log, want one")
	}
	// The example sources hold e-mail addresses outside the data files, so
	// the loop above checks something.
	for _, rel := range []string{"src/main/java/com/example/shop/dao/OrderDao.java", "pom.xml", "scripts/deploy.sh"} {
		data, err := os.ReadFile(filepath.Join("testdata", "legacy-shop", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte("@example.invalid")) {
			t.Errorf("legacy-shop/%s holds no e-mail address, so the test checks nothing there", rel)
		}
	}
}

// ruleMeta is the category and kind a rule file gives a rule.
type ruleMeta struct {
	file, id       string
	category, kind string
}

var (
	tomlID   = regexp.MustCompile(`^id = "([^"]+)"$`)
	tomlTags = regexp.MustCompile(`^tags = \["category:([^"]+)", "kind:([^"]+)"\]$`)
	yamlID   = regexp.MustCompile(`^  - id: (\S+)$`)
	anyID    = regexp.MustCompile(`^\s*-?\s*id:`)
	yamlCat  = regexp.MustCompile(`^      category: (\S+)$`)
	yamlKind = regexp.MustCompile(`^      kind: (\S+)$`)
)

// readRules reads the rules in the repository's rule files. The rule files
// are written in a fixed shape, which this reads line by line: in
// betterleaks.toml each [[rules]] table has an id line and a tags line, and
// in an opengrep file each rule starts with "  - id:" and has its category
// and kind under metadata.
func readRules(t *testing.T) []ruleMeta {
	t.Helper()
	var rules []ruleMeta
	file := repoPath(t, "rules/betterleaks.toml")
	scanLines(t, file, func(line string) {
		if line == "[[rules]]" {
			rules = append(rules, ruleMeta{file: "betterleaks.toml"})
			return
		}
		if len(rules) == 0 {
			return
		}
		cur := &rules[len(rules)-1]
		if m := tomlID.FindStringSubmatch(line); m != nil {
			cur.id = m[1]
		}
		if m := tomlTags.FindStringSubmatch(line); m != nil {
			cur.category, cur.kind = m[1], m[2]
		}
	})
	files, err := filepath.Glob(repoPath(t, "rules/opengrep/*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no opengrep rule files in rules/opengrep")
	}
	for _, file := range files {
		start := len(rules)
		ids := 0
		scanLines(t, file, func(line string) {
			if anyID.MatchString(line) {
				ids++
			}
			if m := yamlID.FindStringSubmatch(line); m != nil {
				rules = append(rules, ruleMeta{file: filepath.Base(file), id: m[1]})
				return
			}
			if len(rules) == start {
				return
			}
			cur := &rules[len(rules)-1]
			if m := yamlCat.FindStringSubmatch(line); m != nil {
				cur.category = m[1]
			}
			if m := yamlKind.FindStringSubmatch(line); m != nil {
				cur.kind = m[1]
			}
		})
		if len(rules) == start {
			t.Errorf("%s holds no rule", filepath.Base(file))
		}
		if ids != len(rules)-start {
			t.Errorf("%s has %d id lines, but %d are in the expected shape", filepath.Base(file), ids, len(rules)-start)
		}
	}
	return rules
}

func scanLines(t *testing.T, file string, fn func(line string)) {
	t.Helper()
	data, err := os.ReadFile(file) //nolint:gosec // G304: the path is a rule file of this repository
	if err != nil {
		t.Fatal(err)
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		fn(s.Text())
	}
	if err := s.Err(); err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
}

func TestRules_EveryRuleHasCategory(t *testing.T) {
	rules := readRules(t)
	if len(rules) == 0 {
		t.Fatal("no rules in rules/")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.id == "" {
			t.Errorf("%s: a rule has no id", r.file)
			continue
		}
		if seen[r.id] {
			t.Errorf("%s: rule id %q is used twice", r.file, r.id)
		}
		seen[r.id] = true
		if _, err := finding.ParseCategory(r.category); err != nil {
			t.Errorf("%s: rule %q category %q: %v", r.file, r.id, r.category, err)
		}
		if _, err := finding.ParseKind(r.kind); err != nil {
			t.Errorf("%s: rule %q kind %q: %v", r.file, r.id, r.kind, err)
		}
	}
}

// writeRules writes files, a map from relative path to content, into a new
// folder in the order given by names and returns the folder.
func writeRules(t *testing.T, files map[string]string, names []string) string {
	t.Helper()
	dir := t.TempDir()
	for i, name := range names {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(files[name]), 0o600); err != nil {
			t.Fatal(err)
		}
		mtime := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func version(t *testing.T, dir string) string {
	t.Helper()
	v, err := ruleset.Version(dir)
	if err != nil {
		t.Fatalf("Version(%s) error = %v", dir, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(v) {
		t.Fatalf("Version(%s) = %q, want 12 lower-case hex digits", dir, v)
	}
	return v
}

func TestVersion_ChangesWithContent(t *testing.T) {
	files := map[string]string{
		"betterleaks.toml":     "[extend]\nuseDefault = true\n",
		"opengrep/java.yml":    "rules: []\n",
		"opengrep/spring.yml":  "rules: []\n",
		"opengrep/zz/deep.yml": "rules: []\n",
	}
	names := []string{"betterleaks.toml", "opengrep/java.yml", "opengrep/spring.yml", "opengrep/zz/deep.yml"}
	reversed := slices.Clone(names)
	slices.Reverse(reversed)

	base := version(t, writeRules(t, files, names))
	if got := version(t, writeRules(t, files, reversed)); got != base {
		t.Errorf("Version(files written in reverse order) = %s, want %s", got, base)
	}

	changed := maps.Clone(files)
	changed["opengrep/java.yml"] = "rules: [] \n"
	if got := version(t, writeRules(t, changed, names)); got == base {
		t.Errorf("Version(one byte changed) = %s, want a different version", got)
	}

	renamed := maps.Clone(files)
	renamed["opengrep/kotlin.yml"] = renamed["opengrep/java.yml"]
	delete(renamed, "opengrep/java.yml")
	renamedNames := []string{"betterleaks.toml", "opengrep/kotlin.yml", "opengrep/spring.yml", "opengrep/zz/deep.yml"}
	if got := version(t, writeRules(t, renamed, renamedNames)); got == base {
		t.Errorf("Version(one file renamed) = %s, want a different version", got)
	}

	moved := map[string]string{
		"betterleaks.toml":     files["betterleaks.toml"],
		"opengrep/java.yml":    files["opengrep/java.yml"] + files["opengrep/spring.yml"],
		"opengrep/spring.yml":  "",
		"opengrep/zz/deep.yml": files["opengrep/zz/deep.yml"],
	}
	if got := version(t, writeRules(t, moved, names)); got == base {
		t.Errorf("Version(content moved between files) = %s, want a different version", got)
	}

	version(t, repoPath(t, "rules"))
}
