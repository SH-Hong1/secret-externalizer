package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// historySuggester suggests what an earlier verdict on the same value
// elsewhere says, and what the rules say when there is none.
type historySuggester struct {
	store TriageStore
}

// NewHistorySuggester returns a Suggester that reads earlier verdicts and
// used names from store.
func NewHistorySuggester(store TriageStore) Suggester {
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
// request. The status defaults to undecided and the page to 1.
func parseTriageQuery(r *http.Request) (triageQuery, selection, error) {
	v := r.URL.Query()
	q := triageQuery{Category: finding.Category(v.Get("category")), Status: triage.StatusUndecided, Page: 1, Sel: v.Get("sel")}
	if q.Category != "" && !validCategory(q.Category) {
		return q, selection{}, fmt.Errorf("%w: category %q", errBadQuery, q.Category)
	}
	if v.Has("status") {
		q.Status = v.Get("status")
	}
	if !validStatus(q.Status) {
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
// detail gets only the detail and the list, which htmx swaps in place.
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
		data := triagePage{User: u, Project: p, Category: q.Category, Status: q.Status, Options: newCategoryOptions(q.Category)}
		sc, err := store.LatestScan(r.Context(), p.ID)
		if errors.Is(err, db.ErrNotFound) {
			data.NoScan = true
			rd.triage(w, r, logger, data)
			return
		}
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading latest scan: %w", err))
			return
		}
		status, err := fillTriagePage(r, logger, store, suggester, &data, q, sel, sc)
		if err != nil {
			rd.fail(w, r, logger, status, err)
			return
		}
		rd.triage(w, r, logger, data)
	})
}

// fillTriagePage reads the list page and the selected detail of scan sc into
// data. On failure it returns the status of the error page.
func fillTriagePage(r *http.Request, logger *slog.Logger, store TriageStore, suggester Suggester, data *triagePage, q triageQuery, sel selection, sc scan.Scan) (int, error) {
	ctx := r.Context()
	pid := data.Project.ID
	page, err := store.Entries(ctx, pid, sc.ID, triage.Filter{Category: q.Category, Status: q.Status, Page: q.Page})
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("listing triage entries: %w", err)
	}
	q.Page = page.Page
	if q.Sel == "" && len(page.Entries) > 0 {
		e := page.Entries[0]
		if e.Bundle != 0 {
			q.Sel, sel = bundleSel(e.Bundle), selection{Bundle: e.Bundle}
		} else {
			q.Sel, sel = groupSel(e.Group), selection{Group: e.Group}
		}
	}

	data.ScanID, data.ScanAt, data.Commit = sc.ID, formatTime(sc.RequestedAt), shortCommit(sc.CommitHash)
	for v, n := range page.Counts {
		data.Total += n
		if v == "" {
			data.Undecided = n
		}
	}
	data.Statuses = newStatusChips(pid, q, page.Counts)
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
		b, err := store.Bundle(ctx, pid, sc.ID, sel.Bundle)
		if errors.Is(err, db.ErrNotFound) {
			return http.StatusNotFound, err
		}
		if err != nil {
			return http.StatusInternalServerError, fmt.Errorf("reading bundle: %w", err)
		}
		if b.Category == finding.CategoryCustomerData {
			sel.Group = triage.GroupKey{File: b.Members[0].File, Kind: b.Kind, Verdict: b.Decision.Verdict}
			return fillCustomerDetail(ctx, store, data, sc.ID, sel.Group, ret)
		}
		var s triage.Suggestion
		if b.Decision.Verdict == "" {
			if s, err = suggester.Suggest(ctx, b); err != nil {
				logger.ErrorContext(ctx, "suggesting a verdict", "project_id", pid, "bundle_id", b.ID, "error", err)
				s = triage.Suggestion{}
			}
		}
		data.Bundle = newBundleDetail(pid, ret, b, s)
	default:
		return fillCustomerDetail(ctx, store, data, sc.ID, sel.Group, ret)
	}
	return http.StatusOK, nil
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
	if errors.Is(err, db.ErrNotFound) {
		return http.StatusNotFound, err
	}
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("reading bundle of customer data group: %w", err)
	}
	data.Customer = newCustomerDetail(pid, ret, g, first)
	return http.StatusOK, nil
}

// triage writes the triage screen, or only its detail and list for an htmx
// request that targets the detail.
func (rd *renderer) triage(w http.ResponseWriter, r *http.Request, logger *slog.Logger, data triagePage) {
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "detail" {
		rd.fragment(w, r, logger, http.StatusOK, "triage.html", "fragment", data)
		return
	}
	rd.page(w, r, logger, http.StatusOK, "triage.html", data)
}
