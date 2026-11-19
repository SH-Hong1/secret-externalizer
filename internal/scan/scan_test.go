package scan_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

func TestValue_Redacted(t *testing.T) {
	const raw = "dev-only-fake-value"
	v := scan.Value(raw)
	r := scan.Raw{
		Tool: "betterleaks", Rule: "fake-rule",
		Category: finding.CategoryOther, Kind: finding.KindOther,
		File: "a.properties", Offset: -1, Line: 1, ByteCol: 1, Value: v,
	}
	// %x of the raw bytes would print them in hex, so look for that form too.
	hexRaw := fmt.Sprintf("%x", raw)

	for _, verb := range []string{"%v", "%s", "%q", "%x", "%X", "%#v", "%+v", "%d"} {
		for _, c := range []struct {
			what string
			arg  any
		}{{"Value", v}, {"Raw", r}, {"*Raw", &r}, {"[]Value", []scan.Value{v}}} {
			out := fmt.Sprintf(verb, c.arg)
			if strings.Contains(out, raw) || strings.Contains(out, hexRaw) || strings.Contains(out, "100 101 118") {
				t.Errorf("Sprintf(%q, %s) = %q, contains the value", verb, c.what, out)
			}
		}
	}
	if got := v.String(); got != "[redacted]" {
		t.Errorf("Value.String() = %q, want [redacted]", got)
	}

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("raw", "value", v, "raw", r)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("raw", "value", v, "raw", r)
	if strings.Contains(buf.String(), raw) {
		t.Errorf("slog output = %q, contains the value", buf.String())
	}
	if !strings.Contains(buf.String(), "[redacted]") {
		t.Errorf("slog output = %q, want it to contain [redacted]", buf.String())
	}

	for _, c := range []struct {
		what string
		arg  any
	}{{"Value", v}, {"Raw", r}} {
		// encoding/json writes a []byte in base64.
		out, err := json.Marshal(c.arg)
		if err != nil || strings.Contains(string(out), raw) || strings.Contains(string(out), base64.StdEncoding.EncodeToString([]byte(raw))) {
			t.Errorf("json.Marshal(%s) = %s, %v, want no value and no error", c.what, out, err)
		}
	}

	if got := string(v); got != raw {
		t.Errorf("string(Value) = %q, want %q", got, raw)
	}
}

func TestRelPath(t *testing.T) {
	tests := []struct {
		dir, path string
		want      string
		ok        bool
	}{
		{"/scan", "/scan/src/A.java", "src/A.java", true},
		{"/scan", "src/A.java", "src/A.java", true},
		{"/scan", "/scan/./src/../src/A.java", "src/A.java", true},
		{"/scan", "/elsewhere/A.java", "", false},
		{"/scan", "/scan/../etc/passwd", "", false},
		{"/scan", "../A.java", "", false},
		{"/scan", "/scan", "", false},
		{"/scan", "", "", false},
		{"/scan", "/scanner/A.java", "", false},
	}
	for _, tt := range tests {
		got, ok := scan.RelPath(tt.dir, tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("RelPath(%q, %q) = %q, %v, want %q, %v", tt.dir, tt.path, got, ok, tt.want, tt.ok)
		}
	}
}
