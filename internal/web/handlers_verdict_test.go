package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// fakeActor is the actor of the user that e.user signs in.
var fakeActor = audit.Actor{Subject: "fake-subject", Name: "Fake User"}

// post sends form to target as a signed-in user from the same origin, with
// the extra header.
func (e *testEnv) post(t *testing.T, target string, form url.Values, header http.Header) result {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	c := e.user(t)
	req.Header.Add("Cookie", c.name+"="+c.value)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return result{status: rec.Code, header: rec.Header(), body: rec.Body.String()}
}

const verdictPath = "/projects/1/bundles/101/verdict"

// verdictForm returns the fields the bundle verdict form always submits.
func verdictForm(verdict, name, reason, note, ret string) url.Values {
	return url.Values{"verdict": {verdict}, "name": {name}, "hold_reason": {reason}, "note": {note}, "return": {ret}}
}

func TestVerdictSaves(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want triage.Decision
	}{
		{
			"externalize",
			verdictForm("externalize", "db.shop.password", "later", "fake note", "status=undecided"),
			triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.shop.password", Note: "fake note"},
		},
		{
			"hold ignores the name",
			verdictForm("hold", "db.shop.password", "later", "", "status=undecided"),
			triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater},
		},
		{
			"false positive ignores the name and the reason",
			verdictForm("false-positive", "not a name", "nope", "fake note", "status=undecided"),
			triage.Decision{Verdict: triage.VerdictFalsePositive, Note: "fake note"},
		},
		{
			"empty verdict cancels",
			verdictForm("", "db.shop.password", "later", "fake note", "status=undecided"),
			triage.Decision{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := triageEnv(t)
			res := e.post(t, verdictPath, tt.form, nil)
			if res.status != http.StatusSeeOther {
				t.Fatalf("POST %s %v status = %d, want 303; body:\n%s", verdictPath, tt.form, res.status, res.body)
			}
			want := []decideCall{{Project: 1, Bundles: []triage.BundleID{passwordBundle}, Decision: tt.want, By: fakeActor}}
			if diff := cmp.Diff(want, e.triage.decideCalls()); diff != "" {
				t.Errorf("POST %s %v Decide calls (-want +got):\n%s", verdictPath, tt.form, diff)
			}
		})
	}
}

func TestVerdictSelectsNext(t *testing.T) {
	tests := []struct {
		name   string
		target string
		form   url.Values
		want   string
	}{
		{
			"undecided list moves to the next bundle",
			verdictPath, verdictForm("false-positive", "", "", "", "status=undecided"),
			"/projects/1/triage?sel=b102&status=undecided",
		},
		{
			"list of every status moves past the decided bundle",
			verdictPath, verdictForm("false-positive", "", "", "", "status=all"),
			"/projects/1/triage?sel=b102&status=all",
		},
		{
			"bundle before customer data",
			"/projects/1/bundles/102/verdict", verdictForm("false-positive", "", "", "", "status=undecided"),
			"/projects/1/triage?sel=" + url.QueryEscape("c.none.phone.logs/sample.log") + "&status=undecided",
		},
		{
			"nothing left in the filter",
			"/projects/1/bundles/102/verdict", verdictForm("hold", "", "structure-change", "", "category=partner-session&status=undecided"),
			"/projects/1/triage?category=partner-session&status=undecided",
		},
		{
			"last line of every status stays selected",
			"/projects/1/customer-data/verdict",
			url.Values{"kind": {"phone"}, "file": {"logs/sample.log"}, "from": {""}, "verdict": {"redact"}, "hold_reason": {"later"}, "note": {""}, "return": {"status=all"}},
			"/projects/1/triage?sel=" + url.QueryEscape("c.redact.phone.logs/sample.log") + "&status=all",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := triageEnv(t)
			res := e.post(t, tt.target, tt.form, nil)
			if res.status != http.StatusSeeOther {
				t.Fatalf("POST %s status = %d, want 303; body:\n%s", tt.target, res.status, res.body)
			}
			if res.location() != tt.want {
				t.Errorf("POST %s Location = %q, want %q", tt.target, res.location(), tt.want)
			}
		})
	}
}

func TestVerdictRejected(t *testing.T) {
	tests := []struct {
		name       string
		form       url.Values
		decideErr  error
		wantStatus int
		wantDecide bool
		wants      []string
	}{
		{
			name:       "name of the wrong form",
			form:       verdictForm("externalize", "Bad Name", "", "fake note", "status=undecided"),
			wantStatus: http.StatusBadRequest,
			wants:      []string{"이름은", `value="Bad Name"`, `value="fake note"`},
		},
		{
			name:       "name with the ext prefix",
			form:       verdictForm("externalize", "ext.db.main.password", "", "", "status=undecided"),
			wantStatus: http.StatusBadRequest,
			wants:      []string{"이름은", `value="ext.db.main.password"`},
		},
		{
			name:       "externalize without a name",
			form:       verdictForm("externalize", "", "", "", "status=undecided"),
			wantStatus: http.StatusBadRequest,
			wants:      []string{"이름은"},
		},
		{
			name:       "unknown verdict",
			form:       verdictForm("maybe", "", "", "", "status=undecided"),
			wantStatus: http.StatusBadRequest,
			wants:      []string{"알 수 없는 판정입니다"},
		},
		{
			name:       "unknown hold reason",
			form:       verdictForm("hold", "", "someday", "", "status=undecided"),
			wantStatus: http.StatusBadRequest,
			wants:      []string{"보류 사유를 골라 주세요"},
		},
		{
			name:       "note too long",
			form:       verdictForm("false-positive", "", "", strings.Repeat("가", triage.MaxNote+1), "status=undecided"),
			wantStatus: http.StatusBadRequest,
			wants:      []string{"메모는 500자 이하"},
		},
		{
			name:       "note with a line break",
			form:       verdictForm("false-positive", "", "", "fake\nnote", "status=undecided"),
			wantStatus: http.StatusBadRequest,
			wants:      []string{"메모는 500자 이하"},
		},
		{
			name:       "verdict the bundle does not allow",
			form:       verdictForm("redact", "", "later", "", "status=undecided"),
			decideErr:  fmt.Errorf("deciding: %w", triage.ErrNotAllowed),
			wantStatus: http.StatusBadRequest,
			wantDecide: true,
			wants:      []string{"이 항목에는 고를 수 없는 판정입니다"},
		},
		{
			name:       "name in use",
			form:       verdictForm("externalize", "db.shop.password", "", "", "status=undecided"),
			decideErr:  fmt.Errorf("deciding: %w", db.ErrDuplicate),
			wantStatus: http.StatusBadRequest,
			wantDecide: true,
			wants:      []string{"이미 쓰는 이름입니다", `value="db.shop.password"`},
		},
		{
			name:       "unknown bundle",
			form:       verdictForm("false-positive", "", "", "", "status=undecided"),
			decideErr:  fmt.Errorf("deciding: %w", db.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantDecide: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := triageEnv(t)
			e.triage.decideErr = tt.decideErr
			res := e.post(t, verdictPath, tt.form, nil)
			if res.status != tt.wantStatus {
				t.Fatalf("POST %s %v status = %d, want %d; body:\n%s", verdictPath, tt.form, res.status, tt.wantStatus, res.body)
			}
			if got := len(e.triage.decideCalls()); got != map[bool]int{false: 0, true: 1}[tt.wantDecide] {
				t.Errorf("POST %s %v called Decide %d times, want it called: %v", verdictPath, tt.form, got, tt.wantDecide)
			}
			if tt.wantStatus != http.StatusBadRequest {
				return
			}
			detail := section(res.body, "section", "detail")
			wantIn(t, "rejected verdict detail", detail, append(tt.wants, `role="alert"`, `action="/projects/1/bundles/101/verdict"`)...)
			wantIn(t, "rejected verdict page", res.body, `id="list"`, "<html")
			wantNotIn(t, "rejected verdict page", res.body, "deciding:")
		})
	}
}

func TestVerdictRefusesCustomerData(t *testing.T) {
	e := triageEnv(t)
	target := "/projects/1/bundles/103/verdict"
	res := e.post(t, target, verdictForm("redact", "", "", "", "status=undecided"), nil)
	if res.status != http.StatusNotFound {
		t.Errorf("POST %s for a customer data bundle status = %d, want 404", target, res.status)
	}
	if n := len(e.triage.decideCalls()); n != 0 {
		t.Errorf("POST %s for a customer data bundle called Decide %d times, want 0", target, n)
	}

	res = e.post(t, "/projects/1/bundles/999/verdict", verdictForm("redact", "", "", "", "status=undecided"), nil)
	if res.status != http.StatusNotFound {
		t.Errorf("POST verdict for a bundle the latest scan does not have status = %d, want 404", res.status)
	}
}

func TestVerdictRejectedFragment(t *testing.T) {
	e := triageEnv(t)
	res := e.post(t, verdictPath, verdictForm("hold", "", "someday", "", "status=undecided"),
		http.Header{"Hx-Request": {"true"}, "Hx-Target": {"detail"}})
	if res.status != http.StatusBadRequest {
		t.Fatalf("htmx POST with a bad reason status = %d, want 400", res.status)
	}
	wantNotIn(t, "htmx rejection", res.body, "<html")
	wantIn(t, "htmx rejection", res.body, `id="detail"`, `id="list"`, "보류 사유를 골라 주세요")
}

func TestVerdictKeepsHoldReason(t *testing.T) {
	e := triageEnv(t)
	e.triage.decideErr = triage.ErrNotAllowed
	res := e.post(t, verdictPath, verdictForm("hold", "", "later", "", "status=undecided"), nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("POST refused hold status = %d, want 400", res.status)
	}
	wantIn(t, "refused hold detail", section(res.body, "section", "detail"), `<option value="later" selected>`)
}

func TestCustomerDataVerdict(t *testing.T) {
	e := triageEnv(t)
	e.triage.putGroup(triage.Group{Key: phoneKey, Bundles: []triage.BundleID{phoneBundle, 201, 202}, Count: 37, Lines: []int{203}})
	form := url.Values{
		"kind": {"phone"}, "file": {"logs/sample.log"}, "from": {""}, "verdict": {"hold"},
		"hold_reason": {"needs-confirmation"}, "name": {"db.ignored.name"}, "note": {"fake note"}, "return": {"status=undecided"},
	}
	res := e.post(t, "/projects/1/customer-data/verdict", form, nil)
	if res.status != http.StatusSeeOther {
		t.Fatalf("POST customer data verdict status = %d, want 303; body:\n%s", res.status, res.body)
	}
	want := []decideCall{{
		Project: 1, Bundles: []triage.BundleID{phoneBundle, 201, 202},
		Decision: triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm, Note: "fake note"}, By: fakeActor,
	}}
	if diff := cmp.Diff(want, e.triage.decideCalls()); diff != "" {
		t.Errorf("POST customer data verdict Decide calls (-want +got):\n%s", diff)
	}

	t.Run("empty group", func(t *testing.T) {
		e := triageEnv(t)
		form := url.Values{"kind": {"phone"}, "file": {"logs/sample.log"}, "from": {"hold"}, "verdict": {"redact"}, "return": {"status=undecided"}}
		res := e.post(t, "/projects/1/customer-data/verdict", form, nil)
		if res.status != http.StatusNotFound {
			t.Errorf("POST customer data verdict for an empty group status = %d, want 404", res.status)
		}
		if n := len(e.triage.decideCalls()); n != 0 {
			t.Errorf("POST customer data verdict for an empty group called Decide %d times, want 0", n)
		}
	})

	t.Run("bad fields", func(t *testing.T) {
		for _, form := range []url.Values{
			{"kind": {"password"}, "file": {"logs/sample.log"}, "verdict": {"redact"}},
			{"kind": {"phone"}, "file": {""}, "verdict": {"redact"}},
			{"kind": {"phone"}, "file": {"logs/\nsample.log"}, "verdict": {"redact"}},
			{"kind": {"phone"}, "file": {"logs/sample.log"}, "from": {"maybe"}, "verdict": {"redact"}},
		} {
			e := triageEnv(t)
			res := e.post(t, "/projects/1/customer-data/verdict", form, nil)
			if res.status != http.StatusBadRequest {
				t.Errorf("POST customer data verdict %v status = %d, want 400", form, res.status)
			}
			if n := len(e.triage.decideCalls()); n != 0 {
				t.Errorf("POST customer data verdict %v called Decide %d times, want 0", form, n)
			}
		}
	})

	t.Run("not allowed", func(t *testing.T) {
		e := triageEnv(t)
		form := url.Values{"kind": {"phone"}, "file": {"logs/sample.log"}, "from": {""}, "verdict": {"externalize"}, "return": {"status=undecided"}}
		res := e.post(t, "/projects/1/customer-data/verdict", form, nil)
		if res.status != http.StatusBadRequest {
			t.Fatalf("POST customer data externalize status = %d, want 400", res.status)
		}
		if n := len(e.triage.decideCalls()); n != 0 {
			t.Errorf("POST customer data externalize called Decide %d times, want 0", n)
		}
		wantIn(t, "refused customer data detail", section(res.body, "section", "detail"),
			"이 항목에는 고를 수 없는 판정입니다", `action="/projects/1/customer-data/verdict"`)
	})
}

func TestMove(t *testing.T) {
	tests := []struct {
		name   string
		form   url.Values
		wantTo triage.BundleID
		want   string
	}{
		{"to a new bundle", url.Values{"to": {"new"}, "from": {"101"}, "return": {"status=undecided"}}, 0, "/projects/1/triage?sel=b101&status=undecided"},
		{"to another bundle", url.Values{"to": {"17"}, "from": {"101"}, "return": {"status=all"}}, 17, "/projects/1/triage?sel=b101&status=all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := triageEnv(t)
			e.triage.moveNew = 300
			res := e.post(t, "/projects/1/findings/1002/move", tt.form, nil)
			if res.status != http.StatusSeeOther {
				t.Fatalf("POST move %v status = %d, want 303; body:\n%s", tt.form, res.status, res.body)
			}
			want := []moveCall{{Project: 1, Finding: 1002, To: tt.wantTo, By: fakeActor}}
			if diff := cmp.Diff(want, e.triage.moveCalls()); diff != "" {
				t.Errorf("POST move %v MoveFinding calls (-want +got):\n%s", tt.form, diff)
			}
			if res.location() != tt.want {
				t.Errorf("POST move %v Location = %q, want %q", tt.form, res.location(), tt.want)
			}
		})
	}

	t.Run("source gone", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.removeBundle(passwordBundle)
		res := e.post(t, "/projects/1/findings/1001/move", url.Values{"to": {"17"}, "from": {"101"}, "return": {"status=all"}}, nil)
		if want := "/projects/1/triage?sel=b17&status=all"; res.status != http.StatusSeeOther || res.location() != want {
			t.Errorf("POST move from a vanished bundle = %d %q, want 303 %q", res.status, res.location(), want)
		}
	})

	t.Run("source unreadable after the move", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.bundleErr = errors.New("fake database down")
		res := e.post(t, "/projects/1/findings/1002/move", url.Values{"to": {"17"}, "from": {"101"}, "return": {"status=all"}}, nil)
		if want := "/projects/1/triage?sel=b17&status=all"; res.status != http.StatusSeeOther || res.location() != want {
			t.Errorf("POST move with the source unreadable = %d %q, want 303 %q", res.status, res.location(), want)
		}
		if n := len(e.triage.moveCalls()); n != 1 {
			t.Errorf("POST move with the source unreadable called MoveFinding %d times, want 1", n)
		}
		if n := countErrors(t, e.log.String()); n != 1 {
			t.Errorf("POST move with the source unreadable logged %d errors, want 1; log = %q", n, e.log.String())
		}
	})

	t.Run("not allowed", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.moveErr = fmt.Errorf("moving: %w", triage.ErrNotAllowed)
		res := e.post(t, "/projects/1/findings/1002/move", url.Values{"to": {"102"}, "from": {"101"}, "return": {"status=undecided"}}, nil)
		if res.status != http.StatusBadRequest {
			t.Fatalf("POST refused move status = %d, want 400", res.status)
		}
		wantIn(t, "refused move detail", section(res.body, "section", "detail"), "이 묶음으로 옮길 수 없습니다", "Sh0p****")
		wantNotIn(t, "refused move page", res.body, "moving:")
	})

	t.Run("unknown finding", func(t *testing.T) {
		e := triageEnv(t)
		e.triage.moveErr = db.ErrNotFound
		res := e.post(t, "/projects/1/findings/9999/move", url.Values{"to": {"new"}}, nil)
		if res.status != http.StatusNotFound {
			t.Errorf("POST move of an unknown finding status = %d, want 404", res.status)
		}
	})

	t.Run("bad target", func(t *testing.T) {
		for _, to := range []string{"", "0", "-3", "x", "b17"} {
			e := triageEnv(t)
			res := e.post(t, "/projects/1/findings/1002/move", url.Values{"to": {to}, "from": {"101"}}, nil)
			if res.status != http.StatusBadRequest {
				t.Errorf("POST move to=%q status = %d, want 400", to, res.status)
			}
			if n := len(e.triage.moveCalls()); n != 0 {
				t.Errorf("POST move to=%q called MoveFinding %d times, want 0", to, n)
			}
		}
	})
}

func TestReturnIsLocal(t *testing.T) {
	targets := []struct {
		target string
		form   url.Values
	}{
		{verdictPath, verdictForm("false-positive", "", "", "", "")},
		{"/projects/1/customer-data/verdict", url.Values{"kind": {"phone"}, "file": {"logs/sample.log"}, "from": {""}, "verdict": {"redact"}}},
		{"/projects/1/findings/1002/move", url.Values{"to": {"new"}, "from": {"101"}}},
	}
	for _, ret := range []string{"https://evil.invalid", "//evil.invalid", "/\\evil.invalid", "status=all&next=//evil.invalid", "status=%zz"} {
		for _, tt := range targets {
			e := triageEnv(t)
			e.triage.moveNew = 300
			form := url.Values{}
			for k, v := range tt.form {
				form[k] = v
			}
			form.Set("return", ret)
			res := e.post(t, tt.target, form, nil)
			if res.status != http.StatusSeeOther {
				t.Errorf("POST %s return=%q status = %d, want 303", tt.target, ret, res.status)
				continue
			}
			if loc := res.location(); !strings.HasPrefix(loc, "/projects/1/triage?") || strings.Contains(loc, "evil") {
				t.Errorf("POST %s return=%q Location = %q, want the triage screen of project 1", tt.target, ret, loc)
			}
		}
	}
}

func TestWritesNeedPost(t *testing.T) {
	targets := []string{verdictPath, "/projects/1/customer-data/verdict", "/projects/1/findings/1002/move"}
	for _, target := range targets {
		e := triageEnv(t)
		if res := e.do(t, http.MethodGet, target, nil, e.user(t)); res.status != http.StatusMethodNotAllowed {
			t.Errorf("GET %s status = %d, want 405", target, res.status)
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader("verdict=redact&to=new&kind=phone&file=logs%2Fsample.log"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		c := e.user(t)
		req.Header.Add("Cookie", c.name+"="+c.value)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %s status = %d, want 403", target, rec.Code)
		}
		if n, m := len(e.triage.decideCalls()), len(e.triage.moveCalls()); n+m != 0 {
			t.Errorf("cross-site POST %s made %d Decide and %d MoveFinding calls, want none", target, n, m)
		}

		if res := e.do(t, http.MethodPost, target, sameOrigin()); res.status != http.StatusUnauthorized {
			t.Errorf("POST %s without a session status = %d, want 401", target, res.status)
		}
	}
}

func TestTriageMoveControls(t *testing.T) {
	e := triageEnv(t)
	e.triage.same[passwordBundle] = []triage.Entry{{
		Bundle: 17, Name: "db.shop.password", Verdict: triage.VerdictExternalize,
		Category: finding.CategoryStorage, Kind: finding.KindPassword, File: "conf/other.yml", FirstLine: 4, Count: 1, Bundles: 1,
	}}
	res := e.getTriage(t, nil, nil)
	detail := section(res.body, "section", "detail")
	wantIn(t, "password detail with another bundle of the value", detail,
		`action="/projects/1/findings/1001/move"`, `action="/projects/1/findings/1002/move"`,
		`name="from" value="101"`, `<option value="17">`, "묶음 #17", "ext.db.shop.password", "다른 묶음으로",
	)
	if n := strings.Count(detail, "다른 묶음으로"); n != 2 {
		t.Errorf("password detail offers the move to another bundle %d times, want once per member (2)", n)
	}

	res = e.getTriage(t, url.Values{"sel": {"b102"}}, nil)
	wantNotIn(t, "token detail without another bundle", section(res.body, "section", "detail"), "다른 묶음으로", "/move")
}

func TestTriageCancelButton(t *testing.T) {
	e := triageEnv(t)
	res := e.getTriage(t, nil, nil)
	wantNotIn(t, "undecided detail", section(res.body, "section", "detail"), "판정 취소")

	b, err := e.triage.Bundle(t.Context(), 1, 12, passwordBundle)
	if err != nil {
		t.Fatalf("Bundle() error = %v", err)
	}
	b.Decision = triage.Decision{Verdict: triage.VerdictFalsePositive}
	e.triage.putBundle(b)
	res = e.getTriage(t, nil, nil)
	wantIn(t, "decided detail", section(res.body, "section", "detail"), `name="verdict" value="">판정 취소</button>`)

	res = e.getTriage(t, url.Values{"sel": {"c.none.phone.logs/sample.log"}}, nil)
	wantNotIn(t, "undecided customer data", section(res.body, "section", "detail"), "판정 취소")
	e.triage.putGroup(triage.Group{Key: triage.GroupKey{File: "logs/sample.log", Kind: finding.KindPhone, Verdict: triage.VerdictHold}, Bundles: []triage.BundleID{phoneBundle}, Count: 1})
	res = e.getTriage(t, url.Values{"sel": {"c.hold.phone.logs/sample.log"}}, nil)
	wantIn(t, "decided customer data", section(res.body, "section", "detail"), "판정 취소", `name="from" value="hold"`)
}

func TestVerdictFormIsBounded(t *testing.T) {
	e := triageEnv(t)
	form := verdictForm("false-positive", "", "", "", "status=undecided")
	form.Set("padding", strings.Repeat("x", 64<<10))
	res := e.post(t, verdictPath, form, nil)
	if res.status != http.StatusBadRequest {
		t.Errorf("POST %s with a 64KiB form status = %d, want 400", verdictPath, res.status)
	}
	if n := len(e.triage.decideCalls()); n != 0 {
		t.Errorf("POST %s with a 64KiB form called Decide %d times, want 0", verdictPath, n)
	}
}
