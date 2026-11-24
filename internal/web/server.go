// Package web provides the HTTP handler for the secret-externalizer user
// interface.
package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
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

// TriageStore keeps the bundles of a project and the verdicts users give them.
// LatestScan returns the project's newest succeeded scan, or an error matching
// db.ErrNotFound. Entries returns one page of the triage list of a scan; a
// page past the last gives the last. VerdictCounts returns the number of
// bundles of each verdict that a scan found. Bundle returns a bundle with the
// members the scan found, or an error matching db.ErrNotFound. CustomerGroup returns
// the customer data a key names, with no bundles when it names nothing.
// SameValueBundles returns the other bundles of a bundle's value that the scan
// found. ApplyActive reports whether an apply of the project is being
// checked, is queued or runs, which is when Decide and MoveFinding refuse.
// AppliedBundles returns one page of the project's applied bundles,
// newest first, and AppliedCount their number. Decide and MoveFinding record a
// user's choice; Decide fails with an error matching triage.ErrNotAllowed or
// db.ErrDuplicate when it stores nothing because of the decision, MoveFinding
// with one matching triage.ErrNotAllowed when the finding may not move there,
// and both with one matching db.ErrNotFound when something they name is
// unknown. MergeBundle moves all findings of a bundle to another bundle, and
// fails like MoveFinding when any of them may not move there, moving none and db.ErrApplyActive while an apply of the project is active.
type TriageStore interface {
	LatestScan(ctx context.Context, projectID project.ID) (scan.Scan, error)
	Entries(ctx context.Context, projectID project.ID, scanID scan.ID, f triage.Filter) (triage.Page, error)
	VerdictCounts(ctx context.Context, projectID project.ID, scanID scan.ID) (map[triage.Verdict]int, error)
	Bundle(ctx context.Context, projectID project.ID, scanID scan.ID, id triage.BundleID) (triage.Bundle, error)
	CustomerGroup(ctx context.Context, projectID project.ID, scanID scan.ID, key triage.GroupKey) (triage.Group, error)
	SameValueBundles(ctx context.Context, projectID project.ID, scanID scan.ID, id triage.BundleID) ([]triage.Entry, error)
	AppliedBundles(ctx context.Context, projectID project.ID, page int) (triage.AppliedPage, error)
	AppliedCount(ctx context.Context, projectID project.ID) (int, error)
	ApplyActive(ctx context.Context, projectID project.ID) (bool, error)
	Decide(ctx context.Context, projectID project.ID, ids []triage.BundleID, d triage.Decision, by audit.Actor) error
	MoveFinding(ctx context.Context, projectID project.ID, id finding.ID, to triage.BundleID, by audit.Actor) (triage.BundleID, error)
	MergeBundle(ctx context.Context, projectID project.ID, from, to triage.BundleID, by audit.Actor) error
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
// db.ErrScanFollowed when a scan is newer than the checked one, and
// db.ErrNotFound when the run is unknown or not ready. ApplyRun and
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

// ReportStore reads what a scan's report shows. Report fails with an
// error matching db.ErrNotFound when the scan is unknown.
type ReportStore interface {
	Report(ctx context.Context, id scan.ID) (report.Data, error)
}

// DashboardStore reads what the dashboard shows. Dashboard fails with an
// error matching db.ErrNotFound when the filter names an unknown project.
type DashboardStore interface {
	Dashboard(ctx context.Context, f dashboard.Filter) (dashboard.Data, error)
}

// Converter turns a document into a PDF. Convert fails with an error
// matching pdf.ErrUnavailable when the converter cannot be reached or is
// too slow, and pdf.ErrRejected when it does not return a PDF.
type Converter interface {
	Convert(ctx context.Context, doc pdf.Document) ([]byte, error)
}

// Limits bounds the calls one request makes: Store the database and the
// secret store, PDF one conversion together with its wait for a free
// conversion. Conversions is how many conversions run at one time; a value
// below one means one.
type Limits struct {
	Store, PDF  time.Duration
	Conversions int
}

// printer is what the PDF export needs: the converter, the files every
// document sends beside its page, made once, and slots, which holds one
// value for every conversion that is running.
type printer struct {
	converter Converter
	files     []pdf.File
	slots     chan struct{}
}

// errPDFBusy means no conversion became free before the context ended.
var errPDFBusy = errors.New("every pdf conversion is taken")

// convert converts doc once fewer conversions are running than slots
// holds. The wait and the conversion share the deadline of ctx. It fails
// with an error matching errPDFBusy when ctx ends during the wait.
func (p printer) convert(ctx context.Context, doc pdf.Document) ([]byte, error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", errPDFBusy, ctx.Err())
	}
	defer func() { <-p.slots }()
	return p.converter.Convert(ctx, doc)
}

// Stores holds the stores the server reads and writes.
type Stores struct {
	Projects   ProjectStore
	Scans      ScanStore
	Triage     TriageStore
	Audits     AuditStore
	Applies    ApplyStore
	Reports    ReportStore
	Dashboards DashboardStore
}

// NewServer returns the complete handler, including middleware. From the
// outside in, a request passes panic recovery, the request log, the security
// headers, and cross-origin protection before it reaches its route. A route
// that calls the stores or the originals runs under a deadline limits.Store
// from the moment it is reached; the session is read before that deadline
// starts. The PDF export reads the store under limits.Store and converts
// under limits.PDF, at most limits.Conversions documents at one time.
// NewServer panics when the stylesheet names a font the
// binary does not hold.
func NewServer(logger *slog.Logger, authn Authenticator, sessions SessionStore, attempts AttemptStore,
	stores Stores, suggester Suggester, originals Originals, converter Converter, limits Limits,
) http.Handler {
	mux := http.NewServeMux()
	a := loadAssets(staticFiles())
	rd := newRenderer(a)
	addRoutes(mux, logger, rd, a, authn, sessions, attempts, stores, suggester, originals, printer{converter, printFiles(a), make(chan struct{}, max(1, limits.Conversions))}, limits)

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
