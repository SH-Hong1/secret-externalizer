package literal_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/literal"
)

// marked splits s, which holds one range between "«" and "»", into the text
// without the marks and the range of that text.
func marked(t *testing.T, s string) ([]byte, finding.Span) {
	t.Helper()
	start := strings.Index(s, "«")
	end := strings.Index(s, "»")
	if start < 0 || end < start {
		t.Fatalf("marked(%q): no range", s)
	}
	text := s[:start] + s[start+len("«"):end] + s[end+len("»"):]
	return []byte(text), finding.Span{Start: start, End: end - len("«")}
}

func TestValue(t *testing.T) {
	tests := []struct {
		name string
		file string
		text string // the value is between « and »
		want string
	}{
		{"properties plain", "app.properties", "db.password=«s3cret»\n", "s3cret"},
		{"properties escapes", "conf/app.properties", "p=«a\\=b\\:c\\\\d\\ e»\n", "a=b:c\\d e"},
		{"properties control escapes", "app.properties", "p=«a\\tb\\nc»", "a\tb\nc"},
		{"properties unicode escape", "app.properties", "p=«\\uD55C\\uAE00x»", "한글x"},
		{"properties other escape drops backslash", "app.properties", "p=«\\q»", "q"},
		{"properties comment", "app.properties", "  # p=«a\\=b»", "a\\=b"},
		{"properties comment bang", "app.properties", "! p=«a\\=b»", "a\\=b"},
		{"properties upper case extension", "APP.PROPERTIES", "p=«a\\=b»", "a=b"},

		{"xml attribute", "ctx.xml", `<property name="password" value="«a&amp;b&lt;&gt;&quot;&apos;»"/>`, `a&b<>"'`},
		{"xml character references", "ctx.xml", `<p>«&#65;&#x42;&#xAC00;»</p>`, "AB가"},
		{"xml plain", "ctx.xml", `<password>«s3cret»</password>`, "s3cret"},
		{"xml cdata", "ctx.xml", "<p><![CDATA[«a&amp;b»]]></p>", "a&amp;b"},
		{"xml after cdata", "ctx.xml", "<p><![CDATA[x]]></p>\n<q>«a&amp;b»</q>", "a&b"},
		{"xml comment", "ctx.xml", "<!-- value=«a&amp;b» -->", "a&amp;b"},
		{"xml after comment", "ctx.xml", "<!-- x -->\n<q>«a&amp;b»</q>", "a&b"},

		{"yaml plain", "application.yml", "spring:\n  password: «a\\nb»\n", "a\\nb"},
		{"yaml double quoted", "application.yaml", "password: \"«a\\\"b\\\\c\\td\\x41\\u00e9»\"\n", "a\"b\\c\tdAé"},
		{"yaml double quoted inside", "application.yml", "url: \"jdbc:x?password=«p\\\"w»&x=1\"\n", "p\"w"},
		{"yaml single quoted", "application.yml", "password: '«it''s\\n»'\n", "it's\\n"},
		{"yaml list item", "application.yml", "- \"«a\\\\b»\"\n", "a\\b"},
		{"yaml flow", "application.yml", "x: {p: \"«a\\\\b»\"}\n", "a\\b"},
		{"yaml quote in plain scalar", "application.yml", "p: it's «a\\\\b»\n", "a\\\\b"},
		{"yaml comment", "application.yml", "# p: \"«a\\\\b»\"\n", "a\\\\b"},
		{"yaml block scalar line", "application.yml", "p: |\n  «a\\\\b»\n", "a\\\\b"},
		{"yaml named escapes", "application.yml", `p: "«\N\_\L\P»"`, string([]rune{0x85, 0xa0, 0x2028, 0x2029})},
		{"yaml escaped slash and space", "application.yml", "p: \"«a\\/b\\ c»\"", "a/b c"},

		{"java string", "Db.java", `String p = "«a\"b\\c\td»";`, "a\"b\\c\td"},
		{"java octal and unicode", "Db.java", `String p = "«\101\7\uuu0042\s»";`, "A\aB "},
		{"java second string", "Db.java", `call("x\"y", "«a\\b»");`, `a\b`},
		{"java line comment", "Db.java", `int x; // "«a\\b»"`, `a\\b`},
		{"java block comment", "Db.java", `/* "«a\\b»" */`, `a\\b`},
		{"java char literal quote", "Db.java", `char c = '"'; String p = "«a\\b»";`, `a\b`},
		{"java code", "Db.java", `int «a\\b» = 1;`, `a\\b`},

		{"shell single quoted", "deploy.sh", `PASS='«a\$b"c»'`, `a\$b"c`},
		{"shell double quoted", "deploy.sh", `PASS="«a\$b\"c\\d\x»"`, `a$b"c\d\x`},
		{"shell unquoted", "deploy.sh", `PASS=«a\$b\ c»`, `a$b c`},
		{"shell comment", "deploy.sh", `# PASS="«$x»"`, `$x`},
		{"shell hash inside word", "deploy.sh", `PASS=a#b; Q="«\$»"`, `$`},
		{"shell upper case extension", "DEPLOY.SH", `P="«a\$»"`, `a$`},

		{"json string", "config.json", `{"password": "«a\"b\\c\/d\né»"}`, "a\"b\\c/d\né"},
		{"json surrogate pair", "config.json", `{"p": "«😀»"}`, "😀"},
		{"json second string", "config.json", `{"a\"": "«x\\y»"}`, `x\y`},
		{"json number", "config.json", `{"port": «5432»}`, "5432"},

		{"other file", "app.conf", `password = "«a\"b&amp;»"`, `a\"b&amp;`},
		{"env file", ".env", `P="«a\$b»"`, `a\$b`},
		{"no extension", "Makefile", `P=«a\\b»`, `a\\b`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, sp := marked(t, tt.text)
			got, err := literal.Value(tt.file, text, sp)
			if err != nil {
				t.Fatalf("Value(%q, %q) error = %v, want %q", tt.file, tt.text, err, tt.want)
			}
			if string(got) != tt.want {
				t.Errorf("Value(%q, %q) = %q, want %q", tt.file, tt.text, got, tt.want)
			}
		})
	}
}

func TestValue_Unclear(t *testing.T) {
	tests := []struct {
		name string
		file string
		text string
	}{
		{"properties trailing backslash", "app.properties", `p=«ab\»`},
		{"properties short unicode", "app.properties", `p=«\u12»`},
		{"properties bad unicode", "app.properties", `p=«\u12zz»`},
		{"properties two lines", "app.properties", "p=«a\\\n  b»"},

		{"xml unknown entity", "ctx.xml", `<p>«a&custom;»</p>`},
		{"xml bare ampersand", "ctx.xml", `<p>«a&b»</p>`},
		{"xml cut reference", "ctx.xml", `<p>«a&am»p;</p>`},
		{"xml bad number", "ctx.xml", `<p>«&#xZZ;»</p>`},
		{"xml tab", "ctx.xml", "<p v=\"«a\tb»\"/>"},
		{"xml two lines", "ctx.xml", "<p>«a\nb»</p>"},

		{"yaml unknown escape", "application.yml", `p: "«a\qb»"`},
		{"yaml short hex", "application.yml", `p: "«\x4»"`},
		{"yaml closing quote", "application.yml", `p: "«a"b»"`},
		{"yaml trailing backslash", "application.yml", `p: "«a\»"`},
		{"yaml lone single quote", "application.yml", `p: '«a'b»'`},
		{"yaml two lines", "application.yml", "p: «a\n  b»"},

		{"java unknown escape", "Db.java", `String p = "«a\qb»";`},
		{"java closing quote", "Db.java", `String p = "«a"b»";`},
		{"java text block", "Db.java", `String p = """«a»`},

		{"shell double dollar", "deploy.sh", `PASS="«a$b»"`},
		{"shell double backtick", "deploy.sh", "PASS=\"«a`b»\""},
		{"shell double closing quote", "deploy.sh", `PASS="«a"b»"`},
		{"shell unquoted dollar", "deploy.sh", `PASS=«a$b»`},
		{"shell unquoted glob", "deploy.sh", `PASS=«a*b»`},
		{"shell unquoted quote", "deploy.sh", `PASS=«a'b c'»`},
		{"shell unquoted space", "deploy.sh", `PASS=«a b»`},
		{"shell unquoted tilde", "deploy.sh", `PASS=«~x»`},
		{"shell single closing quote", "deploy.sh", `PASS='«a'b»'`},
		{"shell ansi c", "deploy.sh", `PASS=$'«a\nb»'`},
		{"shell trailing backslash", "deploy.sh", `PASS=«ab\»`},

		{"json unknown escape", "config.json", `{"p": "«a\qb»"}`},
		{"json lone surrogate", "config.json", `{"p": "«\ud83d»"}`},
		{"json closing quote", "config.json", `{"p": "«a"b»"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, sp := marked(t, tt.text)
			got, err := literal.Value(tt.file, text, sp)
			if !errors.Is(err, literal.ErrUnclear) {
				t.Errorf("Value(%q, %q) = %q, %v, want %v", tt.file, tt.text, got, err, literal.ErrUnclear)
			}
		})
	}
}

func TestValue_Copy(t *testing.T) {
	text, sp := marked(t, "x=«plain»")
	got, err := literal.Value("app.conf", text, sp)
	if err != nil {
		t.Fatalf("Value(%q) error = %v", text, err)
	}
	clear(got)
	if string(text) != "x=plain" {
		t.Errorf("Value(%q): zeroing the result changed the text to %q", "x=plain", text)
	}
}

func TestValue_OutOfRange(t *testing.T) {
	for _, sp := range []finding.Span{{Start: -1, End: 2}, {Start: 2, End: 1}, {Start: 0, End: 9}} {
		if got, err := literal.Value("a.properties", []byte("abc"), sp); err == nil {
			t.Errorf("Value(%q, %v) = %q, want an error", "abc", sp, got)
		}
	}
}

func TestUnclearErrorHoldsNoValue(t *testing.T) {
	text, sp := marked(t, `p=«s3cretvalue\»`)
	_, err := literal.Value("app.properties", text, sp)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("Value error = %v, want an error without the value", err)
	}
}
