package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
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
