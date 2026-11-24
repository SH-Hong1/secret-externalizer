package web

import (
	"slices"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// The views of the apply page, one for each place the project is in.
const (
	applyViewNoScan   = "no-scan"
	applyViewBefore   = "before"
	applyViewChecking = "checking"
	applyViewBlocked  = "blocked"
	applyViewReady    = "ready"
	applyViewApplying = "applying"
)

// applyPage is what the apply page shows. View is one of the applyView
// constants. Error is a refusal shown above everything else. Changes are
// the bundles the plan changes and Kept those it leaves alone. DBChecks are
// the conditions the database shows before a check; Checks are those of
// the shown run. Stale tells that the verdicts or the scan changed after
// the newest check, and StaleScan that it was the scan. CheckFailure is why the newest check failed, set only
// when it failed before anyone confirmed it. Digest is set only when the
// page offers the apply form.
type applyPage struct {
	User    auth.User
	Project project.Project
	View    string
	Error   string
	// NoWorker says that no worker serves the check the page waits for.
	NoWorker     bool
	ScanID       int64
	ScanFinished string
	ScanActive   bool
	Changes      []applyCountRow
	Kept         []applyCountRow
	DBChecks     []applyCheck
	CanCheck     bool
	Stale        bool
	StaleScan    bool
	CheckFailure *blockerView
	Run          *apply.Run
	RunState     applyStateView
	RunHref      string
	RunConfirmed bool
	RequestedAt  string
	CheckedAt    string
	Checks       []applyCheck
	Dirty        []string
	DirtyMore    int
	Unchecked    int
	Planned      []applyActionCount
	PlannedTotal int
	Skips        []skipGroup
	Irreversible []irreversibleFile
	Digest       string
	Blocked      string
}

// applyCountRow is one line of the table of bundles: a verdict, a category
// label (empty for the bundles left alone, which are summed over every
// category), and the counts.
type applyCountRow struct {
	Verdict   verdictView
	Category  string
	Bundles   int
	Locations int
	Effect    string
}

// applyCheck is one condition: what it asks, and whether the check found it
// met. Failures name each blocker that failed it.
type applyCheck struct {
	Label    string
	Status   checkStatus
	Failures []blockerView
}

// checkStatus is the outcome of one condition.
type checkStatus struct {
	Label string
	Class string // completes "badge-"
}

func checkPassed() checkStatus    { return checkStatus{"통과", "succeeded"} }
func checkFailed() checkStatus    { return checkStatus{"실패", "failed"} }
func checkUnchecked() checkStatus { return checkStatus{"점검하지 못함", "queued"} }

// blockerView is a blocker as the page says it: what is wrong, what to do
// about it when the user can act, and the code when the page does not know
// it.
type blockerView struct {
	Label  string
	Action string
	Code   string
}

// applyStateView names the state of a run for its badge: Class completes
// "badge-".
type applyStateView struct {
	Label string
	Class string
}

// applyActionCount is the number of locations a ready run changes in one
// way.
type applyActionCount struct {
	Label string
	N     int
}

// skipGroup is the locations a run leaves alone for one reason. The apply
// page lists at most maxSkipsShown of them in Items and sets Count to all of
// them and More to those it leaves out.
type skipGroup struct {
	Label  string
	Action string
	Code   string
	Items  []skipLine
	Count  int
	More   int
}

// maxSkipsShown is the number of locations the apply page lists for one
// reason, as many as it lists changed files.
const maxSkipsShown = apply.MaxDirtyShown

// skipLine is one location a run leaves alone.
type skipLine struct {
	File string
	Line int
	Kind string
	Name string
}

// irreversibleFile is an untracked file whose customer data a run would
// redact, with the number of its locations.
type irreversibleFile struct {
	File string
	N    int
}

// runStateOf returns the label and badge style of the state of r. A run
// that failed before anyone confirmed it is a failed check, not a failed
// apply.
func runStateOf(r apply.Run) applyStateView {
	if r.State == apply.StateFailed && r.ConfirmedAt.IsZero() {
		return applyStateView{"점검 실패", "failed"}
	}
	return applyStateOf(r.State)
}

// applyStateOf returns the label and badge style of a run state, in the
// colours of the scan states.
func applyStateOf(s apply.State) applyStateView {
	switch s {
	case apply.StateChecking:
		return applyStateView{"점검 중", "running"}
	case apply.StateReady:
		return applyStateView{"적용 가능", "succeeded"}
	case apply.StateBlocked:
		return applyStateView{"조건 미충족", "failed"}
	case apply.StateQueued:
		return applyStateView{"적용 대기 중", "queued"}
	case apply.StateRunning:
		return applyStateView{"적용 중", "running"}
	case apply.StateSucceeded:
		return applyStateView{"적용 완료", "succeeded"}
	case apply.StateFailed:
		return applyStateView{"적용 실패", "failed"}
	case apply.StateSuperseded:
		return applyStateView{"다시 점검됨", "queued"}
	}
	return applyStateView{"알 수 없는 상태", "queued"}
}

// blockerOf returns what the page says about a blocker. A blocker the page
// does not know keeps its code, which the template prints as text.
func blockerOf(b apply.Blocker) blockerView {
	switch b {
	case apply.BlockerWorkspaceReadOnly:
		return blockerView{
			Label:  "worker에 쓰기 가능한 작업 공간이 설정되어 있지 않습니다.",
			Action: "worker에 APPLY_WORKSPACE_ROOT를 설정하고 다시 점검해 주세요.",
		}
	case apply.BlockerProjectPath:
		return blockerView{
			Label:  "작업 공간에서 프로젝트 폴더를 찾을 수 없습니다.",
			Action: "프로젝트 경로가 작업 공간 안의 폴더인지 확인해 주세요.",
		}
	case apply.BlockerGitMissing:
		return blockerView{
			Label:  "worker에서 git을 실행할 수 없습니다.",
			Action: "worker 이미지와 설정에 git이 있는지 확인해 주세요.",
		}
	case apply.BlockerNotRepository:
		return blockerView{
			Label:  "프로젝트 폴더가 git 작업본이 아니거나, 저장소의 git 폴더가 작업 공간 밖에 있습니다.",
			Action: "프로젝트 폴더를 작업 공간 안의 git 저장소로 두고 커밋한 뒤 다시 점검해 주세요.",
		}
	case apply.BlockerNoCommit:
		return blockerView{
			Label:  "저장소에 커밋이 없습니다.",
			Action: "지금 상태를 한 번 커밋한 뒤 다시 점검해 주세요.",
		}
	case apply.BlockerGitFailed:
		return blockerView{
			Label:  "git이 작업본을 읽지 못했습니다.",
			Action: "worker 로그를 확인해 주세요.",
		}
	case apply.BlockerUnsafeGitConfig:
		return blockerView{
			Label:  "저장소 설정에 git이 외부 명령을 실행하게 하는 항목이 있거나, 인덱스에 폴더 밖의 경로가 있습니다.",
			Action: "저장소의 git 설정에서 filter.*.clean, filter.*.process, hook.*, core.worktree를 지운 뒤 다시 점검해 주세요.",
		}
	case apply.BlockerDirty:
		return blockerView{
			Label:  "커밋되지 않은 변경이 있습니다.",
			Action: "변경을 커밋하거나 stash한 뒤 다시 점검해 주세요.",
		}
	case apply.BlockerScanNotLatest:
		return blockerView{
			Label:  "마지막으로 성공한 스캔 뒤에 다른 스캔이 있습니다.",
			Action: "새 스캔이 성공한 뒤 판정을 확인하고 다시 점검해 주세요.",
		}
	case apply.BlockerManifestUnreadable:
		return blockerView{
			Label:  "프로젝트 폴더의 secrets-manifest.yml을 읽을 수 없습니다.",
			Action: "도구가 쓰는 형식이 아닌 내용(주석, 다른 키)을 지우거나 파일을 옮긴 뒤 다시 점검해 주세요.",
		}
	case apply.BlockerNothingToApply:
		return blockerView{
			Label:  "적용할 수 있는 항목이 없습니다.",
			Action: "판정 화면에서 항목을 외부화나 삭제·마스킹으로 판정해 주세요. 판정한 항목이 있다면 모두 적용이 건너뛰는 항목입니다.",
		}
	}
	return blockerView{Label: "알 수 없는 조건입니다.", Code: string(b)}
}

// reasonOf returns what the page says about a reason a location is left
// alone. A reason the page does not know keeps its code, which the template
// prints as text.
func reasonOf(r apply.Reason) blockerView {
	switch r {
	case apply.ReasonFileChanged:
		return blockerView{Label: "재스캔 필요", Action: "스캔 뒤에 파일이 바뀌었습니다. 다시 스캔하고 판정을 확인해 주세요."}
	case apply.ReasonFileMissing:
		return blockerView{Label: "파일이 없음", Action: "스캔 뒤에 파일이 지워졌거나 옮겨졌습니다. 다시 스캔해 주세요."}
	case apply.ReasonNotText:
		return blockerView{Label: "텍스트로 읽을 수 없는 파일"}
	case apply.ReasonValueMismatch:
		return blockerView{Label: "값이 스캔 때와 다름", Action: "다시 스캔해 주세요."}
	case apply.ReasonNotAllowed:
		return blockerView{Label: "이 항목에는 적용할 수 없는 판정", Action: "판정 화면에서 판정을 확인해 주세요."}
	case apply.ReasonOverlap:
		return blockerView{Label: "다른 항목과 범위가 겹침", Action: "판정 화면에서 겹치는 항목의 판정을 확인해 주세요."}
	case apply.ReasonUntrackedIrreversible:
		return blockerView{Label: "git이 추적하지 않아 되돌릴 수 없음", Action: "복구 불가에 동의해야 가립니다."}
	case apply.ReasonNestedRepository:
		return blockerView{Label: "프로젝트 안의 다른 저장소에 있음"}
	case apply.ReasonKeyInSourceFile:
		return blockerView{Label: "소스 파일이라 통째로 옮기지 않음"}
	case apply.ReasonKeyHoldsCustomerData:
		return blockerView{Label: "고객정보가 있을 수 있는 파일이라 통째로 옮기지 않음"}
	case apply.ReasonHoldsCustomerData:
		return blockerView{Label: "고객정보가 섞여 있음", Action: "판정 화면에서 그 자리의 고객정보를 판정해 주세요."}
	case apply.ReasonFileRemoved:
		return blockerView{Label: "파일과 함께 옮겨짐"}
	case apply.ReasonTooLarge:
		return blockerView{Label: "값이 너무 큼"}
	case apply.ReasonStoreFailed:
		return blockerView{Label: "비밀 저장소에 저장하지 못함", Action: "worker 로그를 확인해 주세요."}
	case apply.ReasonWriteFailed:
		return blockerView{Label: "파일을 쓰지 못함", Action: "worker 로그를 확인해 주세요."}
	case apply.ReasonInterrupted:
		return blockerView{Label: "중단됨", Action: "다시 점검해 주세요."}
	}
	return blockerView{Label: "알 수 없는 사유", Code: string(r)}
}

// actionLabel returns what an action does to a location.
func actionLabel(a apply.Action) string {
	switch a {
	case apply.ActionReplace:
		return "값을 비밀 저장소로 옮기고 placeholder로 바꿈"
	case apply.ActionRedact:
		return "[REDACTED]로 가림"
	case apply.ActionRemoveFile:
		return "파일을 비밀 저장소로 옮기고 지움"
	}
	return "알 수 없는 동작"
}

// changedVerdicts returns the verdicts an apply acts on, in the order the
// page lists them; keptVerdicts those it leaves alone.
func changedVerdicts() []triage.Verdict {
	return []triage.Verdict{triage.VerdictExternalize, triage.VerdictRedact}
}

func keptVerdicts() []triage.Verdict {
	return []triage.Verdict{"", triage.VerdictHold, triage.VerdictFalsePositive}
}

// setCounts fills the table of bundles from counts: a line for each
// category of a verdict the apply acts on, in categoryOrder, and one line
// for each other verdict, summed over the categories. counts is not changed.
func (p *applyPage) setCounts(counts []apply.Count) {
	rank := func(c finding.Category) int {
		if i := slices.Index(categoryOrder(), c); i >= 0 {
			return i
		}
		return len(categoryOrder())
	}
	sorted := slices.Clone(counts)
	slices.SortStableFunc(sorted, func(a, b apply.Count) int { return rank(a.Category) - rank(b.Category) })
	for _, v := range changedVerdicts() {
		effect := actionLabel(apply.ActionReplace)
		if v == triage.VerdictRedact {
			effect = actionLabel(apply.ActionRedact)
		}
		for _, c := range sorted {
			if c.Verdict == v {
				p.Changes = append(p.Changes, applyCountRow{verdictOf(v), categoryLabel(c.Category), c.Bundles, c.Locations, effect})
			}
		}
	}
	for _, v := range keptVerdicts() {
		row := applyCountRow{Verdict: verdictOf(v), Effect: "바꾸지 않음"}
		for _, c := range counts {
			if c.Verdict == v {
				row.Bundles += c.Bundles
				row.Locations += c.Locations
			}
		}
		if row.Bundles > 0 {
			p.Kept = append(p.Kept, row)
		}
	}
}

// dbChecks returns the conditions the database shows before a check: no
// scan runs, the latest succeeded scan is the newest, and something is to be
// applied.
func dbChecks(blockers []apply.Blocker, scanActive bool) []applyCheck {
	scanCheck := applyCheck{Label: "진행 중인 스캔이 없음", Status: checkPassed()}
	if scanActive {
		scanCheck.Status = checkFailed()
		scanCheck.Failures = []blockerView{{Label: "이 프로젝트의 스캔이 진행 중입니다.", Action: "스캔이 끝난 뒤 다시 점검해 주세요."}}
	}
	return []applyCheck{
		scanCheck,
		conditionOf("마지막으로 성공한 스캔이 가장 새 스캔임", blockers, false, apply.BlockerScanNotLatest),
		conditionOf("적용할 판정이 있음", blockers, false, apply.BlockerNothingToApply),
	}
}

// runChecks returns the conditions a check looked at. A condition the
// check could not reach because an earlier one failed is unchecked. The last
// one is named apart from that of dbChecks: the database knows only that
// verdicts are there to apply, and the check knows whether any place of them
// can be changed.
func runChecks(blockers []apply.Blocker) []applyCheck {
	has := func(bs ...apply.Blocker) bool {
		for _, b := range bs {
			if slices.Contains(blockers, b) {
				return true
			}
		}
		return false
	}
	noFolder := has(apply.BlockerWorkspaceReadOnly, apply.BlockerProjectPath)
	noGit := noFolder || has(apply.BlockerGitMissing, apply.BlockerNotRepository, apply.BlockerNoCommit, apply.BlockerGitFailed)
	noState := noGit || has(apply.BlockerUnsafeGitConfig)
	checks := []applyCheck{
		conditionOf("작업 공간에 쓸 수 있고 프로젝트 폴더가 있음", blockers, false,
			apply.BlockerWorkspaceReadOnly, apply.BlockerProjectPath),
		conditionOf("프로젝트 폴더가 커밋이 있는 git 작업본임", blockers, noFolder,
			apply.BlockerGitMissing, apply.BlockerNotRepository, apply.BlockerNoCommit, apply.BlockerGitFailed),
		conditionOf("저장소 설정이 외부 명령을 실행하지 않음", blockers, noGit, apply.BlockerUnsafeGitConfig),
		conditionOf("커밋되지 않은 변경이 없음", blockers, noState, apply.BlockerDirty),
		conditionOf("기존 secrets-manifest.yml을 읽을 수 있음", blockers, noFolder, apply.BlockerManifestUnreadable),
		conditionOf("마지막으로 성공한 스캔이 가장 새 스캔임", blockers, false, apply.BlockerScanNotLatest),
		conditionOf("적용할 수 있는 항목이 있음", blockers, false, apply.BlockerNothingToApply),
	}
	var unknown []blockerView
	for _, b := range blockers {
		if v := blockerOf(b); v.Code != "" {
			unknown = append(unknown, v)
		}
	}
	if unknown != nil {
		checks = append(checks, applyCheck{Label: "그 밖의 조건", Status: checkFailed(), Failures: unknown})
	}
	return checks
}

// conditionOf returns the condition label, failed when blockers holds any of
// of, unchecked when it holds none of them and unreached is true, and passed
// otherwise.
func conditionOf(label string, blockers []apply.Blocker, unreached bool, of ...apply.Blocker) applyCheck {
	c := applyCheck{Label: label}
	for _, b := range of {
		if slices.Contains(blockers, b) {
			c.Failures = append(c.Failures, blockerOf(b))
		}
	}
	switch {
	case c.Failures != nil:
		c.Status = checkFailed()
	case unreached:
		c.Status = checkUnchecked()
	default:
		c.Status = checkPassed()
	}
	return c
}

// setItems fills what a ready run will do from its items: the planned
// locations by action, the skipped ones by reason with the files that need
// a new scan first, and the untracked files whose customer data only the
// user's consent lets the apply redact.
func (p *applyPage) setItems(r apply.Run, items []apply.Item) {
	planned := map[apply.Action]int{}
	groups := map[apply.Reason]*skipGroup{}
	var order []apply.Reason
	for _, it := range items {
		switch it.Status {
		case apply.StatusPlanned:
			planned[it.Action]++
			p.PlannedTotal++
		case apply.StatusSkipped:
			g, ok := groups[it.Reason]
			if !ok {
				v := reasonOf(it.Reason)
				g = &skipGroup{Label: v.Label, Action: v.Action, Code: v.Code}
				groups[it.Reason] = g
				order = append(order, it.Reason)
			}
			g.Items = append(g.Items, skipLine{File: it.File, Line: it.StartLine, Kind: kindLabel(it.Kind), Name: it.Name})
		case apply.StatusWriting, apply.StatusApplied, apply.StatusFailed:
		}
	}
	for _, a := range []apply.Action{apply.ActionReplace, apply.ActionRedact, apply.ActionRemoveFile} {
		if planned[a] > 0 {
			p.Planned = append(p.Planned, applyActionCount{actionLabel(a), planned[a]})
		}
	}
	slices.SortStableFunc(order, func(a, b apply.Reason) int {
		return boolRank(a == apply.ReasonFileChanged) - boolRank(b == apply.ReasonFileChanged)
	})
	for _, reason := range order {
		g := *groups[reason]
		g.Count = len(g.Items)
		if g.Count > maxSkipsShown {
			g.Items, g.More = g.Items[:maxSkipsShown], g.Count-maxSkipsShown
		}
		p.Skips = append(p.Skips, g)
	}
	for _, f := range r.IrreversibleFiles {
		n := 0
		for _, it := range items {
			if it.File == f && it.Action == apply.ActionRedact && it.Status == apply.StatusPlanned {
				n++
			}
		}
		p.Irreversible = append(p.Irreversible, irreversibleFile{File: f, N: n})
	}
}

// boolRank orders true before false.
func boolRank(b bool) int {
	if b {
		return 0
	}
	return 1
}

// countPlanned returns the number of planned locations in items.
func countPlanned(items []apply.Item) int {
	n := 0
	for _, it := range items {
		if it.Status == apply.StatusPlanned {
			n++
		}
	}
	return n
}

// applyPath returns the path of the apply page of project id.
func applyPath(id project.ID) string {
	return "/projects/" + strconv.FormatInt(int64(id), 10) + "/apply"
}

// runPath returns the path of the page of apply run id.
func runPath(id apply.ID) string {
	return "/applies/" + strconv.FormatInt(int64(id), 10)
}
