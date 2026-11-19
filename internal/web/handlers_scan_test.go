package web_test

import (
	"errors"
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
	want := []scan.Scan{{ID: 1, ProjectID: pid, Status: scan.StatusQueued, RequestedBy: "fake-subject"}}
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

// succeeded stores a succeeded scan with the findings and returns its page.
func (e *testEnv) succeeded(t *testing.T, sc scan.Scan, fs ...finding.Finding) string {
	t.Helper()
	sc.ID = 5
	sc.ProjectID = e.newProject(t)
	sc.Status = scan.StatusSucceeded
	e.scans.put(sc)
	e.scans.putFindings(sc.ID, fs...)
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

func secretFinding() finding.Finding {
	return finding.Finding{
		Fingerprint: "fake-fingerprint-1",
		File:        "conf/app.properties",
		Category:    finding.CategoryStorage,
		Kind:        finding.KindPassword,
		Preview:     "dev-****",
		Locations: []finding.Location{{
			StartLine: 12, StartCol: 10, EndLine: 12, EndCol: 30,
			Context:          []string{"# settings", "db.password=****", "  indented = 1"},
			ContextFirstLine: 11,
			Detections:       []finding.Detection{{Tool: "betterleaks", Rule: "fake-rule-a"}, {Tool: "opengrep", Rule: "fake-rule-b"}},
		}},
	}
}

func TestScan_ListsFindings(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Files: 30, Findings: 1}, secretFinding())

	wantContains(t, body,
		"conf/app.properties", "12", "dev-****",
		"betterleaks", "fake-rule-a", "opengrep", "fake-rule-b",
		"저장소 접속 정보", "비밀번호",
		"db.password=****", `<pre class="code">`,
		"찾은 항목 1개",
	)
}

func TestScan_LabelsPort(t *testing.T) {
	f := secretFinding()
	f.Kind, f.Preview = finding.KindPort, "****"
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Findings: 1}, f)

	wantContains(t, body, "포트")
}

func TestScan_ShowsContextLineNumbers(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Findings: 1}, secretFinding())

	wantContains(t, body,
		`<span class="code-line"><span class="line-no">11</span># settings</span>`,
		`<mark class="code-line code-line-hit"><span class="sr-only">탐지된 줄 </span><span class="line-no">12</span>db.password=****</mark>`,
		`<span class="code-line"><span class="line-no">13</span>  indented = 1</span>`,
	)
}

func TestScan_NoContextNote(t *testing.T) {
	f := secretFinding()
	f.Locations[0].Context = nil
	f.Locations[0].ContextFirstLine = 0
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Findings: 1}, f)

	wantContains(t, body, "주변 줄을 저장하지 않았습니다.", "conf/app.properties")
	wantAbsent(t, body, "<pre")
}

func TestScan_GroupsCustomerData(t *testing.T) {
	var fs []finding.Finding
	for i := range 37 {
		fs = append(fs, finding.Finding{
			Fingerprint: "fake-phone-" + strconv.Itoa(i),
			File:        "logs/app.log",
			Category:    finding.CategoryCustomerData,
			Kind:        finding.KindPhone,
			Preview:     "010-****-****",
			Locations:   []finding.Location{{StartLine: 100 - i, EndLine: 100 - i}},
		})
	}
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Findings: 37}, fs...)

	wantContains(t, body, "logs/app.log", "전화번호", "37건", "첫 줄 64", "010-****-****")
	if got := strings.Count(body, "logs/app.log"); got != 1 {
		t.Errorf("page names logs/app.log %d times, want 1 (one line per file and kind)", got)
	}
	wantAbsent(t, body, "<pre", "주변 줄을 저장하지 않았습니다.")
}

func TestScan_CountsByCategory(t *testing.T) {
	a, b := secretFinding(), secretFinding()
	b.Fingerprint, b.Category = "fake-fingerprint-2", finding.CategorySSH
	c := finding.Finding{
		Fingerprint: "fake-3", File: "x.csv", Category: finding.CategoryCustomerData, Kind: finding.KindEmail,
		Preview: "****@****", Locations: []finding.Location{{StartLine: 1, EndLine: 1}, {StartLine: 2, EndLine: 2}},
	}
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Findings: 3}, a, b, c)

	wantContains(t, body,
		`<span class="bar-label" id="count-storage-credential">저장소 접속 정보</span><meter class="bar" min="0" max="3" value="1" aria-labelledby="count-storage-credential"></meter><span class="bar-count">1</span>`,
		`<span class="bar-label" id="count-ssh-credential">SSH 접속 정보</span><meter class="bar" min="0" max="3" value="1" aria-labelledby="count-ssh-credential"></meter><span class="bar-count">1</span>`,
		`<span class="bar-label" id="count-customer-data">고객정보</span><meter class="bar" min="0" max="3" value="1" aria-labelledby="count-customer-data"></meter><span class="bar-count">1</span>`,
		"찾은 항목 3개",
	)
}

func TestScan_EscapesContext(t *testing.T) {
	f := secretFinding()
	f.File = `<b>dir</b>/a.js`
	f.Preview = `<i>x</i>`
	f.Locations[0].Context = []string{`<script>alert("x")</script>`}
	f.Locations[0].ContextFirstLine = 12
	f.Locations[0].Detections = []finding.Detection{{Tool: "<tool>", Rule: "<rule>"}}
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Findings: 1}, f)

	wantAbsent(t, body, "<script>", "<b>dir", "<i>x", "<tool>", "<rule>")
	wantContains(t, body, "&lt;script&gt;", "&lt;b&gt;dir", "&lt;rule&gt;")
}

func TestScan_ShowsSkippedAndDropped(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Files: 50, Skipped: 4, Dropped: 3})
	wantContains(t, body, "<dt>검사한 파일</dt><dd>50개</dd>", "건너뛴 파일 4개", "위치가 맞지 않아 버린 결과 3건")

	e = newTestEnv(t)
	body = e.succeeded(t, scan.Scan{Files: 50})
	wantContains(t, body, "<dt>검사한 파일</dt><dd>50개</dd>")
	wantAbsent(t, body, "검사하지 못한 것", "건너뛴 파일", "버린 결과")
}

func TestScan_ShowsHeader(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{
		RequestedBy:  "fake-subject-7",
		RequestedAt:  time.Date(2026, 11, 19, 9, 5, 7, 0, time.FixedZone("KST", 9*3600)),
		CommitHash:   "0123abcd",
		RulesVersion: "rules-v9",
		ToolVersions: map[string]string{"opengrep": "2.0.0", "betterleaks": "1.10.1"},
	})

	wantContains(t, body, "fake-subject-7", `<span class="mono">2026-11-19 00:05:07 UTC</span>`, "0123abcd", "rules-v9", "betterleaks 1.10.1", "opengrep 2.0.0")
	if b, o := strings.Index(body, "betterleaks 1.10.1"), strings.Index(body, "opengrep 2.0.0"); b > o {
		t.Errorf("tool versions are not ordered by tool name")
	}

	e = newTestEnv(t)
	body = e.succeeded(t, scan.Scan{})
	wantContains(t, body, `<dt>커밋</dt><dd class="mono"><span class="muted">없음</span></dd>`)
}

func TestScan_Empty(t *testing.T) {
	e := newTestEnv(t)
	body := e.succeeded(t, scan.Scan{Files: 3})

	wantContains(t, body, "탐지된 항목이 없습니다.")
}

func TestScan_NoFindingsSectionWhileActive(t *testing.T) {
	for _, st := range []scan.Status{scan.StatusQueued, scan.StatusRunning, scan.StatusFailed} {
		t.Run(string(st), func(t *testing.T) {
			e := newTestEnv(t)
			pid := e.newProject(t)
			e.scans.put(scan.Scan{ID: 2, ProjectID: pid, Status: st})
			e.scans.putFindings(2, secretFinding())
			res := e.do(t, http.MethodGet, "/scans/2", nil, e.user(t))

			wantAbsent(t, res.body, "탐지된 항목이 없습니다.", "conf/app.properties", "찾은 항목")
		})
	}
}

func TestScan_FindingsFailure(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 2, ProjectID: pid, Status: scan.StatusSucceeded})
	e.scans.findingsErr = errors.New("fake store failure")
	res := e.do(t, http.MethodGet, "/scans/2", nil, e.user(t))
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET /scans/2 with a failing findings read status = %d, want 500", res.status)
	}
}

func TestProject_ListsScans(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 1, ProjectID: pid, Status: scan.StatusFailed, RequestedAt: time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC)})
	e.scans.put(scan.Scan{ID: 2, ProjectID: pid, Status: scan.StatusSucceeded, Findings: 17, RequestedAt: time.Date(2026, 11, 19, 4, 5, 6, 0, time.UTC)})
	e.scans.put(scan.Scan{ID: 3, ProjectID: pid + 1, Status: scan.StatusSucceeded, Findings: 99})

	res := e.do(t, http.MethodGet, "/projects/"+itoa(pid), nil, e.user(t))

	wantContains(t, res.body, `href="/scans/1"`, `href="/scans/2"`, `<span class="mono">2026-11-19 04:05:06 UTC</span>`, `<span class="mono">2026-11-18 01:02:03 UTC</span>`, "완료", "실패", "17건")
	wantAbsent(t, res.body, `href="/scans/3"`, "99건")
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
