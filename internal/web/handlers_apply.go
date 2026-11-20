package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// The reasons a check or an apply is refused, shown at the top of the apply
// page.
const (
	msgPlanChanged      = "판정이 바뀌었습니다. 다시 점검해 주세요."
	msgApplyScanActive  = "이 프로젝트의 스캔이 진행 중입니다. 스캔이 끝난 뒤 다시 점검해 주세요."
	msgApplyAlready     = "이 프로젝트의 점검이나 적용이 이미 진행 중입니다."
	msgApplyNotPossible = "지금은 적용할 수 없습니다."
	msgCheckNotPossible = "지금은 점검할 수 없습니다. 아래 조건을 확인해 주세요."
	msgCheckNoScan      = "성공한 스캔이 없어 점검할 수 없습니다."
	msgCheckAgain       = "이 점검으로는 적용할 수 없습니다. 다시 점검해 주세요."
)

// applyWriter holds what the handlers of the apply page share.
type applyWriter struct {
	logger   *slog.Logger
	rd       *renderer
	projects ProjectStore
	store    ApplyStore
}

// handleApplyPage shows what an apply of the project would do and where its
// check is: the bundles by verdict and category, the conditions, and, for a
// ready check, the locations it changes and leaves alone and the form that
// confirms it. The page refreshes itself while the check runs.
func handleApplyPage(aw applyWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, ok := pathID(r, "id")
		if !ok {
			aw.rd.fail(w, r, aw.logger, http.StatusNotFound, errors.New("project id is not a number"))
			return
		}
		aw.show(w, r, project.ID(n), http.StatusOK, "")
	})
}

// handleApplyCheck asks the worker to check the project for the signed-in
// user and sends the browser back to the apply page. It refuses what the
// page would not offer: without a succeeded scan, while a scan, check or
// apply is active, or while the database shows a blocker, it shows the page
// again with the reason and status 409 and requests nothing, since such a
// check would be refused or blocked and would supersede the ready run.
func handleApplyCheck(aw applyWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, ok := pathID(r, "id")
		if !ok {
			aw.rd.fail(w, r, aw.logger, http.StatusNotFound, errors.New("project id is not a number"))
			return
		}
		pid := project.ID(n)
		sum, err := aw.store.ApplySummary(r.Context(), pid)
		if err != nil {
			aw.rd.fail(w, r, aw.logger, bundleReadStatus(err), fmt.Errorf("summarizing apply: %w", err))
			return
		}
		if msg := checkRefusal(sum); msg != "" {
			aw.show(w, r, pid, http.StatusConflict, msg)
			return
		}
		_, err = aw.store.RequestApplyCheck(r.Context(), pid, actorOf(r))
		switch {
		case err == nil:
			http.Redirect(w, r, applyPath(pid), http.StatusSeeOther) //nolint:gosec // G710: the path is built from the parsed project ID
		case errors.Is(err, db.ErrScanActive):
			aw.show(w, r, pid, http.StatusConflict, msgApplyScanActive)
		case errors.Is(err, db.ErrApplyActive):
			aw.show(w, r, pid, http.StatusConflict, msgApplyAlready)
		case errors.Is(err, db.ErrNotFound):
			// The project lost its succeeded scan since the summary; show
			// shows 404 when the project itself is gone.
			aw.show(w, r, pid, http.StatusConflict, msgCheckNoScan)
		default:
			aw.rd.fail(w, r, aw.logger, http.StatusInternalServerError, fmt.Errorf("requesting apply check: %w", err))
		}
	})
}

// handleApplyConfirm queues the apply of the run the form names, with the
// digest the page showed and the user's consent for the files that cannot
// be restored, and sends the browser to the page of the run. Consent counts
// only when the check listed such files. A run of another project is
// unknown. A run that is not ready is not confirmed: a confirmed one sends
// the browser to its page, so a second submit lands where the first did,
// and any other shows the apply page with status 409. A plan that changed
// or a scan in the way shows the apply page again with the reason and
// status 409, and nothing is stored.
func handleApplyConfirm(aw applyWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, ok := pathID(r, "id")
		if !ok {
			aw.rd.fail(w, r, aw.logger, http.StatusNotFound, errors.New("project id is not a number"))
			return
		}
		pid := project.ID(n)
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			aw.rd.fail(w, r, aw.logger, http.StatusBadRequest, fmt.Errorf("reading apply form: %w", err))
			return
		}
		runID, err := strconv.ParseInt(r.PostFormValue("run"), 10, 64)
		digest := r.PostFormValue("digest")
		if err != nil || runID < 1 || !validDigest(digest) {
			aw.rd.fail(w, r, aw.logger, http.StatusBadRequest, errors.New("apply form names no run or digest"))
			return
		}
		run, err := aw.store.ApplyRun(r.Context(), apply.ID(runID))
		if errors.Is(err, db.ErrNotFound) || err == nil && run.ProjectID != pid {
			aw.rd.fail(w, r, aw.logger, http.StatusNotFound, fmt.Errorf("apply run %d of project %d not found", runID, pid))
			return
		}
		if err != nil {
			aw.rd.fail(w, r, aw.logger, http.StatusInternalServerError, fmt.Errorf("reading apply run: %w", err))
			return
		}
		if run.State != apply.StateReady {
			aw.notReady(w, r, pid, run)
			return
		}
		allow := len(run.IrreversibleFiles) > 0 && r.PostFormValue("irreversible") == "on"
		err = aw.store.ConfirmApply(r.Context(), run.ID, digest, allow, actorOf(r))
		switch {
		case err == nil:
			http.Redirect(w, r, runPath(run.ID), http.StatusSeeOther)
		case errors.Is(err, db.ErrPlanChanged):
			aw.show(w, r, pid, http.StatusConflict, msgPlanChanged)
		case errors.Is(err, db.ErrScanActive):
			aw.show(w, r, pid, http.StatusConflict, msgApplyScanActive)
		case errors.Is(err, db.ErrNotFound):
			// The run left ready since it was read: another submit or a
			// new check got there first.
			now, err := aw.store.ApplyRun(r.Context(), run.ID)
			if err != nil {
				aw.rd.fail(w, r, aw.logger, bundleReadStatus(err), fmt.Errorf("reading apply run: %w", err))
				return
			}
			aw.notReady(w, r, pid, now)
		default:
			aw.rd.fail(w, r, aw.logger, http.StatusInternalServerError, fmt.Errorf("confirming apply: %w", err))
		}
	})
}

// checkRefusal returns why the page would not offer a check of the project
// sum describes, or "" when it would.
func checkRefusal(sum apply.Summary) string {
	switch {
	case sum.Scan.ID == 0:
		return msgCheckNoScan
	case sum.Check != nil && sum.Check.State.Active():
		return msgApplyAlready
	case sum.ScanActive:
		return msgApplyScanActive
	case len(sum.Blockers) > 0:
		return msgCheckNotPossible
	}
	return ""
}

// notReady answers a confirmation of run, which is not ready: a confirmed
// run sends the browser to its page, and any other run shows the apply page
// with status 409.
func (aw applyWriter) notReady(w http.ResponseWriter, r *http.Request, pid project.ID, run apply.Run) {
	if !run.ConfirmedAt.IsZero() {
		http.Redirect(w, r, runPath(run.ID), http.StatusSeeOther)
		return
	}
	aw.show(w, r, pid, http.StatusConflict, msgCheckAgain)
}

// validDigest reports whether s has the form of a plan digest: 64
// lower-case hexadecimal digits.
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// show renders the apply page of project pid with status and the refusal
// msg, if any.
func (aw applyWriter) show(w http.ResponseWriter, r *http.Request, pid project.ID, status int, msg string) {
	page, failStatus, err := aw.build(r.Context(), pid)
	if err != nil {
		aw.rd.fail(w, r, aw.logger, failStatus, err)
		return
	}
	page.User, _ = userFrom(r.Context()) // requireLogin guarantees a user
	page.Error = msg
	aw.rd.page(w, r, aw.logger, status, "apply.html", page)
}

// build reads what the apply page of project pid shows. When it cannot, it
// returns the status of the error page with the error.
func (aw applyWriter) build(ctx context.Context, pid project.ID) (applyPage, int, error) {
	p, err := aw.projects.Project(ctx, pid)
	if err != nil {
		return applyPage{}, bundleReadStatus(err), fmt.Errorf("reading project: %w", err)
	}
	sum, err := aw.store.ApplySummary(ctx, pid)
	if err != nil {
		return applyPage{}, bundleReadStatus(err), fmt.Errorf("summarizing apply: %w", err)
	}
	page := applyPage{Project: p, ScanActive: sum.ScanActive}
	if sum.Scan.ID == 0 {
		page.View = applyViewNoScan
		return page, 0, nil
	}
	page.ScanID, page.ScanFinished = int64(sum.Scan.ID), formatTime(sum.Scan.FinishedAt)
	page.setCounts(sum.Counts)
	page.DBChecks = dbChecks(sum.Blockers, sum.ScanActive)
	dbOK := !sum.ScanActive && len(sum.Blockers) == 0
	page.CanCheck = dbOK
	page.View = applyViewBefore

	c := sum.Check
	if c == nil {
		return page, 0, nil
	}
	page.Run, page.RunState, page.RunHref = c, runStateOf(*c), runPath(c.ID)
	page.RunConfirmed = !c.ConfirmedAt.IsZero()
	page.RequestedAt, page.CheckedAt = formatTime(c.RequestedAt), formatTime(c.CheckedAt)
	switch c.State {
	case apply.StateChecking:
		page.View, page.CanCheck = applyViewChecking, false
	case apply.StateQueued, apply.StateRunning:
		page.View, page.CanCheck = applyViewApplying, false
	case apply.StateReady, apply.StateBlocked:
		if c.PlanDigest != sum.Digest {
			page.Stale = true
			return page, 0, nil
		}
		items, err := aw.store.ApplyItems(ctx, c.ID)
		if err != nil {
			return applyPage{}, http.StatusInternalServerError, fmt.Errorf("reading apply items: %w", err)
		}
		page.Checks = runChecks(c.Blockers)
		if c.State == apply.StateBlocked {
			page.View = applyViewBlocked
			page.Dirty = slices.Clone(c.DirtyFiles)
			page.DirtyMore = max(0, c.DirtyCount-len(c.DirtyFiles))
			page.Unchecked = countPlanned(items)
			return page, 0, nil
		}
		page.View = applyViewReady
		page.setItems(*c, items)
		if !dbOK {
			page.Blocked = msgApplyNotPossible
			return page, 0, nil
		}
		page.Digest = c.PlanDigest
	case apply.StateSucceeded, apply.StateFailed, apply.StateSuperseded:
	}
	return page, 0, nil
}
