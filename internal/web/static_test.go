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

// footerDefine matches the define of the PDF footer up to its first end
// action; the footer holds no other action that ends with {{end}}.
var footerDefine = regexp.MustCompile(`(?s)\{\{define "print-footer"\}\}.*?\{\{end\}\}`)

// styleElement matches one style element.
var styleElement = regexp.MustCompile(`<style>[^<]*</style>`)

// withoutFooterStyle returns b with the style elements inside the define of
// the PDF footer removed, and how many it removed. The converter draws the
// footer apart from the page and reads no stylesheet for it, and the footer
// is never served, so the CSP does not apply to it. A style element outside
// that define stays.
func withoutFooterStyle(b []byte) ([]byte, int) {
	n := 0
	out := footerDefine.ReplaceAllFunc(b, func(def []byte) []byte {
		n += len(styleElement.FindAll(def, -1))
		return styleElement.ReplaceAll(def, nil)
	})
	return out, n
}

// footerStyles returns how many style elements the print-footer define of
// the template file holds: one in report.html, which draws the footer, and
// none elsewhere.
func footerStyles(file string) int {
	if file == "report.html" {
		return 1
	}
	return 0
}

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
		b, styles := withoutFooterStyle(b)
		if want := footerStyles(file); styles != want {
			t.Errorf("%s holds %d style elements in its print-footer define, want %d", file, styles, want)
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

func TestWithoutFooterStyle(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		styles int
	}{
		{
			name:   "style in the footer",
			in:     `{{define "print-footer"}}<style>b{}</style><b>x</b>{{end}}`,
			want:   `{{define "print-footer"}}<b>x</b>{{end}}`,
			styles: 1,
		},
		{
			name: "style after a footer without one",
			in:   `{{define "print-footer"}}<b>x</b>{{end}}{{define "report"}}<style>p{}</style>{{end}}`,
			want: `{{define "print-footer"}}<b>x</b>{{end}}{{define "report"}}<style>p{}</style>{{end}}`,
		},
		{
			name: "style with no footer",
			in:   `{{define "page"}}<style>p{}</style>{{end}}`,
			want: `{{define "page"}}<style>p{}</style>{{end}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, styles := withoutFooterStyle([]byte(tt.in))
			if string(got) != tt.want || styles != tt.styles {
				t.Errorf("withoutFooterStyle(%q) = %q, %d, want %q, %d", tt.in, got, styles, tt.want, tt.styles)
			}
		})
	}
}
