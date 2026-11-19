package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

func handleHealthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok")) // a failed write means the client is gone; there is nobody to tell
	})
}

func handleNotFound(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd.fail(w, r, logger, http.StatusNotFound, errors.New("no route"))
	})
}

// handlePostOnly answers a request with any other method to a path that
// takes only POST.
func handlePostOnly(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		rd.fail(w, r, logger, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	})
}

// handleHome shows the signed-in user's name and roles and a logout button.
func handleHome(logger *slog.Logger, rd *renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		rd.page(w, r, logger, http.StatusOK, "home.html", struct{ User auth.User }{u})
	})
}

// handleAudit shows the audit trail, newest first, auditPageSize events a
// page. The query parameter before, the ID of the last event of the page
// before, gives the next page.
func handleAudit(logger *slog.Logger, rd *renderer, audits AuditStore) http.Handler {
	type auditPage struct {
		User     auth.User
		Rows     []auditRow
		MoreHref string
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		var before int64
		if q := r.URL.Query(); q.Has("before") {
			n, err := strconv.ParseInt(q.Get("before"), 10, 64)
			if err != nil || n < 1 {
				rd.fail(w, r, logger, http.StatusBadRequest, errors.New("audit page before is not a positive number"))
				return
			}
			before = n
		}
		// One event more than a page tells whether another page follows.
		events, err := audits.AuditEvents(r.Context(), before, auditPageSize+1)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("listing audit events: %w", err))
			return
		}
		data := auditPage{User: u}
		if len(events) > auditPageSize {
			events = events[:auditPageSize]
			data.MoreHref = "/audit?before=" + strconv.FormatInt(events[len(events)-1].ID, 10)
		}
		data.Rows = newAuditRows(events)
		rd.page(w, r, logger, http.StatusOK, "audit.html", data)
	})
}
