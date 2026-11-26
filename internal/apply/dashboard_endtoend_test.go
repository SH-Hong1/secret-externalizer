package apply_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
	"github.com/SH-Hong1/secret-externalizer/internal/web"
)

// dashboardSession is the one session the dashboard checks sign in with.
const dashboardSession session.ID = "dev-only-fake-dashboard-session"

// oneSession is a session store that knows only dashboardSession.
type oneSession struct{}

func (oneSession) Create(context.Context, auth.User) (session.ID, error) {
	return "", fmt.Errorf("creating a session: %w", errNotInTest)
}

func (oneSession) Get(_ context.Context, id session.ID) (session.Session, error) {
	if id != dashboardSession {
		return session.Session{}, session.ErrNotFound
	}
	u := auth.User{Subject: "fake-subject", Name: "fake-user", Roles: []auth.Role{auth.RoleUser}}
	return session.Session{User: u, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (oneSession) Delete(context.Context, session.ID) error { return nil }

// noLogin stands for the identity provider and the login attempts, which
// the dashboard checks never reach.
type noLogin struct{}

func (noLogin) AuthCodeURL(string, string, string) string { return "" }

func (noLogin) Exchange(context.Context, string, string, string) (auth.User, error) {
	return auth.User{}, fmt.Errorf("exchanging a login code: %w", errNotInTest)
}

func (noLogin) LogoutURL() string { return "" }

func (noLogin) PutAttempt(context.Context, auth.Attempt) (session.ID, error) {
	return "", fmt.Errorf("storing a login attempt: %w", errNotInTest)
}

func (noLogin) TakeAttempt(context.Context, session.ID) (auth.Attempt, error) {
	return auth.Attempt{}, fmt.Errorf("taking a login attempt: %w", errNotInTest)
}

// errNotInTest is what a dependency the dashboard checks never call
// returns.
var errNotInTest = errors.New("not part of the dashboard checks")

// endScanDaysAgo moves the end of scan id, and its request, back by days
// from now, as if it had finished then.
func endScanDaysAgo(t *testing.T, admin *pgxpool.Pool, schema string, id scan.ID, days int) {
	t.Helper()
	tag, err := admin.Exec(t.Context(),
		"UPDATE "+schema+".scan SET requested_at = now() - make_interval(days => $2), finished_at = now() - make_interval(days => $2) WHERE id = $1",
		id, days)
	if err != nil {
		t.Fatalf("moving the end of scan %d back %d days: %v", id, days, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("moving the end of scan %d back %d days changed %d rows, want 1", id, days, tag.RowsAffected())
	}
}

// checkDashboardPeriods asks the dashboard page of the project for each
// period, on the server built over the real store as cmd/web builds it.
// first is the scan before the apply and confirming the scan after it;
// externalized is the number of externalized bundles of confirming. The
// check moves first back 40 days, then confirming back 10 days: the period
// picks the scans of the trend, and the tiles stay as they are.
func checkDashboardPeriods(t *testing.T, store *db.Store, admin *pgxpool.Pool, schema string, projectID project.ID, first, confirming scan.ID, externalized int) {
	t.Helper()
	handler := web.NewServer(slog.New(slog.DiscardHandler), noLogin{}, oneSession{}, noLogin{},
		web.Stores{Projects: store, Scans: store, Triage: store, Audits: store, Applies: store, Reports: store, Dashboards: store},
		nil, nil, nil, web.Limits{Store: time.Minute, PDF: time.Minute})
	get := func(period string) string {
		t.Helper()
		target := fmt.Sprintf("/dashboard?project=%d&period=%s", projectID, period)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		req.Header.Set("Cookie", "se_session="+string(dashboardSession))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body:\n%s", target, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	endScanDaysAgo(t, admin, schema, first, 40)
	endScanDaysAgo(t, admin, schema, confirming, 0)
	for _, c := range []struct {
		period  string
		trend   []scan.ID
		leftOut []scan.ID
	}{
		{period: "", trend: []scan.ID{first, confirming}},
		{period: "90d", trend: []scan.ID{first, confirming}},
		{period: "30d", trend: []scan.ID{confirming}, leftOut: []scan.ID{first}},
		{period: "7d", trend: []scan.ID{confirming}, leftOut: []scan.ID{first}},
	} {
		checkDashboardPage(t, get(c.period), c.period, c.trend, c.leftOut, false, externalized)
	}

	endScanDaysAgo(t, admin, schema, confirming, 10)
	checkDashboardPage(t, get("30d"), "30d, confirming 10 days ago", []scan.ID{confirming}, []scan.ID{first}, false, externalized)
	checkDashboardPage(t, get("7d"), "7d, confirming 10 days ago", nil, []scan.ID{first, confirming}, true, externalized)
}

// checkDashboardPage checks one dashboard page over the period named
// period: the trend links to the scans of trend and not to those of
// leftOut, the page says no scan ended in the period exactly when empty is
// true, and the tile counts externalized bundles whatever the period.
func checkDashboardPage(t *testing.T, body, period string, trend, leftOut []scan.ID, empty bool, externalized int) {
	t.Helper()
	scanLink := func(id scan.ID) string { return fmt.Sprintf(">#%d</a>", id) }
	tile := fmt.Sprintf("<span class=\"tile-label\">외부화 완료</span>\n<span class=\"tile-figure\">%d</span>", externalized)
	const noScans = "이 기간에 끝난 스캔이 없습니다."
	for _, id := range trend {
		if !strings.Contains(body, scanLink(id)) {
			t.Errorf("dashboard over period %q does not show scan %d in the trend", period, id)
		}
	}
	for _, id := range leftOut {
		if strings.Contains(body, scanLink(id)) {
			t.Errorf("dashboard over period %q shows scan %d, which ended before the period", period, id)
		}
	}
	if got := strings.Contains(body, noScans); got != empty {
		t.Errorf("dashboard over period %q says %q: %t, want %t", period, noScans, got, empty)
	}
	if !strings.Contains(body, tile) {
		t.Errorf("dashboard over period %q lacks the tile %q", period, tile)
	}
}
