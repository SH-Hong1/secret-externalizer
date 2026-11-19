package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
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

// errorMessage returns the text shown on the error page for status.
func errorMessage(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "요청을 처리할 수 없습니다. 처음부터 다시 로그인해 주세요."
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

// parseTemplates parses every template under templates/ in fsys. Each page
// template is parsed together with the layout. It panics when a template is
// broken, so a bad template stops the program at start-up.
//
// Templates may call two functions. asset returns the versioned address of
// a static file in a. section returns the text of the page's "section"
// template, which names the menu entry the page belongs to ("projects" or
// "audit"), or "" when the page defines none; the layout marks that entry
// as the current one.
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
		}).ParseFS(fsys, "templates/"+layoutFile, file)
		if err != nil {
			panic(fmt.Sprintf("parsing template %s: %v", name, err))
		}
		section := sectionOf(t)
		t.Funcs(template.FuncMap{"section": func() string { return section }})
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

// fail renders the error page for status. It logs err once when the status is
// 500 or higher and never shows err on the page. The page shows the menu when
// the request has a signed-in user.
func (rd *renderer) fail(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, err error) {
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
	}{u, status, errorMessage(status), status == http.StatusBadRequest || status == http.StatusUnauthorized}
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
