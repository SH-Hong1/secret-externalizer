package scan

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type keyCase struct {
	name string
	rel  string
	text string
	want string
}

func keyCases() []keyCase {
	return []keyCase{
		{"properties", "c.properties", "live.db.password=VALUE", "live.db.password"},
		{"properties spaces", "c.properties", "live.db.password = VALUE", "live.db.password"},
		{"properties colon", "c.properties", "live.db.password: VALUE", "live.db.password"},
		{"shell export", "run.sh", "export LIVE_PW='VALUE'", "LIVE_PW"},
		{"java constant", "A.java", `static final String LIVE_PW = "VALUE";`, "LIVE_PW"},
		{"java call", "A.java", `ds.setPassword("VALUE");`, "ds.setPassword"},
		{"json", "c.json", `{"livePassword": "VALUE"}`, "livePassword"},
		{"two on a line", "c.properties", "a=1; b=VALUE", "b"},
		{"shell argument", "run.sh", "sshpass -p 'VALUE' ssh host", ""},
		{"second line", "c.properties", "x=1\nlive.pw=VALUE\n", "live.pw"},
		{"crlf", "c.properties", "x=1\r\nlive.pw=VALUE\r\n", "live.pw"},
		{"korean key", "c.properties", "비밀번호=VALUE", ""},
		{"key too long", "c.properties", strings.Repeat("k", 201) + "=VALUE", ""},
		{"key at limit", "c.properties", strings.Repeat("k", 200) + "=VALUE", strings.Repeat("k", 200)},
		{"yaml nested", "a.yml", "spring:\n  datasource:\n    live:\n      password: VALUE\n", "spring.datasource.live.password"},
		{"yaml blank and comment", "a.yml", "db:\n\n  # note\n  live:\n    password: VALUE\n", "db.live.password"},
		{"yaml sibling skipped", "a.yml", "db:\n  dev:\n    password: x\n  live:\n    password: VALUE\n", "db.live.password"},
		{"yaml tab", "a.yml", "db:\n\tpassword: VALUE\n", "db.password"},
		{"yaml list item", "a.yml", "servers:\n  - name: a\n    password: VALUE\n", "password"},
		{"yaml inline list item", "a.yml", "servers:\n  - password: VALUE\n", "password"},
		{"yaml quoted", "a.yml", "db:\n  password: \"VALUE\"\n", "db.password"},
		{"xml property", "ctx.xml", "<bean id=\"liveDataSource\" class=\"X\">\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "liveDataSource.password"},
		{"xml closed bean", "ctx.xml", "<bean id=\"a\" class=\"X\"></bean>\n<property name=\"password\" value=\"VALUE\"/>", "password"},
		{"xml inner self-closed bean", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <bean class=\"Y\"/>\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml inner self-closed bean with id", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <bean id=\"inner\" class=\"Y\"/>\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml inner closed bean", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <property name=\"a\"><bean id=\"inner\" class=\"Y\"><property name=\"u\" value=\"x\"/></bean></property>\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml nested open bean", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <property name=\"ds\">\n    <bean id=\"inner\" class=\"Y\">\n      <property name=\"password\" value=\"VALUE\"/>", "inner.password"},
		{"xml nested open bean without id", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <property name=\"ds\">\n    <bean class=\"Y\">\n      <property name=\"password\" value=\"VALUE\"/>", "password"},
		{"xml prefixed bean", "ctx.xml", "<beans:bean id=\"ds\" class=\"X\">\n  <beans:property name=\"password\" value=\"VALUE\"/>", "password"},
		{"xml attribute", "build.xml", `<scp user="deploy" password="VALUE"/>`, "password"},
		{"xml namespace", "ctx.xml", `<bean id="ds" p:password="VALUE"/>`, "ds.password"},
		{"xml element text", "settings.xml", "<password>VALUE</password>", "password"},
		{"xml name beside a namespaced value", "ctx.xml", `<bean id="ds" name="alias" p:password="VALUE"/>`, "ds.password"},
		{"xml name beside a plain attribute", "server.xml", `<Resource name="jdbc/live" password="VALUE"/>`, "password"},
		{"xml name with element text", "ctx.xml", `<property name="password">VALUE</property>`, "password"},
		{"at zero", "c.properties", "VALUE", ""},
	}
}

func TestConfigKey(t *testing.T) {
	for _, tc := range keyCases() {
		t.Run(tc.name, func(t *testing.T) {
			at := bytes.Index([]byte(tc.text), []byte("VALUE"))
			got, spans := configKey(tc.rel, []byte(tc.text), at)
			if got != tc.want {
				t.Errorf("configKey(%q, %q, %d) = %q, want %q", tc.rel, tc.text, at, got, tc.want)
			}
			if (got == "") != (spans == nil) {
				t.Errorf("configKey(%q, %q, %d) spans = %v for key %q", tc.rel, tc.text, at, spans, got)
			}
		})
	}
}

func TestConfigKey_Spans(t *testing.T) {
	for _, name := range []string{"properties", "yaml nested", "xml property"} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range keyCases() {
				if tc.name != name {
					continue
				}
				text := []byte(tc.text)
				key, spans := configKey(tc.rel, text, bytes.Index(text, []byte("VALUE")))
				var pieces []string
				for _, s := range spans {
					pieces = append(pieces, string(text[s.Start:s.End]))
				}
				if diff := cmp.Diff(key, strings.Join(pieces, ".")); diff != "" {
					t.Errorf("configKey(%q, %q) spans joined differ from key (-key +spans):\n%s", tc.rel, tc.text, diff)
				}
				if len(spans) == 0 {
					t.Errorf("configKey(%q, %q) returned no spans", tc.rel, tc.text)
				}
			}
		})
	}
}

func FuzzConfigKey(f *testing.F) {
	for _, tc := range keyCases() {
		f.Add(tc.rel, []byte(tc.text), bytes.Index([]byte(tc.text), []byte("VALUE")))
	}
	f.Add("a.yml", []byte("a:\n b:"), -1)
	f.Add("a.xml", []byte("<bean id="), 1000)
	f.Fuzz(func(t *testing.T, rel string, text []byte, at int) {
		key, spans := configKey(rel, text, at)
		if len(key) > maxKeyBytes {
			t.Fatalf("configKey(%q, %q, %d) key is %d bytes, want at most %d", rel, text, at, len(key), maxKeyBytes)
		}
		for i := 0; i < len(key); i++ {
			if !isKeyChar(key[i]) {
				t.Fatalf("configKey(%q, %q, %d) = %q, which holds a byte outside the key alphabet", rel, text, at, key)
			}
		}
		for _, s := range spans {
			if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
				t.Fatalf("configKey(%q, %q, %d) span %v is outside the text", rel, text, at, s)
			}
		}
	})
}
