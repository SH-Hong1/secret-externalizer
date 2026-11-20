package web_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// adminActor is the actor of the user that e.admin signs in.
var adminActor = audit.Actor{Subject: "fake-admin", Name: "Fake Admin"}

// fakeOriginal is the original of the password bundle. It holds markup, so
// a test can see that it is escaped.
const fakeOriginal = "dev-only-fake-<script>alert(1)</script>-pw"

const originalPath = "/projects/1/bundles/101/original"

// admin signs in a user with the admin role and returns its cookie.
func (e *testEnv) admin(t *testing.T) cookie {
	t.Helper()
	return e.signIn(t, auth.User{Subject: adminActor.Subject, Name: adminActor.Name, Roles: []auth.Role{auth.RoleAdmin}})
}

// originalEnv is triageEnv with the original of the password bundle stored.
func originalEnv(t *testing.T) *testEnv {
	t.Helper()
	e := triageEnv(t)
	e.originals.put(1, fpPassword, bao.Value(fakeOriginal))
	return e
}

// reveal asks for the original of target as an admin, from the triage screen
// of every status.
func (e *testEnv) reveal(t *testing.T, target string, header http.Header) result {
	t.Helper()
	return e.postAs(t, e.admin(t), target, url.Values{"return": {"status=all"}}, header)
}

// slot returns the element with id "original" in body, with the elements
// of the same tag it holds, or "" when there is none.
func slot(body string) string {
	start := strings.Index(body, `<div class="original" id="original">`)
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(body); i++ {
		switch {
		case strings.HasPrefix(body[i:], "<div"):
			depth++
		case strings.HasPrefix(body[i:], "</div>"):
			depth--
			if depth == 0 {
				return body[start : i+len("</div>")]
			}
		}
	}
	return ""
}

// htmxOriginal is the header htmx sends for the reveal form.
var htmxOriginal = http.Header{"Hx-Request": {"true"}, "Hx-Target": {"original"}}

func TestOriginalButton(t *testing.T) {
	e := originalEnv(t)
	res := e.do(t, http.MethodGet, "/projects/1/triage", nil, e.admin(t))
	detail := section(res.body, "section", "detail")
	wantIn(t, "admin password detail", detail,
		`id="original"`, `method="post" action="/projects/1/bundles/101/original"`, `hx-post="/projects/1/bundles/101/original"`,
		`hx-target="#original"`, "원본 보기")
	wantNotIn(t, "admin password detail", res.body, fakeOriginal, "dev-only-fake")

	res = e.do(t, http.MethodGet, "/projects/1/triage", nil, e.user(t))
	wantNotIn(t, "user password detail", res.body, "/original", "원본 보기")

	res = e.do(t, http.MethodGet, "/projects/1/triage?sel=b103", nil, e.admin(t))
	wantNotIn(t, "admin customer data detail", section(res.body, "section", "detail"), "/original")

	b, err := e.triage.Bundle(t.Context(), 1, 12, passwordBundle)
	if err != nil {
		t.Fatalf("Bundle() error = %v", err)
	}
	for i := range b.Members {
		b.Members[i].HasOriginal = false
	}
	e.triage.putBundle(b)
	res = e.do(t, http.MethodGet, "/projects/1/triage", nil, e.admin(t))
	wantNotIn(t, "admin detail of a bundle with no original", section(res.body, "section", "detail"), "/original")
}

func TestOriginalAdminOnly(t *testing.T) {
	e := originalEnv(t)
	res := e.post(t, originalPath, url.Values{"return": {"status=all"}}, nil)
	if res.status != http.StatusForbidden {
		t.Errorf("POST %s as user status = %d, want 403", originalPath, res.status)
	}
	if calls := e.calls.all(); len(calls) != 0 {
		t.Errorf("POST %s as user made calls %v, want none", originalPath, calls)
	}
	wantNotIn(t, "user reveal page", res.body, "dev-only-fake")
}

func TestOriginalOrder(t *testing.T) {
	e := originalEnv(t)
	res := e.reveal(t, originalPath, nil)
	if res.status != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", originalPath, res.status)
	}
	if diff := cmp.Diff([]string{"RecordReveal", "ReadPending"}, e.calls.all()); diff != "" {
		t.Errorf("POST %s calls (-want +got):\n%s", originalPath, diff)
	}
	if diff := cmp.Diff([]revealCall{{Project: 1, Bundle: passwordBundle, By: adminActor}}, e.audits.revealCalls()); diff != "" {
		t.Errorf("POST %s RecordReveal calls (-want +got):\n%s", originalPath, diff)
	}
	if diff := cmp.Diff([]string{"1/" + fpPassword}, e.originals.readCalls()); diff != "" {
		t.Errorf("POST %s ReadPending calls (-want +got):\n%s", originalPath, diff)
	}

	t.Run("audit fails", func(t *testing.T) {
		e := originalEnv(t)
		e.audits.revealErr = errors.New("fake database down")
		res := e.reveal(t, originalPath, nil)
		if res.status != http.StatusInternalServerError {
			t.Errorf("POST %s with the audit failing status = %d, want 500", originalPath, res.status)
		}
		if diff := cmp.Diff([]string{"RecordReveal"}, e.calls.all()); diff != "" {
			t.Errorf("POST %s with the audit failing calls (-want +got):\n%s", originalPath, diff)
		}
		wantNotIn(t, "reveal page with the audit failing", res.body, "dev-only-fake")
	})
}

func TestOriginalShown(t *testing.T) {
	escaped := "dev-only-fake-&lt;script&gt;alert(1)&lt;/script&gt;-pw"
	for _, tt := range []struct {
		name     string
		header   http.Header
		wantPage bool
	}{
		{"full page", nil, true},
		{"htmx", htmxOriginal, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := originalEnv(t)
			res := e.reveal(t, originalPath, tt.header)
			if res.status != http.StatusOK {
				t.Fatalf("POST %s status = %d, want 200; body:\n%s", originalPath, res.status, res.body)
			}
			if got := res.header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("POST %s Cache-Control = %q, want no-store", originalPath, got)
			}
			box := slot(res.body)
			wantIn(t, "reveal box", box, escaped, "원본 값", adminActor.Name, "UTC", "닫기",
				`href="/projects/1/triage?sel=b101&amp;status=all"`)
			wantNotIn(t, "reveal response", res.body, "<script>alert(1)", `action="/projects/1/bundles/101/original"`)
			if got := strings.Count(res.body, escaped); got != 1 {
				t.Errorf("POST %s shows the value %d times, want once", originalPath, got)
			}
			if strings.Contains(res.body, "<html") != tt.wantPage {
				t.Errorf("POST %s with header %v is a full page: %v, want %v", originalPath, tt.header, !tt.wantPage, tt.wantPage)
			}
			if tt.wantPage {
				wantIn(t, "full reveal page", res.body, `id="list"`, `id="detail"`, "Sh0p****")
			}
			if loc := res.location(); loc != "" {
				t.Errorf("POST %s Location = %q, want none", originalPath, loc)
			}
			for _, s := range []string{"dev-only-fake", "alert(1)"} {
				if strings.Contains(e.log.String(), s) {
					t.Errorf("log contains %q; log = %q", s, e.log.String())
				}
			}
		})
	}
}

func TestOriginalUnavailable(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		want   string
		errors int
	}{
		{"no original", nil, "보관된 원본이 없습니다. 다시 스캔하면 보관됩니다", 0},
		{"store unreachable", fmt.Errorf("reading a pending value: %w", bao.ErrUnreachable), "비밀 저장소에 연결할 수 없습니다", 1},
		{"store timed out", fmt.Errorf("reading a pending value: %w", context.DeadlineExceeded), "비밀 저장소에 연결할 수 없습니다", 1},
		{"read refused", errors.New("reading a pending value: openbao answered status 403"), "비밀 저장소에서 원본을 읽지 못했습니다", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := triageEnv(t)
			e.originals.err = tt.err
			res := e.reveal(t, originalPath, htmxOriginal)
			if res.status != http.StatusOK {
				t.Fatalf("POST %s status = %d, want 200", originalPath, res.status)
			}
			wantIn(t, "reveal notice", slot(res.body), tt.want)
			wantNotIn(t, "reveal notice", res.body, "dev-only-fake", "원본 값", "openbao", "deadline")
			if n := len(e.audits.revealCalls()); n != 1 {
				t.Errorf("POST %s recorded %d reveals, want 1", originalPath, n)
			}
			if n := countErrors(t, e.log.String()); n != tt.errors {
				t.Errorf("POST %s logged %d errors, want %d; log = %q", originalPath, n, tt.errors, e.log.String())
			}
		})
	}
}

func TestOriginalNotText(t *testing.T) {
	for _, v := range []string{"dev-only-fake\x00pw", "dev-only-fake\xff\xfe"} {
		e := triageEnv(t)
		e.originals.put(1, fpPassword, bao.Value(v))
		res := e.reveal(t, originalPath, htmxOriginal)
		if res.status != http.StatusOK {
			t.Fatalf("POST %s for %q status = %d, want 200", originalPath, v, res.status)
		}
		want := "텍스트가 아닌 원본입니다. " + strconv.Itoa(len(v)) + "바이트."
		wantIn(t, fmt.Sprintf("reveal of %q", v), slot(res.body), want)
		wantNotIn(t, fmt.Sprintf("reveal of %q", v), res.body, "dev-only-fake")
	}

	e := triageEnv(t)
	e.originals.put(1, fpPassword, bao.Value(strings.Repeat("\x00", 2348)))
	res := e.reveal(t, originalPath, htmxOriginal)
	wantIn(t, "reveal of 2348 NUL bytes", res.body, "텍스트가 아닌 원본입니다. 2,348바이트.")
}

func TestOriginalRefused(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		setUp  func(e *testEnv)
	}{
		{"customer data", "/projects/1/bundles/103/original", func(*testEnv) {}},
		{"not in the latest scan", "/projects/1/bundles/999/original", func(*testEnv) {}},
		{"no member has an original", "/projects/1/bundles/105/original", func(e *testEnv) {
			e.triage.putBundle(triage.Bundle{
				ID: 105, ProjectID: 1, Fingerprint: fpToken, Category: finding.CategoryStorage, Kind: finding.KindPassword,
				Members: []triage.Member{{FindingID: 1005, File: "conf/x.yml"}},
			})
		}},
		{"other project", "/projects/2/bundles/101/original", func(*testEnv) {}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := originalEnv(t)
			tt.setUp(e)
			res := e.reveal(t, tt.target, nil)
			if res.status != http.StatusNotFound {
				t.Errorf("POST %s status = %d, want 404", tt.target, res.status)
			}
			if calls := e.calls.all(); len(calls) != 0 {
				t.Errorf("POST %s made calls %v, want none", tt.target, calls)
			}
		})
	}
}

func TestOriginalNeedsPost(t *testing.T) {
	e := originalEnv(t)
	if res := e.do(t, http.MethodGet, originalPath, nil, e.admin(t)); res.status != http.StatusMethodNotAllowed {
		t.Errorf("GET %s status = %d, want 405", originalPath, res.status)
	}
	res := e.postAs(t, e.admin(t), originalPath, nil, http.Header{"Sec-Fetch-Site": {"cross-site"}})
	if res.status != http.StatusForbidden {
		t.Errorf("cross-site POST %s status = %d, want 403", originalPath, res.status)
	}
	if calls := e.calls.all(); len(calls) != 0 {
		t.Errorf("GET and cross-site POST %s made calls %v, want none", originalPath, calls)
	}
}

// auditEvents returns n events of project 1, newest first, with IDs from
// 10*n down to 10.
func auditEvents(n int) []audit.Event {
	at := time.Date(2026, 11, 19, 6, 41, 7, 0, time.UTC)
	out := make([]audit.Event, 0, n)
	for i := range n {
		out = append(out, audit.Event{
			ID: int64(10 * (n - i)), At: at.Add(-time.Duration(i) * time.Minute), Actor: fakeActor,
			Action: audit.ActionVerdict, ProjectID: 1, ProjectName: "shop", BundleID: 7,
			Detail: map[string]string{audit.DetailVerdict: "false-positive"},
		})
	}
	return out
}

func TestAuditPage(t *testing.T) {
	e := newTestEnv(t)
	e.audits.events = []audit.Event{
		{ID: 40, At: time.Date(2026, 11, 19, 6, 41, 7, 0, time.UTC), Actor: adminActor, Action: audit.ActionReveal, ProjectID: 1, ProjectName: "shop", BundleID: 311, Kind: finding.KindPassword, Detail: map[string]string{}},
		{
			ID: 30, At: time.Date(2026, 11, 19, 6, 38, 52, 0, time.UTC), Actor: fakeActor, Action: audit.ActionVerdict, ProjectID: 1, ProjectName: "shop", BundleID: 311,
			Kind: finding.KindPassword, Detail: map[string]string{audit.DetailVerdict: "externalize", audit.DetailName: "db.main.password"},
		},
		{
			ID: 20, At: time.Date(2026, 11, 19, 6, 35, 40, 0, time.UTC), Actor: fakeActor, Action: audit.ActionMove, ProjectID: 2, ProjectName: "<b>other</b>", BundleID: 311, FindingID: 1002,
			Detail: map[string]string{audit.DetailTo: "340"},
		},
		{
			ID: 10, At: time.Date(2026, 11, 19, 6, 33, 2, 0, time.UTC), Actor: fakeActor, Action: audit.ActionVerdict, ProjectID: 1, ProjectName: "shop", BundleID: 298,
			Detail: map[string]string{audit.DetailVerdict: "hold", audit.DetailReason: "structure-change", audit.DetailNote: "fake <note>"},
		},
		{ID: 5, At: time.Date(2026, 11, 19, 6, 30, 0, 0, time.UTC), Actor: fakeActor, Action: audit.ActionVerdict, ProjectID: 1, ProjectName: "shop", BundleID: 298, Detail: map[string]string{audit.DetailVerdict: ""}},
		{
			ID: 3, At: time.Date(2026, 11, 19, 6, 29, 0, 0, time.UTC), Actor: fakeActor, Action: audit.ActionVerdict, ProjectID: 1, ProjectName: "shop", BundleID: 17,
			Kind: finding.KindPhone, File: "samples/<customers>.csv", Detail: map[string]string{audit.DetailVerdict: "redact"},
		},
	}
	res := e.do(t, http.MethodGet, "/audit", nil, e.admin(t))
	if res.status != http.StatusOK {
		t.Fatalf("GET /audit as admin status = %d, want 200", res.status)
	}
	rows := strings.Split(section(res.body, "tbody", "audit-rows"), "<tr")
	if len(rows) != 7 {
		t.Fatalf("GET /audit shows %d rows, want 6; body:\n%s", len(rows)-1, res.body)
	}
	wantIn(t, "reveal row", rows[1], "2026-11-19 06:41:07", "Fake Admin", `badge badge-dark`, "원본 보기", `href="/projects/1"`, "shop", "묶음 #311")
	wantIn(t, "externalize row", rows[2], "06:38:52", "Fake User", "badge-externalize", "판정 · 외부화", "묶음 #311 · 비밀번호 · 이름", "ext.db.main.password")
	wantIn(t, "move row", rows[3], "묶음 옮기기", `href="/projects/2"`, "&lt;b&gt;other&lt;/b&gt;", "항목 #1002", "묶음 #311 → #340")
	wantIn(t, "hold row", rows[4], "badge-hold", "판정 · 보류", "묶음 #298", "구조 변경 필요", `메모 "fake &lt;note&gt;"`)
	wantIn(t, "cancel row", rows[5], "판정 취소", "묶음 #298")
	wantIn(t, "customer data row", rows[6], "판정 · 삭제·마스킹", `묶음 #17 · 전화번호 · <span class="mono">samples/&lt;customers&gt;.csv</span>`)
	wantNotIn(t, "reveal row", rows[1], " · 비밀번호", " · 전화번호")
	wantIn(t, "audit page", res.body, `class="mono`)
	wantNotIn(t, "audit page", res.body, "더 보기", "아직 기록이 없습니다")
	if diff := cmp.Diff([]int64{0}, e.audits.befores); diff != "" {
		t.Errorf("GET /audit AuditEvents before (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int{101}, e.audits.limits); diff != "" {
		t.Errorf("GET /audit AuditEvents limit (-want +got):\n%s", diff)
	}
}

func TestAuditPages(t *testing.T) {
	e := newTestEnv(t)
	e.audits.events = auditEvents(150)
	res := e.do(t, http.MethodGet, "/audit", nil, e.admin(t))
	if n := strings.Count(section(res.body, "tbody", "audit-rows"), "<tr"); n != 100 {
		t.Errorf("GET /audit shows %d rows, want 100", n)
	}
	// The 100th event of 150 has ID 10*(150-99).
	wantIn(t, "first audit page", res.body, `href="/audit?before=510"`, "더 보기")

	res = e.do(t, http.MethodGet, "/audit?before=510", nil, e.admin(t))
	if n := strings.Count(section(res.body, "tbody", "audit-rows"), "<tr"); n != 50 {
		t.Errorf("GET /audit?before=510 shows %d rows, want 50", n)
	}
	wantNotIn(t, "last audit page", res.body, "더 보기")

	e = newTestEnv(t)
	e.audits.events = auditEvents(100)
	res = e.do(t, http.MethodGet, "/audit", nil, e.admin(t))
	wantNotIn(t, "audit page of exactly 100 events", res.body, "더 보기")

	for _, bad := range []string{"x", "0", "-1", ""} {
		res := e.do(t, http.MethodGet, "/audit?before="+bad, nil, e.admin(t))
		if res.status != http.StatusBadRequest {
			t.Errorf("GET /audit?before=%s status = %d, want 400", bad, res.status)
		}
	}

	e = newTestEnv(t)
	res = e.do(t, http.MethodGet, "/audit", nil, e.admin(t))
	wantIn(t, "empty audit page", res.body, "아직 기록이 없습니다")

	e.audits.eventsErr = errors.New("fake database down")
	if res := e.do(t, http.MethodGet, "/audit", nil, e.admin(t)); res.status != http.StatusInternalServerError {
		t.Errorf("GET /audit with the store down status = %d, want 500", res.status)
	}
}

func TestAuditPage_Apply(t *testing.T) {
	e := newTestEnv(t)
	e.audits.events = []audit.Event{
		{
			ID: 50, At: time.Date(2026, 11, 20, 10, 5, 0, 0, time.UTC), Actor: fakeActor, Action: audit.ActionApply, ProjectID: 1, ProjectName: "shop",
			Detail: map[string]string{audit.DetailApply: "5", audit.DetailIrreversible: "yes"},
		},
		{
			ID: 49, At: time.Date(2026, 11, 20, 9, 5, 0, 0, time.UTC), Actor: fakeActor, Action: audit.ActionApply, ProjectID: 1, ProjectName: "shop",
			Detail: map[string]string{audit.DetailApply: "4", audit.DetailIrreversible: "no"},
		},
		{
			ID: 48, At: time.Date(2026, 11, 20, 8, 5, 0, 0, time.UTC), Actor: fakeActor, Action: audit.ActionApply, ProjectID: 1, ProjectName: "shop",
			Detail: map[string]string{audit.DetailApply: "<b>x</b>"},
		},
	}
	res := e.do(t, http.MethodGet, "/audit", nil, e.admin(t))
	rows := strings.Split(section(res.body, "tbody", "audit-rows"), "<tr")
	if len(rows) != 4 {
		t.Fatalf("GET /audit shows %d rows, want 3; body:\n%s", len(rows)-1, res.body)
	}
	wantIn(t, "consented apply row", rows[1], `<span class="badge badge-queued">적용</span>`, `<a href="/applies/5">적용 #5</a>`, "복구 불가 동의함")
	wantIn(t, "apply row", rows[2], `<a href="/applies/4">적용 #4</a>`, "복구 불가 동의 안 함")
	wantNotIn(t, "apply row with a bad run", rows[3], "<b>x</b>", "/applies/")
	wantNotIn(t, "audit page", res.body, "적용 요청")
}
