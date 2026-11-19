package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// handleScanCreate requests a scan of a project for the signed-in user and
// sends the browser to the scan page. When the project already has an active
// scan, it sends the browser to that scan instead.
func handleScanCreate(logger *slog.Logger, rd *renderer, scans ScanStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || n < 1 {
			rd.fail(w, r, logger, http.StatusNotFound, errors.New("project id is not a number"))
			return
		}
		id, err := scans.RequestScan(r.Context(), project.ID(n), u.Subject)
		switch {
		case err == nil, errors.Is(err, db.ErrScanActive):
			http.Redirect(w, r, "/scans/"+strconv.FormatInt(int64(id), 10), http.StatusSeeOther)
		case errors.Is(err, db.ErrNotFound):
			rd.fail(w, r, logger, http.StatusNotFound, err)
		default:
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("requesting scan: %w", err))
		}
	})
}

// handleScan shows one scan under the name of its project. The page
// refreshes itself while the scan is queued or running, and lists the
// findings once it has succeeded.
func handleScan(logger *slog.Logger, rd *renderer, projects ProjectStore, scans ScanStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || n < 1 {
			rd.fail(w, r, logger, http.StatusNotFound, errors.New("scan id is not a number"))
			return
		}
		sc, err := scans.Scan(r.Context(), scan.ID(n))
		if errors.Is(err, db.ErrNotFound) {
			rd.fail(w, r, logger, http.StatusNotFound, err)
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading scan: %w", err))
			return
		}
		p, err := projects.Project(r.Context(), sc.ProjectID)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading project of scan: %w", err))
			return
		}
		var fs []finding.Finding
		if sc.Status == scan.StatusSucceeded {
			if fs, err = scans.FindingsByScan(r.Context(), sc.ID); err != nil {
				rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading findings: %w", err))
				return
			}
		}
		page := newScanPage(sc, fs)
		page.User, page.ProjectName = u, p.Name
		rd.page(w, r, logger, http.StatusOK, "scan.html", page)
	})
}
