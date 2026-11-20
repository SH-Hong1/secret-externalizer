// Package web provides the HTTP handler for the secret-externalizer user
// interface.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// Authenticator runs the login with the identity provider.
type Authenticator interface {
	AuthCodeURL(state, nonce, verifier string) string
	Exchange(ctx context.Context, code, verifier, nonce string) (auth.User, error)
	LogoutURL() string
}

// SessionStore keeps the sessions of signed-in users.
type SessionStore interface {
	Create(ctx context.Context, u auth.User) (session.ID, error)
	Get(ctx context.Context, id session.ID) (session.Session, error)
	Delete(ctx context.Context, id session.ID) error
}

// AttemptStore keeps login attempts between the redirect to the identity
// provider and its callback. TakeAttempt removes the attempt it returns.
type AttemptStore interface {
	PutAttempt(ctx context.Context, a auth.Attempt) (session.ID, error)
	TakeAttempt(ctx context.Context, id session.ID) (auth.Attempt, error)
}

// ProjectStore keeps the registered projects. CreateProject fails with an
// error matching db.ErrDuplicate when the name or path is taken, and Project
// with one matching db.ErrNotFound when the ID is unknown.
// ProjectSummaries returns every project ordered by name, with its newest
// scan and the bundle counts of its newest succeeded scan.
type ProjectStore interface {
	CreateProject(ctx context.Context, p project.Project) (project.ID, error)
	Project(ctx context.Context, id project.ID) (project.Project, error)
	ProjectSummaries(ctx context.Context) ([]project.Summary, error)
}

// ScanStore keeps the scans. RequestScan returns the ID of the project's
// active scan together with an error matching db.ErrScanActive when one is
// already queued or running, and an error matching db.ErrNotFound when the
// project is unknown. Scan fails with an error matching db.ErrNotFound when
// the ID is unknown. ScansByProject returns the project's scans, newest
// first. CategoryCounts returns the number of findings and locations a scan
// found in each category, leaving out the categories it found nothing in.
type ScanStore interface {
	RequestScan(ctx context.Context, projectID project.ID, by audit.Actor) (scan.ID, error)
	Scan(ctx context.Context, id scan.ID) (scan.Scan, error)
	ScansByProject(ctx context.Context, projectID project.ID) ([]scan.Scan, error)
	CategoryCounts(ctx context.Context, id scan.ID) ([]scan.CategoryCount, error)
}

// TriageStore keeps the bundles of a project and the verdicts users give
// them. LatestScan returns the project's newest succeeded scan, or an error
// matching db.ErrNotFound. Entries returns one page of the triage list of a
// scan; a page past the last gives the last. Bundle returns a bundle with
// the members the scan found, or an error matching db.ErrNotFound.
// CustomerGroup returns the customer data a key names, with no bundles when
// it names nothing. SameValueBundles returns the other bundles of a bundle's
// value that the scan found. AppliedBundles returns one page of the
// project's applied bundles, newest first, and AppliedCount their number. Decide and MoveFinding record a user's choice;
// Decide fails with an error matching triage.ErrNotAllowed or db.ErrDuplicate
// when it stores nothing because of the decision, MoveFinding with one
// matching triage.ErrNotAllowed when the finding may not move there, and both
// with one matching db.ErrNotFound when something they name is unknown and
// db.ErrApplyActive while an apply of the project is active.
type TriageStore interface {
	LatestScan(ctx context.Context, projectID project.ID) (scan.Scan, error)
	Entries(ctx context.Context, projectID project.ID, scanID scan.ID, f triage.Filter) (triage.Page, error)
	Bundle(ctx context.Context, projectID project.ID, scanID scan.ID, id triage.BundleID) (triage.Bundle, error)
	CustomerGroup(ctx context.Context, projectID project.ID, scanID scan.ID, key triage.GroupKey) (triage.Group, error)
	SameValueBundles(ctx context.Context, projectID project.ID, scanID scan.ID, id triage.BundleID) ([]triage.Entry, error)
	AppliedBundles(ctx context.Context, projectID project.ID, page int) (triage.AppliedPage, error)
	AppliedCount(ctx context.Context, projectID project.ID) (int, error)
	Decide(ctx context.Context, projectID project.ID, ids []triage.BundleID, d triage.Decision, by audit.Actor) error
	MoveFinding(ctx context.Context, projectID project.ID, id finding.ID, to triage.BundleID, by audit.Actor) (triage.BundleID, error)
}

// Suggester proposes a verdict and a name for a bundle that has no verdict.
// A suggestion is shown, never stored.
type Suggester interface {
	Suggest(ctx context.Context, b triage.Bundle) (triage.Suggestion, error)
}

// AuditStore keeps the audit trail. RecordReveal records that a user asked
// to see the original value of a bundle, and fails with an error matching
// db.ErrNotFound when the project has no such bundle. AuditEvents returns at
// most limit events, newest first, starting below the event ID before, or
// from the newest when before is 0.
type AuditStore interface {
	RecordReveal(ctx context.Context, projectID project.ID, id triage.BundleID, by audit.Actor) error
	AuditEvents(ctx context.Context, before int64, limit int) ([]audit.Event, error)
}

// Originals reads the original values that scans keep in the secret store.
// ReadPending fails with an error matching bao.ErrNoOriginal when the store
// holds no original for the fingerprint in the project.
type Originals interface {
	ReadPending(ctx context.Context, projectID int64, fingerprint string) (bao.Value, error)
}

// ApplyStore keeps the applies of projects. ApplySummary returns what the
// database tells about applying a project, or an error matching
// db.ErrNotFound when the project is unknown. RequestApplyCheck asks the
// worker to check a project; it fails with an error matching
// db.ErrScanActive or db.ErrApplyActive when a scan or an apply of the
// project is active and db.ErrNotFound when the project or its succeeded
// scan is unknown. ConfirmApply queues the apply of a ready run; it fails
// with an error matching db.ErrScanActive when a scan of the project is
// active, db.ErrPlanChanged when the plan is not the one the check showed,
// and db.ErrNotFound when the run is unknown or not ready. ApplyRun and
// ApplyItems read a run and its outcomes; ApplyHistory returns at most limit
// runs of a project, newest first: those a user confirmed and the active
// one, with their counts taken from their items. Reappeared counts the
// findings of applied bundles a scan found again.
type ApplyStore interface {
	ApplySummary(ctx context.Context, projectID project.ID) (apply.Summary, error)
	RequestApplyCheck(ctx context.Context, projectID project.ID, by audit.Actor) (apply.ID, error)
	ConfirmApply(ctx context.Context, id apply.ID, digest string, allowIrreversible bool, by audit.Actor) error
	ApplyRun(ctx context.Context, id apply.ID) (apply.Run, error)
	ApplyHistory(ctx context.Context, projectID project.ID, limit int) ([]apply.Run, error)
	ApplyItems(ctx context.Context, id apply.ID) ([]apply.Item, error)
	Reappeared(ctx context.Context, id scan.ID) (int, error)
}

// Stores holds the stores the server reads and writes.
type Stores struct {
	Projects ProjectStore
	Scans    ScanStore
	Triage   TriageStore
	Audits   AuditStore
	Applies  ApplyStore
}

// NewServer returns the complete handler, including middleware. From the
// outside in, a request passes panic recovery, the request log, the security
// headers, and cross-origin protection before it reaches its route. A route
// that calls the stores or the originals runs under a deadline storeLimit
// from the moment it is reached; the session is read before that deadline
// starts.
func NewServer(logger *slog.Logger, authn Authenticator, sessions SessionStore, attempts AttemptStore, stores Stores, suggester Suggester, originals Originals, storeLimit time.Duration) http.Handler {
	mux := http.NewServeMux()
	a := loadAssets(staticFiles())
	rd := newRenderer(a)
	addRoutes(mux, logger, rd, a, authn, sessions, attempts, stores, suggester, originals, storeLimit)

	return chain(mux,
		recoverPanic(logger, rd),
		requestLog(logger),
		securityHeaders(),
		crossOriginProtection(logger, rd),
	)
}

// chain wraps h in middleware. The first middleware is the outermost, so a
// request passes them in the order given.
func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}
