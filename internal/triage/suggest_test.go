package triage_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

func suggestBundle(c finding.Category, k finding.Kind, file, line string) triage.Bundle {
	loc := finding.Location{StartLine: 3, ContextFirstLine: 2}
	if line != "" {
		loc.Context = []string{"before", line, "after"}
		loc.StartLine = 3
	}
	return triage.Bundle{
		Category: c,
		Kind:     k,
		Members:  []triage.Member{{File: file, Locations: []finding.Location{loc}}},
	}
}

func TestSuggestName(t *testing.T) {
	tests := []struct {
		name string
		cat  finding.Category
		kind finding.Kind
		line string
		used []triage.Name
		want triage.Name
	}{
		{"yaml password", finding.CategoryStorage, finding.KindPassword, "    password: Sh0p****", nil, "db.main.password"},
		{"dotted key", finding.CategoryStorage, finding.KindPassword, "order.datasource.password=****", nil, "db.order.password"},
		{"xml name attribute", finding.CategoryStorage, finding.KindAccount, `<property name="username" value="****"/>`, nil, "db.main.account"},
		{"spaced equals", finding.CategoryStorage, finding.KindPassword, "jdbc.readonly.passwd = ****", nil, "db.readonly.password"},
		{"used name", finding.CategoryStorage, finding.KindPassword, "", []triage.Name{"db.main.password"}, "db.main2.password"},
		{"url key", finding.CategoryStorage, finding.KindHost, "spring.datasource.url=jdbc:mysql://****:****/shop", nil, "db.main.host"},
		{"sshpass", finding.CategorySSH, finding.KindPassword, "sshpass -p '****' ssh deploy@****", nil, "ssh.main.password"},
		{"key file", finding.CategorySSH, finding.KindKeyFile, "", nil, "ssh.main.key"},
		{"java constant", finding.CategoryPartnerSession, finding.KindToken, `private static final String PARTNER_API_TOKEN = "eyJh****";`, nil, "partner.api.token"},
		{"camel case", finding.CategoryOther, finding.KindOther, `secretValue = "abcd****"`, nil, "secret.main.value"},
		{"used twice", finding.CategoryStorage, finding.KindPassword, "order.password=****", []triage.Name{"db.order.password", "db.order2.password"}, "db.order3.password"},
		{"korean only", finding.CategoryOther, finding.KindOther, "비밀번호 = ****", nil, "secret.main.value"},
		{"digit first", finding.CategoryOther, finding.KindOther, "1st.password = ****", nil, "secret.main.value"},
		{"symbols", finding.CategoryOther, finding.KindOther, "@@@ = ****", nil, "secret.main.value"},
		{"masked account before a colon", finding.CategoryPartnerSession, finding.KindPassword, "curl -u svcu****:Sh0p**** https://partner.example.invalid/api", nil, "partner.main.password"},
		{"masked key before an equals", finding.CategoryOther, finding.KindOther, "abcd****.token = ****", nil, "secret.main.value"},
		{"very long", finding.CategoryOther, finding.KindOther, strings.Repeat("a", 300) + " = ****", nil, "secret.main.value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := suggestBundle(tt.cat, tt.kind, "a/b.properties", tt.line)
			got := triage.SuggestName(b, tt.used)
			if got != tt.want {
				t.Errorf("SuggestName(%q, used %v) = %q, want %q", tt.line, tt.used, got, tt.want)
			}
			if _, err := triage.ParseName(string(got)); err != nil {
				t.Errorf("SuggestName(%q) = %q does not parse: %v", tt.line, got, err)
			}
		})
	}
}

func TestSuggestName_Environment(t *testing.T) {
	const line = "    password: Sh0p****"
	tests := []struct {
		name  string
		files []string
		line  string
		want  triage.Name
	}{
		{"profile suffix", []string{"src/main/resources/application-prod.yml"}, line, "db.prod.main.password"},
		{"underscore suffix", []string{"conf/jdbc_dev.properties"}, line, "db.dev.main.password"},
		{"dotted suffix", []string{"conf/config.stage.xml"}, line, "db.stg.main.password"},
		{"synonym", []string{"conf/application-production.yml"}, line, "db.prod.main.password"},
		{"upper case", []string{"conf/application-PRD.yml"}, line, "db.prod.main.password"},
		{"folder", []string{"conf/prod/jdbc.properties"}, line, "db.prod.main.password"},
		{"nearest folder", []string{"dev/conf/qa/jdbc.properties"}, line, "db.qa.main.password"},
		{"file before folder", []string{"local/dev.properties"}, line, "db.dev.main.password"},
		{"test folder", []string{"src/test/resources/application.yml"}, line, "db.test.main.password"},
		{"no environment", []string{"src/main/resources/jdbc.properties"}, line, "db.main.password"},
		{"word inside a name", []string{"src/LocalCache.java", "conf/realm.xml", "delivery/a.yml"}, line, "db.main.password"},
		{"same environment twice", []string{"conf/prod/a.yml", "application-prod.yml"}, line, "db.prod.main.password"},
		{"two environments", []string{"application-dev.yml", "application-prod.yml"}, line, "db.main.password"},
		{"one file without", []string{"application-prod.yml", "jdbc.properties"}, line, "db.main.password"},
		{"with a target", []string{"application-prod.yml"}, "order.datasource.password=****", "db.prod.order.password"},
		{"key repeats the file", []string{"application-prod.yml"}, "prod.order.password=****", "db.prod.order.password"},
		{"key only", []string{"jdbc.properties"}, "prod.order.password=****", "db.prod.order.password"},
		{"key only without a target", []string{"jdbc.properties"}, "real.db.password=****", "db.prod.main.password"},
		{"key differs from the file", []string{"application-prod.yml"}, "dev.password=****", "db.prod.dev.password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := suggestBundle(finding.CategoryStorage, finding.KindPassword, tt.files[0], tt.line)
			for _, f := range tt.files[1:] {
				b.Members = append(b.Members, triage.Member{File: f})
			}
			got := triage.SuggestName(b, nil)
			if got != tt.want {
				t.Errorf("SuggestName(files %v, %q) = %q, want %q", tt.files, tt.line, got, tt.want)
			}
			if _, err := triage.ParseName(string(got)); err != nil {
				t.Errorf("SuggestName(files %v) = %q does not parse: %v", tt.files, got, err)
			}
		})
	}
}

func TestSuggestName_EnvironmentTaken(t *testing.T) {
	b := suggestBundle(finding.CategoryStorage, finding.KindPassword, "application-prod.yml", "password: ****")
	used := []triage.Name{"db.prod.main.password"}
	if got := triage.SuggestName(b, used); got != "db.prod.main2.password" {
		t.Errorf("SuggestName(used %v) = %q, want db.prod.main2.password", used, got)
	}
}

// The key of a value is the one written just before it, not the first one on
// the line.
func TestSuggestName_KeyBeforeTheValue(t *testing.T) {
	tests := []struct {
		name string
		cat  finding.Category
		line string
		at   string // the masked value as the line shows it
		col  int    // the column the scan stored, when it is not where at is
		want triage.Name
	}{
		{
			"ant scp task", finding.CategorySSH,
			`    <scp file="target/shop.war" todir="deploy@was1.example.invalid:/opt/was" password="dev-****" trust="true"/>`,
			"dev-****", 0, "ssh.main.password",
		},
		{
			// The account and the host before the password are masked to a
			// shorter text, so the stored column lies past the value.
			"earlier values masked shorter", finding.CategorySSH,
			`    <scp file="target/shop.war" todir="****@****:/opt/was" password="****" trust="true"/>`,
			`"****" trust`, 88, "ssh.main.password",
		},
		{
			"earlier value masked longer", finding.CategorySSH,
			`<scp todir="****:/opt" keyfile="k" password="****"/>`,
			`"****"/>`, 38, "ssh.main.password",
		},
		{
			"url value", finding.CategoryStorage,
			`spring.datasource.url=jdbc:mysql://****:****/shop`,
			"****", 0, "db.main.password",
		},
		{
			"namespaced attribute", finding.CategoryStorage,
			`<bean id="ds" p:url="jdbc:oracle:thin:@****" p:orderPassword="****"/>`,
			`"****"/>`, 0, "db.order.password",
		},
		{
			"second attribute names the target", finding.CategoryOther,
			`<signjar jar="a.jar" storepass="dev-****" releasePassword="rel-****"/>`,
			"rel-****", 0, "secret.release.password",
		},
		{
			"name attribute still wins", finding.CategoryStorage,
			`<property name="order.password" value="dev-****"/>`,
			"dev-****", 0, "db.order.password",
		},
		{
			"no key before the value", finding.CategorySSH,
			`deploy.mode=x; sshpass -p '****' ssh host`,
			"****", 0, "ssh.deploy.password",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := suggestBundle(tt.cat, finding.KindPassword, "a/build.xml", tt.line)
			i := strings.Index(tt.line, tt.at)
			if i < 0 {
				t.Fatalf("line %q does not hold %q", tt.line, tt.at)
			}
			col := i + 1
			if tt.col != 0 {
				col = tt.col
			}
			b.Members[0].Locations[0].StartCol = col
			if got := triage.SuggestName(b, nil); got != tt.want {
				t.Errorf("SuggestName(%q at column %d) = %q, want %q", tt.line, col, got, tt.want)
			}
		})
	}
}

func TestSuggestNameGuards(t *testing.T) {
	b := triage.Bundle{Category: finding.CategoryStorage, Kind: finding.KindPassword}
	if got := triage.SuggestName(b, nil); got != "db.main.password" {
		t.Errorf("SuggestName(no members) = %q, want db.main.password", got)
	}
	b = suggestBundle(finding.CategoryStorage, finding.KindPassword, "a.xml", "x=1")
	b.Members[0].Locations[0].StartLine = 99
	if got := triage.SuggestName(b, nil); got != "db.main.password" {
		t.Errorf("SuggestName(line outside context) = %q, want db.main.password", got)
	}
	var used []triage.Name
	for i := 2; i < 200; i++ {
		used = append(used, triage.Name("db.main"+strconv.Itoa(i)+".password"))
	}
	used = append(used, "db.main.password")
	got := triage.SuggestName(b, used)
	if got != "db.main200.password" {
		t.Errorf("SuggestName(many used) = %q, want db.main200.password", got)
	}
}

func TestDefault(t *testing.T) {
	tests := []struct {
		name string
		b    triage.Bundle
		want triage.Suggestion
	}{
		{
			"secret",
			suggestBundle(finding.CategoryStorage, finding.KindPassword, "a.properties", "db.password=****"),
			triage.Suggestion{Decision: triage.Decision{Name: "db.main.password"}, Source: "rule"},
		},
		{
			"frontend",
			suggestBundle(finding.CategoryStorage, finding.KindPassword, "web/app.JS", "var password = '****'"),
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldStructure}, Source: "rule"},
		},
		{
			"customer data",
			suggestBundle(finding.CategoryCustomerData, finding.KindEmail, "a.csv", ""),
			triage.Suggestion{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := triage.Default(tt.b, nil)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Default() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAdapt(t *testing.T) {
	storage := suggestBundle(finding.CategoryStorage, finding.KindPassword, "a.properties", "order.password=****")
	frontend := suggestBundle(finding.CategoryStorage, finding.KindPassword, "a.jsp", "x")
	customer := suggestBundle(finding.CategoryCustomerData, finding.KindEmail, "a.csv", "")
	tests := []struct {
		name string
		past triage.Decision
		b    triage.Bundle
		used []triage.Name
		want triage.Suggestion
	}{
		{
			"externalize takes the name of this bundle",
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password", Note: "old"},
			storage, nil,
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.order.password"}, Source: "history"},
		},
		{
			"externalize in another environment",
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"},
			suggestBundle(finding.CategoryStorage, finding.KindPassword, "application-prod.yml", "password: ****"), nil,
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.prod.main.password"}, Source: "history"},
		},
		{
			"externalize used name",
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password"},
			storage,
			[]triage.Name{"db.order.password"},
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.order2.password"}, Source: "history"},
		},
		{
			"externalize onto frontend",
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password"},
			frontend, nil,
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldStructure}, Source: "rule"},
		},
		{
			"false positive",
			triage.Decision{Verdict: triage.VerdictFalsePositive, Note: "sample"},
			storage, nil,
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictFalsePositive}, Source: "history"},
		},
		{
			"hold with reason",
			triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm, Note: "ask"},
			storage, nil,
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm}, Source: "history"},
		},
		{
			"redact onto secret",
			triage.Decision{Verdict: triage.VerdictRedact},
			storage, nil,
			triage.Suggestion{Decision: triage.Decision{Name: "db.order.password"}, Source: "rule"},
		},
		{
			"zero past decision",
			triage.Decision{},
			storage, nil,
			triage.Suggestion{Decision: triage.Decision{Name: "db.order.password"}, Source: "rule"},
		},
		{
			"redact onto customer data",
			triage.Decision{Verdict: triage.VerdictRedact},
			customer, nil,
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictRedact}, Source: "history"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := triage.Adapt(tt.past, tt.b, tt.used)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Adapt(%+v) mismatch (-want +got):\n%s", tt.past, diff)
			}
		})
	}
}

func TestSuggestName_StoredKey(t *testing.T) {
	tests := []struct {
		name string
		file string
		key  string
		line string
		want triage.Name
	}{
		{"local prefix", "conf/config.properties", "local.db.password", "", "db.local.main.password"},
		{"dev prefix", "conf/config.properties", "dev.db.password", "", "db.dev.main.password"},
		{"live prefix", "conf/config.properties", "live.db.password", "", "db.prod.main.password"},
		{"target word", "conf/config.properties", "order.db.password", "", "db.order.password"},
		{"environment in the middle", "conf/application.yml", "spring.datasource.live.password", "", "db.prod.main.password"},
		{"camel case", "conf/context.xml", "liveDataSource.password", "", "db.prod.data.password"},
		{"file environment first", "conf/application-dev.yml", "live.db.password", "", "db.dev.live.password"},
		{"no stored key", "conf/config.properties", "", "    password: ****", "db.main.password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc := finding.Location{StartLine: 3, StartCol: 15, ContextFirstLine: 2}
			if tt.line != "" {
				loc.Context = []string{"before", tt.line, "after"}
			}
			b := triage.Bundle{
				Category: finding.CategoryStorage,
				Kind:     finding.KindPassword,
				Members:  []triage.Member{{File: tt.file, Key: tt.key, Locations: []finding.Location{loc}}},
			}
			if got := triage.SuggestName(b, nil); got != tt.want {
				t.Errorf("SuggestName(file %q, key %q, line %q) = %q, want %q", tt.file, tt.key, tt.line, got, tt.want)
			}
		})
	}
}
