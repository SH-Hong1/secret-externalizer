package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// dashboardData is project 3 ("shop"), scanned twice with succeeded scans
// 5 and 7, and project 4 ("billing"), never scanned. Scan 7 leaves 4
// undecided, 1 held and 2 pending bundles, 4 fewer than scan 5 left; it
// has 5 externalized and 2 redacted bundles. One rule found 3 bundles, 2
// of them false positives.
func dashboardData() dashboard.Data {
	at := func(h int) time.Time { return time.Date(2026, 11, 20, h, 0, 0, 0, time.UTC) }
	return dashboard.Data{
		Projects: []dashboard.ProjectState{
			{Project: project.Project{ID: 4, Name: "billing", Path: "billing"}},
			{
				Project: project.Project{ID: 3, Name: "shop", Path: "shop"},
				Last:    scan.Scan{ID: 7, ProjectID: 3, Status: scan.StatusSucceeded, RequestedAt: at(9), FinishedAt: at(10)},
				Basis:   7, Previous: 5,
				Totals: report.Totals{
					Undecided:     report.Count{Bundles: 4, Locations: 6},
					Hold:          report.Count{Bundles: 1, Locations: 1},
					Pending:       report.Count{Bundles: 2, Locations: 3},
					FalsePositive: report.Count{Bundles: 3, Locations: 3},
					Externalized:  report.Count{Bundles: 5, Locations: 9},
					Redacted:      report.Count{Bundles: 2, Locations: 4},
				},
				PreviousTotals: report.Totals{Undecided: report.Count{Bundles: 11, Locations: 12}},
			},
		},
		Points: []dashboard.Point{
			{Project: 3, Scan: 5, At: at(6), Remaining: map[finding.Category]dashboard.Split{
				finding.CategoryStorage: {Undecided: 8}, finding.CategoryCustomerData: {Undecided: 3},
			}},
			{Project: 3, Scan: 7, At: at(10), Remaining: map[finding.Category]dashboard.Split{
				finding.CategoryStorage: {Undecided: 2, Pending: 2}, finding.CategorySSH: {Hold: 1}, finding.CategoryOther: {Undecided: 2},
			}},
		},
		PointTotal: 2,
		Rules:      []dashboard.Rule{{Tool: "opengrep", Rule: "dev-only-fake-rule", Bundles: 3, FalsePositive: 2}},
	}
}

// dashboardSegments is the number of non-zero categories over the points
// of dashboardData: 2 in scan 5 and 3 in scan 7.
const dashboardSegments = 5

// getDashboard asks for target as a signed-in user.
func (e *testEnv) getDashboard(t *testing.T, target string) result {
	t.Helper()
	return e.do(t, http.MethodGet, target, nil, e.user(t))
}

func TestHandleDashboard_OK(t *testing.T) {
	e := newTestEnv(t)
	e.dashboards.data = dashboardData()
	res := e.getDashboard(t, "/dashboard")
	if res.status != http.StatusOK {
		t.Fatalf("GET /dashboard status = %d, want 200; body:\n%s", res.status, res.body)
	}
	wantIn(t, "GET /dashboard", res.body,
		"<h1 class=\"page-title\">대시보드</h1>",
		"기간은 추이에만 적용됩니다.",
		"남은 항목", "지난 스캔 대비 -4", "미판정 4 · 보류 1 · 적용 대기 2",
		"외부화 완료", "위치 9곳", "삭제·마스킹 완료", "위치 4곳", "오탐", "보류",
		"남은 항목 추이", `x1="48" x2="952"`, `class="chart-tick" x="42"`, `y="292"`, `role="img"`, `aria-label="스캔별 남은 항목"`, "<summary>표로 보기</summary>",
		"분류별 남은 항목", `<meter class="bar"`,
		"오탐이 많은 규칙", "dev-only-fake-rule", "66%",
		"50%", "—", "스캔한 적 없음", "프로젝트 열기", `href="/projects/3"`,
		`<a class="nav-link" href="/dashboard" aria-current="page">대시보드</a>`,
	)
	// Every tile's figure is distinct, so a figure under the wrong label
	// fails.
	for _, tile := range []struct {
		label  string
		figure int
	}{
		{"남은 항목", 7}, {"외부화 완료", 5}, {"삭제·마스킹 완료", 2}, {"오탐", 3}, {"보류", 1},
	} {
		want := fmt.Sprintf("<span class=\"tile-label\">%s</span>\n<span class=\"tile-figure\">%d</span>", tile.label, tile.figure)
		if !strings.Contains(res.body, want) {
			t.Errorf("GET /dashboard lacks the tile %s with figure %d (%q)", tile.label, tile.figure, want)
		}
	}
	if got := strings.Count(res.body, "<rect"); got != dashboardSegments {
		t.Errorf("GET /dashboard draws %d <rect>, want %d", got, dashboardSegments)
	}
	if got, want := e.dashboards.calls(), []dashboard.Filter{{}}; !cmp.Equal(got, want) {
		t.Errorf("GET /dashboard store filters = %v, want %v", got, want)
	}
}

func TestHandleDashboard_Filter(t *testing.T) {
	now := time.Now()
	e := newTestEnv(t)
	e.dashboards.data = dashboardData()
	res := e.getDashboard(t, "/dashboard?project=3&period=30d")
	if res.status != http.StatusOK {
		t.Fatalf("GET /dashboard?project=3&period=30d status = %d, want 200; body:\n%s", res.status, res.body)
	}
	calls := e.dashboards.calls()
	if len(calls) != 1 {
		t.Fatalf("GET /dashboard?project=3&period=30d called the store %d times, want 1", len(calls))
	}
	got := calls[0]
	if got.Project != 3 {
		t.Errorf("GET /dashboard?project=3&period=30d filter project = %d, want 3", got.Project)
	}
	if want := now.Add(-30 * 24 * time.Hour); got.Since.Sub(want).Abs() > time.Minute {
		t.Errorf("GET /dashboard?project=3&period=30d filter since = %v, want about %v", got.Since, want)
	}
	wantIn(t, "GET /dashboard?project=3&period=30d", res.body,
		`<option value="3" selected>shop</option>`,
		`<option value="30d" selected>최근 30일</option>`,
		`<tr aria-current="true">`,
		"period=30d",
		`href="/projects/3/triage"`,
	)
	if n := strings.Count(res.body, `aria-current="true"`); n != 1 {
		t.Errorf("GET /dashboard?project=3&period=30d marks %d rows, want 1", n)
	}

	for _, target := range []string{"/dashboard", "/dashboard?project=&period="} {
		e := newTestEnv(t)
		e.dashboards.data = dashboardData()
		if res := e.getDashboard(t, target); res.status != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", target, res.status)
		}
		if got, want := e.dashboards.calls(), []dashboard.Filter{{}}; !cmp.Equal(got, want) {
			t.Errorf("GET %s store filters = %v, want %v", target, got, want)
		}
	}
}

func TestHandleDashboard_BadQuery(t *testing.T) {
	for _, target := range []string{
		"/dashboard?project=abc", "/dashboard?project=0", "/dashboard?project=-1", "/dashboard?period=1y",
	} {
		e := newTestEnv(t)
		e.dashboards.data = dashboardData()
		if res := e.getDashboard(t, target); res.status != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", target, res.status)
		}
		if n := len(e.dashboards.calls()); n != 0 {
			t.Errorf("GET %s called the store %d times, want 0", target, n)
		}
	}
	for _, tt := range []struct {
		err  error
		want int
	}{
		{db.ErrNotFound, http.StatusNotFound},
		{errors.New("dev-only-fake-failure"), http.StatusInternalServerError},
	} {
		e := newTestEnv(t)
		e.dashboards.err = tt.err
		if res := e.getDashboard(t, "/dashboard?project=9"); res.status != tt.want {
			t.Errorf("GET /dashboard?project=9 with store error %v status = %d, want %d", tt.err, res.status, tt.want)
		}
	}
}

func TestHandleDashboard_Empty(t *testing.T) {
	e := newTestEnv(t)
	res := e.getDashboard(t, "/dashboard")
	if res.status != http.StatusOK {
		t.Fatalf("GET /dashboard with no project status = %d, want 200", res.status)
	}
	wantIn(t, "GET /dashboard with no project", res.body, "등록된 프로젝트가 없습니다.", `href="/projects/new"`)
	wantNotIn(t, "GET /dashboard with no project", res.body, "<svg", "지난 스캔 대비")
}

func TestHandleDashboard_NoScans(t *testing.T) {
	e := newTestEnv(t)
	e.dashboards.data = dashboard.Data{Projects: []dashboard.ProjectState{{Project: project.Project{ID: 4, Name: "billing", Path: "billing"}}}}
	res := e.getDashboard(t, "/dashboard")
	wantIn(t, "GET /dashboard with no scan", res.body,
		"성공한 스캔이 없습니다. 프로젝트를 스캔하면 숫자가 보입니다.", "스캔한 적 없음", "billing")
	wantNotIn(t, "GET /dashboard with no scan", res.body, "<svg", "지난 스캔 대비", "분류별 남은 항목", "오탐이 많은 규칙")

	e = newTestEnv(t)
	d := dashboardData()
	for i := range d.Points {
		d.Points[i].Baseline = true
	}
	d.PointTotal = 0
	e.dashboards.data = d
	res = e.getDashboard(t, "/dashboard?period=7d")
	wantIn(t, "GET /dashboard?period=7d with no scan in the period", res.body,
		"지난 스캔 대비 -4", "이 기간에 끝난 스캔이 없습니다.", "분류별 남은 항목", "오탐이 많은 규칙")
	wantNotIn(t, "GET /dashboard?period=7d with no scan in the period", res.body, "<svg")
}

func TestHandleDashboard_Escapes(t *testing.T) {
	const name, rule = "<script>alert(1)</script>", "<b>x</b>"
	e := newTestEnv(t)
	d := dashboardData()
	d.Projects[1].Project.Name = name
	d.Rules[0].Rule = rule
	e.dashboards.data = d
	res := e.getDashboard(t, "/dashboard?project=3")
	if res.status != http.StatusOK {
		t.Fatalf("GET /dashboard status = %d, want 200", res.status)
	}
	wantNotIn(t, "GET /dashboard", res.body, name, rule)
	wantIn(t, "GET /dashboard", res.body,
		"<title>&lt;script&gt;alert(1)&lt;/script&gt; · 스캔 #",
		">&lt;script&gt;alert(1)&lt;/script&gt;</option>",
		"&lt;b&gt;x&lt;/b&gt;",
	)
}

func TestHandleDashboard_Roles(t *testing.T) {
	for _, u := range []auth.User{
		{Subject: "fake-subject-user", Name: "dev-user", Roles: []auth.Role{auth.RoleUser}},
		{Subject: "fake-subject-admin", Name: "dev-admin", Roles: []auth.Role{auth.RoleAdmin}},
	} {
		e := newTestEnv(t)
		e.dashboards.data = dashboardData()
		if res := e.do(t, http.MethodGet, "/dashboard", nil, e.signIn(t, u)); res.status != http.StatusOK {
			t.Errorf("GET /dashboard as %s status = %d, want 200", u.Name, res.status)
		}
	}
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/dashboard", nil)
	if res.status != http.StatusSeeOther {
		t.Errorf("GET /dashboard without a session status = %d, want 303", res.status)
	}
	if want := "/login?return_to=%2Fdashboard"; res.location() != want {
		t.Errorf("GET /dashboard without a session Location = %q, want %q", res.location(), want)
	}
}

func TestHandleDashboard_NoInlineStyle(t *testing.T) {
	e := newTestEnv(t)
	e.dashboards.data = dashboardData()
	res := e.getDashboard(t, "/dashboard")
	if strings.Contains(res.body, "style=") {
		t.Errorf("GET /dashboard has a style attribute")
	}
	for _, m := range regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`).FindAllStringSubmatch(res.body, -1) {
		if strings.TrimSpace(m[1]) != "" {
			t.Errorf("GET /dashboard has an inline script %q", m[1])
		}
	}
}

// Every wide thing on the dashboard scrolls inside its own wrapper, so the
// page itself never scrolls sideways.
func TestHandleDashboard_ScrollsInsideWrappers(t *testing.T) {
	e := newTestEnv(t)
	e.dashboards.data = dashboardData()
	res := e.getDashboard(t, "/dashboard")
	if res.status != http.StatusOK {
		t.Fatalf("GET /dashboard status = %d, want 200", res.status)
	}
	if got, want := strings.Count(res.body, `<div class="chart-scroll">`), 1; got != want {
		t.Errorf("GET /dashboard has %d chart-scroll wrappers, want %d", got, want)
	}
	if !regexp.MustCompile(`(?s)<div class="chart-scroll">\s*<svg class="chart"`).MatchString(res.body) {
		t.Errorf("GET /dashboard does not wrap the chart directly in chart-scroll")
	}
	tables := strings.Count(res.body, "<table")
	wraps := strings.Count(res.body, `<div class="table-wrap">`)
	if got := strings.Count(res.body, `<table class="table table-nowrap`); got != tables {
		t.Errorf("GET /dashboard has %d table-nowrap tables, want %d", got, tables)
	}
	if tables != wraps || tables != 3 {
		t.Errorf("GET /dashboard has %d tables and %d table-wrap, want 3 and 3", tables, wraps)
	}
}
