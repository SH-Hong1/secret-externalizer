package scan

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// fakeDecoded is the value the encoded texts of these tests hold.
const fakeDecoded = "dev-only-fake-decoded-9"

func b64(s string) string    { return base64.StdEncoding.EncodeToString([]byte(s)) }
func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestEncodedSpan(t *testing.T) {
	inner := b64("db.password=" + fakeDecoded)
	tests := []struct {
		name     string
		before   string // text before the encoded text, which starts right after it
		encoded  string // the encoded text, which the span must cover
		after    string // text after the encoded text
		encoding finding.Encoding
		depth    int
		secret   string
	}{
		{"base64 at line end", "x=", inner, "\ny=1\n", finding.EncodingBase64, 1, fakeDecoded},
		{"base64 before punctuation", "cmd(", inner, ", more)\n", finding.EncodingBase64, 1, fakeDecoded},
		{"base64 before crlf", "x=", inner, "\r\n", finding.EncodingBase64, 1, fakeDecoded},
		{"base64 at end of text", "", inner, "", finding.EncodingBase64, 1, fakeDecoded},
		{"base64 secret runs into plain text", "k=한글", inner, "끝\n", finding.EncodingBase64, 1, fakeDecoded + "끝"},
		{"base64 url-safe unpadded", "x=", b64url("pw=" + fakeDecoded + "??>>"), " y\n", finding.EncodingBase64, 1, fakeDecoded},
		{"hex", "hex=", hex.EncodeToString([]byte("db.password=" + fakeDecoded)), "\n", finding.EncodingHex, 1, fakeDecoded},
		{"hex upper case", "hex=", strings.ToUpper(hex.EncodeToString([]byte("pw=" + fakeDecoded))), ";\n", finding.EncodingHex, 1, fakeDecoded},
		{"base64 in base64", "x=", b64("x=" + inner), "\n", finding.EncodingBase64, 2, fakeDecoded},
		{"hex in base64", "x=", b64("v=" + hex.EncodeToString([]byte("pw="+fakeDecoded))), "\n", finding.EncodingBase64, 2, fakeDecoded},
		{"percent", "pct=db.password%3D", "Fake%41Pass9", "&x=1\n", finding.EncodingPercent, 1, "FakeAPass9"},
		{"percent secret starts with an escape", "u=", "%40Secret9x", "\n", finding.EncodingPercent, 1, "@Secret9x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := []byte(tt.before + tt.encoded + tt.after)
			start := len(tt.before)
			if tt.encoding == finding.EncodingPercent {
				// betterleaks reports where the decoded match starts, which
				// for these texts is the start of the line's value.
				start = strings.LastIndexByte(tt.before, '=') + 1
			}
			raw := Raw{Encoding: tt.encoding, Depth: tt.depth, Value: Value(tt.secret)}
			got, ok := encodedSpan(text, start, raw)
			want := finding.Span{Start: len(tt.before), End: len(tt.before) + len(tt.encoded)}
			if !ok || got != want {
				t.Errorf("encodedSpan(%q, %d, %s depth %d) = %v, %t, want %v, true", text, start, tt.encoding, tt.depth, got, ok, want)
			}
		})
	}
}

func TestEncodedSpan_NotPlaced(t *testing.T) {
	inner := b64("db.password=" + fakeDecoded)
	tests := []struct {
		name     string
		text     string
		start    int
		encoding finding.Encoding
		depth    int
		secret   string
	}{
		{"decodes to something else", "x=" + b64("db.password=other-value") + "\n", 2, finding.EncodingBase64, 1, fakeDecoded},
		{"secret only in plain text after", "x=" + b64("hello world!") + " " + fakeDecoded + "\n", 2, finding.EncodingBase64, 1, fakeDecoded},
		{"nested but depth one", "x=" + b64("x="+inner) + "\n", 2, finding.EncodingBase64, 1, fakeDecoded},
		{"not base64 at start", "x= " + inner + "\n", 2, finding.EncodingBase64, 1, fakeDecoded},
		{"start past the end", "x=1", 4, finding.EncodingBase64, 1, fakeDecoded},
		{"broken base64", "x=" + inner[:len(inner)-3] + "\n", 2, finding.EncodingBase64, 1, fakeDecoded},
		{"odd hex", "x=" + hex.EncodeToString([]byte("pw="+fakeDecoded)) + "a\n", 2, finding.EncodingHex, 1, fakeDecoded},
		{"percent secret has no escape", "u=a%3Db&pw=" + fakeDecoded + "\n", 2, finding.EncodingPercent, 1, fakeDecoded},
		{"percent nested", "u=%2541bc\n", 2, finding.EncodingPercent, 2, "Abc"},
		{"empty secret", "x=" + inner + "\n", 2, finding.EncodingBase64, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := Raw{Encoding: tt.encoding, Depth: tt.depth, Value: Value(tt.secret)}
			if got, ok := encodedSpan([]byte(tt.text), tt.start, raw); ok {
				t.Errorf("encodedSpan(%q, %d, %s depth %d) = %v, true, want not placed", tt.text, tt.start, tt.encoding, tt.depth, got)
			}
		})
	}
}

// A text with many encoded runs inside one another does not make the check
// decode without bound: it stops when its budget runs out and reports that
// the value is not there.
func TestHolds_WorkIsBounded(t *testing.T) {
	layer := strings.Repeat(b64("aaaaaaaaaaaaaaaa")+" ", 40)
	for range 2 {
		layer = b64(layer)
	}
	budget := 1 << 20
	if holds([]byte(layer), []byte(fakeDecoded), 4, &budget) {
		t.Errorf("holds(deeply nested runs) = true, want false")
	}
	if budget >= 0 {
		t.Errorf("holds(deeply nested runs) left budget %d, want it run out (the runs need about 60000 decodings of 1KiB or more)", budget)
	}
	raw := Raw{Encoding: finding.EncodingBase64, Depth: 5, Value: Value(fakeDecoded)}
	if got, ok := encodedSpan([]byte("x="+layer+"\n"), 2, raw); ok {
		t.Errorf("encodedSpan(deeply nested runs) = %v, true, want not placed", got)
	}
}
