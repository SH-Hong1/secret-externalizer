package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

var (
	fpPassword = strings.Repeat("a", 64)
	fpToken    = strings.Repeat("b", 64)
	fpPhone    = strings.Repeat("c", 64)
)

const (
	passwordBundle triage.BundleID = 101
	tokenBundle    triage.BundleID = 102
	phoneBundle    triage.BundleID = 103
	shellBundle    triage.BundleID = 104
)

// phoneKey names the undecided phone numbers in logs/sample.log.
var phoneKey = triage.GroupKey{File: "logs/sample.log", Kind: finding.KindPhone}

// triageEnv is a test server with project 1 "shop" and its succeeded scan
// 12: a password in two files, a token in a JavaScript file, and 37 phone
// numbers in a log.
func triageEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	if _, err := e.projects.CreateProject(t.Context(), project.Project{Name: "shop", Path: "legacy/shop"}); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	e.triage.setScan(scan.Scan{
		ID: 12, ProjectID: 1, Status: scan.StatusSucceeded, CommitHash: "a41c9e2ffffffff",
		RequestedAt: time.Date(2026, 11, 19, 5, 12, 0, 0, time.UTC),
	}, map[triage.Verdict]int{"": 3, triage.VerdictExternalize: 18},
		triage.Entry{
			Bundle: passwordBundle, Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "Sh0p****",
			File: "src/main/resources/application-prod.yml", FirstLine: 14, Count: 2, Bundles: 1,
		},
		triage.Entry{
			Bundle: tokenBundle, Category: finding.CategoryPartnerSession, Kind: finding.KindToken, Preview: "eyJh****",
			File: "src/main/webapp/js/partner.js", FirstLine: 42, Count: 1, Bundles: 1,
		},
		triage.Entry{
			Group: phoneKey, Category: finding.CategoryCustomerData, Kind: finding.KindPhone, Preview: "010-****-****",
			File: "logs/sample.log", FirstLine: 203, Count: 37, Bundles: 37,
		},
	)
	e.triage.putBundle(triage.Bundle{
		ID: passwordBundle, ProjectID: 1, Fingerprint: fpPassword,
		Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "Sh0p****",
		Members: []triage.Member{
			{FindingID: 1001, File: "src/main/resources/application-prod.yml", HasOriginal: true, Locations: []finding.Location{{
				StartLine: 14, EndLine: 14, ContextFirstLine: 12,
				Context:    []string{"  datasource:", "    username: ****", "    password: Sh0p****", "    url: jdbc:mysql://****:****/shop"},
				Detections: []finding.Detection{{Tool: "betterleaks", Rule: "generic-password"}, {Tool: "opengrep", Rule: "yaml-password"}},
			}}},
			{FindingID: 1002, File: "src/main/webapp/WEB-INF/spring/root-context.xml", HasOriginal: true, Locations: []finding.Location{{
				StartLine: 31, EndLine: 31, ContextFirstLine: 30,
				Context:    []string{`  <property name="username" value="****"/>`, `  <property name="password" value="Sh0p****"/>`},
				Detections: []finding.Detection{{Tool: "opengrep", Rule: "spring-xml-password"}},
			}}},
		},
	})
	e.triage.putBundle(triage.Bundle{
		ID: tokenBundle, ProjectID: 1, Fingerprint: fpToken,
		Category: finding.CategoryPartnerSession, Kind: finding.KindToken, Preview: "eyJh****",
		Members: []triage.Member{{FindingID: 1003, File: "src/main/webapp/js/partner.js", HasOriginal: true, Locations: []finding.Location{{
			StartLine: 42, EndLine: 42, ContextFirstLine: 41,
			Context:    []string{"var partnerApi = {", `  token: "eyJh****",`},
			Detections: []finding.Detection{{Tool: "betterleaks", Rule: "jwt"}},
		}}}},
	})
	e.triage.putBundle(triage.Bundle{
		ID: phoneBundle, ProjectID: 1, Fingerprint: fpPhone,
		Category: finding.CategoryCustomerData, Kind: finding.KindPhone, Preview: "010-****-****",
		Members: []triage.Member{{FindingID: 1004, File: "logs/sample.log", Locations: []finding.Location{{
			StartLine: 203, EndLine: 203,
			Detections: []finding.Detection{{Tool: "betterleaks", Rule: "kr-phone-number"}},
		}}}},
	})
	e.triage.putGroup(triage.Group{Key: phoneKey, Bundles: []triage.BundleID{phoneBundle}, Count: 37, Lines: []int{203, 204, 211}})
	return e
}

// getTriage requests the triage screen of project 1 with query as a signed-in
// user.
func (e *testEnv) getTriage(t *testing.T, query url.Values, header http.Header) result {
	t.Helper()
	target := "/projects/1/triage"
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return e.do(t, http.MethodGet, target, header, e.user(t))
}

// section returns the part of body from the start tag of the tag element
// whose id is id to its first end tag, or "" when there is none. It relies
// on the element holding no element of the same tag.
func section(body, tag, id string) string {
	loc := regexp.MustCompile(`<` + tag + `[^>]* id="` + id + `"`).FindStringIndex(body)
	if loc == nil {
		return ""
	}
	start := loc[0]
	end := strings.Index(body[start:], `</`+tag+`>`)
	if end < 0 {
		return body[start:]
	}
	return body[start : start+end]
}

func wantIn(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("%s lacks %q", what, want)
		}
	}
}

func wantNotIn(t *testing.T, what, body string, bads ...string) {
	t.Helper()
	for _, bad := range bads {
		if strings.Contains(body, bad) {
			t.Errorf("%s contains %q, want it absent", what, bad)
		}
	}
}

func TestTriagePage(t *testing.T) {
	e := triageEnv(t)
	res := e.getTriage(t, nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage status = %d, want 200; body:\n%s", res.status, res.body)
	}
	if f, ok := e.triage.lastFilter(); !ok || f != (triage.Filter{Status: triage.StatusUndecided, Page: 1}) {
		t.Errorf("GET triage asked Entries for %+v, want undecided page 1", f)
	}

	list := section(res.body, "nav", "list")
	wantIn(t, "triage list", list,
		"비밀번호", "저장소 접속 정보", "2곳", "src/main/resources/application-prod.yml:14",
		"토큰", "src/main/webapp/js/partner.js:42",
		"전화번호", "고객정보", "37건", "logs/sample.log",
		`hx-target="#detail"`, `hx-push-url="true"`,
	)
	wantNotIn(t, "triage list", list, "스토리지", "그 밖의 비밀 값")
	first := regexp.MustCompile(`<a class="list-row"[^>]*href="([^"]*)"[^>]*aria-current="true"`).FindStringSubmatch(list)
	if first == nil || !strings.Contains(first[1], "sel=b101") {
		t.Errorf("triage list current row = %v, want the password bundle b101 chosen", first)
	}
	if strings.Count(list, `aria-current="true"`) != 1 {
		t.Errorf("triage list marks %d rows current, want 1", strings.Count(list, `aria-current="true"`))
	}

	detail := section(res.body, "section", "detail")
	wantIn(t, "password detail", detail,
		"비밀번호", "Sh0p****", "미판정",
		"같은 값이 나온 위치 2곳",
		"src/main/resources/application-prod.yml:14", "src/main/webapp/WEB-INF/spring/root-context.xml:31",
		"betterleaks generic-password", "opengrep yaml-password", "opengrep spring-xml-password",
		`<mark class="code-line code-line-hit"><span class="sr-only">탐지된 줄 </span><span class="line-no">14</span>    password: Sh0p****</mark>`,
		`<span class="line-no">13</span>    username: ****`,
		`<form method="post" action="/projects/1/bundles/101/verdict"`,
		`name="name"`, `value="db.main.password"`, "ext.", "${ext.db.main.password}",
		`name="note"`,
		`name="verdict" value="externalize"`, `name="verdict" value="false-positive"`, `name="verdict" value="hold"`,
		`name="hold_reason"`, `value="structure-change"`, `value="needs-confirmation"`, `value="later"`,
		`name="return" value="status=undecided"`,
		`<form class="move-form" method="post" action="/projects/1/findings/1002/move"`, `name="to" value="new"`, "따로 떼기",
	)
	wantNotIn(t, "password detail", detail,
		`value="redact"`, "/findings/1001/move", "원본 보기", "btn-suggested", "notice-suggest",
	)
	wantIn(t, "triage page", res.body, "스캔 #12", "a41c9e2", "미판정 3", "외부화 18", "전체 21")
}

func TestTriageFrontendBundle(t *testing.T) {
	e := triageEnv(t)
	res := e.getTriage(t, url.Values{"sel": {"b102"}}, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage sel=b102 status = %d, want 200", res.status)
	}
	detail := section(res.body, "section", "detail")
	wantIn(t, "frontend detail", detail,
		"src/main/webapp/js/partner.js:42", "외부화할 수 없습니다",
		`name="verdict" value="false-positive"`, `name="verdict" value="hold"`,
		`<option value="structure-change" selected>`, "· 제안",
	)
	wantNotIn(t, "frontend detail", detail, `value="externalize"`, `name="name"`)
	list := section(res.body, "nav", "list")
	if !regexp.MustCompile(`href="[^"]*sel=b102[^"]*"[^>]*aria-current="true"`).MatchString(list) {
		t.Errorf("triage list with sel=b102 does not mark b102 current:\n%s", list)
	}
}

func TestTriageShellName(t *testing.T) {
	e := triageEnv(t)
	shell := triage.Bundle{
		ID: shellBundle, ProjectID: 1, Fingerprint: strings.Repeat("e", 64),
		Category: finding.CategorySSH, Kind: finding.KindPassword, Preview: "dp!2****",
		Members: []triage.Member{{FindingID: 1005, File: "deploy/release.sh", Locations: []finding.Location{{StartLine: 8, EndLine: 8}}}},
	}
	e.triage.putBundle(shell)
	res := e.getTriage(t, url.Values{"sel": {"b104"}}, nil)
	detail := section(res.body, "section", "detail")
	wantIn(t, "shell detail", detail, "EXT_", `value="ssh.main.password"`, "${EXT_SSH_MAIN_PASSWORD}")
	wantNotIn(t, "shell detail", detail, "${ext.ssh.main.password}")

	shell.Members = append(shell.Members, triage.Member{FindingID: 1006, File: "conf/ssh.properties", Locations: []finding.Location{{StartLine: 2, EndLine: 2}}})
	e.triage.putBundle(shell)
	res = e.getTriage(t, url.Values{"sel": {"b104"}}, nil)
	detail = section(res.body, "section", "detail")
	wantIn(t, "mixed shell and config detail", detail, "${ext.ssh.main.password}", "${EXT_SSH_MAIN_PASSWORD}")
}

func TestTriageCustomerData(t *testing.T) {
	e := triageEnv(t)
	res := e.getTriage(t, url.Values{"sel": {"c.none.phone.logs/sample.log"}}, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage customer data status = %d, want 200; body:\n%s", res.status, res.body)
	}
	detail := section(res.body, "section", "detail")
	wantIn(t, "customer data detail", detail,
		"전화번호", "고객정보", "logs/sample.log", "37건", "203, 204, 211", "010-****-****",
		"betterleaks kr-phone-number",
		`<form method="post" action="/projects/1/customer-data/verdict"`,
		`name="kind" value="phone"`, `name="file" value="logs/sample.log"`, `name="from" value=""`,
		`name="verdict" value="redact"`, `name="verdict" value="false-positive"`, `name="verdict" value="hold"`,
	)
	wantNotIn(t, "customer data detail", detail,
		`class="code"`, "code-line", `name="name"`, "원본 보기", `value="externalize"`, "/move",
	)
	list := section(res.body, "nav", "list")
	if !regexp.MustCompile(`href="[^"]*sel=c\.none\.phone\.logs%2Fsample\.log[^"]*"[^>]*aria-current="true"`).MatchString(list) {
		t.Errorf("triage list does not mark the phone group current:\n%s", list)
	}
}

func TestTriageCustomerBundleSelectsGroup(t *testing.T) {
	e := triageEnv(t)
	res := e.getTriage(t, url.Values{"sel": {"b103"}}, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage sel=b103 status = %d, want 200", res.status)
	}
	wantIn(t, "customer data detail", section(res.body, "section", "detail"), "37건")
	list := section(res.body, "nav", "list")
	if !regexp.MustCompile(`href="[^"]*sel=c\.none\.phone\.logs%2Fsample\.log[^"]*"[^>]*aria-current="true"`).MatchString(list) {
		t.Errorf("triage list with sel=b103 does not mark the phone group current:\n%s", list)
	}
}

func TestTriageSuggestion(t *testing.T) {
	e := triageEnv(t)
	e.triage.past[fpPassword] = triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.elsewhere.password", Note: "fake past note"}

	res := e.getTriage(t, nil, nil)
	detail := section(res.body, "section", "detail")
	wantIn(t, "suggested detail", detail,
		`value="db.main.password"`, "제안: 외부화",
	)
	wantNotIn(t, "suggested detail", detail, "db.elsewhere.password")
	if !regexp.MustCompile(`<button class="[^"]*btn-suggested[^"]*" type="submit" name="verdict" value="externalize">외부화 · 제안</button>`).MatchString(detail) {
		t.Errorf("suggested detail does not ring the externalize button:\n%s", detail)
	}
	wantNotIn(t, "suggested detail", detail, "fake past note")
	if !strings.Contains(section(res.body, "nav", "list"), "sel=b101") {
		t.Errorf("undecided list lacks the suggested bundle b101")
	}

	res = e.getTriage(t, url.Values{"status": {"all"}}, nil)
	row := regexp.MustCompile(`(?s)<a class="list-row"[^>]*sel=b101.*?</a>`).FindString(section(res.body, "nav", "list"))
	wantIn(t, "list row of b101 under status all", row, "badge-untriaged", "미판정")
}

func TestTriageSuggesterError(t *testing.T) {
	e := triageEnv(t)
	e.triage.pastErr = errors.New("fake past decision failure")
	res := e.getTriage(t, nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage with a failing suggester status = %d, want 200", res.status)
	}
	detail := section(res.body, "section", "detail")
	wantNotIn(t, "detail without a suggestion", detail, "· 제안", `value="db.main.password"`, "fake past decision failure")
	wantIn(t, "detail without a suggestion", detail, `name="name"`)
	if n := strings.Count(e.log.String(), "fake past decision failure"); n != 1 {
		t.Errorf("log mentions the suggester error %d times, want 1; log:\n%s", n, e.log.String())
	}
}

func TestTriageDecided(t *testing.T) {
	e := triageEnv(t)
	b, err := e.triage.Bundle(t.Context(), 1, 12, passwordBundle)
	if err != nil {
		t.Fatalf("Bundle() error = %v", err)
	}
	b.Decision = triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password", Note: "fake note"}
	b.DecidedBy = "fake-subject"
	e.triage.putBundle(b)
	e.triage.past[fpPassword] = triage.Decision{Verdict: triage.VerdictFalsePositive}

	res := e.getTriage(t, nil, nil)
	detail := section(res.body, "section", "detail")
	wantIn(t, "decided detail", detail, "badge-externalize", `value="db.shop.password"`, `value="fake note"`)
	wantNotIn(t, "decided detail", detail, "· 제안")
}

func TestTriageFragment(t *testing.T) {
	e := triageEnv(t)
	res := e.getTriage(t, url.Values{"sel": {"b102"}}, http.Header{"Hx-Request": {"true"}, "Hx-Target": {"detail"}})
	if res.status != http.StatusOK {
		t.Fatalf("GET triage fragment status = %d, want 200", res.status)
	}
	wantNotIn(t, "fragment", res.body, "<html", "site-header")
	wantIn(t, "fragment", res.body, `id="detail"`, "partner.js:42", `hx-swap-oob="true"`)

	res = e.getTriage(t, url.Values{"sel": {"b102"}}, http.Header{"Hx-Request": {"true"}})
	wantIn(t, "htmx request without the detail target", res.body, "<html")
}

func TestTriageEmpty(t *testing.T) {
	t.Run("everything decided", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.setScan(scan.Scan{ID: 12, ProjectID: 1, Status: scan.StatusSucceeded},
			map[triage.Verdict]int{"": 0, triage.VerdictExternalize: 71, triage.VerdictRedact: 38, triage.VerdictFalsePositive: 14, triage.VerdictHold: 5})
		res := e.getTriage(t, nil, nil)
		if res.status != http.StatusOK {
			t.Fatalf("GET triage with nothing undecided status = %d, want 200", res.status)
		}
		wantIn(t, "all decided", section(res.body, "section", "detail"),
			"미판정 항목이 없습니다", "128개를 모두 판정했습니다", "외부화 71", "삭제·마스킹 38", "오탐 14", "보류 5",
			`href="/projects/1/triage?status=all"`)
	})
	t.Run("scan found nothing", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.setScan(scan.Scan{ID: 12, ProjectID: 1, Status: scan.StatusSucceeded},
			map[triage.Verdict]int{"": 0, triage.VerdictExternalize: 0, triage.VerdictRedact: 0, triage.VerdictFalsePositive: 0, triage.VerdictHold: 0})
		res := e.getTriage(t, nil, nil)
		if res.status != http.StatusOK {
			t.Fatalf("GET triage after an empty scan status = %d, want 200", res.status)
		}
		detail := section(res.body, "section", "detail")
		wantIn(t, "empty scan", detail, "찾은 항목이 없습니다", "스캔 #12에서 비밀 값이나 고객정보를 찾지 못했습니다", `href="/projects/1"`)
		wantNotIn(t, "empty scan", detail, "모두 판정했습니다")
	})
	t.Run("filter matches nothing", func(t *testing.T) {
		e := triageEnv(t)
		res := e.getTriage(t, url.Values{"status": {"hold"}, "category": {"ssh-credential"}}, nil)
		if res.status != http.StatusOK {
			t.Fatalf("GET triage with an empty filter status = %d, want 200", res.status)
		}
		wantIn(t, "empty filter", section(res.body, "section", "detail"),
			"이 조건에 맞는 항목이 없습니다", "필터 지우기", `href="/projects/1/triage"`)
	})
}

func TestTriagePages(t *testing.T) {
	e := triageEnv(t)
	var entries []triage.Entry
	for i := range 60 {
		entries = append(entries, triage.Entry{
			Bundle: triage.BundleID(1000 + i), Category: finding.CategoryStorage, Kind: finding.KindPassword,
			File: "conf/app.yml", FirstLine: i + 1, Count: 1, Bundles: 1,
		})
	}
	e.triage.setScan(scan.Scan{ID: 12, ProjectID: 1, Status: scan.StatusSucceeded}, map[triage.Verdict]int{"": 60}, entries...)
	e.triage.putBundle(triage.Bundle{
		ID: 1050, ProjectID: 1, Category: finding.CategoryStorage, Kind: finding.KindPassword,
		Members: []triage.Member{{FindingID: 9, File: "conf/app.yml", Locations: []finding.Location{{StartLine: 51, EndLine: 51}}}},
	})

	res := e.getTriage(t, url.Values{"page": {"9"}}, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage page=9 status = %d, want 200", res.status)
	}
	list := section(res.body, "nav", "list")
	if n := strings.Count(list, `class="list-row"`); n != 10 {
		t.Errorf("GET triage page=9 lists %d rows, want the 10 of the last page", n)
	}
	wantIn(t, "last page", list, "2 / 2쪽", `href="/projects/1/triage?page=1&amp;status=undecided"`, "conf/app.yml:51")
}

func TestTriageNoScan(t *testing.T) {
	e := newTestEnv(t)
	if _, err := e.projects.CreateProject(t.Context(), project.Project{Name: "shop", Path: "legacy/shop"}); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	res := e.getTriage(t, nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage without a scan status = %d, want 200", res.status)
	}
	wantIn(t, "triage without a scan", res.body, "아직 성공한 스캔이 없습니다", `href="/projects/1"`, "프로젝트에서 스캔 시작")
}

func TestTriageBadInput(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"unknown category", "/projects/1/triage?category=nope", http.StatusBadRequest},
		{"unknown status", "/projects/1/triage?status=nope", http.StatusBadRequest},
		{"empty status", "/projects/1/triage?status=", http.StatusBadRequest},
		{"page not a number", "/projects/1/triage?page=x", http.StatusBadRequest},
		{"page zero", "/projects/1/triage?page=0", http.StatusBadRequest},
		{"sel without a form", "/projects/1/triage?sel=x", http.StatusBadRequest},
		{"bundle without a number", "/projects/1/triage?sel=b", http.StatusBadRequest},
		{"bundle not a number", "/projects/1/triage?sel=bx", http.StatusBadRequest},
		{"bundle zero", "/projects/1/triage?sel=b0", http.StatusBadRequest},
		{"group without a file", "/projects/1/triage?sel=c.none.phone", http.StatusBadRequest},
		{"group with an empty file", "/projects/1/triage?sel=c.none.phone.", http.StatusBadRequest},
		{"group with an unknown verdict", "/projects/1/triage?sel=c.maybe.phone.logs/sample.log", http.StatusBadRequest},
		{"group of a secret kind", "/projects/1/triage?sel=c.none.password.logs/sample.log", http.StatusBadRequest},
		{"unknown bundle", "/projects/1/triage?sel=b999", http.StatusNotFound},
		{"empty group", "/projects/1/triage?sel=c.hold.phone.logs/sample.log", http.StatusNotFound},
		{"unknown project", "/projects/99/triage", http.StatusNotFound},
		{"project not a number", "/projects/x/triage", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := triageEnv(t)
			res := e.do(t, http.MethodGet, tt.target, nil, e.user(t))
			if res.status != tt.want {
				t.Errorf("GET %s status = %d, want %d", tt.target, res.status, tt.want)
			}
			if tt.want == http.StatusBadRequest {
				wantIn(t, "GET "+tt.target, res.body, "요청을 처리할 수 없습니다. 주소나 입력을 확인해 주세요.")
				wantNotIn(t, "GET "+tt.target, res.body, "다시 로그인", `href="/login"`)
			}
		})
	}
}

func TestTriageStoreError(t *testing.T) {
	e := triageEnv(t)
	e.triage.entriesErr = errors.New("fake entries failure")
	res := e.getTriage(t, nil, nil)
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET triage with a failing store status = %d, want 500", res.status)
	}
	wantNotIn(t, "error page", res.body, "fake entries failure")
}

func TestTriageEscapes(t *testing.T) {
	e := triageEnv(t)
	const evilFile = "conf/<script>x()</script>.yml"
	e.triage.setScan(scan.Scan{ID: 12, ProjectID: 1, Status: scan.StatusSucceeded}, map[triage.Verdict]int{"": 1},
		triage.Entry{Bundle: 105, Category: finding.CategoryOther, Kind: finding.KindOther, File: evilFile, FirstLine: 1, Count: 1, Bundles: 1})
	e.triage.putBundle(triage.Bundle{
		ID: 105, ProjectID: 1, Category: finding.CategoryOther, Kind: finding.KindOther, Preview: "<b>****",
		Members: []triage.Member{{FindingID: 1007, File: evilFile, Locations: []finding.Location{{
			StartLine: 1, EndLine: 1, ContextFirstLine: 1,
			Context: []string{`<script>alert(1)</script> = ****`},
		}}}},
	})
	res := e.getTriage(t, nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage status = %d, want 200", res.status)
	}
	wantNotIn(t, "triage page", res.body, "<script>alert", "<script>x()", "<b>****")
	wantIn(t, "triage page", res.body, "&lt;script&gt;alert(1)&lt;/script&gt;", "conf/&lt;script&gt;x()&lt;/script&gt;.yml")
}

func TestTriageRequiresLogin(t *testing.T) {
	e := triageEnv(t)
	res := e.do(t, http.MethodGet, "/projects/1/triage?status=all", nil)
	if res.status != http.StatusSeeOther {
		t.Fatalf("GET triage without a session status = %d, want 303", res.status)
	}
	if want := "/login?return_to=" + url.QueryEscape("/projects/1/triage?status=all"); res.location() != want {
		t.Errorf("GET triage without a session Location = %q, want %q", res.location(), want)
	}
}

// appliedPassword is the password bundle of triageEnv after an apply stored
// its value.
func appliedPassword(e *testEnv) triage.Bundle {
	b := e.triage.stored(passwordBundle)
	b.Decision = triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}
	b.AppliedAt = time.Date(2026, 11, 20, 10, 7, 0, 0, time.UTC)
	b.StorePath = "secret/data/projects/1/db.main.password"
	return b
}

func TestTriage_AppliedTab(t *testing.T) {
	e := triageEnv(t)
	e.triage.putApplied(1,
		triage.Applied{
			Bundle: passwordBundle, Name: "db.main.password", Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "Sh0p****",
			StorePath: "secret/data/projects/1/db.main.password", AppliedAt: time.Date(2026, 11, 20, 10, 7, 0, 0, time.UTC),
			Locations: 3, Config: true, Shell: true, CodeChange: true, LastRun: 5, Checked: true, Reappeared: true,
		},
		triage.Applied{
			Bundle: 140, Name: "partner.api.token", Category: finding.CategoryPartnerSession, Kind: finding.KindToken, Preview: "eyJh****",
			StorePath: "secret/data/projects/1/partner.api.token", AppliedAt: time.Date(2026, 11, 19, 10, 7, 0, 0, time.UTC),
			Locations: 1, Config: true, LastRun: 4, Checked: true,
		},
		triage.Applied{
			Bundle: 141, Name: "mail.smtp.password", Category: finding.CategoryOther, Kind: finding.KindPassword, Preview: "mai1****",
			StorePath: "secret/data/projects/1/mail.smtp.password", AppliedAt: time.Date(2026, 11, 19, 9, 7, 0, 0, time.UTC),
			Locations: 1, Config: true, LastRun: 6,
		},
	)
	res := e.getTriage(t, url.Values{"status": {"applied"}}, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage?status=applied status = %d, want 200; body:\n%s", res.status, res.body)
	}
	wantIn(t, "applied tab", res.body,
		`aria-current="true">적용됨 3</a>`,
		"<th>이름</th>", "<th>분류</th>", "<th>미리보기</th>", "<th>저장 경로</th>", "<th>적용 시각</th>", "<th class=\"num\">위치</th>", "<th>마지막 실행</th>", "<th>마지막 스캔</th>",
		"ext.db.main.password", "EXT_DB_MAIN_PASSWORD", "ext.partner.api.token", "저장소 접속 정보 · 비밀번호", "Sh0p****",
		"secret/data/projects/1/db.main.password", "2026-11-20 10:07:00 UTC", ">3곳<", "코드 수정 필요",
		`<a href="/applies/5">#5</a>`, `<a href="/applies/4">#4</a>`,
		`<a class="badge badge-hold" href="/projects/1/triage?sel=b101&amp;status=all">다시 발견</a>`,
		"1 / 1쪽 · 3줄",
	)
	for _, tt := range []struct {
		mark string
		n    int
	}{{"다시 발견</a>", 1}, {`<span class="muted">없음</span>`, 1}, {`<span class="muted">확인 전</span>`, 1}} {
		if got := strings.Count(res.body, tt.mark); got != tt.n {
			t.Errorf("applied tab has %q %d times, want %d", tt.mark, got, tt.n)
		}
	}
	wantNotIn(t, "applied tab", res.body, `class="verdict-form"`, "원본 보기", `name="verdict"`, `id="detail"`, "EXT_PARTNER_API_TOKEN")

	res = e.getTriage(t, nil, nil)
	wantIn(t, "undecided tab", res.body, `href="/projects/1/triage?status=applied">적용됨 3</a>`)

	empty := triageEnv(t)
	res = empty.getTriage(t, url.Values{"status": {"applied"}}, nil)
	wantIn(t, "empty applied tab", res.body, "적용한 묶음이 없습니다", `href="/projects/1/apply"`)
}

func TestTriage_AppliedBundleDetail(t *testing.T) {
	e := triageEnv(t)
	e.triage.putBundle(appliedPassword(e))
	res := e.getTriage(t, url.Values{"sel": {"b101"}, "status": {"all"}}, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage sel=b101 status = %d, want 200", res.status)
	}
	detail := section(res.body, "section", "detail")
	wantIn(t, "applied detail", detail,
		`<span class="badge badge-lg badge-succeeded">적용됨</span>`,
		"secret/data/projects/1/db.main.password", "판정과 이름은 바꿀 수 없습니다",
		`id="verdict-name" value="db.main.password" disabled`,
		`<input type="hidden" name="name" value="db.main.password">`,
		`name="verdict" value="externalize" disabled>`, `name="verdict" value="false-positive" disabled>`, `name="verdict" value="hold" disabled>`,
		`<select class="input verdict-reason" id="verdict-reason" name="hold_reason" disabled>`,
		`<button class="btn btn-lg" type="submit" name="verdict" value="externalize">메모 저장</button>`,
	)
	wantNotIn(t, "applied detail", detail, `value="none"`, `id="verdict-name" name="name"`)
}

func TestTriage_ShellName(t *testing.T) {
	e := triageEnv(t)
	e.triage.setScan(scan.Scan{ID: 12, ProjectID: 1, Status: scan.StatusSucceeded}, map[triage.Verdict]int{triage.VerdictExternalize: 2},
		triage.Entry{
			Bundle: shellBundle, Name: "ssh.main.password", Category: finding.CategorySSH, Kind: finding.KindPassword,
			File: "deploy/release.sh", FirstLine: 8, Count: 1, Bundles: 1, Verdict: triage.VerdictExternalize, Shell: true,
		},
		triage.Entry{
			Bundle: passwordBundle, Name: "db.main.password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
			File: "conf/a.yml", FirstLine: 3, Count: 2, Bundles: 1, Verdict: triage.VerdictExternalize, Applied: true,
		},
	)
	e.triage.putBundle(triage.Bundle{
		ID: shellBundle, ProjectID: 1, Fingerprint: strings.Repeat("e", 64),
		Category: finding.CategorySSH, Kind: finding.KindPassword, Preview: "dp!2****",
		Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "ssh.main.password"},
		Members:  []triage.Member{{FindingID: 1005, File: "deploy/release.sh", Locations: []finding.Location{{StartLine: 8, EndLine: 8}}}},
	})
	res := e.getTriage(t, url.Values{"status": {"externalize"}}, nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET triage?status=externalize status = %d, want 200", res.status)
	}
	list := section(res.body, "nav", "list")
	wantIn(t, "triage list", list, `<span class="list-row-sub">EXT_SSH_MAIN_PASSWORD</span>`, `<span class="list-row-sub">ext.db.main.password</span>`,
		`<span class="badge badge-succeeded">적용됨</span>`)
	wantNotIn(t, "triage list", list, "ext.ssh.main.password")
	if n := strings.Count(list, "적용됨"); n != 1 {
		t.Errorf("triage list marks %d lines applied, want 1", n)
	}
}

func TestTriage_LinksToApply(t *testing.T) {
	e := triageEnv(t)
	res := e.getTriage(t, nil, nil)
	wantIn(t, "triage head", res.body, `<a class="btn btn-secondary" href="/projects/1/apply">적용</a>`)
}

func TestTriage_NoOriginalNotice(t *testing.T) {
	e := triageEnv(t)
	b := e.triage.stored(passwordBundle)
	for i := range b.Members {
		b.Members[i].HasOriginal = false
	}
	e.triage.putBundle(b)
	res := e.do(t, http.MethodGet, "/projects/1/triage?sel=b101", nil, e.admin(t))
	detail := section(res.body, "section", "detail")
	wantIn(t, "undecided detail without an original", detail, "보관된 원본이 없습니다. 다시 스캔하면 보관됩니다.")
	wantNotIn(t, "undecided detail without an original", detail, "원본 보기", "alert-error")

	b.Decision = triage.Decision{Verdict: triage.VerdictFalsePositive}
	e.triage.putBundle(b)
	res = e.do(t, http.MethodGet, "/projects/1/triage?sel=b101&status=all", nil, e.admin(t))
	detail = section(res.body, "section", "detail")
	wantIn(t, "false-positive detail without an original", detail, "오탐으로 판정한 값은 원본을 보관하지 않습니다. 판정을 바꾸고 다시 스캔하면 보관됩니다.")
	wantNotIn(t, "false-positive detail without an original", detail, "보관된 원본이 없습니다", "원본 보기")

	b.Decision = triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm}
	e.triage.putBundle(b)
	res = e.do(t, http.MethodGet, "/projects/1/triage?sel=b101&status=all", nil, e.admin(t))
	wantIn(t, "held detail without an original", section(res.body, "section", "detail"), "보관된 원본이 없습니다. 다시 스캔하면 보관됩니다.")

	b.Decision = triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}
	b.AppliedAt, b.StorePath = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC), "secret/data/externalized/1/db.main.password"
	e.triage.putBundle(b)
	res = e.do(t, http.MethodGet, "/projects/1/triage?sel=b101&status=all", nil, e.admin(t))
	wantNotIn(t, "applied detail without an original", section(res.body, "section", "detail"), "보관된 원본이 없습니다", "원본을 보관하지 않습니다", "원본 보기")

	res = e.do(t, http.MethodGet, "/projects/1/triage?sel=b101&status=all", nil, e.user(t))
	wantNotIn(t, "detail for a user who is not an admin", section(res.body, "section", "detail"), "보관된 원본이 없습니다", "원본을 보관하지 않습니다")
}

func TestTriage_LockedWhileApplying(t *testing.T) {
	const notice = "점검이나 적용이 진행 중이라 지금은 판정을 바꿀 수 없습니다."
	submit := regexp.MustCompile(`<button[^>]*type="submit"[^>]*>`)
	for _, tt := range []struct {
		name string
		sel  string
	}{
		{"bundle", "b101"},
		{"customer data", "c.none.phone.logs/sample.log"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := triageEnv(t)
			e.triage.same[passwordBundle] = []triage.Entry{{Bundle: 104, File: "conf/other.xml", FirstLine: 3}}
			query := url.Values{"sel": {tt.sel}}

			open := section(e.getTriage(t, query, nil).body, "section", "detail")
			wantNotIn(t, "detail of sel="+tt.sel+" with no apply", open, notice)
			if !strings.Contains(open, `name="verdict" value="false-positive">`) {
				t.Fatalf("detail of sel=%s with no apply lacks an enabled verdict button:\n%s", tt.sel, open)
			}

			e.triage.applyActive = true
			res := e.getTriage(t, query, nil)
			if res.status != http.StatusOK {
				t.Fatalf("GET triage sel=%s while applying status = %d, want 200", tt.sel, res.status)
			}
			detail := section(res.body, "section", "detail")
			wantIn(t, "detail of sel="+tt.sel+" while applying", detail, notice, `<a href="/projects/1/apply">적용 화면 보기</a>`,
				`id="verdict-note" name="note" value="" maxlength="500" autocomplete="off" disabled>`)
			wantNotIn(t, "detail of sel="+tt.sel+" while applying", detail, `class="move-form"`, `id="verdict-name" name="name"`)
			buttons := submit.FindAllString(detail, -1)
			if len(buttons) < 3 {
				t.Fatalf("detail of sel=%s while applying has %d submit buttons, want the verdict buttons:\n%s", tt.sel, len(buttons), detail)
			}
			for _, b := range buttons {
				if !strings.Contains(b, " disabled") {
					t.Errorf("detail of sel=%s while applying has the enabled button %s", tt.sel, b)
				}
			}
		})
	}
	t.Run("decided bundle", func(t *testing.T) {
		e := triageEnv(t)
		b := e.triage.stored(passwordBundle)
		b.Decision = triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}
		e.triage.putBundle(b)
		e.triage.applyActive = true
		detail := section(e.getTriage(t, url.Values{"sel": {"b101"}, "status": {"all"}}, nil).body, "section", "detail")
		wantIn(t, "decided detail while applying", detail, notice, `name="verdict" value="none" disabled>판정 취소</button>`,
			`id="verdict-name" value="db.main.password" disabled`)
	})
	t.Run("refused verdict", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.applyActive = true
		e.triage.decideErr = fmt.Errorf("deciding: %w", db.ErrApplyActive)
		res := e.post(t, verdictPath, verdictForm("false-positive", "", "", "", "status=undecided"), nil)
		detail := section(res.body, "section", "detail")
		wantIn(t, "refused verdict detail", detail, "적용 중에는 판정을 바꿀 수 없습니다")
		wantNotIn(t, "refused verdict detail", detail, notice)
	})
	t.Run("store error", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.activeErr = errors.New("fake failure")
		if res := e.getTriage(t, nil, nil); res.status != http.StatusInternalServerError {
			t.Errorf("GET triage when ApplyActive fails status = %d, want 500", res.status)
		}
	})
}

func TestTriageDetail_ShowsKey(t *testing.T) {
	e := triageEnv(t)
	loc := []finding.Location{{StartLine: 3, EndLine: 3}}
	e.triage.putBundle(triage.Bundle{
		ID: passwordBundle, ProjectID: 1, Fingerprint: fpPassword,
		Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "Sh0p****",
		Members: []triage.Member{
			{FindingID: 1001, File: "conf/app.properties", Key: "local.db.password", Locations: loc},
			{FindingID: 1002, File: "conf/app.properties", Locations: loc},
		},
	})
	res := e.getTriage(t, nil, nil)
	detail := section(res.body, "section", "detail")
	wantIn(t, "detail of a bundle with a keyed member", detail,
		`<span class="muted">키</span> <span class="mono">local.db.password</span>`)
	if n := strings.Count(detail, "local.db.password"); n != 1 {
		t.Errorf("detail shows the key %d times, want once", n)
	}
	if n := strings.Count(detail, `<span class="muted">키</span>`); n != 1 {
		t.Errorf("detail has %d key marks, want one: the member without a key shows none", n)
	}
}

func TestTriageDetail_MoveTargetShowsKey(t *testing.T) {
	e := triageEnv(t)
	e.triage.same[passwordBundle] = []triage.Entry{
		{
			Bundle: 17, Name: "db.shop.password", Verdict: triage.VerdictExternalize, Key: "live.db.password",
			Category: finding.CategoryStorage, Kind: finding.KindPassword, File: "conf/other.yml", FirstLine: 4, Count: 1, Bundles: 1,
		},
		{
			Bundle: 18, Category: finding.CategoryStorage, Kind: finding.KindPassword, File: "conf/third.yml", FirstLine: 5, Count: 1, Bundles: 1,
		},
	}
	res := e.getTriage(t, nil, nil)
	detail := section(res.body, "section", "detail")
	wantIn(t, "detail with move targets", detail,
		"묶음 #17 · ", " · live.db.password</option>", "conf/third.yml:5</option>")
}
