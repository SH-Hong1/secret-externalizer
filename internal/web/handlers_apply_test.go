package web_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

const (
	applyPagePath  = "/projects/1/apply"
	applyCheckPath = "/projects/1/apply/check"
	// digestNow is the digest of the current plan in the fixtures and
	// digestOld that of a plan before the verdicts changed.
	digestNow = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestOld = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// fakePhone is a customer-data value that must never reach the page.
	fakePhone = "010-0000-0000"
)

// applyEnv is a test server with the project "shop" (ID 1) and no apply
// summary yet.
func applyEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	if _, err := e.projects.CreateProject(t.Context(), project.Project{Name: "shop", Path: "shop"}); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	return e
}

// summary is the summary of project 1 with its latest succeeded scan 7 and
// check, which may be nil.
func summary(check *apply.Run) apply.Summary {
	return apply.Summary{
		Scan:   scan.Scan{ID: 7, ProjectID: 1, Status: scan.StatusSucceeded, FinishedAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)},
		Digest: digestNow,
		Counts: []apply.Count{
			{Verdict: "", Category: finding.CategoryOther, Bundles: 2, Locations: 2},
			{Verdict: triage.VerdictExternalize, Category: finding.CategoryStorage, Bundles: 3, Locations: 7},
			{Verdict: triage.VerdictFalsePositive, Category: finding.CategoryOther, Bundles: 4, Locations: 9},
			{Verdict: triage.VerdictHold, Category: finding.CategoryStorage, Bundles: 1, Locations: 1},
			{Verdict: triage.VerdictRedact, Category: finding.CategoryCustomerData, Bundles: 2, Locations: 40},
		},
		Check: check,
	}
}

// run is run 5 of project 1 in state with the current digest.
func run(state apply.State) *apply.Run {
	return &apply.Run{
		ID: 5, ProjectID: 1, ScanID: 7, State: state, PlanDigest: digestNow, Requester: fakeActor,
		RequestedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
}

// readyItems are the outcomes a ready check saved: two places to replace,
// one to redact, and one file changed since the scan.
func readyItems() []apply.Item {
	return []apply.Item{
		{File: "conf/a.yml", StartLine: 3, Action: apply.ActionReplace, Kind: finding.KindPassword, Name: "ext.db.main.password", Status: apply.StatusPlanned},
		{File: "deploy/run.sh", StartLine: 4, Action: apply.ActionReplace, Kind: finding.KindPassword, Name: "EXT_DB_MAIN_PASSWORD", Status: apply.StatusPlanned},
		{File: "logs/x.log", StartLine: 7, Action: apply.ActionRedact, Kind: finding.KindPhone, Status: apply.StatusPlanned},
		{File: "conf/b.xml", StartLine: 9, Action: apply.ActionReplace, Kind: finding.KindPassword, Name: "ext.db.main.password", Status: apply.StatusSkipped, Reason: apply.ReasonFileChanged},
	}
}

// getApply shows the apply page of project 1 to a signed-in user.
func (e *testEnv) getApply(t *testing.T) result {
	t.Helper()
	res := e.do(t, http.MethodGet, applyPagePath, nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body:\n%s", applyPagePath, res.status, res.body)
	}
	return res
}

const (
	checkForm   = `action="/projects/1/apply/check"`
	digestField = `name="digest"`
	applyOff    = `<button class="btn btn-primary btn-lg" type="button" disabled>적용</button>`
	refresh     = `<meta http-equiv="refresh" content="3">`
)

func TestApplyPage_States(t *testing.T) {
	blocked := run(apply.StateBlocked)
	blocked.Blockers = []apply.Blocker{apply.BlockerDirty}
	blocked.DirtyCount = 53
	for i := range apply.MaxDirtyShown {
		blocked.DirtyFiles = append(blocked.DirtyFiles, "src/File"+strconv.Itoa(i)+".java")
	}
	stale := run(apply.StateReady)
	stale.PlanDigest = digestOld
	busy := summary(nil)
	busy.ScanActive = true
	busy.Blockers = []apply.Blocker{apply.BlockerScanNotLatest}
	nothing := summary(nil)
	nothing.Blockers = []apply.Blocker{apply.BlockerNothingToApply}
	readyBusy := summary(run(apply.StateReady))
	readyBusy.ScanActive = true

	tests := []struct {
		name     string
		summary  apply.Summary
		items    []apply.Item
		want     []string
		wantNot  []string
		noDigest bool
	}{
		{
			name:    "no succeeded scan",
			summary: apply.Summary{},
			want:    []string{"성공한 스캔이 없습니다", `href="/projects/1">프로젝트로 가기`},
			wantNot: []string{checkForm, "판정별 항목", refresh},
		},
		{
			name:    "never checked",
			summary: summary(nil),
			want:    []string{checkForm, ">조건 점검</button>", "진행 중인 스캔이 없음", "판정별 항목"},
			wantNot: []string{refresh, "점검한 뒤 판정이 바뀌었습니다", applyOff},
		},
		{
			name:    "checked before the verdicts changed",
			summary: summary(stale),
			want:    []string{"점검한 뒤 판정이 바뀌었습니다", checkForm, ">다시 점검</button>"},
			wantNot: []string{"적용하면 바뀌는 위치"},
		},
		{
			name:    "nothing to apply",
			summary: nothing,
			want:    []string{"외부화나 삭제·마스킹으로 판정한 항목이 없습니다.", "판정 화면에서 항목을 판정해 주세요.", `type="button" disabled>조건 점검</button>`},
			wantNot: []string{checkForm},
		},
		{
			name:    "scan active",
			summary: busy,
			want:    []string{"이 프로젝트의 스캔이 진행 중입니다.", `type="button" disabled>조건 점검</button>`},
			wantNot: []string{checkForm},
		},
		{
			name:    "checking",
			summary: summary(run(apply.StateChecking)),
			want:    []string{refresh, "점검 중입니다.", "badge-running\">점검 중"},
			wantNot: []string{checkForm, applyOff},
		},
		{
			name:    "blocked",
			summary: summary(blocked),
			items:   readyItems()[:3],
			want: []string{
				"커밋되지 않은 변경 53개", "src/File49.java", "외 3개", "변경을 커밋하거나 stash한 뒤 다시 점검해 주세요.",
				checkForm, ">다시 점검</button>", applyOff, "badge-failed\">조건 미충족", "바꿀 위치 3곳을 점검하지 못함",
			},
			wantNot: []string{refresh, "src/File50.java", "적용하면 바뀌는 위치"},
		},
		{
			name:    "ready",
			summary: summary(run(apply.StateReady)),
			items:   readyItems(),
			want: []string{
				"적용하면 바뀌는 위치 3곳", "값을 비밀 저장소로 옮기고 placeholder로 바꿈</dt><dd>2곳",
				"[REDACTED]로 가림</dt><dd>1곳", `name="run" value="5"`, `name="digest" value="` + digestNow + `"`,
				`<button class="btn btn-primary btn-lg" type="submit">적용</button>`, "재스캔 필요", checkForm,
			},
			wantNot: []string{"복구 불가", `name="irreversible"`, applyOff, "badge-failed"},
		},
		{
			name:    "ready while a scan runs",
			summary: readyBusy,
			items:   readyItems(),
			want:    []string{"지금은 적용할 수 없습니다.", applyOff, "이 프로젝트의 스캔이 진행 중입니다."},
			wantNot: []string{checkForm},
		},
		{
			name:    "apply queued",
			summary: summary(run(apply.StateQueued)),
			want:    []string{`href="/applies/5"`, "적용 #5이 적용 대기 중입니다."},
			wantNot: []string{checkForm, applyOff, refresh},
		},
		{
			name:    "apply running",
			summary: summary(run(apply.StateRunning)),
			want:    []string{`href="/applies/5"`, "적용 #5이 적용 중입니다.", `role="progressbar"`},
			wantNot: []string{checkForm, applyOff},
		},
		{
			name:    "after an apply",
			summary: summary(run(apply.StateSucceeded)),
			want:    []string{checkForm, `<a href="/applies/5">#5</a> 적용 완료`},
			wantNot: []string{"점검한 뒤 판정이 바뀌었습니다"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := applyEnv(t)
			e.applies.put(tt.summary, tt.items...)
			res := e.getApply(t)
			for _, w := range tt.want {
				if !strings.Contains(res.body, w) {
					t.Errorf("GET %s (%s) lacks %q; body:\n%s", applyPagePath, tt.name, w, res.body)
				}
			}
			for _, w := range tt.wantNot {
				if strings.Contains(res.body, w) {
					t.Errorf("GET %s (%s) holds %q", applyPagePath, tt.name, w)
				}
			}
			if tt.name != "ready" && strings.Contains(res.body, digestField) {
				t.Errorf("GET %s (%s) holds the digest form, want it only for a ready check", applyPagePath, tt.name)
			}
		})
	}
}

func TestApplyPage_Blockers(t *testing.T) {
	tests := []struct {
		blocker   apply.Blocker
		label     string
		unchecked bool
	}{
		{apply.BlockerWorkspaceReadOnly, "APPLY_WORKSPACE_ROOT를 설정하고", true},
		{apply.BlockerProjectPath, "작업 공간에서 프로젝트 폴더를 찾을 수 없습니다.", true},
		{apply.BlockerGitMissing, "worker에서 git을 실행할 수 없습니다.", true},
		{apply.BlockerNotRepository, "프로젝트 폴더가 git 작업본이 아니거나", true},
		{apply.BlockerNoCommit, "저장소에 커밋이 없습니다.", true},
		{apply.BlockerGitFailed, "git이 작업본을 읽지 못했습니다.", true},
		{apply.BlockerUnsafeGitConfig, "filter.*.clean, filter.*.process, hook.*, core.worktree를 지운 뒤", true},
		{apply.BlockerDirty, "변경을 커밋하거나 stash한 뒤 다시 점검해 주세요.", false},
		{apply.BlockerScanNotLatest, "마지막으로 성공한 스캔 뒤에 다른 스캔이 있습니다.", false},
		{apply.BlockerManifestUnreadable, "secrets-manifest.yml을 읽을 수 없습니다.", false},
		{apply.BlockerNothingToApply, "판정 화면에서 항목을 판정해 주세요.", false},
		{"made-up-blocker", "알 수 없는 조건입니다. <span class=\"mono\">made-up-blocker</span>", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.blocker), func(t *testing.T) {
			e := applyEnv(t)
			r := run(apply.StateBlocked)
			r.Blockers = []apply.Blocker{tt.blocker}
			e.applies.put(summary(r), readyItems()[:2]...)
			res := e.getApply(t)
			if !strings.Contains(res.body, tt.label) {
				t.Errorf("GET %s blocked by %s lacks %q; body:\n%s", applyPagePath, tt.blocker, tt.label, res.body)
			}
			if got := strings.Contains(res.body, "badge-queued\">점검하지 못함"); got != tt.unchecked {
				t.Errorf("GET %s blocked by %s shows an unchecked condition = %t, want %t", applyPagePath, tt.blocker, got, tt.unchecked)
			}
			if !strings.Contains(res.body, "바꿀 위치 2곳을 점검하지 못함") || strings.Contains(res.body, "적용하면 바뀌는 위치") {
				t.Errorf("GET %s blocked by %s does not show its planned places as unchecked", applyPagePath, tt.blocker)
			}
			if !strings.Contains(res.body, "badge-failed\">실패") || !strings.Contains(res.body, applyOff) || strings.Contains(res.body, digestField) {
				t.Errorf("GET %s blocked by %s lacks the failed condition or the disabled apply button, or offers the digest form", applyPagePath, tt.blocker)
			}
		})
	}
}

// row returns the table row of body that holds text, or "".
func row(body, text string) string {
	for _, part := range strings.Split(body, "<tr>") {
		end := strings.Index(part, "</tr>")
		if end >= 0 && strings.Contains(part[:end], text) {
			return part[:end]
		}
	}
	return ""
}

func TestApplyPage_Counts(t *testing.T) {
	e := applyEnv(t)
	e.applies.put(summary(nil))
	res := e.getApply(t)
	tests := []struct {
		key  string
		want []string
	}{
		{"저장소 접속 정보", []string{"외부화", "3묶음", "7곳", "placeholder로 바꿈"}},
		{"고객정보", []string{"삭제·마스킹", "2묶음", "40곳", "[REDACTED]로 가림"}},
		{"오탐", []string{"모든 분류", "4묶음", "9곳", "바꾸지 않음"}},
		{"보류", []string{"모든 분류", "1묶음", "1곳", "바꾸지 않음"}},
		{"미판정", []string{"모든 분류", "2묶음", "2곳", "바꾸지 않음"}},
	}
	for _, tt := range tests {
		r := row(res.body, tt.key)
		for _, w := range tt.want {
			if !strings.Contains(r, w) {
				t.Errorf("GET %s row of %q = %q, want it to hold %q", applyPagePath, tt.key, r, w)
			}
		}
	}
	if r := row(res.body, "오탐"); strings.Contains(r, "기타 비밀 값") {
		t.Errorf("GET %s row of false positives names a category: %q", applyPagePath, r)
	}
}

func TestApplyPage_Irreversible(t *testing.T) {
	items := append(readyItems(),
		apply.Item{File: "data/members.csv", StartLine: 2, Action: apply.ActionRedact, Kind: finding.KindPhone, Status: apply.StatusPlanned},
		apply.Item{File: "data/members.csv", StartLine: 3, Action: apply.ActionRedact, Kind: finding.KindPhone, Status: apply.StatusPlanned},
		apply.Item{File: "data/members.csv", StartLine: 4, Action: apply.ActionRedact, Kind: finding.KindEmail, Status: apply.StatusPlanned},
		apply.Item{File: "tmp/export.log", StartLine: 1, Action: apply.ActionRedact, Kind: finding.KindRRN, Status: apply.StatusPlanned},
	)
	t.Run("listed files", func(t *testing.T) {
		e := applyEnv(t)
		r := run(apply.StateReady)
		r.IrreversibleFiles = []string{"data/members.csv", "tmp/export.log"}
		e.applies.put(summary(r), items...)
		res := e.getApply(t)
		for _, w := range []string{
			"복구 불가", "여기 보인 파일만 가립니다", "data/members.csv · 3곳", "tmp/export.log · 1곳",
			`<input type="checkbox" name="irreversible" value="on" form="apply-form">`, `id="apply-form"`,
		} {
			if !strings.Contains(res.body, w) {
				t.Errorf("GET %s with files that cannot be restored lacks %q; body:\n%s", applyPagePath, w, res.body)
			}
		}
		if strings.Contains(res.body, "checked") {
			t.Errorf("GET %s checks the consent box for the user", applyPagePath)
		}
		if strings.Contains(res.body, fakePhone) {
			t.Errorf("GET %s shows a customer-data value", applyPagePath)
		}
	})
	t.Run("none", func(t *testing.T) {
		e := applyEnv(t)
		e.applies.put(summary(run(apply.StateReady)), items...)
		res := e.getApply(t)
		if strings.Contains(res.body, "복구 불가") || strings.Contains(res.body, `name="irreversible"`) {
			t.Errorf("GET %s without files that cannot be restored shows the consent section", applyPagePath)
		}
	})
}

func TestApplyPage_SkipReasons(t *testing.T) {
	labels := map[apply.Reason]string{
		apply.ReasonFileChanged:           "재스캔 필요",
		apply.ReasonFileMissing:           "파일이 없음",
		apply.ReasonNotText:               "텍스트로 읽을 수 없는 파일",
		apply.ReasonValueMismatch:         "값이 스캔 때와 다름",
		apply.ReasonNotAllowed:            "이 항목에는 적용할 수 없는 판정",
		apply.ReasonOverlap:               "다른 항목과 범위가 겹침",
		apply.ReasonUntrackedIrreversible: "git이 추적하지 않아 되돌릴 수 없음",
		apply.ReasonNestedRepository:      "프로젝트 안의 다른 저장소에 있음",
		apply.ReasonKeyInSourceFile:       "소스 파일이라 통째로 옮기지 않음",
		apply.ReasonKeyHoldsCustomerData:  "고객정보가 있을 수 있는 파일이라 통째로 옮기지 않음",
		apply.ReasonHoldsCustomerData:     "고객정보가 섞여 있음",
		apply.ReasonFileRemoved:           "파일과 함께 옮겨짐",
		apply.ReasonTooLarge:              "값이 너무 큼",
		apply.ReasonStoreFailed:           "비밀 저장소에 저장하지 못함",
		apply.ReasonWriteFailed:           "파일을 쓰지 못함",
		apply.ReasonInterrupted:           "중단됨",
		"made-up-reason":                  "알 수 없는 사유 <span class=\"mono\">made-up-reason</span>",
	}
	// The reasons are listed with file-changed last, so the page has to
	// move it to the top.
	var items []apply.Item
	for reason := range labels {
		if reason != apply.ReasonFileChanged {
			items = append(items, apply.Item{File: "conf/" + string(reason) + ".yml", StartLine: 1, Kind: finding.KindPassword, Status: apply.StatusSkipped, Reason: reason})
		}
	}
	items = append(items, apply.Item{File: "conf/changed.yml", StartLine: 2, Kind: finding.KindPassword, Status: apply.StatusSkipped, Reason: apply.ReasonFileChanged})

	e := applyEnv(t)
	e.applies.put(summary(run(apply.StateReady)), items...)
	res := e.getApply(t)
	for reason, label := range labels {
		if !strings.Contains(res.body, "<summary>"+label) {
			t.Errorf("GET %s lacks the group %q for reason %s; body:\n%s", applyPagePath, label, reason, res.body)
		}
	}
	first := strings.Index(res.body, "<summary>")
	if first < 0 || !strings.HasPrefix(res.body[first:], "<summary>재스캔 필요") {
		t.Errorf("GET %s first skipped group is not the one that needs a new scan", applyPagePath)
	}
	if !strings.Contains(res.body, "conf/changed.yml:2 · 비밀번호") {
		t.Errorf("GET %s does not list the changed file under its reason", applyPagePath)
	}
}

func TestApplyPage_Escapes(t *testing.T) {
	const evil = "src/<script>alert(1)</script>.java"
	e := applyEnv(t)
	r := run(apply.StateBlocked)
	r.Blockers = []apply.Blocker{apply.BlockerDirty, "<b>bold</b>"}
	r.DirtyFiles, r.DirtyCount = []string{evil}, 1
	e.applies.put(summary(r))
	res := e.getApply(t)
	if strings.Contains(res.body, "<script>alert(1)") || strings.Contains(res.body, "<b>bold</b>") {
		t.Errorf("GET %s holds unescaped markup from a path or a code", applyPagePath)
	}
	if !strings.Contains(res.body, "src/&lt;script&gt;alert(1)&lt;/script&gt;.java") {
		t.Errorf("GET %s lacks the escaped path; body:\n%s", applyPagePath, res.body)
	}

	ready := run(apply.StateReady)
	ready.IrreversibleFiles = []string{evil}
	e.applies.put(summary(ready), apply.Item{File: evil, StartLine: 1, Action: apply.ActionRedact, Kind: finding.KindPhone, Status: apply.StatusPlanned},
		apply.Item{File: evil, StartLine: 2, Kind: finding.KindPassword, Status: apply.StatusSkipped, Reason: apply.ReasonFileChanged})
	res = e.getApply(t)
	if strings.Contains(res.body, "<script>alert(1)") {
		t.Errorf("GET %s of a ready check holds an unescaped path", applyPagePath)
	}
}

func TestApplyCheck_Post(t *testing.T) {
	t.Run("requested", func(t *testing.T) {
		e := applyEnv(t)
		e.applies.put(summary(nil))
		res := e.post(t, applyCheckPath, nil, nil)
		if res.status != http.StatusSeeOther || res.location() != applyPagePath {
			t.Fatalf("POST %s = %d %q, want 303 %q", applyCheckPath, res.status, res.location(), applyPagePath)
		}
		if diff := cmp.Diff([]checkCall{{1, fakeActor}}, e.applies.checkCalls()); diff != "" {
			t.Errorf("POST %s RequestApplyCheck calls (-want +got):\n%s", applyCheckPath, diff)
		}
	})
	for _, tt := range []struct {
		err  error
		want string
	}{
		{db.ErrScanActive, "이 프로젝트의 스캔이 진행 중입니다. 스캔이 끝난 뒤 다시 점검해 주세요."},
		{db.ErrApplyActive, "이 프로젝트의 점검이나 적용이 이미 진행 중입니다."},
	} {
		t.Run(tt.err.Error(), func(t *testing.T) {
			e := applyEnv(t)
			e.applies.put(summary(nil))
			e.applies.requestErr = tt.err
			res := e.post(t, applyCheckPath, nil, nil)
			if res.status != http.StatusConflict || !strings.Contains(res.body, `role="alert">`+tt.want) {
				t.Errorf("POST %s refused with %v = %d, want 409 and %q; body:\n%s", applyCheckPath, tt.err, res.status, tt.want, res.body)
			}
			if !strings.Contains(res.body, "판정별 항목") {
				t.Errorf("POST %s refused with %v does not show the apply page again", applyCheckPath, tt.err)
			}
		})
	}
	t.Run("no project", func(t *testing.T) {
		e := applyEnv(t)
		res := e.post(t, "/projects/2/apply/check", nil, nil)
		if res.status != http.StatusNotFound {
			t.Errorf("POST /projects/2/apply/check status = %d, want 404", res.status)
		}
	})
}

// applyForm is the form the ready page submits.
func applyForm(runID, digest string, irreversible bool) url.Values {
	v := url.Values{"run": {runID}, "digest": {digest}}
	if irreversible {
		v.Set("irreversible", "on")
	}
	return v
}

func TestApply_Post(t *testing.T) {
	setup := func(t *testing.T) *testEnv {
		t.Helper()
		e := applyEnv(t)
		e.applies.put(summary(run(apply.StateReady)), readyItems()...)
		other := run(apply.StateReady)
		other.ID, other.ProjectID = 6, 2
		e.applies.putRun(*other)
		return e
	}
	for _, irreversible := range []bool{true, false} {
		t.Run("confirmed, consent "+strconv.FormatBool(irreversible), func(t *testing.T) {
			e := setup(t)
			res := e.post(t, applyPagePath, applyForm("5", digestNow, irreversible), nil)
			if res.status != http.StatusSeeOther || res.location() != "/applies/5" {
				t.Fatalf("POST %s = %d %q, want 303 /applies/5; body:\n%s", applyPagePath, res.status, res.location(), res.body)
			}
			want := []confirmCall{{Run: 5, Digest: digestNow, AllowIrreversible: irreversible, By: fakeActor}}
			if diff := cmp.Diff(want, e.applies.confirmCalls()); diff != "" {
				t.Errorf("POST %s ConfirmApply calls (-want +got):\n%s", applyPagePath, diff)
			}
		})
	}
	for _, tt := range []struct {
		err    error
		status int
		want   string
	}{
		{db.ErrPlanChanged, http.StatusConflict, "판정이 바뀌었습니다. 다시 점검해 주세요"},
		{db.ErrScanActive, http.StatusConflict, "이 프로젝트의 스캔이 진행 중입니다."},
		{db.ErrApplyActive, http.StatusConflict, "이 프로젝트의 점검이나 적용이 이미 진행 중입니다."},
		{db.ErrNotFound, http.StatusNotFound, "화면을 찾을 수 없습니다."},
	} {
		t.Run(tt.err.Error(), func(t *testing.T) {
			e := setup(t)
			e.applies.confirmErr = tt.err
			res := e.post(t, applyPagePath, applyForm("5", digestNow, false), nil)
			if res.status != tt.status || !strings.Contains(res.body, tt.want) {
				t.Errorf("POST %s refused with %v = %d, want %d and %q; body:\n%s", applyPagePath, tt.err, res.status, tt.status, tt.want, res.body)
			}
			if n := len(e.applies.confirmCalls()); n != 1 {
				t.Errorf("POST %s ConfirmApply calls = %d, want 1", applyPagePath, n)
			}
		})
	}
	for _, tt := range []struct {
		name   string
		form   url.Values
		status int
	}{
		{"run of another project", applyForm("6", digestNow, false), http.StatusNotFound},
		{"unknown run", applyForm("77", digestNow, false), http.StatusNotFound},
		{"run not a number", applyForm("five", digestNow, false), http.StatusBadRequest},
		{"run zero", applyForm("0", digestNow, false), http.StatusBadRequest},
		{"no run", url.Values{"digest": {digestNow}}, http.StatusBadRequest},
		{"no digest", url.Values{"run": {"5"}}, http.StatusBadRequest},
		{"short digest", applyForm("5", digestNow[1:], false), http.StatusBadRequest},
		{"upper-case digest", applyForm("5", strings.ToUpper(digestNow), false), http.StatusBadRequest},
		{"digest not hex", applyForm("5", strings.Repeat("g", 64), false), http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			res := e.post(t, applyPagePath, tt.form, nil)
			if res.status != tt.status {
				t.Errorf("POST %s %v status = %d, want %d", applyPagePath, tt.form, res.status, tt.status)
			}
			if calls := e.applies.confirmCalls(); len(calls) != 0 {
				t.Errorf("POST %s %v ConfirmApply calls = %v, want none", applyPagePath, tt.form, calls)
			}
		})
	}
	t.Run("form too large", func(t *testing.T) {
		e := setup(t)
		form := applyForm("5", digestNow, false)
		form.Set("pad", strings.Repeat("x", 64<<10))
		res := e.post(t, applyPagePath, form, nil)
		if res.status != http.StatusBadRequest || len(e.applies.confirmCalls()) != 0 {
			t.Errorf("POST %s with a 64KiB form = %d, %d calls, want 400 and none", applyPagePath, res.status, len(e.applies.confirmCalls()))
		}
	})
}

func TestApply_Roles(t *testing.T) {
	for _, tt := range []struct {
		name   string
		signIn func(*testEnv, *testing.T) cookie
	}{
		{"user", (*testEnv).user},
		{"admin", (*testEnv).admin},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := applyEnv(t)
			e.applies.put(summary(run(apply.StateReady)), readyItems()...)
			c := tt.signIn(e, t)
			if res := e.do(t, http.MethodGet, applyPagePath, nil, c); res.status != http.StatusOK {
				t.Errorf("GET %s as %s status = %d, want 200", applyPagePath, tt.name, res.status)
			}
			if res := e.postAs(t, c, applyCheckPath, nil, nil); res.status != http.StatusSeeOther {
				t.Errorf("POST %s as %s status = %d, want 303", applyCheckPath, tt.name, res.status)
			}
			if res := e.postAs(t, c, applyPagePath, applyForm("5", digestNow, false), nil); res.status != http.StatusSeeOther {
				t.Errorf("POST %s as %s status = %d, want 303", applyPagePath, tt.name, res.status)
			}
		})
	}
	t.Run("no session", func(t *testing.T) {
		e := applyEnv(t)
		e.applies.put(summary(run(apply.StateReady)))
		res := e.do(t, http.MethodGet, applyPagePath, nil)
		if want := "/login?return_to=" + url.QueryEscape(applyPagePath); res.status != http.StatusSeeOther || res.location() != want {
			t.Errorf("GET %s without a session = %d %q, want 303 %q", applyPagePath, res.status, res.location(), want)
		}
		for _, target := range []string{applyCheckPath, applyPagePath} {
			res := e.do(t, http.MethodPost, target, sameOrigin())
			if res.status != http.StatusUnauthorized {
				t.Errorf("POST %s without a session status = %d, want 401", target, res.status)
			}
		}
		if len(e.applies.checkCalls()) != 0 || len(e.applies.confirmCalls()) != 0 {
			t.Errorf("POST without a session reached the store")
		}
	})
}

func TestApply_WritesNeedPost(t *testing.T) {
	e := applyEnv(t)
	e.applies.put(summary(run(apply.StateReady)), readyItems()...)
	c := e.user(t)
	for _, tt := range []struct {
		method, target, allow string
	}{
		{http.MethodGet, applyCheckPath, "POST"},
		{http.MethodPut, applyCheckPath, "POST"},
		{http.MethodDelete, applyPagePath, "GET, POST"},
		{http.MethodPut, applyPagePath, "GET, POST"},
	} {
		res := e.do(t, tt.method, tt.target, sameOrigin(), c)
		if res.status != http.StatusMethodNotAllowed || res.header.Get("Allow") != tt.allow {
			t.Errorf("%s %s = %d, Allow %q, want 405 and %q", tt.method, tt.target, res.status, res.header.Get("Allow"), tt.allow)
		}
	}
	cross := http.Header{"Sec-Fetch-Site": {"cross-site"}}
	for _, tt := range []struct {
		target string
		form   url.Values
	}{
		{applyCheckPath, nil},
		{applyPagePath, applyForm("5", digestNow, true)},
	} {
		if res := e.postAs(t, c, tt.target, tt.form, cross); res.status != http.StatusForbidden {
			t.Errorf("cross-site POST %s status = %d, want 403", tt.target, res.status)
		}
	}
	if len(e.applies.checkCalls()) != 0 || len(e.applies.confirmCalls()) != 0 {
		t.Errorf("405 and cross-site requests reached the store: checks %v, confirms %v", e.applies.checkCalls(), e.applies.confirmCalls())
	}
}

func TestApplyPage_UnknownProject(t *testing.T) {
	e := applyEnv(t)
	if res := e.do(t, http.MethodGet, "/projects/2/apply", nil, e.user(t)); res.status != http.StatusNotFound {
		t.Errorf("GET /projects/2/apply status = %d, want 404", res.status)
	}
	if res := e.do(t, http.MethodGet, "/projects/x/apply", nil, e.user(t)); res.status != http.StatusNotFound {
		t.Errorf("GET /projects/x/apply status = %d, want 404", res.status)
	}
}

const runPagePath = "/applies/5"

// finishedRun is run 5 of project 1 after an apply that ended in state,
// with confirmation scan rescan (0 for none).
func finishedRun(state apply.State, rescan scan.ID) apply.Run {
	r := *run(state)
	r.ConfirmedAt = time.Date(2026, 11, 20, 10, 5, 0, 0, time.UTC)
	r.StartedAt = time.Date(2026, 11, 20, 10, 6, 0, 0, time.UTC)
	r.FinishedAt = time.Date(2026, 11, 20, 10, 7, 30, 0, time.UTC)
	r.RescanID = rescan
	return r
}

const storePath = "secret/data/projects/1/db.main.password"

// runItems are the outcomes of an apply: the password replaced in a YAML
// file and in a Java file, the phone number redacted, the password skipped
// in a file changed since the scan, and a token whose value was not stored.
func runItems() []apply.Item {
	return []apply.Item{
		{
			BundleID: 101, FindingID: 1001, File: "conf/a.yml", StartLine: 3, Line: 3, Action: apply.ActionReplace, Category: finding.CategoryStorage,
			Kind: finding.KindPassword, Name: "ext.db.main.password", StorePath: storePath, StoreVersion: 1, Tracked: true, Status: apply.StatusApplied,
		},
		{
			BundleID: 101, FindingID: 1002, File: "src/main/java/shop/Db.java", StartLine: 12, Line: 14, Action: apply.ActionReplace, Category: finding.CategoryStorage,
			Kind: finding.KindPassword, Name: "ext.db.main.password", StorePath: storePath, StoreVersion: 1, Tracked: true, CodeChange: true, Status: apply.StatusApplied,
		},
		{
			BundleID: 103, FindingID: 1004, File: "logs/x.log", StartLine: 7, Line: 7, Action: apply.ActionRedact, Category: finding.CategoryCustomerData,
			Kind: finding.KindPhone, Tracked: true, Status: apply.StatusApplied,
		},
		{
			BundleID: 101, FindingID: 1005, File: "conf/b.xml", StartLine: 9, Action: apply.ActionReplace, Category: finding.CategoryStorage,
			Kind: finding.KindPassword, Name: "ext.db.main.password", Status: apply.StatusSkipped, Reason: apply.ReasonFileChanged,
		},
		{
			BundleID: 102, FindingID: 1006, File: "conf/c.properties", StartLine: 2, Action: apply.ActionReplace, Category: finding.CategoryPartnerSession,
			Kind: finding.KindToken, Name: "ext.partner.token", Status: apply.StatusFailed, Reason: apply.ReasonStoreFailed,
		},
	}
}

// putRun stores r with items and, when it names one, its confirmation scan
// sc with the number of reappeared findings.
func (e *testEnv) putRun(r apply.Run, items []apply.Item, sc *scan.Scan, reappeared int) {
	e.applies.putRun(r)
	e.applies.putItems(r.ID, items)
	if sc != nil {
		e.scans.put(*sc)
		e.applies.putReappeared(sc.ID, reappeared)
	}
}

// getRun shows the page of run 5 to a signed-in user.
func (e *testEnv) getRun(t *testing.T) result {
	t.Helper()
	res := e.do(t, http.MethodGet, runPagePath, nil, e.user(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body:\n%s", runPagePath, res.status, res.body)
	}
	return res
}

func confirmationScan(status scan.Status) *scan.Scan {
	return &scan.Scan{ID: 8, ProjectID: 1, Status: status, ApplyID: 5, RequestedAt: time.Date(2026, 11, 20, 10, 7, 31, 0, time.UTC)}
}

func TestApplyRunPage(t *testing.T) {
	succeededWithFailure := finishedRun(apply.StateSucceeded, 8)
	failed := finishedRun(apply.StateFailed, 8)
	failed.FailureReason = apply.FailureSecretStore
	gaveUp := finishedRun(apply.StateFailed, 0)
	gaveUp.FailureReason = "worker stopped during the apply three times"
	unreached := runItems()
	unreached = append(unreached, apply.Item{
		BundleID: 104, FindingID: 1007, File: "deploy/run.sh", StartLine: 4, Action: apply.ActionReplace,
		Category: finding.CategoryStorage, Kind: finding.KindPassword, Name: "EXT_DB_MAIN_PASSWORD", Status: apply.StatusPlanned,
	})
	partial := confirmationScan(scan.StatusSucceeded)
	partial.Skipped = 2
	notApplied := []apply.Item{runItems()[3]}

	tests := []struct {
		name       string
		run        apply.Run
		items      []apply.Item
		scan       *scan.Scan
		reappeared int
		want       []string
		wantNot    []string
	}{
		{
			name: "succeeded", run: succeededWithFailure, items: runItems(), scan: confirmationScan(scan.StatusSucceeded),
			want: []string{
				`<span class="badge badge-lg badge-succeeded">적용 완료</span>`, "Fake User", "2026-11-20 10:00:00 UTC", "2026-11-20 10:07:30 UTC",
				`<span class="tile-label">적용</span><span class="tile-figure">3</span>`,
				`<span class="tile-label">건너뜀</span><span class="tile-figure">1</span>`,
				`<span class="tile-label">실패</span><span class="tile-figure">1</span>`,
				`<a href="/scans/8">확인 스캔 #8</a>`, "적용한 값이 모두 사라졌습니다",
				"ext.db.main.password", storePath, "conf/a.yml", "src/main/java/shop/Db.java", ">14<", "코드 수정 필요",
				"[REDACTED]로 가림", "logs/x.log",
				"재스캔 필요", "conf/b.xml:9", "비밀 저장소에 저장하지 못함", "conf/c.properties:2",
				"실패한 위치 1곳이 있습니다", `href="/projects/1/apply"`,
			},
			wantNot: []string{refresh, "처리하지 않음", "git diff", fakePhone, "Sh0p"},
		},
		{
			name: "failed after changing files", run: failed, items: runItems(), scan: confirmationScan(scan.StatusQueued),
			want: []string{
				`<span class="badge badge-lg badge-failed">적용 실패</span>`, "비밀 저장소에 연결할 수 없었습니다",
				"이미 바꾼 파일 3개는 그대로입니다. <code>git diff</code>로 확인하세요. 다음 점검이 기록을 맞춥니다.",
				`<a href="/scans/8">확인 스캔 #8</a>`, "확인 스캔이 진행 중입니다",
			},
			wantNot: []string{"적용한 값이 모두 사라졌습니다", "스캔을 시작해 주세요"},
		},
		{
			name: "given up by the queue", run: gaveUp, items: runItems(),
			want: []string{
				"worker가 세 번 멈춰 적용을 포기했습니다", "이미 바꾼 파일 3개는 그대로입니다",
				"확인 스캔이 요청되지 않았습니다", `<a class="btn btn-primary" href="/projects/1">프로젝트에서 스캔 시작</a>`,
			},
		},
		{
			name: "failed before changing anything", run: gaveUp, items: notApplied,
			want:    []string{"소스는 바뀌지 않았습니다"},
			wantNot: []string{"이미 바꾼 파일", "확인 스캔이 요청되지 않았습니다"},
		},
		{
			name: "places not reached", run: failed, items: unreached, scan: confirmationScan(scan.StatusQueued),
			want: []string{"처리하지 않음 · 1곳", "deploy/run.sh:4"},
		},
		{
			name: "values found again", run: succeededWithFailure, items: runItems(), scan: confirmationScan(scan.StatusSucceeded), reappeared: 2,
			want:    []string{"적용한 묶음의 값이 2개 항목에서 다시 발견되었습니다", `href="/projects/1/triage?status=applied"`},
			wantNot: []string{"적용한 값이 모두 사라졌습니다"},
		},
		{
			name: "confirmation scan skipped files", run: succeededWithFailure, items: runItems(), scan: partial,
			want: []string{
				"이 스캔이 검사한 범위에서는 적용한 값이 다시 발견되지 않았습니다. 건너뛴 파일이나 버린 결과가 있습니다.",
				`href="/scans/8#unchecked"`,
			},
			wantNot: []string{"적용한 값이 모두 사라졌습니다"},
		},
		{
			name: "confirmation scan failed", run: succeededWithFailure, items: runItems(), scan: confirmationScan(scan.StatusFailed),
			want: []string{"확인 스캔이 실패했습니다"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := applyEnv(t)
			e.putRun(tt.run, tt.items, tt.scan, tt.reappeared)
			res := e.getRun(t)
			wantIn(t, "GET "+runPagePath, res.body, tt.want...)
			wantNotIn(t, "GET "+runPagePath, res.body, tt.wantNot...)
		})
	}
}

func TestApplyRunPage_Refresh(t *testing.T) {
	for _, tt := range []struct {
		state    apply.State
		status   int
		location string
		refresh  bool
	}{
		{apply.StateQueued, http.StatusOK, "", true},
		{apply.StateRunning, http.StatusOK, "", true},
		{apply.StateSucceeded, http.StatusOK, "", false},
		{apply.StateFailed, http.StatusOK, "", false},
		{apply.StateChecking, http.StatusSeeOther, applyPagePath, false},
		{apply.StateReady, http.StatusSeeOther, applyPagePath, false},
		{apply.StateBlocked, http.StatusSeeOther, applyPagePath, false},
		{apply.StateSuperseded, http.StatusSeeOther, applyPagePath, false},
	} {
		t.Run(string(tt.state), func(t *testing.T) {
			e := applyEnv(t)
			e.putRun(*run(tt.state), nil, nil, 0)
			res := e.do(t, http.MethodGet, runPagePath, nil, e.user(t))
			if res.status != tt.status || res.location() != tt.location {
				t.Fatalf("GET %s of a %s run = %d to %q, want %d to %q", runPagePath, tt.state, res.status, res.location(), tt.status, tt.location)
			}
			if got := strings.Contains(res.body, refresh); got != tt.refresh {
				t.Errorf("GET %s of a %s run has the refresh tag = %v, want %v", runPagePath, tt.state, got, tt.refresh)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		e := applyEnv(t)
		e.putRun(*run(apply.StateSucceeded), nil, nil, 0)
		other := *run(apply.StateSucceeded)
		other.ID, other.ProjectID = 6, 9
		e.putRun(other, nil, nil, 0)
		for _, target := range []string{"/applies/99", "/applies/abc", "/applies/0", "/applies/6"} {
			if res := e.do(t, http.MethodGet, target, nil, e.user(t)); res.status != http.StatusNotFound {
				t.Errorf("GET %s status = %d, want 404", target, res.status)
			}
		}
	})
	t.Run("signed out", func(t *testing.T) {
		e := applyEnv(t)
		e.putRun(*run(apply.StateSucceeded), nil, nil, 0)
		if res := e.do(t, http.MethodGet, runPagePath, nil); res.status != http.StatusSeeOther || !strings.HasPrefix(res.location(), "/login") {
			t.Errorf("GET %s signed out = %d to %q, want 303 to the login", runPagePath, res.status, res.location())
		}
	})
}

func TestProjectPage_Apply(t *testing.T) {
	const scanButton = `<button class="btn btn-secondary btn-lg" type="submit">스캔 시작</button>`
	done := finishedRun(apply.StateSucceeded, 8)
	done.Applied, done.Skipped, done.Failed = 3, 1, 1
	check := *run(apply.StateReady)
	check.ID = 6

	t.Run("after an apply", func(t *testing.T) {
		e := applyEnv(t)
		e.putRun(done, nil, nil, 0)
		e.putRun(check, nil, nil, 0)
		res := e.do(t, http.MethodGet, "/projects/1", nil, e.user(t))
		if res.status != http.StatusOK {
			t.Fatalf("GET /projects/1 status = %d, want 200", res.status)
		}
		wantIn(t, "GET /projects/1", res.body,
			`<a class="btn btn-secondary btn-lg" href="/projects/1/apply">적용</a>`, scanButton,
			"적용 기록", `<a class="row-link" href="/applies/5">#5</a>`, "2026-11-20 10:05:00 UTC", "Fake User",
			`<span class="badge badge-succeeded">적용 완료</span>`, "적용 3 · 건너뜀 1 · 실패 1",
		)
		wantNotIn(t, "GET /projects/1", res.body, `href="/applies/6"`, "스캔을 시작할 수 없습니다")
	})
	t.Run("while applying", func(t *testing.T) {
		e := applyEnv(t)
		running := *run(apply.StateRunning)
		running.ConfirmedAt = time.Date(2026, 11, 20, 10, 5, 0, 0, time.UTC)
		e.putRun(running, nil, nil, 0)
		res := e.do(t, http.MethodGet, "/projects/1", nil, e.user(t))
		wantIn(t, "GET /projects/1", res.body,
			`<button class="btn btn-secondary btn-lg" type="button" disabled>스캔 시작</button>`,
			`<a href="/applies/5">적용 #5</a>이 적용 중이라 스캔을 시작할 수 없습니다`,
			`<span class="badge badge-running">적용 중</span>`, "끝나면 보입니다",
		)
		wantNotIn(t, "GET /projects/1", res.body, scanButton)
	})
	t.Run("while checking", func(t *testing.T) {
		e := applyEnv(t)
		e.putRun(*run(apply.StateChecking), nil, nil, 0)
		res := e.do(t, http.MethodGet, "/projects/1", nil, e.user(t))
		wantIn(t, "GET /projects/1", res.body, `<a href="/projects/1/apply">조건 점검</a>이 진행 중이라 스캔을 시작할 수 없습니다`)
		wantNotIn(t, "GET /projects/1", res.body, scanButton, "적용 기록")
	})
}
