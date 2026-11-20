package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// reportPage is what the report screen shows. PDF is the address of the
// PDF export, or empty when the screen offers none.
type reportPage struct {
	User auth.User
	View reportView
	PDF  string
}

// reportPath returns the path of the report of scan id.
func reportPath(id scan.ID) string {
	return scanPath(id) + "/report"
}

// handleReport shows the report of one succeeded scan, made at the time now
// returns. A scan that has not succeeded has no report, and the browser is
// sent to its scan page instead.
func handleReport(logger *slog.Logger, rd *renderer, reports ReportStore, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || n < 1 {
			rd.fail(w, r, logger, http.StatusNotFound, errors.New("scan id is not a number"))
			return
		}
		d, err := reports.Report(r.Context(), scan.ID(n))
		if errors.Is(err, db.ErrNotFound) {
			rd.fail(w, r, logger, http.StatusNotFound, err)
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading report: %w", err))
			return
		}
		if d.Scan.Status != scan.StatusSucceeded {
			http.Redirect(w, r, scanPath(d.Scan.ID), http.StatusSeeOther)
			return
		}
		page := reportPage{User: u, View: newReportView(report.Build(d, now()))}
		rd.page(w, r, logger, http.StatusOK, "report.html", page)
	})
}
