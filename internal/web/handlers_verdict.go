package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// The reasons a verdict or a move is refused, shown above the detail.
const (
	msgUnknownVerdict = "알 수 없는 판정입니다."
	msgHoldReason     = "보류 사유를 골라 주세요."
	msgName           = "이름은 점으로 나눈 소문자 낱말 두 개에서 다섯 개로 쓰고, ext로 시작하지 않게 입력해 주세요."
	msgNote           = "메모는 500자 이하로, 줄바꿈 없이 입력해 주세요."
	msgNotAllowed     = "이 항목에는 고를 수 없는 판정입니다."
	msgDuplicate      = "이미 쓰는 이름입니다. 다른 이름을 입력해 주세요."
	msgMoveTarget     = "옮길 묶음을 알 수 없습니다."
	msgMoveNotAllowed = "이 묶음으로 옮길 수 없습니다."
)

// verdictInput is what a verdict form submitted, kept to show it again when
// the verdict is refused.
type verdictInput struct {
	Verdict string
	Reason  string
	Name    string
	Note    string
}

// readVerdictInput reads the verdict fields of a parsed form.
func readVerdictInput(r *http.Request) verdictInput {
	return verdictInput{
		Verdict: r.PostFormValue("verdict"),
		Reason:  r.PostFormValue("hold_reason"),
		Name:    r.PostFormValue("name"),
		Note:    r.PostFormValue("note"),
	}
}

// decision returns the decision in asks for, or the reason it cannot be
// one. An empty verdict clears the verdict. The name counts only for
// externalize and the reason only for hold.
func (in verdictInput) decision() (triage.Decision, string) {
	var d triage.Decision
	switch v := triage.Verdict(in.Verdict); v {
	case "":
		return triage.Decision{}, ""
	case triage.VerdictExternalize:
		n, err := triage.ParseName(in.Name)
		if err != nil {
			return triage.Decision{}, msgName
		}
		d = triage.Decision{Verdict: v, Name: n}
	case triage.VerdictHold:
		r := triage.HoldReason(in.Reason)
		if !knownReason(r) {
			return triage.Decision{}, msgHoldReason
		}
		d = triage.Decision{Verdict: v, Reason: r}
	case triage.VerdictRedact, triage.VerdictFalsePositive:
		d = triage.Decision{Verdict: v}
	default:
		return triage.Decision{}, msgUnknownVerdict
	}
	if utf8.RuneCountInString(in.Note) > triage.MaxNote || !validText(in.Note) {
		return triage.Decision{}, msgNote
	}
	d.Note = in.Note
	return d, ""
}

// triageWriter holds what the handlers that record a choice on the triage
// screen share.
type triageWriter struct {
	logger    *slog.Logger
	rd        *renderer
	projects  ProjectStore
	store     TriageStore
	suggester Suggester
}

// pathID reads the path value name as an ID of at least one.
func pathID(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil && n >= 1
}

// readForm bounds and parses the form of a request that records a choice,
// and returns the screen to go back to. A return value the screen does not
// know gives the default screen, so the address is always the project's
// triage screen.
func readForm(w http.ResponseWriter, r *http.Request) (triageQuery, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return triageQuery{}, fmt.Errorf("reading triage form: %w", err)
	}
	def := triageQuery{Status: triage.StatusUndecided, Page: 1}
	v, err := url.ParseQuery(r.PostFormValue("return"))
	if err != nil {
		return def, nil //nolint:nilerr // a broken return value falls back to the default screen
	}
	v.Del("sel")
	q, _, err := parseTriageValues(v)
	if err != nil {
		return def, nil //nolint:nilerr // an unknown filter falls back to the default screen
	}
	return q, nil
}

// actorOf returns the signed-in user of r as the actor of an audit event.
func actorOf(r *http.Request) audit.Actor {
	u, _ := userFrom(r.Context()) // requireLogin guarantees a user
	return audit.Actor{Subject: u.Subject, Name: u.Name}
}

// back sends the browser to the triage screen of project pid for q. The
// address is built from the project ID and parsed, checked values, never
// from the text of the request.
func back(w http.ResponseWriter, r *http.Request, pid project.ID, q triageQuery) {
	http.Redirect(w, r, q.href(pid, false), http.StatusSeeOther) //nolint:gosec // G710: the path is fixed and the query holds only validated filter values and IDs
}

// reject shows the triage screen of q with sel selected, msg above the
// detail and the submitted input in the form, with status 400.
func (tw triageWriter) reject(w http.ResponseWriter, r *http.Request, pid project.ID, q triageQuery, sel string, in verdictInput, msg string) {
	ctx := r.Context()
	u, _ := userFrom(ctx) // requireLogin guarantees a user
	p, err := tw.projects.Project(ctx, pid)
	if err != nil {
		tw.rd.fail(w, r, tw.logger, bundleReadStatus(err), fmt.Errorf("reading project: %w", err))
		return
	}
	sc, err := tw.store.LatestScan(ctx, pid)
	if err != nil {
		tw.rd.fail(w, r, tw.logger, bundleReadStatus(err), fmt.Errorf("reading latest scan: %w", err))
		return
	}
	var s selection
	if sel != "" {
		s, _ = parseSel(sel) // sel is built by the handlers, never taken from input
	}
	q.Sel = sel
	data := triagePage{User: u, Project: p, Status: q.Status, Options: newCategoryOptions(q.Category), Error: msg}
	if status, err := fillTriagePage(ctx, tw.logger, tw.store, tw.suggester, &data, q, s, sc); err != nil {
		tw.rd.fail(w, r, tw.logger, status, err)
		return
	}
	switch {
	case data.Bundle != nil:
		data.Bundle.Form.keep(in)
		if data.Bundle.Name != nil {
			data.Bundle.Name.Suggested = false
			data.Bundle.Name.setValue(in.Name)
		}
	case data.Customer != nil:
		data.Customer.Form.keep(in)
	}
	tw.rd.triage(w, r, tw.logger, http.StatusBadRequest, data)
}

// decideError answers an error from Decide: a refused decision shows the
// screen again with the reason, and anything else the error page.
func (tw triageWriter) decideError(w http.ResponseWriter, r *http.Request, pid project.ID, q triageQuery, sel string, in verdictInput, err error) {
	switch {
	case errors.Is(err, triage.ErrNotAllowed):
		tw.reject(w, r, pid, q, sel, in, msgNotAllowed)
	case errors.Is(err, db.ErrDuplicate):
		tw.reject(w, r, pid, q, sel, in, msgDuplicate)
	case errors.Is(err, db.ErrNotFound):
		tw.rd.fail(w, r, tw.logger, http.StatusNotFound, err)
	default:
		tw.rd.fail(w, r, tw.logger, http.StatusInternalServerError, fmt.Errorf("recording verdict: %w", err))
	}
}

// decide records d for the bundles ids, which make up the list line old
// of scan sc and become the line cur, and sends the browser to the next
// line of the filter q.
func (tw triageWriter) decide(w http.ResponseWriter, r *http.Request, pid project.ID, sc scan.Scan, q triageQuery, ids []triage.BundleID, d triage.Decision, old, cur string, in verdictInput) {
	ctx := r.Context()
	filter := triage.Filter{Category: q.Category, Status: q.Status, Page: q.Page}
	before, err := tw.store.Entries(ctx, pid, sc.ID, filter)
	if err != nil {
		tw.rd.fail(w, r, tw.logger, http.StatusInternalServerError, fmt.Errorf("listing triage entries: %w", err))
		return
	}
	if err := tw.store.Decide(ctx, pid, ids, d, actorOf(r)); err != nil {
		tw.decideError(w, r, pid, q, old, in, err)
		return
	}
	after, err := tw.store.Entries(ctx, pid, sc.ID, filter)
	if err != nil {
		tw.rd.fail(w, r, tw.logger, http.StatusInternalServerError, fmt.Errorf("listing triage entries: %w", err))
		return
	}
	q.Sel, q.Page = nextSel(before, after, old, cur)
	back(w, r, pid, q)
}

// nextSel returns the selection and page to show after the list line old,
// as listed in before, was decided and became the line cur. It is the line
// after cur when the filter still lists cur, and the line that took old's
// place otherwise. When no line follows, cur stays selected if it is listed,
// and otherwise nothing is.
func nextSel(before, after triage.Page, old, cur string) (string, int) {
	if j := entryIndex(after.Entries, cur); j >= 0 {
		switch {
		case j+1 < len(after.Entries):
			s, _ := entrySel(after.Entries[j+1])
			return s, after.Page
		case after.Page < after.Pages:
			return "", after.Page + 1
		}
		return cur, after.Page
	}
	if i := entryIndex(before.Entries, old); i >= 0 && i < len(after.Entries) {
		s, _ := entrySel(after.Entries[i])
		return s, after.Page
	}
	return "", after.Page
}

func entryIndex(entries []triage.Entry, sel string) int {
	for i, e := range entries {
		if s, _ := entrySel(e); s == sel {
			return i
		}
	}
	return -1
}

// latestScan returns the newest succeeded scan of pid, or answers the
// request with the error page and returns false.
func (tw triageWriter) latestScan(w http.ResponseWriter, r *http.Request, pid project.ID) (scan.Scan, bool) {
	sc, err := tw.store.LatestScan(r.Context(), pid)
	if err != nil {
		tw.rd.fail(w, r, tw.logger, bundleReadStatus(err), fmt.Errorf("reading latest scan: %w", err))
		return scan.Scan{}, false
	}
	return sc, true
}

// handleBundleVerdict records the verdict a user gives one secret bundle and
// sends the browser back to the triage screen, on the next line of its
// filter.
func handleBundleVerdict(tw triageWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, ok1 := pathID(r, "id")
		b, ok2 := pathID(r, "bundle")
		if !ok1 || !ok2 {
			tw.rd.fail(w, r, tw.logger, http.StatusNotFound, errors.New("project or bundle id is not a number"))
			return
		}
		pid, id := project.ID(n), triage.BundleID(b)
		q, err := readForm(w, r)
		if err != nil {
			tw.rd.fail(w, r, tw.logger, http.StatusBadRequest, err)
			return
		}
		in := readVerdictInput(r)
		sel := bundleSel(id)
		d, msg := in.decision()
		if msg != "" {
			tw.reject(w, r, pid, q, sel, in, msg)
			return
		}
		sc, ok := tw.latestScan(w, r, pid)
		if !ok {
			return
		}
		tw.decide(w, r, pid, sc, q, []triage.BundleID{id}, d, sel, sel, in)
	})
}

// handleCustomerDataVerdict records one verdict for the customer data of one
// kind in one file that has the verdict the form was shown with, and sends
// the browser back to the triage screen, on the next line of its filter.
func handleCustomerDataVerdict(tw triageWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, ok := pathID(r, "id")
		if !ok {
			tw.rd.fail(w, r, tw.logger, http.StatusNotFound, errors.New("project id is not a number"))
			return
		}
		pid := project.ID(n)
		q, err := readForm(w, r)
		if err != nil {
			tw.rd.fail(w, r, tw.logger, http.StatusBadRequest, err)
			return
		}
		key := triage.GroupKey{
			File:    r.PostFormValue("file"),
			Kind:    finding.Kind(r.PostFormValue("kind")),
			Verdict: triage.Verdict(r.PostFormValue("from")),
		}
		if !customerKind(key.Kind) || key.File == "" || !validText(key.File) ||
			(key.Verdict != "" && (!validStatus(string(key.Verdict)) || string(key.Verdict) == triage.StatusUndecided || string(key.Verdict) == triage.StatusAll)) {
			tw.rd.fail(w, r, tw.logger, http.StatusBadRequest, errors.New("customer data form names no group"))
			return
		}
		in := readVerdictInput(r)
		in.Name = ""
		old := groupSel(key)
		d, msg := in.decision()
		if triage.Verdict(in.Verdict) == triage.VerdictExternalize {
			msg = msgNotAllowed // customer data never takes a placeholder name
		}
		if msg != "" {
			tw.reject(w, r, pid, q, old, in, msg)
			return
		}
		sc, ok := tw.latestScan(w, r, pid)
		if !ok {
			return
		}
		g, err := tw.store.CustomerGroup(r.Context(), pid, sc.ID, key)
		if err != nil {
			tw.rd.fail(w, r, tw.logger, http.StatusInternalServerError, fmt.Errorf("reading customer data group: %w", err))
			return
		}
		if len(g.Bundles) == 0 {
			tw.rd.fail(w, r, tw.logger, http.StatusNotFound, errors.New("customer data group names nothing"))
			return
		}
		cur := key
		cur.Verdict = d.Verdict
		tw.decide(w, r, pid, sc, q, g.Bundles, d, old, groupSel(cur), in)
	})
}

// handleMove moves one finding to a bundle of its own (to=new) or to another
// bundle of the same value, and sends the browser back to the triage screen
// with the bundle it came from selected, or the one it went to when the
// first is gone.
func handleMove(tw triageWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, ok1 := pathID(r, "id")
		f, ok2 := pathID(r, "finding")
		if !ok1 || !ok2 {
			tw.rd.fail(w, r, tw.logger, http.StatusNotFound, errors.New("project or finding id is not a number"))
			return
		}
		pid, fid := project.ID(n), finding.ID(f)
		q, err := readForm(w, r)
		if err != nil {
			tw.rd.fail(w, r, tw.logger, http.StatusBadRequest, err)
			return
		}
		var from triage.BundleID
		if n, err := strconv.ParseInt(r.PostFormValue("from"), 10, 64); err == nil && n >= 1 {
			from = triage.BundleID(n)
		}
		sel := ""
		if from != 0 {
			sel = bundleSel(from)
		}
		var to triage.BundleID
		if t := r.PostFormValue("to"); t != "new" {
			n, err := strconv.ParseInt(t, 10, 64)
			if err != nil || n < 1 {
				tw.reject(w, r, pid, q, sel, verdictInput{}, msgMoveTarget)
				return
			}
			to = triage.BundleID(n)
		}
		dest, err := tw.store.MoveFinding(r.Context(), pid, fid, to, actorOf(r))
		switch {
		case errors.Is(err, triage.ErrNotAllowed):
			tw.reject(w, r, pid, q, sel, verdictInput{}, msgMoveNotAllowed)
			return
		case errors.Is(err, db.ErrNotFound):
			tw.rd.fail(w, r, tw.logger, http.StatusNotFound, err)
			return
		case err != nil:
			tw.rd.fail(w, r, tw.logger, http.StatusInternalServerError, fmt.Errorf("moving finding: %w", err))
			return
		}
		q.Sel = bundleSel(dest)
		if from != 0 {
			sc, ok := tw.latestScan(w, r, pid)
			if !ok {
				return
			}
			_, err := tw.store.Bundle(r.Context(), pid, sc.ID, from)
			switch {
			case err == nil:
				q.Sel = bundleSel(from)
			case !errors.Is(err, db.ErrNotFound):
				tw.rd.fail(w, r, tw.logger, http.StatusInternalServerError, fmt.Errorf("reading bundle: %w", err))
				return
			}
		}
		back(w, r, pid, q)
	})
}
