package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// addRoutes registers every route of the server. Only the routes that need a
// signed-in user read the session, so /healthz answers and logout works even
// when the session store is down. The routes that call the stores or the
// originals get a deadline of limits.Store. The PDF export sets its own
// deadlines, one for reading the store and one for the conversion. The
// static files need no session.
func addRoutes(mux *http.ServeMux, logger *slog.Logger, rd *renderer, a *assets, authn Authenticator, sessions SessionStore,
	attempts AttemptStore, stores Stores, suggester Suggester, originals Originals, p printer, limits Limits,
) {
	storeLimit := limits.Store
	projects, scans, triage, audits := stores.Projects, stores.Scans, stores.Triage, stores.Audits
	signedIn := func(h http.Handler) http.Handler {
		return chain(h, loadSession(logger, rd, sessions), requireLogin(logger, rd))
	}
	// usesStore is signedIn for a route that calls the database.
	usesStore := func(h http.Handler) http.Handler {
		return signedIn(timeLimit(storeLimit)(h))
	}
	// adminStore is usesStore for a route only an admin may use. The role
	// is checked before the deadline starts and before the handler runs.
	adminStore := func(h http.Handler) http.Handler {
		return chain(h, loadSession(logger, rd, sessions), requireLogin(logger, rd), requireRole(logger, rd, auth.RoleAdmin), timeLimit(storeLimit))
	}

	mux.Handle("GET /healthz", handleHealthz())
	mux.Handle("GET /static/{path...}", handleStatic(logger, rd, a))
	mux.Handle("GET /login", handleLogin(logger, rd, authn, attempts))
	mux.Handle("GET /auth/callback", handleCallback(logger, rd, authn, sessions, attempts))
	// Logout needs no live session; see handleLogout.
	mux.Handle("POST /logout", handleLogout(logger, authn, sessions))
	mux.Handle("/logout", handlePostOnly(logger, rd))
	mux.Handle("GET /{$}", signedIn(handleHome(logger, rd)))
	mux.Handle("GET /dashboard", usesStore(handleDashboard(logger, rd, stores.Dashboards, time.Now)))
	mux.Handle("GET /audit", adminStore(handleAudit(logger, rd, audits)))
	mux.Handle("GET /projects", usesStore(handleProjectList(logger, rd, projects)))
	mux.Handle("GET /projects/new", signedIn(handleProjectNew(logger, rd)))
	mux.Handle("POST /projects", usesStore(handleProjectCreate(logger, rd, projects)))
	mux.Handle("GET /projects/{id}", usesStore(handleProject(logger, rd, projects, scans, triage, stores.Applies)))
	mux.Handle("GET /projects/{id}/triage", usesStore(handleTriage(logger, rd, projects, triage, suggester)))
	tw := triageWriter{logger: logger, rd: rd, projects: projects, store: triage, suggester: suggester}
	mux.Handle("POST /projects/{id}/bundles/{bundle}/verdict", usesStore(handleBundleVerdict(tw)))
	mux.Handle("/projects/{id}/bundles/{bundle}/verdict", handlePostOnly(logger, rd))
	mux.Handle("POST /projects/{id}/customer-data/verdict", usesStore(handleCustomerDataVerdict(tw)))
	mux.Handle("/projects/{id}/customer-data/verdict", handlePostOnly(logger, rd))
	mux.Handle("POST /projects/{id}/bundles/{bundle}/merge", usesStore(handleMerge(tw)))
	mux.Handle("/projects/{id}/bundles/{bundle}/merge", handlePostOnly(logger, rd))
	mux.Handle("POST /projects/{id}/findings/{finding}/move", usesStore(handleMove(tw)))
	mux.Handle("/projects/{id}/findings/{finding}/move", handlePostOnly(logger, rd))
	aw := applyWriter{logger: logger, rd: rd, projects: projects, store: stores.Applies, scans: scans}
	mux.Handle("GET /projects/{id}/apply", usesStore(handleApplyPage(aw)))
	mux.Handle("POST /projects/{id}/apply", usesStore(handleApplyConfirm(aw)))
	mux.Handle("/projects/{id}/apply", handleNotAllowed(logger, rd, http.MethodGet, http.MethodPost))
	mux.Handle("POST /projects/{id}/apply/check", usesStore(handleApplyCheck(aw)))
	mux.Handle("/projects/{id}/apply/check", handlePostOnly(logger, rd))
	mux.Handle("GET /applies/{id}", usesStore(handleApplyRun(logger, rd, projects, scans, stores.Applies)))
	mux.Handle("POST /projects/{id}/bundles/{bundle}/original", adminStore(handleOriginal(tw, audits, originals)))
	mux.Handle("/projects/{id}/bundles/{bundle}/original", handlePostOnly(logger, rd))
	mux.Handle("POST /projects/{id}/scans", usesStore(handleScanCreate(logger, rd, scans)))
	mux.Handle("GET /scans/{id}", usesStore(handleScan(logger, rd, projects, scans, triage, stores.Applies)))
	mux.Handle("GET /scans/{id}/report", usesStore(handleReport(logger, rd, stores.Reports, time.Now)))
	mux.Handle("GET /scans/{id}/report.pdf", signedIn(handleReportPDF(logger, rd, stores.Reports, p, limits, time.Now)))
	mux.Handle("/", handleNotFound(logger, rd))
}
