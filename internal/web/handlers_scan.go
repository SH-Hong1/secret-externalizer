package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// msgScanWhileApplying is the refusal of a scan requested while an apply of
// the project is being checked, queued or run.
const msgScanWhileApplying = "이 프로젝트의 적용이 진행 중이라 스캔을 시작할 수 없습니다. 적용이 끝난 뒤 프로젝트 화면에서 다시 시작해 주세요."

// handleScanCreate requests a scan of a project for the signed-in user and
// sends the browser to the scan page. When the project already has an active
// scan, it sends the browser to that scan instead. An active apply of the
// project refuses the scan with status 409.
func handleScanCreate(logger *slog.Logger, rd *renderer, scans ScanStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || n < 1 {
			rd.fail(w, r, logger, http.StatusNotFound, errors.New("project id is not a number"))
			return
		}
		id, err := scans.RequestScan(r.Context(), project.ID(n), actorOf(r))
		switch {
		case err == nil, errors.Is(err, db.ErrScanActive):
			http.Redirect(w, r, "/scans/"+strconv.FormatInt(int64(id), 10), http.StatusSeeOther)
		case errors.Is(err, db.ErrApplyActive):
			rd.errorPage(w, r, logger, http.StatusConflict, msgScanWhileApplying, false, err)
		case errors.Is(err, db.ErrNotFound):
			rd.fail(w, r, logger, http.StatusNotFound, err)
		default:
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("requesting scan: %w", err))
		}
	})
}

// handleScan shows a summary of one scan under the name of its project. The
// page refreshes itself while the scan is queued or running. A succeeded
// scan shows its counts per category and what it could not look at; the
// findings themselves are on the triage screen, which works on the project's
// latest succeeded scan, so the page links there only from that scan. A
// scan that confirms an apply names its run and says whether the values the
// apply replaced are gone.
func handleScan(logger *slog.Logger, rd *renderer, projects ProjectStore, scans ScanStore, store TriageStore, applies ApplyStore) http.Handler {
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
		page := newScanPage(sc)
		page.User, page.ProjectName = u, p.Name
		if sc.ApplyID != 0 {
			n := 0
			if sc.Status == scan.StatusSucceeded {
				if n, err = applies.Reappeared(r.Context(), sc.ID); err != nil {
					rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("counting reappeared values: %w", err))
					return
				}
			}
			page.Confirmation = newConfirmation(sc, apply.ID(sc.ApplyID), p.ID, n)
		}
		if sc.Status == scan.StatusFailed {
			rd.page(w, r, logger, http.StatusOK, "scan.html", page)
			return
		}

		latest, err := store.LatestScan(r.Context(), p.ID)
		hasLatest := err == nil
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading latest scan: %w", err))
			return
		}
		if hasLatest {
			page.Triage = triagePath(p.ID)
		}
		if sc.Status != scan.StatusSucceeded {
			rd.page(w, r, logger, http.StatusOK, "scan.html", page)
			return
		}

		counts, err := scans.CategoryCounts(r.Context(), sc.ID)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("counting findings: %w", err))
			return
		}
		page.setCounts(counts)
		if !hasLatest || latest.ID != sc.ID {
			// The triage screen shows a newer scan, not this one.
			page.Triage, page.LaterScan = "", latest.ID
			rd.page(w, r, logger, http.StatusOK, "scan.html", page)
			return
		}
		verdicts, err := store.VerdictCounts(r.Context(), p.ID, sc.ID)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("counting verdicts: %w", err))
			return
		}
		page.Undecided = verdicts[""]
		rd.page(w, r, logger, http.StatusOK, "scan.html", page)
	})
}
