package web_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
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
	d.PreviousTotals.Found = report.Count{Bundles: 82, Locations: 101}
	d.Changes = []report.Change{
		{Run: 2, Item: apply.Item{
			BundleID: 11, File: "src/main/resources/application-prod.yml", StartLine: 14, Line: 14, Action: apply.ActionReplace,
			Category: finding.CategoryStorage, Kind: finding.KindPassword, Name: "ext.db.main.password",
			StorePath: "secret/data/projects/3/ext.db.main.password", Tracked: true, CodeChange: true, Status: apply.StatusApplied,
		}},
		{Run: 2, Item: apply.Item{
			BundleID: 12, File: "keys/deploy.pem", Action: apply.ActionRemoveFile,
			Category: finding.CategorySSH, Kind: finding.KindKeyFile, Name: "EXT_SSH_DEPLOY_KEY",
			StorePath: "secret/data/projects/3/ext.ssh.deploy.key", Tracked: true, Status: apply.StatusApplied,
		}},
	}
	d.Redactions = []report.Redaction{
		{Kind: finding.KindPhone, File: "logs/app.log", Untracked: true, Bundles: 2, Locations: 5},
	}
	decided := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	d.Lines = []report.Line{
		{
			Bundle: 21, Category: finding.CategoryPartnerSession, Kind: finding.KindToken, File: "web/login.jsp", FirstLine: 3,
			Files: 1, Bundles: 1, Locations: 1, Frontend: true,
		},
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
		{
			Bundle: 23, Category: finding.CategoryOther, Kind: finding.KindOther, File: "test/Fixture.java", FirstLine: 20,
			Files: 1, Bundles: 1, Locations: 1, Verdict: triage.VerdictFalsePositive, Note: "test fixture", DecidedAt: decided,
		},
	}
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
		"-70",
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
	wantNotIn(t, "GET /scans/7/report without a previous scan", res.body, "-70", `class="delta"`)
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
	for i := range 1005 {
		d.Lines = append(d.Lines, report.Line{
			Bundle: triage.BundleID(i + 1), Category: finding.CategoryOther, Kind: finding.KindToken,
			File: "conf/a.properties", FirstLine: i + 1, Files: 1, Bundles: 1, Locations: 1,
		})
	}
	e.reports.put(d)
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report with 1005 remaining lines", res.body, "처음 1000줄만 싣습니다. 외 5줄.")
}

func TestHandleReport_Escapes(t *testing.T) {
	const (
		file = "<img src=x onerror=alert(1)>.yml"
		note = "</td><script>alert(1)</script>"
	)
	e := newTestEnv(t)
	d := fullReport()
	d.Lines[1].File, d.Lines[1].Note = file, note
	d.Changes[0].Item.File = file
	d.Redactions[0].File = file
	d.Events[0].File = file
	d.Events[0].Detail[audit.DetailNote] = note
	e.reports.put(d)
	res := e.getReport(t)
	wantIn(t, "GET /scans/7/report with markup in names", res.body,
		"&lt;img src=x onerror=alert(1)&gt;.yml", "&lt;/td&gt;&lt;script&gt;alert(1)&lt;/script&gt;")
	wantNotIn(t, "GET /scans/7/report with markup in names", res.body, "<script>alert", "<img src=x")
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
