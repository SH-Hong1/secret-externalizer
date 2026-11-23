package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// HistoryStore gives the earlier decisions a suggestion is built from.
// UsedNames returns the placeholder names a project already uses, and
// PastDecision the newest decision on a value in a bundle other than except,
// with false when there is none.
type HistoryStore interface {
	UsedNames(ctx context.Context, projectID project.ID) ([]triage.Name, error)
	PastDecision(ctx context.Context, fingerprint string, except triage.BundleID) (triage.Decision, bool, error)
}

// historySuggester suggests what an earlier verdict on the same value
// elsewhere says, and what the rules say when there is none.
type historySuggester struct {
	store HistoryStore
}

// NewHistorySuggester returns a Suggester that reads earlier verdicts and
// used names from store.
func NewHistorySuggester(store HistoryStore) Suggester {
	return historySuggester{store: store}
}

// Suggest adapts the newest decision on b's value in another bundle to b,
// or falls back to the rules. A name it suggests is not in use in b's
// project.
func (h historySuggester) Suggest(ctx context.Context, b triage.Bundle) (triage.Suggestion, error) {
	used, err := h.store.UsedNames(ctx, b.ProjectID)
	if err != nil {
		return triage.Suggestion{}, fmt.Errorf("reading used names: %w", err)
	}
	past, found, err := h.store.PastDecision(ctx, b.Fingerprint, b.ID)
	if err != nil {
		return triage.Suggestion{}, fmt.Errorf("reading past decision: %w", err)
	}
	if found {
		return triage.Adapt(past, b, used), nil
	}
	return triage.Default(b, used), nil
}

// errBadQuery means a triage screen request has a filter, page or selection
// the screen does not know.
var errBadQuery = errors.New("bad triage query")

// parseTriageQuery reads the filter, page and selection of a triage screen
// request. The status defaults to undecided and the page to 1; it may also
// be statusApplied.
func parseTriageQuery(r *http.Request) (triageQuery, selection, error) {
	return parseTriageValues(r.URL.Query())
}

// parseTriageValues reads a filter, page and selection from v.
func parseTriageValues(v url.Values) (triageQuery, selection, error) {
	q := triageQuery{Category: finding.Category(v.Get("category")), Status: triage.StatusUndecided, Page: 1, Sel: v.Get("sel")}
	if q.Category != "" && !validCategory(q.Category) {
		return q, selection{}, fmt.Errorf("%w: category %q", errBadQuery, q.Category)
	}
	if v.Has("status") {
		q.Status = v.Get("status")
	}
	if !validStatus(q.Status) && q.Status != statusApplied {
		return q, selection{}, fmt.Errorf("%w: status %q", errBadQuery, q.Status)
	}
	if v.Has("page") {
		n, err := strconv.Atoi(v.Get("page"))
		if err != nil || n < 1 {
			return q, selection{}, fmt.Errorf("%w: page %q", errBadQuery, v.Get("page"))
		}
		q.Page = n
	}
	var sel selection
	if q.Sel != "" {
		var ok bool
		if sel, ok = parseSel(q.Sel); !ok {
			return q, selection{}, fmt.Errorf("%w: selection", errBadQuery)
		}
	}
	return q, sel, nil
}

// handleTriage shows the triage screen of a project: one page of the
// bundles of its newest succeeded scan, and the detail of the selected one,
// or of the first when none is selected. An htmx request that targets the
// detail gets only the detail and the list, which htmx swaps in place. The
// status statusApplied shows one page of the project's applied bundles
// instead, which take no verdict.
func handleTriage(logger *slog.Logger, rd *renderer, projects ProjectStore, store TriageStore, suggester Suggester) http.Handler {
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
		q, sel, err := parseTriageQuery(r)
		if err != nil {
			rd.fail(w, r, logger, http.StatusBadRequest, err)
			return
		}
		data := triagePage{User: u, Project: p, Status: q.Status, Options: newCategoryOptions(q.Category), ApplyHref: applyPath(p.ID)}
		sc, err := store.LatestScan(r.Context(), p.ID)
		if errors.Is(err, db.ErrNotFound) {
			data.NoScan = true
			rd.triage(w, r, logger, http.StatusOK, data)
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading latest scan: %w", err))
			return
		}
		if q.Status == statusApplied {
			if err := fillAppliedPage(r.Context(), store, &data, q, sc); err != nil {
				rd.fail(w, r, logger, http.StatusInternalServerError, err)
				return
			}
			rd.page(w, r, logger, http.StatusOK, "triage.html", data)
			return
		}
		status, err := fillTriagePage(r.Context(), logger, store, suggester, &data, q, sel, sc)
		if err != nil {
			rd.fail(w, r, logger, status, err)
			return
		}
		rd.triage(w, r, logger, http.StatusOK, data)
	})
}

// fillAppliedPage reads the page of applied bundles q asks for into data,
// with the counts of the status filters for scan sc.
func fillAppliedPage(ctx context.Context, store TriageStore, data *triagePage, q triageQuery, sc scan.Scan) error {
	pid := data.Project.ID
	counts, err := store.VerdictCounts(ctx, pid, sc.ID)
	if err != nil {
		return fmt.Errorf("counting verdicts: %w", err)
	}
	applied, err := store.AppliedBundles(ctx, pid, q.Page)
	if err != nil {
		return fmt.Errorf("listing applied bundles: %w", err)
	}
	setScanHead(data, sc, counts)
	data.Statuses = newStatusChips(pid, q, counts, applied.Total)
	data.Applied = newAppliedTable(pid, applied)
	return nil
}

// setScanHead fills the head of the triage screen: the scan and the
// numbers of bundles.
func setScanHead(data *triagePage, sc scan.Scan, counts map[triage.Verdict]int) {
	data.ScanID, data.ScanAt, data.Commit = sc.ID, formatTime(sc.RequestedAt), shortCommit(sc.CommitHash)
	for v, n := range counts {
		data.Total += n
		if v == "" {
			data.Undecided = n
		}
	}
}

// fillTriagePage reads the list page and the selected detail of scan sc into
// data. On failure it returns the status of the error page.
func fillTriagePage(ctx context.Context, logger *slog.Logger, store TriageStore, suggester Suggester, data *triagePage, q triageQuery, sel selection, sc scan.Scan) (int, error) {
	pid := data.Project.ID
	page, err := store.Entries(ctx, pid, sc.ID, triage.Filter{Category: q.Category, Status: q.Status, Page: q.Page})
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("listing triage entries: %w", err)
	}
	q.Page = page.Page
	if q.Sel == "" && len(page.Entries) > 0 {
		q.Sel, sel = entrySel(page.Entries[0])
	}
	// A customer data bundle is shown as its group, so the selection is
	// resolved before the list marks its current line.
	var b triage.Bundle
	if sel.Bundle != 0 {
		if b, err = store.Bundle(ctx, pid, sc.ID, sel.Bundle); err != nil {
			return bundleReadStatus(err), fmt.Errorf("reading bundle: %w", err)
		}
		if b.Category == finding.CategoryCustomerData {
			sel = selection{Group: triage.GroupKey{File: b.Members[0].File, Kind: b.Kind, Verdict: b.Decision.Verdict}}
			q.Sel = groupSel(sel.Group)
		}
	}

	applied, err := store.AppliedCount(ctx, pid)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("counting applied bundles: %w", err)
	}
	data.ApplyHref = applyPath(pid)
	setScanHead(data, sc, page.Counts)
	data.Statuses = newStatusChips(pid, q, page.Counts, applied)
	data.Rows = newListRows(pid, q, page.Entries, q.Sel)
	data.ShowState = q.Status == triage.StatusAll
	data.Page, data.Pages, data.Entries = page.Page, page.Pages, page.Total
	if page.Page > 1 {
		pq := triageQuery{Category: q.Category, Status: q.Status, Page: page.Page - 1}
		data.PrevHref = pq.href(pid, true)
	}
	if page.Page < page.Pages {
		nq := triageQuery{Category: q.Category, Status: q.Status, Page: page.Page + 1}
		data.NextHref = nq.href(pid, true)
	}

	ret := triageQuery{Category: q.Category, Status: q.Status, Page: q.Page}.values(false).Encode()
	switch {
	case q.Sel == "":
		data.Empty = newEmptyState(pid, q, sc.ID, page.Counts)
	case sel.Bundle != 0:
		var s triage.Suggestion
		if b.Decision.Verdict == "" {
			if s, err = suggester.Suggest(ctx, b); err != nil {
				logger.ErrorContext(ctx, "suggesting a verdict", "project_id", pid, "bundle_id", b.ID, "error", err)
				s = triage.Suggestion{}
			}
		}
		same, err := store.SameValueBundles(ctx, pid, sc.ID, b.ID)
		if err != nil {
			return http.StatusInternalServerError, fmt.Errorf("listing bundles of the same value: %w", err)
		}
		data.Bundle = newBundleDetail(pid, ret, b, s, same)
		switch {
		case !data.User.Has(auth.RoleAdmin):
		case hasOriginal(b):
			data.Bundle.Original = newOriginalForm(pid, b.ID, ret)
		case b.Decision.Verdict == "":
			// The value's other bundles are all false positives, so the
			// scan did not keep its original; the next scan keeps it.
			data.Bundle.Original = &originalView{Shown: true, Notice: msgNoOriginal}
		}
	default:
		return fillCustomerDetail(ctx, store, data, sc.ID, sel.Group, ret)
	}
	return http.StatusOK, nil
}

// entrySel returns the selection of a list entry, as a parameter and parsed.
func entrySel(e triage.Entry) (string, selection) {
	if e.Bundle != 0 {
		return bundleSel(e.Bundle), selection{Bundle: e.Bundle}
	}
	return groupSel(e.Group), selection{Group: e.Group}
}

// bundleReadStatus returns the status of the error page for an error from
// reading a bundle.
func bundleReadStatus(err error) int {
	if errors.Is(err, db.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// fillCustomerDetail reads the customer data group key names into data.
func fillCustomerDetail(ctx context.Context, store TriageStore, data *triagePage, scanID scan.ID, key triage.GroupKey, ret string) (int, error) {
	pid := data.Project.ID
	g, err := store.CustomerGroup(ctx, pid, scanID, key)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("reading customer data group: %w", err)
	}
	if len(g.Bundles) == 0 {
		return http.StatusNotFound, errors.New("customer data group names nothing")
	}
	first, err := store.Bundle(ctx, pid, scanID, g.Bundles[0])
	if err != nil {
		return bundleReadStatus(err), fmt.Errorf("reading bundle of customer data group: %w", err)
	}
	data.Customer = newCustomerDetail(pid, ret, g, first)
	return http.StatusOK, nil
}

// triage writes the triage screen with status, or only its detail and list
// for an htmx request that targets the detail.
func (rd *renderer) triage(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, data triagePage) {
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "detail" {
		rd.fragment(w, r, logger, status, "triage.html", "fragment", data)
		return
	}
	rd.page(w, r, logger, status, "triage.html", data)
}
