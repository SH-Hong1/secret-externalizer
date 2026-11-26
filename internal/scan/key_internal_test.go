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
		{"xml inner self-closed bean with > in a value", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <bean id=\"inner\" init=\"a>b\"/>\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml inner open bean with > in a value", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <bean id=\"inner\" init='a>b'>\n    <property name=\"password\" value=\"VALUE\"/>", "inner.password"},
		{"xml bean in a comment", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <!-- <bean id=\"old\" class=\"Y\"> -->\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml bean end in a comment", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <!-- </bean> -->\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml comment before the bean", "ctx.xml", "<!-- <bean id=\"old\"> -->\n<bean id=\"outer\" class=\"X\">\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml two comments", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <!-- <bean id=\"a\"> -->\n  <!-- </bean> </bean> -->\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "outer.password"},
		{"xml unclosed comment", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <!-- <bean id=\"a\">\n  <property name=\"password\" value=\"VALUE\"/>", "a.password"},
		{"xml property over lines", "ctx.xml", "<bean id=\"ds\" class=\"X\">\n  <property name=\"password\"\n            value=\"VALUE\"/>\n</bean>", "ds.password"},
		{"xml bean id on another line", "ctx.xml", "<bean class=\"X\"\n      id=\"ds\">\n  <property name=\"password\" value=\"VALUE\"/>\n</bean>", "ds.password"},
		{"xml attribute over lines", "server.xml", "<Resource name=\"jdbc/live\"\n   password=\"VALUE\"/>", "password"},
		{"xml closed tag before on the line", "ctx.xml", "<a name=\"x\">1</a> pw=VALUE", "pw"},
		{"xml nested open bean", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <property name=\"ds\">\n    <bean id=\"inner\" class=\"Y\">\n      <property name=\"password\" value=\"VALUE\"/>", "inner.password"},
		{"xml nested open bean without id", "ctx.xml", "<bean id=\"outer\" class=\"X\">\n  <property name=\"ds\">\n    <bean class=\"Y\">\n      <property name=\"password\" value=\"VALUE\"/>", "password"},
		{"xml prefixed bean", "ctx.xml", "<beans:bean id=\"ds\" class=\"X\">\n  <beans:property name=\"password\" value=\"VALUE\"/>", "ds.password"},
		{"xml prefixed bean closed", "ctx.xml", "<beans:bean id=\"a\" class=\"X\"></beans:bean>\n<beans:property name=\"password\" value=\"VALUE\"/>", "password"},
		{"xml prefixed inner closed bean", "ctx.xml", "<beans:bean id=\"outer\" class=\"X\">\n  <beans:property name=\"a\"><beans:bean id=\"inner\"></beans:bean></beans:property>\n  <beans:property name=\"password\" value=\"VALUE\"/>", "outer.password"},
		{"xml prefixed self-closed bean", "ctx.xml", "<beans:bean id=\"outer\" class=\"X\">\n  <beans:bean id=\"inner\" class=\"Y\"/>\n  <beans:property name=\"password\" value=\"VALUE\"/>", "outer.password"},
		{"xml beans root is not a bean", "ctx.xml", "<beans:beans id=\"root\">\n  <beans:property name=\"password\" value=\"VALUE\"/>", "password"},
		{"xml prefix only is not a bean", "ctx.xml", "<x:beanx id=\"root\">\n  <property name=\"password\" value=\"VALUE\"/>", "password"},
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

func TestURLStart(t *testing.T) {
	tests := []struct {
		name, text string
		want       int
		wantOK     bool
	}{
		{"mysql user info", "db.url=jdbc:mysql://shopadmin:VALUE@db.invalid/shop", 7, true},
		{"ssh scheme", "repo.url=ssh://deploy:VALUE@git.invalid/shop.git", 9, true},
		{"oracle thin password", "db.url=jdbc:oracle:thin:shopadmin/VALUE@db.invalid:1521/shop", 7, true},
		{"oracle thin user", "db.url=jdbc:oracle:thin:VALUE/x@db.invalid:1521/shop", 7, true},
		{"oracle thin in quotes", `url: "jdbc:oracle:thin:shopadmin/VALUE@db.invalid:1521/shop"`, 6, true},
		{"jdbc after white space", "db.url=jdbc:oracle:thin:a/b@h VALUE", 0, false},
		{"jdbc inside a word", "db.url=xjdbc:oracle:thin:a/VALUE@h", 0, false},
		{"jdbc on the line before", "db.url=jdbc:oracle:thin:a/b@h\npw=VALUE", 0, false},
		{"no url", "db.password=VALUE", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at := strings.Index(tt.text, "VALUE")
			got, ok := urlStart([]byte(tt.text), at)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("urlStart(%q, %d) = %d, %v, want %d, %v", tt.text, at, got, ok, tt.want, tt.wantOK)
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
	f.Add("a.xml", []byte("<bean id=\"a\">x=VALUE <"), 15)
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
