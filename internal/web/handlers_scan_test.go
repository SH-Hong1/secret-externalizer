package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

const refreshTag = `<meta http-equiv="refresh" content="3">`

// postScan sends a form-encoded POST to target with the same-origin header.
func (e *testEnv) postScan(t *testing.T, target string, cookies ...cookie) result {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, c := range cookies {
		req.Header.Add("Cookie", c.name+"="+c.value)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return result{status: rec.Code, header: rec.Header(), cookies: (&http.Response{Header: rec.Header()}).Cookies(), body: rec.Body.String()}
}

func (e *testEnv) newProject(t *testing.T) project.ID {
	t.Helper()
	id, err := e.projects.CreateProject(t.Context(), project.Project{Name: "shop", Path: "shop"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	return id
}

func TestScan_RequiresLogin(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/scans/1", nil)
	if res.status != http.StatusSeeOther {
		t.Errorf("GET /scans/1 without a session status = %d, want 303", res.status)
	}
	res = e.postScan(t, "/projects/1/scans")
	if res.status != http.StatusUnauthorized {
		t.Errorf("POST /projects/1/scans without a session status = %d, want 401", res.status)
	}
	if got := len(e.scans.all()); got != 0 {
		t.Errorf("scans stored by an anonymous POST = %d, want 0", got)
	}
}

func TestProject_HasScanButton(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	res := e.do(t, http.MethodGet, "/projects/"+itoa(pid), nil, e.user(t))
	if want := `<form method="post" action="/projects/` + itoa(pid) + `/scans">`; !strings.Contains(res.body, want) {
		t.Errorf("GET /projects/%d body lacks %q", pid, want)
	}
}

func TestScanCreate_Redirects(t *testing.T) {
	e := newTestEnv(t)
	c := e.user(t)
	pid := e.newProject(t)
	target := "/projects/" + itoa(pid) + "/scans"

	res := e.postScan(t, target, c)
	if res.status != http.StatusSeeOther || res.location() != "/scans/1" {
		t.Fatalf("POST %s = %d to %q, want 303 to /scans/1", target, res.status, res.location())
	}
	want := []scan.Scan{{ID: 1, ProjectID: pid, Status: scan.StatusQueued, RequestedBy: "fake-subject", RequesterName: "Fake User"}}
	if diff := cmp.Diff(want, e.scans.all()); diff != "" {
		t.Errorf("stored scans mismatch (-want +got):\n%s", diff)
	}

	// A scan is already active, so the second request goes to the same page.
	res = e.postScan(t, target, c)
	if res.status != http.StatusSeeOther || res.location() != "/scans/1" {
		t.Errorf("second POST %s = %d to %q, want 303 to /scans/1", target, res.status, res.location())
	}
	if got := len(e.scans.all()); got != 1 {
		t.Errorf("scans after a second request = %d, want 1", got)
	}
}

func TestScanCreate_Errors(t *testing.T) {
	tests := []struct {
		name   string
		target string
		err    error
		want   int
	}{
		{"unknown project", "/projects/9/scans", db.ErrNotFound, http.StatusNotFound},
		{"bad id", "/projects/abc/scans", nil, http.StatusNotFound},
		{"store failure", "/projects/1/scans", errors.New("fake store failure"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.scans.requestErr = tt.err
			res := e.postScan(t, tt.target, e.user(t))
			if res.status != tt.want {
				t.Errorf("POST %s status = %d, want %d", tt.target, res.status, tt.want)
			}
		})
	}
}

func TestScanCreate_CrossOrigin(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/projects/"+itoa(pid)+"/scans", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	c := e.user(t)
	req.Header.Add("Cookie", c.name+"="+c.value)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST scan status = %d, want 403", rec.Code)
	}
	if got := len(e.scans.all()); got != 0 {
		t.Errorf("cross-site POST stored %d scans, want 0", got)
	}
}

func TestScan_RefreshesWhileActive(t *testing.T) {
	tests := []struct {
		status scan.Status
		want   bool
	}{
		{scan.StatusQueued, true},
		{scan.StatusRunning, true},
		{scan.StatusSucceeded, false},
		{scan.StatusFailed, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			e := newTestEnv(t)
			pid := e.newProject(t)
			e.scans.put(scan.Scan{ID: 7, ProjectID: pid, Status: tt.status})
			res := e.do(t, http.MethodGet, "/scans/7", nil, e.user(t))
			if res.status != http.StatusOK {
				t.Fatalf("GET /scans/7 status = %d, want 200", res.status)
			}
			if got := strings.Contains(res.body, refreshTag); got != tt.want {
				t.Errorf("GET /scans/7 with status %q has the refresh tag = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestScan_ShowsFailureReason(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 3, ProjectID: pid, Status: scan.StatusFailed, FailureReason: `<script>alert("x")</script>`})

	res := e.do(t, http.MethodGet, "/scans/3", nil, e.user(t))

	if res.status != http.StatusOK {
		t.Fatalf("GET /scans/3 status = %d, want 200", res.status)
	}
	if strings.Contains(res.body, "<script>") {
		t.Errorf("GET /scans/3 body has an unescaped reason: %q", res.body)
	}
	if want := "&lt;script&gt;"; !strings.Contains(res.body, want) {
		t.Errorf("GET /scans/3 body lacks the escaped reason %q", want)
	}
	if want := `href="/projects/` + strconv.Itoa(int(pid)) + `"`; !strings.Contains(res.body, want) {
		t.Errorf("GET /scans/3 body lacks the project link %q", want)
	}
}

func TestScan_Errors(t *testing.T) {
	tests := []struct {
		name   string
		target string
		err    error
		want   int
	}{
		{"unknown scan", "/scans/99", nil, http.StatusNotFound},
		{"bad id", "/scans/abc", nil, http.StatusNotFound},
		{"store failure", "/scans/1", errors.New("fake store failure"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.scans.getErr = tt.err
			res := e.do(t, http.MethodGet, tt.target, nil, e.user(t))
			if res.status != tt.want {
				t.Errorf("GET %s status = %d, want %d", tt.target, res.status, tt.want)
			}
		})
	}
}

// succeeded stores a succeeded scan with the category counts, makes it the
// latest succeeded scan of its project, and returns its page.
func (e *testEnv) succeeded(t *testing.T, sc scan.Scan, counts ...scan.CategoryCount) string {
	t.Helper()
	sc.ID = 5
	sc.ProjectID = e.newProject(t)
	sc.Status = scan.StatusSucceeded
	e.scans.put(sc)
	e.scans.putCounts(sc.ID, counts...)
	e.triage.setScan(sc, map[triage.Verdict]int{"": 96, triage.VerdictHold: 4})
	res := e.do(t, http.MethodGet, "/scans/5", nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /scans/5 status = %d, want 200", res.status)
	}
	return res.body
}

func wantContains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q; page:\n%s", want, body)
		}
	}
}

func wantAbsent(t *testing.T, body string, bads ...string) {
	t.Helper()
	for _, bad := range bads {
		if strings.Contains(body, bad) {
			t.Errorf("page has %q, want it absent; page:\n%s", bad, body)
		}
	}
}

func TestScan_CountsByCategory(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Files: 30, Findings: 3},
		scan.CategoryCount{Category: finding.CategoryCustomerData, Findings: 1, Locations: 37},
		scan.CategoryCount{Category: finding.CategoryStorage, Findings: 2, Locations: 3},
	)

	wantContains(t, body,
		"찾은 항목 3개(위치 40곳)",
		`<span class="bar-label" id="count-storage-credential">저장소 접속 정보</span><meter class="bar" min="0" max="3" value="2" aria-labelledby="count-storage-credential"></meter><span class="bar-count">2</span>`,
		`<span class="bar-label" id="count-customer-data">고객정보</span><meter class="bar" min="0" max="3" value="1" aria-labelledby="count-customer-data"></meter><span class="bar-count">1</span>`,
	)
	if s, c := strings.Index(body, "count-storage-credential"), strings.Index(body, "count-customer-data"); s > c {
		t.Errorf("GET /scans/5 lists customer data before storage credentials, want the triage screen's category order")
	}
	// The findings themselves are on the triage screen.
	wantAbsent(t, body, "<pre", "<table", "code-box")
}

func TestScan_LinksToTriage(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Findings: 1}, scan.CategoryCount{Category: finding.CategoryStorage, Findings: 1, Locations: 1})

	wantContains(t, body, `<a class="btn btn-primary btn-lg" href="/projects/1/triage">판정하기 · 미판정 96</a>`)
	wantAbsent(t, body, "이후 스캔이 있습니다")
}

func TestScan_LaterScanHasNoTriageLink(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 5, ProjectID: pid, Status: scan.StatusSucceeded, Findings: 1})
	e.scans.putCounts(5, scan.CategoryCount{Category: finding.CategoryStorage, Findings: 1, Locations: 1})
	e.triage.setScan(scan.Scan{ID: 8, ProjectID: pid, Status: scan.StatusSucceeded}, nil)

	res := e.do(t, http.MethodGet, "/scans/5", nil, e.user(t))

	if res.status != http.StatusOK {
		t.Fatalf("GET /scans/5 status = %d, want 200", res.status)
	}
	wantContains(t, res.body, "이후 스캔이 있습니다", `href="/scans/8"`)
	wantAbsent(t, res.body, "/triage", "판정하기")
}

func TestScan_ShowsSkippedFiles(t *testing.T) {
	kept := make([]string, 0, scan.MaxSkippedFiles)
	for i := range scan.MaxSkippedFiles {
		kept = append(kept, fmt.Sprintf("bin/fake-%03d.jar", i))
	}
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Files: 300, Skipped: 250, SkippedFiles: kept})

	wantContains(t, body, "<details", "건너뛴 파일 250개", "<li>bin/fake-000.jar</li>", "<li>bin/fake-199.jar</li>", "처음 200개만 보여 줍니다. 외 50개.")
	if d, l := strings.Index(body, "<details"), strings.Index(body, "bin/fake-000.jar"); d < 0 || l < d || strings.Index(body, "</details>") < l {
		t.Errorf("GET /scans/5 does not list the skipped files inside <details>")
	}
}

func TestScan_ShowsSkippedFilesWithoutNames(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Skipped: 3, SkippedFiles: []string{"lib/fake.jar"}})
	wantContains(t, body, "<li>lib/fake.jar</li>", "외 2개", "이름이 UTF-8이 아니거나")
	wantAbsent(t, body, "처음 200개만", "이름을 남기지 않았습니다")
}

func TestScan_ShowsSkippedWithNoNamesKept(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Skipped: 3})
	wantContains(t, body, "건너뛴 파일 3개", "이 스캔은 건너뛴 파일과 폴더의 이름을 남기지 않았습니다.")
	wantAbsent(t, body, "외 3개", "<ul class=\"skips-list\">")
}

func TestScan_EscapesSkippedFiles(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Skipped: 1, SkippedFiles: []string{`<script>alert("x")</script>.bin`}})
	wantAbsent(t, body, "<script>")
	wantContains(t, body, "&lt;script&gt;")
}

func TestScan_ShowsScannerErrors(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{ScannerErrors: map[string]int{"opengrep": 2, "betterleaks": 1}})
	wantContains(t, body, "검사하지 못한 것", `스캐너 오류 <span class="mono">betterleaks</span> 1건 · <span class="mono">opengrep</span> 2건.`, "일부 파일을 끝까지 분석하지 못했습니다")

	e = newTestEnv(t)
	body = e.succeeded(t, scan.Scan{})
	wantAbsent(t, body, "스캐너 오류", "일부 파일을 끝까지 분석하지 못했습니다")
}

func TestScan_ShowsSkippedAndDropped(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Files: 50, Skipped: 4, Dropped: 3})
	wantContains(t, body, "<dt>찾은 파일</dt><dd>50개 <span class=\"muted\">읽지 못해 건너뛴 파일도 셉니다</span></dd>",
		"건너뛴 파일 4개", "위치가 맞지 않아 버린 결과 3건")
	wantAbsent(t, body, "검사한 파일")

	e = newTestEnv(t)
	body = e.succeeded(t, scan.Scan{Files: 50})
	wantContains(t, body, "<dt>찾은 파일</dt><dd>50개</dd>")
	wantAbsent(t, body, "검사하지 못한 것", "건너뛴 파일", "버린 결과")
}

func TestScan_ShowsHeader(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{
		RequestedBy:   "fake-subject-7",
		RequesterName: "fake-user-7",
		RequestedAt:   time.Date(2026, 11, 19, 9, 5, 7, 0, time.FixedZone("KST", 9*3600)),
		CommitHash:    "0123abcd",
		RulesVersion:  "rules-v9",
		ToolVersions:  map[string]string{"opengrep": "2.0.0", "betterleaks": "1.10.1"},
	})

	wantContains(t, body, "fake-user-7", `<span class="mono">2026-11-19 00:05:07 UTC</span>`, "0123abcd", "rules-v9", "betterleaks 1.10.1", "opengrep 2.0.0")
	wantAbsent(t, body, "fake-subject-7")
	if b, o := strings.Index(body, "betterleaks 1.10.1"), strings.Index(body, "opengrep 2.0.0"); b > o {
		t.Errorf("tool versions are not ordered by tool name")
	}

	e = newTestEnv(t)
	body = e.succeeded(t, scan.Scan{RequestedBy: "fake-subject-8"})
	wantContains(t, body, `<dt>커밋</dt><dd class="mono"><span class="muted">없음</span></dd>`, "fake-subject-8")
}

func TestScan_Empty(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Files: 3})

	wantContains(t, body, "탐지된 항목이 없습니다.")
}

func TestScan_FailedShowsRequester(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{
		ID: 3, ProjectID: pid, Status: scan.StatusFailed, FailureReason: "fake failure",
		RequestedBy: "fake-subject-3", RequesterName: "fake-user-3",
		RequestedAt: time.Date(2026, 11, 18, 9, 40, 2, 0, time.UTC),
		FinishedAt:  time.Date(2026, 11, 18, 9, 40, 19, 0, time.UTC),
	})
	res := e.do(t, http.MethodGet, "/scans/3", nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /scans/3 status = %d, want 200", res.status)
	}
	wantContains(t, res.body, "fake-user-3", "2026-11-18 09:40:02 UTC", "2026-11-18 09:40:19 UTC")
}

func TestScan_ActiveLinksToTriage(t *testing.T) {
	for _, st := range []scan.Status{scan.StatusQueued, scan.StatusRunning} {
		t.Run(string(st), func(t *testing.T) {
			e := newTestEnv(t)
			pid := e.newProject(t)
			e.scans.put(scan.Scan{ID: 9, ProjectID: pid, Status: st, RequesterName: "fake-user-9"})
			res := e.do(t, http.MethodGet, "/scans/9", nil, e.user(t))
			wantContains(t, res.body, "fake-user-9")
			wantAbsent(t, res.body, "/triage")

			e.triage.setScan(scan.Scan{ID: 4, ProjectID: pid, Status: scan.StatusSucceeded}, nil)
			res = e.do(t, http.MethodGet, "/scans/9", nil, e.user(t))
			wantContains(t, res.body, "지난 스캔 기준으로 판정할 수 있습니다", `href="/projects/`+itoa(pid)+`/triage"`)
		})
	}
}

func TestScan_NoFindingsSectionWhileActive(t *testing.T) {
	for _, st := range []scan.Status{scan.StatusQueued, scan.StatusRunning, scan.StatusFailed} {
		t.Run(string(st), func(t *testing.T) {
			e := newTestEnv(t)
			pid := e.newProject(t)
			e.scans.put(scan.Scan{ID: 2, ProjectID: pid, Status: st})
			e.scans.putCounts(2, scan.CategoryCount{Category: finding.CategoryStorage, Findings: 1, Locations: 1})
			res := e.do(t, http.MethodGet, "/scans/2", nil, e.user(t))

			wantAbsent(t, res.body, "탐지된 항목이 없습니다.", "찾은 항목", "판정하기")
		})
	}
}

func TestScan_StoreFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *testEnv)
	}{
		{"counts", func(e *testEnv) { e.scans.countsErr = errors.New("fake store failure") }},
		{"latest scan", func(e *testEnv) { e.triage.latestErr = errors.New("fake store failure") }},
		{"entries", func(e *testEnv) { e.triage.entriesErr = errors.New("fake store failure") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			pid := e.newProject(t)
			sc := scan.Scan{ID: 2, ProjectID: pid, Status: scan.StatusSucceeded}
			e.scans.put(sc)
			e.triage.setScan(sc, nil)
			tt.setup(e)
			res := e.do(t, http.MethodGet, "/scans/2", nil, e.user(t))
			if res.status != http.StatusInternalServerError {
				t.Errorf("GET /scans/2 with a failing %s read status = %d, want 500", tt.name, res.status)
			}
		})
	}
}

func TestProject_ListsScans(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 1, ProjectID: pid, Status: scan.StatusFailed, RequestedAt: time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC)})
	e.scans.put(scan.Scan{ID: 2, ProjectID: pid, Status: scan.StatusSucceeded, Findings: 17, RequestedBy: "fake-subject-2", RequesterName: "fake-user-2", RequestedAt: time.Date(2026, 11, 19, 4, 5, 6, 0, time.UTC)})
	e.scans.put(scan.Scan{ID: 3, ProjectID: pid + 1, Status: scan.StatusSucceeded, Findings: 99})

	res := e.do(t, http.MethodGet, "/projects/"+itoa(pid), nil, e.user(t))

	wantContains(t, res.body, `href="/scans/1"`, `href="/scans/2"`, `<span class="mono">2026-11-19 04:05:06 UTC</span>`, `<span class="mono">2026-11-18 01:02:03 UTC</span>`, "완료", "실패", "17개", "fake-user-2")
	wantAbsent(t, res.body, `href="/scans/3"`, "99개", "fake-subject-2")
	if i, j := strings.Index(res.body, `href="/scans/2"`), strings.Index(res.body, `href="/scans/1"`); i > j {
		t.Errorf("project page lists scan 1 before scan 2, want newest first")
	}
}

func TestProject_NoScans(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	res := e.do(t, http.MethodGet, "/projects/"+itoa(pid), nil, e.user(t))
	wantContains(t, res.body, "스캔한 적이 없습니다.")
}

func TestProject_ScanListFailure(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.listErr = errors.New("fake store failure")
	res := e.do(t, http.MethodGet, "/projects/"+itoa(pid), nil, e.user(t))
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET /projects/%d with a failing scan list status = %d, want 500", pid, res.status)
	}
}

func TestScan_NamesProject(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 4, ProjectID: pid, Status: scan.StatusQueued})
	res := e.do(t, http.MethodGet, "/scans/4", nil, e.user(t))
	wantContains(t, res.body, `<a href="/projects/`+itoa(pid)+`">shop</a>`)
}

func TestScan_ProjectFailure(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 4, ProjectID: pid, Status: scan.StatusQueued})
	e.projects.getErr = errors.New("fake store failure")
	res := e.do(t, http.MethodGet, "/scans/4", nil, e.user(t))
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET /scans/4 with a failing project read status = %d, want 500", res.status)
	}
}

func TestScanCreate_WhileApplying(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.requestErr = fmt.Errorf("requesting scan: %w", db.ErrApplyActive)
	target := "/projects/" + itoa(pid) + "/scans"
	res := e.postScan(t, target, e.user(t))
	if res.status != http.StatusConflict {
		t.Fatalf("POST %s while an apply is active status = %d, want 409", target, res.status)
	}
	wantIn(t, "POST "+target, res.body, "이 프로젝트의 적용이 진행 중이라 스캔을 시작할 수 없습니다")
}

func TestScanPage_Confirmation(t *testing.T) {
	const unchecked = "이 스캔이 검사한 범위에서는 외부화한 값이 다시 발견되지 않았습니다. 건너뛴 파일이나 버린 결과가 있습니다."
	for _, tt := range []struct {
		name       string
		scan       scan.Scan
		reappeared int
		want       []string
		wantNot    []string
	}{
		{
			name: "values found again", scan: scan.Scan{Status: scan.StatusSucceeded}, reappeared: 3,
			want:    []string{"외부화한 묶음의 값이 3개 항목에서 다시 발견되었습니다", `href="/projects/1/triage?status=applied"`},
			wantNot: []string{"외부화한 값이 모두 사라졌습니다", unchecked},
		},
		{
			name: "all gone", scan: scan.Scan{Status: scan.StatusSucceeded},
			want:    []string{"외부화한 값이 모두 사라졌습니다"},
			wantNot: []string{unchecked, "다시 발견되었습니다"},
		},
		{
			name: "skipped files", scan: scan.Scan{Status: scan.StatusSucceeded, Skipped: 1},
			want:    []string{unchecked, `href="/scans/8#unchecked"`, `id="unchecked"`},
			wantNot: []string{"외부화한 값이 모두 사라졌습니다"},
		},
		{
			name: "dropped results", scan: scan.Scan{Status: scan.StatusSucceeded, Dropped: 2},
			want: []string{unchecked},
		},
		{
			name: "scanner errors", scan: scan.Scan{Status: scan.StatusSucceeded, ScannerErrors: map[string]int{"opengrep": 1}},
			want: []string{unchecked},
		},
		{
			name: "running", scan: scan.Scan{Status: scan.StatusRunning},
			want:    []string{"확인 스캔이 진행 중입니다"},
			wantNot: []string{"외부화한 값이 모두 사라졌습니다"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			pid := e.newProject(t)
			sc := tt.scan
			sc.ID, sc.ProjectID, sc.ApplyID = 8, pid, 4
			e.scans.put(sc)
			if sc.Status == scan.StatusSucceeded {
				e.triage.setScan(sc, map[triage.Verdict]int{})
			}
			e.applies.putReappeared(8, tt.reappeared)
			res := e.do(t, http.MethodGet, "/scans/8", nil, e.user(t))
			if res.status != http.StatusOK {
				t.Fatalf("GET /scans/8 status = %d, want 200", res.status)
			}
			wantIn(t, "GET /scans/8", res.body, append([]string{`<a href="/applies/4">적용 #4</a>의 확인 스캔`}, tt.want...)...)
			wantNotIn(t, "GET /scans/8", res.body, tt.wantNot...)
		})
	}
	t.Run("a user's scan", func(t *testing.T) {
		e := newTestEnv(t)
		pid := e.newProject(t)
		e.scans.put(scan.Scan{ID: 8, ProjectID: pid, Status: scan.StatusSucceeded})
		res := e.do(t, http.MethodGet, "/scans/8", nil, e.user(t))
		wantNotIn(t, "GET /scans/8", res.body, "확인 스캔", "/applies/")
	})
}
