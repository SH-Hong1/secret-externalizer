package betterleaks

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestLeak_Redacted(t *testing.T) {
	const value = "dev-only-fake-leak-value"
	l := leak{
		RuleID: "generic-password", File: "conf/app.properties", StartLine: 3, StartColumn: 7,
		Match: "key = " + value, Secret: value, Tags: []string{"category:other-secret"},
	}

	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("result read", "leak", l, "leaks", []leak{l})

	outputs := map[string]string{
		"%v":          fmt.Sprintf("%v", l),
		"%+v":         fmt.Sprintf("%+v", l),
		"%#v":         fmt.Sprintf("%#v", l),
		"%s":          fmt.Sprintf("%s", l),
		"%x":          fmt.Sprintf("%x", l),
		"pointer %+v": fmt.Sprintf("%+v", &l),
		"slice %+v":   fmt.Sprintf("%+v", []leak{l}),
		"String":      l.String(),
		"slog":        logged.String(),
	}
	for name, got := range outputs {
		for _, text := range []string{value, fmt.Sprintf("%x", value)} {
			if strings.Contains(got, text) {
				t.Errorf("%s of leak = %q, want neither the value nor the matched text", name, got)
			}
		}
	}
	if got := l.String(); !strings.Contains(got, "generic-password") || !strings.Contains(got, "conf/app.properties") {
		t.Errorf("leak.String() = %q, want the rule and the file", got)
	}
}
