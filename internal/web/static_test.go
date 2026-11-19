package web_test

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

func TestStatic(t *testing.T) {
	srv := newTestServer(t, io.Discard)
	tests := []struct {
		path        string
		contentType string
	}{
		{"/static/app.css", "text/css; charset=utf-8"},
		{"/static/htmx.min.js", "text/javascript; charset=utf-8"},
		{"/static/fonts/D2Coding-1.4.0-Regular.woff2", "font/woff2"},
		{"/static/fonts/D2Coding-LICENSE.txt", "text/plain; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp := get(t, srv.URL+tt.path)
			if resp.status != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", tt.path, resp.status)
			}
			if got := resp.header.Get("Content-Type"); got != tt.contentType {
				t.Errorf("GET %s Content-Type = %q, want %q", tt.path, got, tt.contentType)
			}
			if got, want := resp.header.Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
				t.Errorf("GET %s Cache-Control = %q, want %q", tt.path, got, want)
			}
			if got, want := resp.header.Get("X-Content-Type-Options"), "nosniff"; got != want {
				t.Errorf("GET %s X-Content-Type-Options = %q, want %q", tt.path, got, want)
			}
			if resp.body == "" {
				t.Errorf("GET %s body is empty", tt.path)
			}
		})
	}
}

func TestStatic_NotFound(t *testing.T) {
	srv := newTestServer(t, io.Discard)
	for _, path := range []string{
		"/static/nope.css",
		"/static/../render.go",
		"/static/..%2frender.go",
		"/static/fonts/../../render.go",
		"/static/fonts",
		"/static/fonts/",
		"/static/",
		"/static/app.css/",
	} {
		t.Run(path, func(t *testing.T) {
			resp := get(t, srv.URL+path)
			if resp.status != http.StatusNotFound {
				t.Errorf("GET %s status = %d, want 404", path, resp.status)
			}
			if got, want := resp.header.Get("Cache-Control"), "no-store"; got != want {
				t.Errorf("GET %s Cache-Control = %q, want %q", path, got, want)
			}
		})
	}
}

func TestLayoutNavigation(t *testing.T) {
	tests := []struct {
		user      auth.User
		wantAudit bool
	}{
		{auth.User{Subject: "fake-subject-user", Name: "dev-user", Roles: []auth.Role{auth.RoleUser}}, false},
		{auth.User{Subject: "fake-subject-admin", Name: "dev-admin", Roles: []auth.Role{auth.RoleAdmin}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.user.Name, func(t *testing.T) {
			e := newTestEnv(t)
			res := e.do(t, http.MethodGet, "/projects", nil, e.signIn(t, tt.user))
			if res.status != http.StatusOK {
				t.Fatalf("GET /projects status = %d, want 200", res.status)
			}
			wantContains(t, res.body,
				tt.user.Name,
				`<a class="nav-link" href="/projects" aria-current="page">프로젝트</a>`,
				`<form method="post" action="/logout">`,
				`<link rel="stylesheet" href="/static/app.css?v=`,
				`<script src="/static/htmx.min.js?v=`,
				`<meta name="htmx-config"`,
			)
			if got := strings.Contains(res.body, `href="/audit"`); got != tt.wantAudit {
				t.Errorf("GET /projects as %s has the audit link = %v, want %v", tt.user.Name, got, tt.wantAudit)
			}
		})
	}
}

func TestLayoutNavigation_AuditIsCurrent(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject-admin", Name: "dev-admin", Roles: []auth.Role{auth.RoleAdmin}})
	res := e.do(t, http.MethodGet, "/audit", nil, c)
	wantContains(t, res.body,
		`<a class="nav-link" href="/audit" aria-current="page">감사 로그</a>`,
		`<a class="nav-link" href="/projects">프로젝트</a>`,
	)
}

func TestLayoutNavigation_SignedOut(t *testing.T) {
	e := newTestEnv(t)
	res := e.do(t, http.MethodGet, "/nope", nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("GET /nope status = %d, want 404", res.status)
	}
	wantContains(t, res.body, `<link rel="stylesheet" href="/static/app.css?v=`)
	wantAbsent(t, res.body, `class="nav-link"`, `action="/logout"`)
}

func TestLayoutNavigation_ErrorPageKeepsMenu(t *testing.T) {
	e := newTestEnv(t)
	c := e.signIn(t, auth.User{Subject: "fake-subject-user", Name: "dev-user", Roles: []auth.Role{auth.RoleUser}})
	res := e.do(t, http.MethodGet, "/audit", nil, c)
	if res.status != http.StatusForbidden {
		t.Fatalf("GET /audit as user status = %d, want 403", res.status)
	}
	wantContains(t, res.body, "dev-user", `href="/projects"`, `action="/logout"`)
}

// inlineCode matches what the CSP forbids or what would need a looser CSP: a
// script element with a body, a style attribute or element, and an inline
// event handler attribute.
var inlineCode = regexp.MustCompile(`(?is)<script\b[^>]*>\s*[^<\s]|\sstyle\s*=|<style\b|\son[a-z]+\s*=`)

func TestNoInlineScriptOrStyle(t *testing.T) {
	templates := os.DirFS("templates")
	files, err := fs.Glob(templates, "*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("Glob(templates/*.html) = %v, %v, want template files", files, err)
	}
	for _, file := range files {
		b, err := fs.ReadFile(templates, file)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", file, err)
		}
		if m := inlineCode.Find(b); m != nil {
			t.Errorf("%s contains %q, want no inline script, style or event handler", file, m)
		}
	}
}

func TestNoInlineScriptOrStyle_Pattern(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{`<script src="/static/htmx.min.js" defer></script>`, false},
		{`<script>alert(1)</script>`, true},
		{`<script type="module">x()</script>`, true},
		{`<p style="color: red">`, true},
		{`<style>p{}</style>`, true},
		{`<button onclick="x()">`, true},
		{`<p class="note">`, false},
		{`<meta name="htmx-config" content="{}">`, false},
	}
	for _, tt := range tests {
		if got := inlineCode.MatchString(tt.in); got != tt.want {
			t.Errorf("inlineCode.MatchString(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
