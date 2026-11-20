package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// statusApplied is the triage screen status that lists the applied bundles
// instead of the bundles of the latest scan.
const statusApplied = "applied"

// applyRunPage is what the page of one apply run shows. Active is set while
// the run is queued or running, and the page then refreshes itself.
// Counts are taken from the items, so a run the queue gave up shows what it
// recorded. Changed counts the files an item of the run changed or was
// changing; NeedScan tells that such a run has no confirmation scan.
// Untouched are the places a finished run never reached, and CodeChanges
// counts the changed places whose code has to read the value elsewhere.
type applyRunPage struct {
	User         auth.User
	Project      project.Project
	Run          apply.Run
	State        applyStateView
	Active       bool
	ApplyHref    string
	ProjectHref  string
	RequestedAt  string
	ConfirmedAt  string
	StartedAt    string
	FinishedAt   string
	Irreversible string
	Failure      *blockerView
	Applied      int
	Skipped      int
	Failed       int
	Changed      int
	CodeChanges  int
	NeedScan     bool
	Bundles      []runBundle
	Skips        []skipGroup
	Failures     []skipGroup
	Writing      []skipLine
	Untouched    []skipLine
	Rescan       *confirmationView
}

// runBundle is the places an apply changed for one bundle: its names as
// written, where its value went, and each place with the line it holds
// after the apply.
type runBundle struct {
	Names     []string
	StorePath string
	KindLabel string
	Action    string
	Places    []runPlace
}

// runPlace is one place an apply changed. Line is empty for a removed file.
type runPlace struct {
	File       string
	Line       string
	CodeChange bool
}

// confirmationView is what a page says about the scan that confirms an
// apply. Kind picks the message: "active", "failed", "gone" (nothing found
// again and the scan checked everything), "partial" (nothing found again
// but the scan left something unchecked) or "again" (Reappeared findings of
// applied bundles found again).
type confirmationView struct {
	ScanID        scan.ID
	ScanHref      string
	RunID         apply.ID
	RunHref       string
	Kind          string
	Reappeared    int
	UncheckedHref string
	AppliedHref   string
}

// failureOf returns what a page says about the reason a check or an apply
// failed. A reason the page does not know keeps its text, which the
// template prints as code.
func failureOf(reason string) blockerView {
	switch reason {
	case apply.FailureDirty:
		return blockerView{Label: "커밋되지 않은 변경이 있습니다.", Action: "변경을 커밋하거나 stash한 뒤 다시 점검해 주세요."}
	case apply.FailureNotRepository:
		return blockerView{Label: "프로젝트 폴더가 git 작업본이 아닙니다.", Action: "프로젝트 폴더를 git 저장소로 두고 다시 점검해 주세요."}
	case apply.FailureUnsafeConfig:
		return blockerView{Label: "저장소 설정을 안전하게 읽을 수 없습니다.", Action: "확인 화면의 안내대로 git 설정을 고친 뒤 다시 점검해 주세요."}
	case apply.FailureManifest:
		return blockerView{Label: "기존 secrets-manifest.yml을 읽을 수 없습니다.", Action: "도구가 쓰는 형식이 아닌 내용을 지운 뒤 다시 점검해 주세요."}
	case apply.FailurePlanChanged:
		return blockerView{Label: "점검한 뒤 판정이나 스캔이 바뀌었습니다.", Action: "다시 점검하고 적용해 주세요."}
	case apply.FailureSecretStore:
		return blockerView{Label: "비밀 저장소에 연결할 수 없었습니다.", Action: "worker 로그를 확인한 뒤 다시 점검하고 적용해 주세요."}
	case apply.FailureTimeLimit:
		return blockerView{Label: "제한 시간 안에 끝나지 않았습니다.", Action: "다시 점검하고 적용하면 남은 위치를 이어서 바꿉니다."}
	case apply.FailureOther:
		return blockerView{Label: "적용을 끝내지 못했습니다.", Action: "worker 로그를 확인해 주세요."}
	case apply.FailureManifestWrite:
		return blockerView{Label: "secrets-manifest.yml을 쓰지 못했습니다.", Action: "worker 로그를 확인해 주세요. 다음 적용이 manifest를 다시 씁니다."}
	case db.ApplyGaveUpReason:
		return blockerView{Label: "worker가 세 번 멈춰 적용을 포기했습니다.", Action: "worker 로그를 확인해 주세요."}
	case db.CheckGaveUpReason:
		return blockerView{Label: "worker가 세 번 멈춰 점검을 포기했습니다.", Action: "worker 로그를 확인한 뒤 다시 점검해 주세요."}
	}
	return blockerView{Label: "알 수 없는 사유입니다.", Code: reason}
}

// handleApplyRun shows one apply run: its state, the counts, the places it
// changed by bundle, those it skipped or failed with the reason, and the
// scan that confirms it. The page refreshes itself while the run is queued
// or running. A run that is a check, or a check a newer one replaced, sends
// the browser to the apply page of its project. A run of a project that is
// gone is unknown.
func handleApplyRun(logger *slog.Logger, rd *renderer, projects ProjectStore, scans ScanStore, applies ApplyStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		n, ok := pathID(r, "id")
		if !ok {
			rd.fail(w, r, logger, http.StatusNotFound, errors.New("apply run id is not a number"))
			return
		}
		run, err := applies.ApplyRun(ctx, apply.ID(n))
		if err != nil {
			rd.fail(w, r, logger, bundleReadStatus(err), fmt.Errorf("reading apply run: %w", err))
			return
		}
		p, err := projects.Project(ctx, run.ProjectID)
		if err != nil {
			rd.fail(w, r, logger, bundleReadStatus(err), fmt.Errorf("reading project of apply run: %w", err))
			return
		}
		switch run.State {
		case apply.StateChecking, apply.StateReady, apply.StateBlocked, apply.StateSuperseded:
			http.Redirect(w, r, applyPath(p.ID), http.StatusSeeOther)
			return
		case apply.StateQueued, apply.StateRunning, apply.StateSucceeded, apply.StateFailed:
		}
		page := newApplyRunPage(p, run)
		page.User, _ = userFrom(ctx) // requireLogin guarantees a user
		if !page.Active {
			items, err := applies.ApplyItems(ctx, run.ID)
			if err != nil {
				rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading apply items: %w", err))
				return
			}
			page.setOutcomes(items)
		}
		if run.RescanID != 0 {
			c, err := readConfirmation(r, scans, applies, run.RescanID, run.ID, p.ID)
			if err != nil {
				rd.fail(w, r, logger, http.StatusInternalServerError, err)
				return
			}
			page.Rescan = c
		}
		page.NeedScan = run.State == apply.StateFailed && page.Changed > 0 && run.RescanID == 0
		rd.page(w, r, logger, http.StatusOK, "apply_run.html", page)
	})
}

// readConfirmation reads scan id, which confirms run, and what it found
// again.
func readConfirmation(r *http.Request, scans ScanStore, applies ApplyStore, id scan.ID, run apply.ID, pid project.ID) (*confirmationView, error) {
	sc, err := scans.Scan(r.Context(), id)
	if err != nil {
		return nil, fmt.Errorf("reading confirmation scan: %w", err)
	}
	n := 0
	if sc.Status == scan.StatusSucceeded {
		if n, err = applies.Reappeared(r.Context(), id); err != nil {
			return nil, fmt.Errorf("counting reappeared values: %w", err)
		}
	}
	return newConfirmation(sc, run, pid, n), nil
}

// newConfirmation describes scan sc, which confirms run of project pid and
// found n findings of applied bundles again.
func newConfirmation(sc scan.Scan, run apply.ID, pid project.ID, n int) *confirmationView {
	c := &confirmationView{
		ScanID: sc.ID, ScanHref: scanPath(sc.ID), RunID: run, RunHref: runPath(run), Reappeared: n,
		UncheckedHref: scanPath(sc.ID) + "#unchecked",
		AppliedHref:   triagePath(pid) + "?status=" + statusApplied,
	}
	switch {
	case sc.Status.Active():
		c.Kind = "active"
	case sc.Status != scan.StatusSucceeded:
		c.Kind = "failed"
	case n > 0:
		c.Kind = "again"
	case leftUnchecked(sc):
		c.Kind = "partial"
	default:
		c.Kind = "gone"
	}
	return c
}

// leftUnchecked reports whether sc skipped files, dropped results or met
// scanner errors.
func leftUnchecked(sc scan.Scan) bool {
	return sc.Skipped > 0 || sc.Dropped > 0 || len(scannerErrors(sc.ScannerErrors)) > 0
}

// scanPath returns the path of the page of scan id.
func scanPath(id scan.ID) string {
	return "/scans/" + strconv.FormatInt(int64(id), 10)
}

// newApplyRunPage shapes the head of the page of run of project p.
func newApplyRunPage(p project.Project, run apply.Run) applyRunPage {
	page := applyRunPage{
		Project:     p,
		Run:         run,
		State:       applyStateOf(run.State),
		Active:      run.State.Active(),
		ApplyHref:   applyPath(p.ID),
		ProjectHref: "/projects/" + strconv.FormatInt(int64(p.ID), 10),
		RequestedAt: formatTime(run.RequestedAt),
		ConfirmedAt: formatTime(run.ConfirmedAt),
		StartedAt:   formatTime(run.StartedAt),
		FinishedAt:  formatTime(run.FinishedAt),
	}
	if len(run.IrreversibleFiles) > 0 {
		page.Irreversible = "동의하지 않음 · 복구 불가 파일 " + strconv.Itoa(len(run.IrreversibleFiles)) + "개는 건너뜀"
		if run.AllowIrreversible {
			page.Irreversible = "동의함 · 복구 불가 파일 " + strconv.Itoa(len(run.IrreversibleFiles)) + "개"
		}
	}
	if run.State == apply.StateFailed {
		f := failureOf(run.FailureReason)
		page.Failure = &f
	}
	return page
}

// setOutcomes fills the counts and the lists of a finished run from its
// items: the applied places by bundle, the skipped and failed ones by
// reason, the ones left writing, and the ones never reached.
func (p *applyRunPage) setOutcomes(items []apply.Item) {
	changed := map[string]bool{}
	bundles := map[triage.BundleID]*runBundle{}
	var order []triage.BundleID
	skips := map[apply.Reason]*skipGroup{}
	failures := map[apply.Reason]*skipGroup{}
	var skipOrder, failOrder []apply.Reason
	group := func(groups map[apply.Reason]*skipGroup, order *[]apply.Reason, it apply.Item) {
		g, ok := groups[it.Reason]
		if !ok {
			v := reasonOf(it.Reason)
			g = &skipGroup{Label: v.Label, Action: v.Action, Code: v.Code}
			groups[it.Reason] = g
			*order = append(*order, it.Reason)
		}
		g.Items = append(g.Items, lineOf(it))
	}
	for _, it := range items {
		switch it.Status {
		case apply.StatusApplied:
			p.Applied++
			changed[it.File] = true
			b, ok := bundles[it.BundleID]
			if !ok {
				b = &runBundle{StorePath: it.StorePath, KindLabel: kindLabel(it.Kind), Action: actionLabel(it.Action)}
				bundles[it.BundleID] = b
				order = append(order, it.BundleID)
			}
			if it.Name != "" && !slices.Contains(b.Names, it.Name) {
				b.Names = append(b.Names, it.Name)
			}
			place := runPlace{File: it.File, CodeChange: it.CodeChange}
			if it.CodeChange {
				p.CodeChanges++
			}
			if it.Action != apply.ActionRemoveFile {
				place.Line = strconv.Itoa(it.Line)
			}
			b.Places = append(b.Places, place)
		case apply.StatusSkipped:
			p.Skipped++
			group(skips, &skipOrder, it)
		case apply.StatusFailed:
			p.Failed++
			group(failures, &failOrder, it)
		case apply.StatusWriting:
			changed[it.File] = true
			p.Writing = append(p.Writing, lineOf(it))
		case apply.StatusPlanned:
			p.Untouched = append(p.Untouched, lineOf(it))
		}
	}
	p.Changed = len(changed)
	for _, id := range order {
		p.Bundles = append(p.Bundles, *bundles[id])
	}
	slices.SortStableFunc(skipOrder, func(a, b apply.Reason) int {
		return boolRank(a == apply.ReasonFileChanged) - boolRank(b == apply.ReasonFileChanged)
	})
	for _, reason := range skipOrder {
		p.Skips = append(p.Skips, *skips[reason])
	}
	for _, reason := range failOrder {
		p.Failures = append(p.Failures, *failures[reason])
	}
}

// lineOf returns how a list shows the place of it: where the scan found it.
func lineOf(it apply.Item) skipLine {
	return skipLine{File: it.File, Line: it.StartLine, Kind: kindLabel(it.Kind), Name: it.Name}
}

// maxApplyRows is the number of applies the project page lists.
const maxApplyRows = 10

// applyRow is one apply on the project page. Counts is empty until the run
// has finished.
type applyRow struct {
	Href        string
	ID          apply.ID
	ConfirmedAt string
	Requester   string
	State       applyStateView
	Counts      string
}

// activeApply is the check or apply that keeps a project from being
// scanned: what it is, and the page that follows it.
type activeApply struct {
	Label string
	Href  string
	State string
}

// newApplyRows returns the newest confirmed runs of runs, which are ordered
// newest first, and the run that keeps the project busy, or nil.
func newApplyRows(runs []apply.Run) ([]applyRow, *activeApply) {
	var (
		rows   []applyRow
		active *activeApply
	)
	for _, r := range runs {
		if r.State.Active() && active == nil {
			active = &activeApply{Label: "적용 #" + strconv.FormatInt(int64(r.ID), 10), Href: runPath(r.ID), State: applyStateOf(r.State).Label}
			if r.State == apply.StateChecking {
				active.Label, active.Href, active.State = "조건 점검", applyPath(r.ProjectID), "진행 중"
			}
		}
		if r.ConfirmedAt.IsZero() || len(rows) == maxApplyRows {
			continue
		}
		row := applyRow{
			Href: runPath(r.ID), ID: r.ID, ConfirmedAt: formatTime(r.ConfirmedAt), Requester: r.Requester.Name,
			State: applyStateOf(r.State),
		}
		if r.State == apply.StateSucceeded || r.State == apply.StateFailed {
			row.Counts = "적용 " + strconv.Itoa(r.Applied) + " · 건너뜀 " + strconv.Itoa(r.Skipped) + " · 실패 " + strconv.Itoa(r.Failed)
		}
		rows = append(rows, row)
	}
	return rows, active
}
