package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// The reasons the slot of an original shows no value.
const (
	msgNoOriginal = "보관된 원본이 없습니다. 다시 스캔하면 보관됩니다."
	msgNotKept    = "오탐으로 판정한 값은 원본을 보관하지 않습니다. 판정을 바꾸고 다시 스캔하면 보관됩니다."
	msgStoreDown  = "비밀 저장소에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요."
	msgReadFailed = "비밀 저장소에서 원본을 읽지 못했습니다. 잠시 뒤 다시 시도하고, 계속되면 관리자에게 알려 주세요."
)

// originalView is the slot of a bundle's original value in the detail.
// Before a reveal it holds the form that asks for the value. After one,
// Shown is set and it holds the value read for that request, or its size
// when it is not text, or Notice when there is none to show. It lives only
// for the response that draws it. The value stays a bao.Value, which fmt,
// slog and encoding/json print redacted, so neither the view nor the page
// that carries it can print it; only the template gets it, through Text.
type originalView struct {
	Action    string
	Return    string
	Shown     bool
	CloseHref string
	Notice    string
	IsText    bool
	Value     bao.Value
	Size      string
	By        string
	At        string
}

// Text returns the value as text for the template. It is empty unless the
// value is text.
func (o *originalView) Text() string {
	if !o.IsText {
		return ""
	}
	return string(o.Value.Reveal())
}

// newOriginalForm returns the slot of bundle id of project pid that offers
// the reveal, which returns to the triage screen ret.
func newOriginalForm(pid project.ID, id triage.BundleID, ret string) *originalView {
	return &originalView{
		Action: "/projects/" + strconv.FormatInt(int64(pid), 10) + "/bundles/" + strconv.FormatInt(int64(id), 10) + "/original",
		Return: ret,
	}
}

// hasOriginal reports whether a scan kept the original of any member of b.
func hasOriginal(b triage.Bundle) bool {
	for _, m := range b.Members {
		if m.HasOriginal {
			return true
		}
	}
	return false
}

// show puts v in the slot: the text when it is UTF-8 without NUL, and only
// its size otherwise.
func (o *originalView) show(v bao.Value) {
	raw := v.Reveal()
	if utf8.Valid(raw) && bytes.IndexByte(raw, 0) < 0 {
		o.IsText, o.Value = true, v
		return
	}
	o.Size = groupDigits(len(raw))
}

// groupDigits writes n with a comma between groups of three digits.
func groupDigits(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// handleOriginal shows an admin the original value of a secret bundle that
// the latest scan found. It records the request in the audit trail, then
// reads the value from the secret store for this response only. When the
// audit record fails nothing is read. An htmx request that targets the
// slot gets only the slot; any other request gets the whole triage screen
// with the value in the bundle's detail.
func handleOriginal(tw triageWriter, audits AuditStore, originals Originals) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
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
		sc, ok := tw.latestScan(w, r, pid)
		if !ok {
			return
		}
		bundle, ok := tw.secretBundle(w, r, pid, sc, id)
		if !ok {
			return
		}
		if !hasOriginal(bundle) {
			tw.rd.fail(w, r, tw.logger, http.StatusNotFound, errors.New("no member of the bundle has an original"))
			return
		}
		ret := q.values(false).Encode()
		q.Sel = bundleSel(id)
		fragment := r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "original"

		// The whole screen is read before the reveal is recorded, so a
		// failure there neither records nor reads anything.
		var data triagePage
		if !fragment {
			u, _ := userFrom(ctx) // requireLogin guarantees a user
			p, err := tw.projects.Project(ctx, pid)
			if err != nil {
				tw.rd.fail(w, r, tw.logger, bundleReadStatus(err), fmt.Errorf("reading project: %w", err))
				return
			}
			data = triagePage{User: u, Project: p, Status: q.Status, Options: newCategoryOptions(q.Category)}
			if status, err := fillTriagePage(ctx, tw.logger, tw.store, tw.suggester, &data, q, selection{Bundle: id}, sc); err != nil {
				tw.rd.fail(w, r, tw.logger, status, err)
				return
			}
			if data.Bundle == nil {
				tw.rd.fail(w, r, tw.logger, http.StatusInternalServerError, errors.New("triage screen of a secret bundle has no bundle detail"))
				return
			}
		}

		by := actorOf(r)
		if err := audits.RecordReveal(ctx, pid, id, by); err != nil {
			tw.rd.fail(w, r, tw.logger, bundleReadStatus(err), fmt.Errorf("recording reveal: %w", err))
			return
		}
		view := newOriginalForm(pid, id, ret)
		view.Shown, view.By, view.At, view.CloseHref = true, by.Name, formatTime(time.Now()), q.href(pid, false)
		v, err := originals.ReadPending(ctx, int64(pid), bundle.Fingerprint)
		switch {
		case errors.Is(err, bao.ErrNoOriginal):
			view.Notice = msgNoOriginal
		case err != nil:
			tw.logger.ErrorContext(ctx, "reading original", "project_id", pid, "bundle_id", id, "error", err)
			view.Notice = msgReadFailed
			if errors.Is(err, bao.ErrUnreachable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				view.Notice = msgStoreDown
			}
		default:
			view.show(v)
		}

		if fragment {
			tw.rd.fragment(w, r, tw.logger, http.StatusOK, "original.html", "original", view)
			return
		}
		data.Bundle.Original = view
		tw.rd.triage(w, r, tw.logger, http.StatusOK, data)
	})
}
