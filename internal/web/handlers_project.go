package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

const (
	// maxProjectNameLen is the longest project name, in characters, that the
	// database accepts.
	maxProjectNameLen = 100
	// maxFormBytes bounds the size of the project form.
	maxFormBytes = 64 << 10
)

// formInvalid is the alert of a registration form that has a field error.
const formInvalid = "등록하지 못했습니다. 표시된 항목을 고쳐 주세요."

// projectForm is what the registration form shows. It carries the values the
// user typed, so a rejected form can be shown again. Error is the alert above
// the form; NameError, PathError and ExcludesError are the messages of the
// fields that failed, and the form marks those fields invalid.
type projectForm struct {
	User          auth.User
	Name          string
	Path          string
	Excludes      string
	Error         string
	NameError     string
	PathError     string
	ExcludesError string
}

// handleProjectList lists every project with its newest scan and the
// number of its bundles that have no verdict.
func handleProjectList(logger *slog.Logger, rd *renderer, projects ProjectStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		list, err := projects.ProjectSummaries(r.Context())
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("listing projects: %w", err))
			return
		}
		rd.page(w, r, logger, http.StatusOK, "projects.html", struct {
			User     auth.User
			Projects []projectRow
		}{u, newProjectRows(list)})
	})
}

// projectRow is one line of the project list.
type projectRow struct {
	project.Summary
	Status      scan.Status
	StatusLabel string
	ScanAt      string
}

func newProjectRows(list []project.Summary) []projectRow {
	out := make([]projectRow, 0, len(list))
	for _, s := range list {
		st := scan.Status(s.LastScanStatus)
		out = append(out, projectRow{Summary: s, Status: st, StatusLabel: statusLabel(st), ScanAt: formatTime(s.LastScanAt)})
	}
	return out
}

// handleProjectNew shows the empty registration form.
func handleProjectNew(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		rd.page(w, r, logger, http.StatusOK, "project_new.html", projectForm{User: u})
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
			User:     u,
			Name:     strings.TrimSpace(r.PostFormValue("name")),
			Path:     strings.TrimSpace(r.PostFormValue("path")),
			Excludes: r.PostFormValue("excludes"),
		}
		p := project.Project{Name: form.Name, CreatedBy: u.Subject}
		if n := utf8.RuneCountInString(form.Name); n < 1 || n > maxProjectNameLen {
			form.Error, form.NameError = formInvalid, "이름은 1자 이상 100자 이하로 입력해 주세요."
			rd.page(w, r, logger, http.StatusBadRequest, "project_new.html", form)
			return
		}
		if !validText(form.Name) {
			form.Error, form.NameError = formInvalid, "이름에는 제어 문자나 깨진 문자를 쓸 수 없습니다."
			rd.page(w, r, logger, http.StatusBadRequest, "project_new.html", form)
			return
		}
		var err error
		if p.Path, err = project.ParsePath(form.Path); err != nil {
			form.Error, form.PathError = formInvalid, "경로는 작업 공간 루트 기준의 상대 경로여야 하고 루트 밖으로 나갈 수 없습니다."
			rd.page(w, r, logger, http.StatusBadRequest, "project_new.html", form)
			return
		}
		if p.Excludes, err = parseExcludes(form.Excludes); err != nil {
			form.Error, form.ExcludesError = formInvalid, "제외할 경로는 한 줄에 하나씩, 작업 공간 루트 기준의 상대 경로로 입력해 주세요."
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

// validText reports whether s is valid UTF-8 without control characters.
func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
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

// handleProject shows one project, the verdict counts of its newest
// succeeded scan, its scans, newest first, and its latest applies. While a
// check or an apply of the project is active the scan button is disabled,
// since the database would refuse the scan.
func handleProject(logger *slog.Logger, rd *renderer, projects ProjectStore, scans ScanStore, store TriageStore, applies ApplyStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
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
		list, err := scans.ScansByProject(r.Context(), p.ID)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("listing scans: %w", err))
			return
		}
		var tiles []verdictTile
		sc, err := store.LatestScan(r.Context(), p.ID)
		switch {
		case errors.Is(err, db.ErrNotFound):
		case err != nil:
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading latest scan: %w", err))
			return
		default:
			counts, err := store.VerdictCounts(r.Context(), p.ID, sc.ID)
			if err != nil {
				rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("counting verdicts: %w", err))
				return
			}
			tiles = newVerdictTiles(p.ID, counts)
		}
		// One more than the rows, since the active check is listed but not shown.
		runs, err := applies.ApplyHistory(r.Context(), p.ID, maxApplyRows+1)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("listing apply runs: %w", err))
			return
		}
		rows := newScanRows(list)
		for i := range rows {
			rows[i].Basis = tiles != nil && rows[i].ID == sc.ID
		}
		applyRows, active := newApplyRows(runs)
		rd.page(w, r, logger, http.StatusOK, "project.html", struct {
			User        auth.User
			Project     project.Project
			Scans       []scanRow
			Tiles       []verdictTile
			Triage      string
			ApplyHref   string
			Applies     []applyRow
			ActiveApply *activeApply
		}{u, p, rows, tiles, triagePath(p.ID), applyPath(p.ID), applyRows, active})
	})
}
