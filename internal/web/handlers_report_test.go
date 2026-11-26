package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
	"github.com/SH-Hong1/secret-externalizer/internal/web"
)

// reportSections are the titles of the seven sections of a report.
var reportSections = []string{
	"1. 요약", "2. 외부화한 항목", "3. 삭제·마스킹한 고객정보", "4. 구조 변경이 필요한 항목",
	"5. 남은 항목", "6. 오탐으로 판정한 항목", "7. 판정 이력",
}

// reportEmpty are the texts of the sections that list nothing.
var reportEmpty = []string{
	"외부화한 항목이 없습니다.", "삭제·마스킹한 고객정보가 없습니다.", "구조 변경이 필요한 항목이 없습니다.",
	"남은 항목이 없습니다.", "오탐으로 판정한 항목이 없습니다.", "판정 이력이 없습니다.",
}

// reportScan is succeeded scan 7 of project 3.
func reportScan() scan.Scan {
	return scan.Scan{
		ID: 7, ProjectID: 3, Status: scan.StatusSucceeded,
		RequestedAt:  time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 11, 20, 9, 1, 0, 0, time.UTC),
		CommitHash:   "0123456789abcdef0123456789abcdef01234567",
		RulesVersion: "rules-fake-1",
		ToolVersions: map[string]string{"betterleaks": "0.0.0-fake"},
	}
}

// emptyReport is the data of scan 7 that found nothing, with no scan
// before it.
func emptyReport() report.Data {
	return report.Data{
		Project: project.Project{ID: 3, Name: "shop", Path: "shop"},
		Scan:    reportScan(),
		Latest:  7,
	}
}

// fullReport is the data of scan 7 with a line in every section and scan 5
// before it.
func fullReport() report.Data {
	d := emptyReport()
	d.Previous = 5
	d.Totals = report.Totals{
		Found:         report.Count{Bundles: 12, Locations: 31},
		FalsePositive: report.Count{Bundles: 1, Locations: 1},
		Hold:          report.Count{Bundles: 2, Locations: 4},
		Undecided:     report.Count{Bundles: 6, Locations: 9},
		Pending:       report.Count{Bundles: 3, Locations: 17},
		Externalized:  report.Count{Bundles: 4, Locations: 8},
		Redacted:      report.Count{Bundles: 2, Locations: 5},
	}
	d.PreviousTotals = d.Totals
	d.PreviousTotals.Found = report.Count{Bundles: 82, Locations: 121}
	d.Externalized = report.List[report.Entry]{Total: 2, Rows: []report.Entry{
		{
			Bundle: 12, Name: "EXT_SSH_DEPLOY_KEY", Category: finding.CategorySSH, Kind: finding.KindKeyFile,
			StorePath: "secret/data/projects/3/ext.ssh.deploy.key", PlaceTotal: 1,
			Places: []report.Place{{File: "keys/deploy.pem", Removed: true, Run: 2}},
		},
		{
			Bundle: 11, Name: "ext.db.main.password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
			StorePath: "secret/data/projects/3/ext.db.main.password", PlaceTotal: 1, CodeChange: true, Reappeared: true,
			Places: []report.Place{{File: "src/main/resources/application-prod.yml", Line: 14, Run: 2}},
		},
	}}
	d.Redactions = []report.Redaction{
		{Kind: finding.KindPhone, File: "logs/app.log", Untracked: true, Bundles: 2, Locations: 5},
	}
	decided := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	d.Structure = report.List[report.Line]{Total: 1, Rows: []report.Line{{
		Bundle: 21, Category: finding.CategoryPartnerSession, Kind: finding.KindToken, File: "web/login.jsp", FirstLine: 3,
		Files: 1, Bundles: 1, Locations: 1, Frontend: true,
	}}}
	d.Remaining = report.List[report.Line]{Total: 2, Rows: []report.Line{
		{
			Bundle: 22, Category: finding.CategoryOther, Kind: finding.KindToken, File: "conf/a.properties", FirstLine: 8,
			Files: 2, Bundles: 1, Locations: 3, Verdict: triage.VerdictHold, Reason: triage.HoldConfirm,
			Note: "dev-only-fake-note", DecidedAt: decided,
		},
		{
			Bundle: 11, Category: finding.CategoryStorage, Kind: finding.KindPassword, File: "conf/b.xml", FirstLine: 4,
			Files: 1, Bundles: 1, Locations: 1, Verdict: triage.VerdictExternalize, Name: "db.main.password",
			DecidedAt: decided, Applied: report.AppliedBefore,
		},
	}}
	d.FalsePositives = report.List[report.Line]{Total: 1, Rows: []report.Line{{
		Bundle: 23, Category: finding.CategoryOther, Kind: finding.KindOther, File: "test/Fixture.java", FirstLine: 20,
		Files: 1, Bundles: 1, Locations: 1, Verdict: triage.VerdictFalsePositive, Note: "test fixture", DecidedAt: decided,
	}}}
	d.Events = []report.Event{
		{
			At: decided, Actor: audit.Actor{Subject: "fake-subject", Name: "Fake User"}, Action: audit.ActionVerdict,
			Detail:  map[string]string{audit.DetailVerdict: string(triage.VerdictExternalize), audit.DetailName: "db.main.password"},
			Bundles: 1, Category: finding.CategoryStorage, Kind: finding.KindPassword, File: "conf/b.xml",
		},
		{
			At: decided, Actor: audit.Actor{Subject: "fake-subject", Name: "Fake User"}, Action: audit.ActionApply,
			Detail: map[string]string{audit.DetailApply: "2"},
		},
	}
	d.EventTotal = len(d.Events)
	return d
}

// getReport shows the report of scan 7 to a signed-in user.
func (e *testEnv) getReport(t *testing.T) result {
	t.Helper()
	const target = "/scans/7/report"
	res := e.do(t, http.MethodGet, target, nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body:\n%s", target, res.status, res.body)
	}
	return res
}

func TestHandleReport_OK(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(fullReport())
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report", res.body, reportSections...)
	wantIn(t, "GET /scans/7/report", res.body,
		"리포트 · 스캔 #7",
		`<article id="report"`,
		"이 리포트에는 값, 미리보기, 주변 코드가 없습니다.",
		"${ext.db.main.password}",
		"${EXT_SSH_DEPLOY_KEY}",
		"openbao:secret/data/projects/3/ext.db.main.password",
		"application-prod.yml:14",
		"keys/deploy.pem · 파일 제거",
		"코드 수정 필요",
		"다시 발견",
		"복구 불가",
		"12개 · 위치 31곳",
		`이전 스캔보다 <span class="delta">-70개 · 위치 -90곳</span>`,
		`이전 스캔보다 <span class="delta">0개 · 위치 0곳</span>`,
		"보류 · 확인 필요",
		"적용됨 · 다시 발견",
		"dev-only-fake-note",
		"판정 · 외부화",
		"적용 #2",
		`href="/projects/3"`,
		`href="/scans/7"`,
	)
	wantNotIn(t, "GET /scans/7/report", res.body, "이후 스캔이 있습니다", "이전 스캔 없음")
}

func TestHandleReport_Redirects(t *testing.T) {
	for _, status := range []scan.Status{scan.StatusQueued, scan.StatusRunning, scan.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			e := newTestEnv(t)
			d := emptyReport()
			d.Scan.Status = status
			e.reports.put(d)
			res := e.do(t, http.MethodGet, "/scans/7/report", nil, e.user(t))
			if res.status != http.StatusSeeOther || res.location() != "/scans/7" {
				t.Errorf("GET /scans/7/report of a %s scan = %d to %q, want 303 to /scans/7", status, res.status, res.location())
			}
		})
	}
}

func TestHandleReport_NotFound(t *testing.T) {
	e := newTestEnv(t)
	for _, target := range []string{"/scans/9/report", "/scans/x/report", "/scans/0/report"} {
		if res := e.do(t, http.MethodGet, target, nil, e.user(t)); res.status != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", target, res.status)
		}
	}
}

func TestHandleReport_RequiresLogin(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(emptyReport())
	res := e.do(t, http.MethodGet, "/scans/7/report", nil)
	if res.status != http.StatusSeeOther {
		t.Errorf("GET /scans/7/report without a session status = %d, want 303", res.status)
	}
	c := e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})
	if res := e.do(t, http.MethodGet, "/scans/7/report", nil, c); res.status != http.StatusOK {
		t.Errorf("GET /scans/7/report as a user status = %d, want 200", res.status)
	}
}

// TestHandleReport_HistoryWords pins the boxes the printed history keeps
// whole: the person and the name of a verdict are one word each, so a login
// does not break at its hyphen and a name does not break mid-word.
func TestHandleReport_HistoryWords(t *testing.T) {
	e := newTestEnv(t)
	d := fullReport()
	d.Events[0].Actor.Name = "dev-admin"
	e.reports.put(d)
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report with a verdict by dev-admin", res.body,
		`<td><span class="report-user">dev-admin</span></td>`,
		`<td class="report-note"><span class="mono report-name">ext.db.main.password</span></td>`,
	)
}

func TestHandleReport_OlderScan(t *testing.T) {
	e := newTestEnv(t)
	d := emptyReport()
	d.Latest = 9
	e.reports.put(d)
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report with scan 9 later", res.body,
		"이후 스캔이 있습니다. 프로젝트의 마지막 성공한 스캔은 #9입니다.", `href="/scans/9/report"`)

	e.reports.put(emptyReport())
	res = e.getReport(t)
	wantNotIn(t, "GET /scans/7/report of the latest scan", res.body, "이후 스캔이 있습니다", `href="/scans/9/report"`)
}

func TestHandleReport_NoPrevious(t *testing.T) {
	e := newTestEnv(t)
	d := fullReport()
	d.Previous = 0
	e.reports.put(d)
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report without a previous scan", res.body, "이전 스캔 없음")
	wantNotIn(t, "GET /scans/7/report without a previous scan", res.body, "-70", "-90", `class="delta"`)
}

func TestHandleReport_Empty(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(emptyReport())
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report of an empty scan", res.body, reportSections...)
	wantIn(t, "GET /scans/7/report of an empty scan", res.body, reportEmpty...)
}

func TestHandleReport_Capped(t *testing.T) {
	e := newTestEnv(t)
	d := emptyReport()
	for i := range report.MaxRows {
		d.Remaining.Rows = append(d.Remaining.Rows, report.Line{
			Bundle: triage.BundleID(i + 1), Category: finding.CategoryOther, Kind: finding.KindToken,
			File: "conf/a.properties", FirstLine: i + 1, Files: 1, Bundles: 1, Locations: 1,
		})
	}
	d.Remaining.Total = report.MaxRows + 5
	e.reports.put(d)
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report with MaxRows+5 remaining lines", res.body, "처음 100줄만 싣습니다. 외 5줄.")
}

func TestHandleReport_Escapes(t *testing.T) {
	const (
		file = "<img src=x onerror=alert(1)>.yml"
		note = "</td><script>alert(1)</script>"
	)
	e := newTestEnv(t)
	d := fullReport()
	d.Remaining.Rows[0].File, d.Remaining.Rows[0].Note = file, note
	d.Externalized.Rows[1].Places[0].File = file
	d.Redactions[0].File = file
	d.Events[0].File = file
	d.Events[0].Detail[audit.DetailNote] = note
	e.reports.put(d)
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report with markup in names", res.body,
		"&lt;img src=x onerror=alert(1)&gt;.yml", "&lt;/td&gt;&lt;script&gt;alert(1)&lt;/script&gt;")
	wantNotIn(t, "GET /scans/7/report with markup in names", res.body, "<script>alert", "<img src=x")
}

func TestHandleReport_ShowsHiddenCharacters(t *testing.T) {
	const file = "conf/a\u202egnp.yml\u200b"
	const shown = "conf/a&lt;U&#43;202E&gt;gnp.yml&lt;U&#43;200B&gt;"
	e := newTestEnv(t)
	d := fullReport()
	d.Remaining.Rows[0].File = file
	d.Externalized.Rows[1].Places[0].File = file
	d.Redactions[0].File = file
	d.Events[0].File = file
	e.reports.put(d)
	res := e.getReport(t)
	what := "GET /scans/7/report with format characters in file names"
	wantNotIn(t, what, res.body, "\u202e", "\u200b")
	if got := strings.Count(res.body, shown); got != 4 {
		t.Errorf("%s shows %q %d times, want 4", what, shown, got)
	}
	if res := e.getReportPDF(t); res.status != http.StatusOK {
		t.Fatalf("GET /scans/7/report.pdf status = %d, want 200; body:\n%s", res.status, res.body)
	}
	index := string(e.lastDocument(t).Index)
	wantNotIn(t, "the PDF page", index, "\u202e", "\u200b")
	wantIn(t, "the PDF page", index, shown)
}

func TestHandleReport_NoPreview(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(fullReport())
	res := e.getReport(t)
	wantNotIn(t, "GET /scans/7/report", res.body, "****")
}

func TestHandleReport_StoreError(t *testing.T) {
	e := newTestEnv(t)
	e.reports.err = errors.New("fake report store failure")
	res := e.do(t, http.MethodGet, "/scans/7/report", nil, e.user(t))
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET /scans/7/report with a failing store status = %d, want 500", res.status)
	}
	wantNotIn(t, "GET /scans/7/report with a failing store", res.body, "fake report store failure")
}

// getReportPDF asks for the PDF of the report of scan 7 as a signed-in user.
func (e *testEnv) getReportPDF(t *testing.T) result {
	t.Helper()
	return e.do(t, http.MethodGet, "/scans/7/report.pdf", nil, e.user(t))
}

// lastDocument returns the document the converter was given last.
func (e *testEnv) lastDocument(t *testing.T) pdf.Document {
	t.Helper()
	docs, _ := e.converter.calls()
	if len(docs) == 0 {
		t.Fatal("the converter was not called")
	}
	return docs[len(docs)-1]
}

// wantNoConversion fails the test when the converter was called.
func (e *testEnv) wantNoConversion(t *testing.T, what string) {
	t.Helper()
	if docs, _ := e.converter.calls(); len(docs) != 0 {
		t.Errorf("%s called the converter %d times, want none", what, len(docs))
	}
}

// reportArticle returns the report block in page, from its opening tag to
// its closing tag, with the time it was made left out: two requests may fall
// in different seconds.
func reportArticle(t *testing.T, what, page string) string {
	t.Helper()
	start := strings.Index(page, `<article id="report"`)
	end := strings.Index(page, "</article>")
	if start < 0 || end < start {
		t.Fatalf("%s has no report article", what)
	}
	return generatedAt.ReplaceAllString(page[start:end+len("</article>")], "")
}

var generatedAt = regexp.MustCompile(`<dt>만든 시각</dt><dd class="mono">[^<]*</dd>`)

func TestHandleReportPDF_OK(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(fullReport())
	res := e.getReportPDF(t)
	if res.status != http.StatusOK {
		t.Fatalf("GET /scans/7/report.pdf status = %d, want 200; body:\n%s", res.status, res.body)
	}
	if got := res.header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("GET /scans/7/report.pdf Content-Type = %q, want application/pdf", got)
	}
	if got, want := res.header.Get("Content-Disposition"), `attachment; filename="report-project-3-scan-7.pdf"`; got != want {
		t.Errorf("GET /scans/7/report.pdf Content-Disposition = %q, want %q", got, want)
	}
	if got, want := res.header.Get("Content-Length"), strconv.Itoa(len(e.converter.out)); got != want {
		t.Errorf("GET /scans/7/report.pdf Content-Length = %q, want %q", got, want)
	}
	if res.body != string(e.converter.out) {
		t.Errorf("GET /scans/7/report.pdf body = %q, want the converter's %q", res.body, e.converter.out)
	}
}

func TestHandleReportPDF_Document(t *testing.T) {
	const file = "<img src=x onerror=alert(1)>.yml"
	e := newTestEnv(t)
	d := fullReport()
	d.Remaining.Rows[0].File = file
	d.Project.Name = "결제 <b>서비스</b>"
	e.reports.put(d)
	if res := e.getReportPDF(t); res.status != http.StatusOK {
		t.Fatalf("GET /scans/7/report.pdf status = %d, want 200; body:\n%s", res.status, res.body)
	}
	doc := e.lastDocument(t)
	index, footer := string(doc.Index), string(doc.Footer)

	wantIn(t, "the PDF page", index, reportSections...)
	wantIn(t, "the PDF page", index, `href="app.css"`, `<body class="print">`, `lang="ko"`, "&lt;img src=x onerror=alert(1)&gt;.yml")
	wantNotIn(t, "the PDF page", index, "/static/", "<script", "<form", "<nav", "****", "<img src=x", "PDF로 내보내기")
	wantIn(t, "the PDF footer", footer, `lang="ko"`, "secret-externalizer", "결제 &lt;b&gt;서비스&lt;/b&gt;", "scan #7",
		`class="pageNumber"`, `class="totalPages"`, `"Noto Sans CJK KR"`, "justify-content: flex-end", "text-overflow: ellipsis")
	wantNotIn(t, "the PDF footer", footer, "<b>")

	var names []string
	for _, f := range doc.Files {
		names = append(names, f.Name)
		if f.Name == "app.css" && strings.Contains(string(f.Data), "url(fonts/") {
			t.Errorf("the PDF's app.css still names url(fonts/...), want bare file names")
		}
	}
	want := []string{
		"app.css",
		"D2Coding-1.4.0-Bold.woff2", "D2Coding-1.4.0-Regular.woff2",
		"IBMPlexSansKR-1.1.0-Bold.woff2", "IBMPlexSansKR-1.1.0-Regular.woff2", "IBMPlexSansKR-1.1.0-SemiBold.woff2",
	}
	slices.Sort(names)
	slices.Sort(want)
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("the PDF's files mismatch (-want +got):\n%s", diff)
	}
}

// linkedReport is fullReport with everything that makes the report link to
// another screen: a later scan, files the scan could not look at, and an
// apply run in the history.
func linkedReport() report.Data {
	d := fullReport()
	d.Latest = 9
	d.Scan.Skipped = 2
	return d
}

var (
	plainLink = regexp.MustCompile(` <a href="[^"]*">[^<]*</a>`)
	badgeLink = regexp.MustCompile(`<a class="(badge badge-[a-z-]+)" href="[^"]*">([^<]*)</a>`)
)

// withoutLinks returns the report block as the PDF page draws it: a link
// after a sentence is left out, and a badge that links is a plain badge.
func withoutLinks(article string) string {
	article = plainLink.ReplaceAllString(article, "")
	return badgeLink.ReplaceAllString(article, `<span class="$1">$2</span>`)
}

func TestHandleReportPDF_SameAsPage(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(linkedReport())
	page := e.getReport(t)
	if res := e.getReportPDF(t); res.status != http.StatusOK {
		t.Fatalf("GET /scans/7/report.pdf status = %d, want 200; body:\n%s", res.status, res.body)
	}
	screen := reportArticle(t, "GET /scans/7/report", page.body)
	wantIn(t, "GET /scans/7/report", screen, `href="/scans/9/report"`, `href="/scans/7#unchecked"`, `href="/applies/2"`)
	want := withoutLinks(screen)
	got := reportArticle(t, "the PDF page", string(e.lastDocument(t).Index))
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the PDF page's report differs from the screen's without its links (-screen +pdf):\n%s", diff)
	}
}

func TestHandleReportPDF_NoLinks(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(linkedReport())
	if res := e.getReportPDF(t); res.status != http.StatusOK {
		t.Fatalf("GET /scans/7/report.pdf status = %d, want 200; body:\n%s", res.status, res.body)
	}
	index := string(e.lastDocument(t).Index)
	wantIn(t, "the PDF page", index, "프로젝트의 마지막 성공한 스캔은 #9입니다.", `<span class="badge badge-queued">적용 #2</span>`)
	wantNotIn(t, "the PDF page", index, "<a ", `href="/`, "스캔 화면에서 보기")
}

func TestHandleReportPDF_OneAtATime(t *testing.T) {
	e := newTestEnvWith(t, web.Limits{Store: time.Minute, PDF: 50 * time.Millisecond, Conversions: 1}, nil)
	e.reports.put(fullReport())
	e.converter.hold = make(chan struct{})
	e.converter.entered = make(chan struct{}, 2)
	user := e.user(t)

	first := make(chan result, 1)
	go func() { first <- e.do(t, http.MethodGet, "/scans/7/report.pdf", nil, user) }()
	<-e.converter.entered

	res := e.do(t, http.MethodGet, "/scans/7/report.pdf", nil, user)
	if res.status != http.StatusServiceUnavailable {
		t.Errorf("GET /scans/7/report.pdf during another conversion status = %d, want 503", res.status)
	}
	wantIn(t, "GET /scans/7/report.pdf during another conversion", res.body,
		"다른 PDF를 만드는 중입니다. 잠시 뒤 다시 시도해 주세요.")
	if docs, _ := e.converter.calls(); len(docs) != 1 {
		t.Errorf("the converter was called %d times while one conversion ran, want 1", len(docs))
	}

	close(e.converter.hold)
	if res := <-first; res.status != http.StatusOK {
		t.Errorf("GET /scans/7/report.pdf of the first request status = %d, want 200", res.status)
	}
	if res := e.do(t, http.MethodGet, "/scans/7/report.pdf", nil, user); res.status != http.StatusOK {
		t.Errorf("GET /scans/7/report.pdf after the first conversion ended status = %d, want 200", res.status)
	}
}

func TestHandleReportPDF_ConverterDown(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(fullReport())
	e.converter.err = fmt.Errorf("%w: dial tcp pdf.invalid:3000", pdf.ErrUnavailable)
	res := e.getReportPDF(t)
	if res.status != http.StatusServiceUnavailable {
		t.Errorf("GET /scans/7/report.pdf with the converter down status = %d, want 503", res.status)
	}
	if got := res.header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("GET /scans/7/report.pdf with the converter down Content-Type = %q, want text/html", got)
	}
	if got := res.header.Get("Content-Disposition"); got != "" {
		t.Errorf("GET /scans/7/report.pdf with the converter down Content-Disposition = %q, want none", got)
	}
	wantIn(t, "GET /scans/7/report.pdf with the converter down", res.body, "PDF 변환 서비스에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요.")
	wantNotIn(t, "GET /scans/7/report.pdf with the converter down", res.body, "pdf.invalid")
	if n := strings.Count(e.log.String(), `"msg":"request failed"`); n != 1 {
		t.Errorf("GET /scans/7/report.pdf with the converter down logged %d failures, want 1; log:\n%s", n, e.log)
	}
}

func TestHandleReportPDF_Rejected(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(fullReport())
	e.converter.err = fmt.Errorf("%w: status 400", pdf.ErrRejected)
	res := e.getReportPDF(t)
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET /scans/7/report.pdf with a rejected document status = %d, want 500", res.status)
	}
	if got := res.header.Get("Content-Disposition"); got != "" {
		t.Errorf("GET /scans/7/report.pdf with a rejected document Content-Disposition = %q, want none", got)
	}
	wantIn(t, "GET /scans/7/report.pdf with a rejected document", res.body,
		"PDF를 만들지 못했습니다. 리포트가 너무 커서 변환기가 인쇄하지 못했을 수 있습니다.")
}

func TestHandleReportPDF_Deadline(t *testing.T) {
	e := newTestEnvWith(t, web.Limits{Store: time.Minute, PDF: 50 * time.Millisecond}, nil)
	e.reports.put(fullReport())
	e.converter.wait = true
	res := e.getReportPDF(t)
	if res.status != http.StatusServiceUnavailable {
		t.Errorf("GET /scans/7/report.pdf with a converter that does not answer status = %d, want 503", res.status)
	}
	wantIn(t, "GET /scans/7/report.pdf with a converter that does not answer", res.body, "PDF 변환 서비스에 연결할 수 없습니다.")
	if _, deadlines := e.converter.calls(); len(deadlines) != 1 || !deadlines[0] {
		t.Errorf("the converter's contexts had deadlines %v, want one call with a deadline", deadlines)
	}
}

func TestHandleReportPDF_Redirects(t *testing.T) {
	for _, status := range []scan.Status{scan.StatusQueued, scan.StatusRunning, scan.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			e := newTestEnv(t)
			d := emptyReport()
			d.Scan.Status = status
			e.reports.put(d)
			res := e.getReportPDF(t)
			if res.status != http.StatusSeeOther || res.location() != "/scans/7" {
				t.Errorf("GET /scans/7/report.pdf of a %s scan = %d to %q, want 303 to /scans/7", status, res.status, res.location())
			}
			e.wantNoConversion(t, "GET /scans/7/report.pdf of a "+string(status)+" scan")
		})
	}
}

func TestHandleReportPDF_NotFound(t *testing.T) {
	e := newTestEnv(t)
	for _, target := range []string{"/scans/9/report.pdf", "/scans/x/report.pdf", "/scans/0/report.pdf"} {
		if res := e.do(t, http.MethodGet, target, nil, e.user(t)); res.status != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", target, res.status)
		}
	}
	e.wantNoConversion(t, "GET of an unknown scan's PDF")
}

func TestHandleReportPDF_RequiresLogin(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(emptyReport())
	res := e.do(t, http.MethodGet, "/scans/7/report.pdf", nil)
	if res.status != http.StatusSeeOther {
		t.Errorf("GET /scans/7/report.pdf without a session status = %d, want 303", res.status)
	}
	e.wantNoConversion(t, "GET /scans/7/report.pdf without a session")
	if res := e.getReportPDF(t); res.status != http.StatusOK {
		t.Errorf("GET /scans/7/report.pdf as a user status = %d, want 200", res.status)
	}
}

func TestHandleReport_PDFButton(t *testing.T) {
	e := newTestEnv(t)
	e.reports.put(fullReport())
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report", res.body, `href="/scans/7/report.pdf"`, "PDF로 내보내기")
}
