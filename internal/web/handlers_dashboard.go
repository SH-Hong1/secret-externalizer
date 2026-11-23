package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// handleDashboard shows the dashboard for the project and period the query
// names, with the period ending at the time now returns. An empty or
// missing value means every project and every scan. A project that is not
// a positive number or a period that is not known is a bad request, and a
// project that does not exist is not found.
func handleDashboard(logger *slog.Logger, rd *renderer, dashboards DashboardStore, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		q := r.URL.Query()
		var f dashboard.Filter
		if s := q.Get("project"); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 1 {
				rd.fail(w, r, logger, http.StatusBadRequest, errors.New("project is not a positive number"))
				return
			}
			f.Project = project.ID(n)
		}
		period, err := dashboard.ParsePeriod(q.Get("period"))
		if err != nil {
			rd.fail(w, r, logger, http.StatusBadRequest, err)
			return
		}
		f.Since = period.Since(now())

		d, err := dashboards.Dashboard(r.Context(), f)
		if errors.Is(err, db.ErrNotFound) {
			rd.fail(w, r, logger, http.StatusNotFound, err)
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading dashboard: %w", err))
			return
		}
		rd.page(w, r, logger, http.StatusOK, "dashboard.html", newDashboardPage(u, dashboard.Build(d, f), period))
	})
}
