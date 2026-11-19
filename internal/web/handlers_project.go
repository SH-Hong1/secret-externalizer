package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

const (
	// maxProjectNameLen is the longest project name, in characters, that the
	// database accepts.
	maxProjectNameLen = 100
	// maxFormBytes bounds the size of the project form.
	maxFormBytes = 64 << 10
)

// projectForm is what the registration form shows. It carries the values the
// user typed, so a rejected form can be shown again.
type projectForm struct {
	Name     string
	Path     string
	Excludes string
	Error    string
}

// handleProjectList lists every project.
func handleProjectList(logger *slog.Logger, rd *renderer, projects ProjectStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := projects.Projects(r.Context())
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("listing projects: %w", err))
			return
		}
		rd.page(w, r, logger, http.StatusOK, "projects.html", list)
	})
}

// handleProjectNew shows the empty registration form.
func handleProjectNew(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd.page(w, r, logger, http.StatusOK, "project_new.html", projectForm{})
	})
}

// handleProjectCreate registers a project for the signed-in user. It checks
// only the shape of the paths; the worker resolves them against the source
// when it scans.
func handleProjectCreate(logger *slog.Logger, rd *renderer, projects ProjectStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			rd.fail(w, r, logger, http.StatusBadRequest, fmt.Errorf("reading project form: %w", err))
			return
		}
		form := projectForm{
			Name:     strings.TrimSpace(r.PostFormValue("name")),
			Path:     r.PostFormValue("path"),
			Excludes: r.PostFormValue("excludes"),
		}
		p := project.Project{Name: form.Name, CreatedBy: u.Subject}
		if n := utf8.RuneCountInString(form.Name); n < 1 || n > maxProjectNameLen {
			form.Error = "이름은 1자 이상 100자 이하로 입력해 주세요."
			rd.page(w, r, logger, http.StatusBadRequest, "project_new.html", form)
			return
		}
		var err error
		if p.Path, err = project.ParsePath(form.Path); err != nil {
			form.Error = "경로는 작업 공간 루트 기준의 상대 경로여야 하고 루트 밖으로 나갈 수 없습니다."
			rd.page(w, r, logger, http.StatusBadRequest, "project_new.html", form)
			return
		}
		if p.Excludes, err = parseExcludes(form.Excludes); err != nil {
			form.Error = "제외할 경로는 한 줄에 하나씩, 작업 공간 루트 기준의 상대 경로로 입력해 주세요."
			rd.page(w, r, logger, http.StatusBadRequest, "project_new.html", form)
			return
		}
		id, err := projects.CreateProject(r.Context(), p)
		if errors.Is(err, db.ErrDuplicate) {
			form.Error = "이미 등록된 이름이거나 경로입니다."
			rd.page(w, r, logger, http.StatusConflict, "project_new.html", form)
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("creating project: %w", err))
			return
		}
		http.Redirect(w, r, "/projects/"+strconv.FormatInt(int64(id), 10), http.StatusSeeOther)
	})
}

// parseExcludes splits text into one path per line, drops blank lines, and
// checks each remaining line with project.ParsePath.
func parseExcludes(text string) ([]string, error) {
	var out []string
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, err := project.ParsePath(line)
		if err != nil {
			return nil, fmt.Errorf("checking exclude: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}

// handleProject shows one project.
func handleProject(logger *slog.Logger, rd *renderer, projects ProjectStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || n < 1 {
			rd.fail(w, r, logger, http.StatusNotFound, errors.New("project id is not a number"))
			return
		}
		p, err := projects.Project(r.Context(), project.ID(n))
		if errors.Is(err, db.ErrNotFound) {
			rd.fail(w, r, logger, http.StatusNotFound, err)
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading project: %w", err))
			return
		}
		rd.page(w, r, logger, http.StatusOK, "project.html", p)
	})
}
