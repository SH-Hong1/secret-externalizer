package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
)

// TestOriginalViewRedacts checks that a revealed value, held in the view and
// in the page that carries it, is not printed by fmt, slog or encoding/json.
// Only the template, through Text, gets it.
func TestOriginalViewRedacts(t *testing.T) {
	const value = "dev-only-fake-revealed-value"
	view := newOriginalForm(1, 101, "status=all")
	view.Shown = true
	view.show(bao.Value(value))
	if !view.IsText || view.Text() != value {
		t.Fatalf("originalView.Text() = %q (IsText %v), want the value", view.Text(), view.IsText)
	}
	detail := &bundleDetail{Original: view}
	page := triagePage{Bundle: detail}

	var buf bytes.Buffer
	for _, h := range []slog.Handler{slog.NewJSONHandler(&buf, nil), slog.NewTextHandler(&buf, nil)} {
		slog.New(h).Info("view", "view", view, "deref", *view, "detail", *detail, "page", page)
	}
	outputs := []string{buf.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%q"} {
		outputs = append(outputs,
			fmt.Sprintf(verb, view), fmt.Sprintf(verb, *view),
			fmt.Sprintf(verb, *detail), fmt.Sprintf(verb, page))
	}
	for _, x := range []any{view, *detail, page} {
		j, err := json.Marshal(x)
		if err != nil {
			t.Fatalf("json.Marshal(%T) error = %v", x, err)
		}
		outputs = append(outputs, string(j))
	}
	hexValue := fmt.Sprintf("%x", value)
	for _, out := range outputs {
		if strings.Contains(out, value) || strings.Contains(out, hexValue) {
			t.Errorf("output %q contains the value", out)
		}
	}
}
