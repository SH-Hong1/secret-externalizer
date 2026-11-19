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
			"externalize free name",
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password", Note: "old"},
			storage, nil,
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password"}, Source: "history"},
		},
		{
			"externalize used name",
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password"},
			storage,
			[]triage.Name{"db.shop.password"},
			triage.Suggestion{Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.order.password"}, Source: "history"},
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
