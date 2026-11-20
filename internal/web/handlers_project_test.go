package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// postForm sends a form-encoded POST to /projects with header and cookies.
func (e *testEnv) postForm(t *testing.T, form url.Values, header http.Header, cookies ...cookie) result {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for _, c := range cookies {
		req.Header.Add("Cookie", c.name+"="+c.value)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return result{status: rec.Code, header: rec.Header(), cookies: (&http.Response{Header: rec.Header()}).Cookies(), body: rec.Body.String()}
}

func (e *testEnv) user(t *testing.T) cookie {
	t.Helper()
	return e.signIn(t, auth.User{Subject: "fake-subject", Name: "Fake User", Roles: []auth.Role{auth.RoleUser}})
}

func TestProjects_RequiresLogin(t *testing.T) {
	e := newTestEnv(t)
	for _, target := range []string{"/projects", "/projects/new", "/projects/1"} {
		res := e.do(t, http.MethodGet, target, nil)
		if res.status != http.StatusSeeOther {
			t.Errorf("GET %s without a session status = %d, want 303", target, res.status)
		}
		if want := "/login?return_to=" + url.QueryEscape(target); res.location() != want {
			t.Errorf("GET %s without a session Location = %q, want %q", target, res.location(), want)
		}
	}
	res := e.postForm(t, url.Values{"name": {"shop"}, "path": {"shop"}}, sameOrigin())
	if res.status != http.StatusUnauthorized {
		t.Errorf("POST /projects without a session status = %d, want 401", res.status)
	}
	if got := len(e.projects.all()); got != 0 {
		t.Errorf("projects stored by an anonymous POST = %d, want 0", got)
	}
}

func TestProjectList_ShowsProjects(t *testing.T) {
	e := newTestEnv(t)
	c := e.user(t)
	for _, p := range []project.Project{{Name: "alpha", Path: "a"}, {Name: "bravo", Path: "b"}} {
		if _, err := e.projects.CreateProject(t.Context(), p); err != nil {
			t.Fatalf("CreateProject() error = %v", err)
		}
	}
	res := e.do(t, http.MethodGet, "/projects", nil, c)
	if res.status != http.StatusOK {
		t.Fatalf("GET /projects status = %d, want 200", res.status)
	}
	for _, want := range []string{"alpha", "bravo", `href="/projects/1"`, `href="/projects/new"`} {
		if !strings.Contains(res.body, want) {
			t.Errorf("GET /projects body lacks %q", want)
		}
	}
}

func TestProjectList_StoreError(t *testing.T) {
	e := newTestEnv(t)
	c := e.user(t)
	e.projects.listErr = errors.New("fake store failure")
	res := e.do(t, http.MethodGet, "/projects", nil, c)
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET /projects with a failing store status = %d, want 500", res.status)
	}
	if strings.Contains(res.body, "fake store failure") {
		t.Errorf("GET /projects error page shows the store error: %q", res.body)
	}
}

func TestProjectNew_ShowsForm(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/projects/new", nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /projects/new status = %d, want 200", res.status)
	}
	for _, want := range []string{`<form method="post" action="/projects">`, `name="name"`, `name="path"`, `<textarea name="excludes"`} {
		if !strings.Contains(res.body, want) {
			t.Errorf("GET /projects/new body lacks %q", want)
		}
	}
	for _, bad := range []string{`aria-invalid`, `role="alert"`, `class="field-error"`} {
		if strings.Contains(res.body, bad) {
			t.Errorf("GET /projects/new body contains %q, want an untouched form", bad)
		}
	}
}

func TestProjectCreate_Valid(t *testing.T) {
	e := newTestEnv(t)
	c := e.user(t)
	form := url.Values{"name": {"shop"}, "path": {"team//shop/"}}

	res := e.postForm(t, form, sameOrigin(), c)

	if res.status != http.StatusSeeOther {
		t.Fatalf("POST /projects status = %d, want 303", res.status)
	}
	if want := "/projects/1"; res.location() != want {
		t.Errorf("POST /projects Location = %q, want %q", res.location(), want)
	}
	want := []project.Project{{ID: 1, Name: "shop", Path: "team/shop", CreatedBy: "fake-subject"}}
	if diff := cmp.Diff(want, e.projects.all()); diff != "" {
		t.Errorf("stored projects mismatch (-want +got):\n%s", diff)
	}
}

func TestProjectCreate_TrimsPath(t *testing.T) {
	e := newTestEnv(t)
	form := url.Values{"name": {"shop"}, "path": {"  shop \r\n"}}

	res := e.postForm(t, form, sameOrigin(), e.user(t))

	if res.status != http.StatusSeeOther {
		t.Fatalf("POST /projects with padded path status = %d, want 303", res.status)
	}
	got := e.projects.all()
	if len(got) != 1 || got[0].Path != "shop" {
		t.Errorf("POST /projects with path %q stored %+v, want one project with path %q", form.Get("path"), got, "shop")
	}
}

func TestProjectCreate_InvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		form  url.Values
		field string // the id suffix of the field marked invalid
	}{
		{"path leaves the root", url.Values{"name": {"shop"}, "path": {"../x"}}, "path"},
		{"absolute path", url.Values{"name": {"shop"}, "path": {"/etc"}}, "path"},
		{"empty path", url.Values{"name": {"shop"}, "path": {""}}, "path"},
		{"empty name", url.Values{"name": {"  "}, "path": {"shop"}}, "name"},
		{"name too long", url.Values{"name": {strings.Repeat("a", 101)}, "path": {"shop"}}, "name"},
		{"name with NUL", url.Values{"name": {"sh\x00op"}, "path": {"shop"}}, "name"},
		{"name with control character", url.Values{"name": {"sh\x1bop"}, "path": {"shop"}}, "name"},
		{"name with newline", url.Values{"name": {"sh\nop"}, "path": {"shop"}}, "name"},
		{"name with invalid UTF-8", url.Values{"name": {"sh\xffop"}, "path": {"shop"}}, "name"},
		{"invalid exclude", url.Values{"name": {"shop"}, "path": {"shop"}, "excludes": {"vendor\n../x"}}, "excludes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			res := e.postForm(t, tt.form, sameOrigin(), e.user(t))
			if res.status != http.StatusBadRequest {
				t.Errorf("POST /projects(%v) status = %d, want 400", tt.form, res.status)
			}
			if got := len(e.projects.all()); got != 0 {
				t.Errorf("POST /projects(%v) stored %d projects, want 0", tt.form, got)
			}
			if !strings.Contains(res.body, `<form method="post" action="/projects">`) {
				t.Errorf("POST /projects(%v) body has no form, want the form again", tt.form)
			}
			wantInvalidField(t, res.body, tt.field)
		})
	}
}

// wantInvalidField checks that the registration form shows the general
// alert and marks exactly the field project-<field> as invalid, with a
// message that the field names in aria-describedby.
func wantInvalidField(t *testing.T, body, field string) {
	t.Helper()
	if want := `<p class="alert-error" role="alert">등록하지 못했습니다. 표시된 항목을 고쳐 주세요.</p>`; !strings.Contains(body, want) {
		t.Errorf("registration form lacks the general alert %q", want)
	}
	if got := strings.Count(body, `aria-invalid="true"`); got != 1 {
		t.Errorf("registration form marks %d fields invalid, want 1", got)
	}
	control := regexp.MustCompile(`<(input|textarea)\b[^>]*\bid="project-` + field + `"[^>]*>`).FindString(body)
	if !strings.Contains(control, `aria-invalid="true"`) {
		t.Errorf("field project-%s = %q, want aria-invalid=\"true\"", field, control)
	}
	if want := `aria-describedby="project-` + field + `-error `; !strings.Contains(control, want) {
		t.Errorf("field project-%s = %q, want it to contain %q", field, control, want)
	}
	if !regexp.MustCompile(`<span class="field-error" id="project-` + field + `-error">[^<]+</span>`).MatchString(body) {
		t.Errorf("registration form lacks the message project-%s-error", field)
	}
}

func TestProjectCreate_InvalidPathKeepsInput(t *testing.T) {
	e := newTestEnv(t)
	form := url.Values{"name": {"my shop"}, "path": {"../x"}, "excludes": {"vendor\nbuild"}}
	res := e.postForm(t, form, sameOrigin(), e.user(t))
	for _, want := range []string{`value="my shop"`, `value="../x"`, "vendor\nbuild"} {
		if !strings.Contains(res.body, want) {
			t.Errorf("POST /projects with an invalid path: body lacks the input %q", want)
		}
	}
}

func TestProjectCreate_Duplicate(t *testing.T) {
	e := newTestEnv(t)
	e.projects.createErr = db.ErrDuplicate
	form := url.Values{"name": {"shop"}, "path": {"shop"}}

	res := e.postForm(t, form, sameOrigin(), e.user(t))

	if res.status != http.StatusConflict {
		t.Fatalf("POST /projects with a duplicate status = %d, want 409", res.status)
	}
	if want := "이미 등록된 이름이거나 경로입니다."; !strings.Contains(res.body, want) {
		t.Errorf("POST /projects with a duplicate body lacks %q", want)
	}
	if !strings.Contains(res.body, `value="shop"`) {
		t.Errorf("POST /projects with a duplicate body lacks the input, want the form again")
	}
	if strings.Contains(res.body, `aria-invalid="true"`) {
		t.Errorf("POST /projects with a duplicate marks a field invalid, want only the alert (the store does not say which)")
	}
}

func TestProjectCreate_StoreError(t *testing.T) {
	e := newTestEnv(t)
	e.projects.createErr = errors.New("fake store failure")
	res := e.postForm(t, url.Values{"name": {"shop"}, "path": {"shop"}}, sameOrigin(), e.user(t))
	if res.status != http.StatusInternalServerError {
		t.Errorf("POST /projects with a failing store status = %d, want 500", res.status)
	}
}

func TestProjectCreate_Excludes(t *testing.T) {
	e := newTestEnv(t)
	form := url.Values{
		"name":     {"shop"},
		"path":     {"shop"},
		"excludes": {"vendor\r\n\r\n  \n./build//out/\n"},
	}

	res := e.postForm(t, form, sameOrigin(), e.user(t))

	if res.status != http.StatusSeeOther {
		t.Fatalf("POST /projects status = %d, want 303", res.status)
	}
	got := e.projects.all()
	if len(got) != 1 {
		t.Fatalf("stored projects = %d, want 1", len(got))
	}
	if diff := cmp.Diff([]string{"vendor", "build/out"}, got[0].Excludes); diff != "" {
		t.Errorf("stored Excludes mismatch (-want +got):\n%s", diff)
	}
}

func TestProjectCreate_CrossOrigin(t *testing.T) {
	e := newTestEnv(t)
	form := url.Values{"name": {"shop"}, "path": {"shop"}}
	res := e.postForm(t, form, http.Header{"Sec-Fetch-Site": {"cross-site"}}, e.user(t))
	if res.status != http.StatusForbidden {
		t.Errorf("cross-site POST /projects status = %d, want 403", res.status)
	}
	if got := len(e.projects.all()); got != 0 {
		t.Errorf("cross-site POST /projects stored %d projects, want 0", got)
	}
}

func TestProject_Shows(t *testing.T) {
	e := newTestEnv(t)
	id, err := e.projects.CreateProject(t.Context(), project.Project{Name: "shop", Path: "team/shop", Excludes: []string{"vendor"}})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	res := e.do(t, http.MethodGet, "/projects/"+itoa(id), nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /projects/%d status = %d, want 200", id, res.status)
	}
	for _, want := range []string{"shop", "team/shop", "vendor"} {
		if !strings.Contains(res.body, want) {
			t.Errorf("GET /projects/%d body lacks %q", id, want)
		}
	}
}

func itoa(id project.ID) string { return strconv.Itoa(int(id)) }

func TestProject_NotFound(t *testing.T) {
	for _, target := range []string{"/projects/999", "/projects/abc", "/projects/-1", "/projects/1x"} {
		t.Run(target, func(t *testing.T) {
			e := newTestEnv(t)
			res := e.do(t, http.MethodGet, target, nil, e.user(t))
			if res.status != http.StatusNotFound {
				t.Errorf("GET %s status = %d, want 404", target, res.status)
			}
		})
	}
}

func TestProject_StoreError(t *testing.T) {
	e := newTestEnv(t)
	e.projects.getErr = errors.New("fake store failure")
	res := e.do(t, http.MethodGet, "/projects/1", nil, e.user(t))
	if res.status != http.StatusInternalServerError {
		t.Errorf("GET /projects/1 with a failing store status = %d, want 500", res.status)
	}
}

func TestProject_EscapesName(t *testing.T) {
	e := newTestEnv(t)
	id, err := e.projects.CreateProject(t.Context(), project.Project{Name: "<script>", Path: "shop"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	for _, target := range []string{"/projects/" + itoa(id), "/projects"} {
		res := e.do(t, http.MethodGet, target, nil, e.user(t))
		if strings.Contains(res.body, "<script>") {
			t.Errorf("GET %s body contains an unescaped <script>", target)
		}
		if !strings.Contains(res.body, "&lt;script&gt;") {
			t.Errorf("GET %s body lacks the escaped name &lt;script&gt;", target)
		}
	}
}

func TestHome_LinksToProjects(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/", nil, e.user(t))
	if !strings.Contains(res.body, `href="/projects"`) {
		t.Errorf("GET / body lacks a link to /projects")
	}
}

func TestProjectList_ShowsSummaries(t *testing.T) {
	e := newTestEnv(t)
	c := e.user(t)
	for _, p := range []project.Project{{Name: "alpha", Path: "a"}, {Name: "bravo", Path: "b"}, {Name: "charlie", Path: "c"}} {
		if _, err := e.projects.CreateProject(t.Context(), p); err != nil {
			t.Fatalf("CreateProject() error = %v", err)
		}
	}
	at := time.Date(2026, 11, 19, 5, 12, 0, 0, time.UTC)
	e.projects.putSummary(project.Summary{Project: project.Project{ID: 1}, LastScanID: 12, LastScanStatus: "succeeded", LastScanAt: at, HasResult: true, Undecided: 96, Total: 128})
	e.projects.putSummary(project.Summary{Project: project.Project{ID: 2}, LastScanID: 13, LastScanStatus: "failed", LastScanAt: at})

	res := e.do(t, http.MethodGet, "/projects", nil, c)
	if res.status != http.StatusOK {
		t.Fatalf("GET /projects status = %d, want 200", res.status)
	}
	wantIn(t, "project list", res.body,
		"<th>마지막 스캔</th>", `<th class="num">미판정</th>`, `<th class="num">전체</th>`,
		"2026-11-19 05:12:00 UTC", `<span class="badge badge-succeeded">완료</span>`, ">96<", ">128<",
		`<span class="badge badge-failed">실패</span>`, "스캔 없음",
	)
}

func TestProject_ShowsVerdictCounts(t *testing.T) {
	e := triageEnv(t)
	res := e.do(t, http.MethodGet, "/projects/1", nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /projects/1 status = %d, want 200", res.status)
	}
	wantIn(t, "project page", res.body,
		`href="/projects/1/triage"`, "판정하기 · 미판정 3",
		`href="/projects/1/triage?status=externalize"`, "외부화", ">18<",
		`href="/projects/1/triage?status=hold"`,
	)
}

func TestProject_NoVerdictCountsWithoutScan(t *testing.T) {
	e := newTestEnv(t)
	if _, err := e.projects.CreateProject(t.Context(), project.Project{Name: "shop", Path: "shop"}); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	res := e.do(t, http.MethodGet, "/projects/1", nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /projects/1 status = %d, want 200", res.status)
	}
	wantNotIn(t, "project page without a scan", res.body, "판정하기", "/triage?status=")
}

func TestProject_ReportLink(t *testing.T) {
	e := newTestEnv(t)
	pid := e.newProject(t)
	e.scans.put(scan.Scan{ID: 7, ProjectID: pid, Status: scan.StatusSucceeded})
	e.scans.put(scan.Scan{ID: 8, ProjectID: pid, Status: scan.StatusFailed})
	res := e.do(t, http.MethodGet, "/projects/"+itoa(pid), nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /projects/%d status = %d, want 200", pid, res.status)
	}
	wantIn(t, "GET /projects/"+itoa(pid), res.body, `href="/scans/7/report"`)
	wantNotIn(t, "GET /projects/"+itoa(pid), res.body, `href="/scans/8/report"`)
}
