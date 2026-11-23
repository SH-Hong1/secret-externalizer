package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

//go:embed templates/*.html
var templateFS embed.FS

const (
	layoutFile = "layout.html"
	errorFile  = "error.html"
)

// msgLoginAgain is the error page text for a login that cannot go on.
const msgLoginAgain = "요청을 처리할 수 없습니다. 처음부터 다시 로그인해 주세요."

// errorMessage returns the text shown on the error page for status.
func errorMessage(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "요청을 처리할 수 없습니다. 주소나 입력을 확인해 주세요."
	case http.StatusUnauthorized:
		return "로그인이 필요합니다."
	case http.StatusForbidden:
		return "이 화면을 볼 권한이 없습니다."
	case http.StatusNotFound:
		return "화면을 찾을 수 없습니다."
	case http.StatusMethodNotAllowed:
		return "이 주소는 이 방식의 요청을 받지 않습니다."
	case http.StatusServiceUnavailable:
		return "세션 저장소에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요."
	default:
		return "처리 중 문제가 생겼습니다. 잠시 뒤 다시 시도해 주세요."
	}
}

// renderer draws pages from the embedded templates.
type renderer struct {
	pages map[string]*template.Template
}

// newRenderer parses the embedded templates with parseTemplates.
func newRenderer(a *assets) *renderer {
	return parseTemplates(templateFS, a)
}

// partialsOf returns the template files that the page name uses besides the
// layout. The triage screen draws the slot of the original value, which the
// reveal also renders on its own from original.html. The pages of a scan
// and of an apply run both tell what a confirmation scan found.
func partialsOf(name string) []string {
	switch name {
	case "triage.html":
		return []string{"templates/original.html"}
	case "scan.html", "apply_run.html":
		return []string{"templates/confirmation.html"}
	}
	return nil
}

// parseTemplates parses every template under templates/ in fsys. Each page
// template is parsed together with the layout and the files partialsOf
// names. It panics when a template is
// broken or when the layout's "assets" template names a static file that a
// does not hold, so a bad template or a missing file stops the program at
// start-up.
//
// Templates may call two functions. asset returns the versioned address of
// a static file in a. section returns the text of the page's "section"
// template, which names the menu entry the page belongs to ("dashboard",
// "projects" or "audit"), or "" when the page defines none; the layout
// marks that entry as the current one.
func parseTemplates(fsys fs.FS, a *assets) *renderer {
	files, err := fs.Glob(fsys, "templates/*.html")
	if err != nil {
		panic(fmt.Sprintf("listing templates: %v", err))
	}
	rd := &renderer{pages: make(map[string]*template.Template)}
	for _, file := range files {
		name := path.Base(file)
		if name == layoutFile {
			continue
		}
		t, err := template.New(name).Funcs(template.FuncMap{
			"asset":   a.url,
			"section": func() string { return "" },
		}).ParseFS(fsys, append([]string{"templates/" + layoutFile, file}, partialsOf(name)...)...)
		if err != nil {
			panic(fmt.Sprintf("parsing template %s: %v", name, err))
		}
		section := sectionOf(t)
		t.Funcs(template.FuncMap{"section": func() string { return section }})
		if err := t.ExecuteTemplate(io.Discard, "assets", nil); err != nil {
			panic(fmt.Sprintf("checking the static files of template %s: %v", name, err))
		}
		rd.pages[name] = t
	}
	if _, ok := rd.pages[errorFile]; !ok {
		panic("missing template " + errorFile)
	}
	return rd
}

// sectionOf returns the trimmed text of t's "section" template, or "" when t
// has none.
func sectionOf(t *template.Template) string {
	s := t.Lookup("section")
	if s == nil || s.Tree == nil {
		return ""
	}
	return strings.TrimSpace(s.Tree.Root.String())
}

// page renders the named template with data and writes it with status. The
// page is rendered into a buffer first, so a failure produces the error page
// and never a partial page.
func (rd *renderer) page(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, name string, data any) {
	t, ok := rd.pages[name]
	if !ok {
		rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("unknown template %q", name))
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("rendering template %q: %w", name, err))
		return
	}
	rd.write(w, r, logger, status, &buf)
}

// fragment renders the template tmpl of the named page with data, without
// the layout, and writes it with status. Like page, it renders into a buffer
// first.
func (rd *renderer) fragment(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, name, tmpl string, data any) {
	t, ok := rd.pages[name]
	if !ok {
		rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("unknown template %q", name))
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, tmpl, data); err != nil {
		rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("rendering template %q of %q: %w", tmpl, name, err))
		return
	}
	rd.write(w, r, logger, status, &buf)
}

// render returns the template tmpl of the named page drawn with data.
func (rd *renderer) render(name, tmpl string, data any) ([]byte, error) {
	t, ok := rd.pages[name]
	if !ok {
		return nil, fmt.Errorf("unknown template %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, tmpl, data); err != nil {
		return nil, fmt.Errorf("rendering template %q of %q: %w", tmpl, name, err)
	}
	return buf.Bytes(), nil
}

// fail renders the error page for status with the text errorMessage gives
// it. A 401 page links to the login.
func (rd *renderer) fail(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, err error) {
	rd.errorPage(w, r, logger, status, errorMessage(status), status == http.StatusUnauthorized, err)
}

// failLogin renders the error page with status 400 for a login that cannot
// go on, asking the user to log in again from the start.
func (rd *renderer) failLogin(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	rd.errorPage(w, r, logger, http.StatusBadRequest, msgLoginAgain, true, err)
}

// errorPage renders the error page for status with msg, and a link to the
// login when loginLink is true. It logs err once when the status is 500 or
// higher and never shows err on the page. The page shows the menu when the
// request has a signed-in user.
func (rd *renderer) errorPage(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, msg string, loginLink bool, err error) {
	if status >= http.StatusInternalServerError {
		logger.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "status", status, "error", err)
	}
	var buf bytes.Buffer
	u, _ := userFrom(r.Context()) // the zero User leaves out the menu
	data := struct {
		User      auth.User
		Status    int
		Message   string
		LoginLink bool
	}{u, status, msg, loginLink}
	if execErr := rd.pages[errorFile].ExecuteTemplate(&buf, "layout", data); execErr != nil {
		logger.ErrorContext(r.Context(), "rendering error page", "error", execErr)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	rd.write(w, r, logger, status, &buf)
}

func (rd *renderer) write(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, buf *bytes.Buffer) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		logger.DebugContext(r.Context(), "writing response", "error", err)
	}
}
