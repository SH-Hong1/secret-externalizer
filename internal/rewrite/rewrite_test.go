package rewrite_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/text/encoding/korean"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/rewrite"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

const bom = "\xef\xbb\xbf"

// eucKR encodes s as EUC-KR (CP949).
func eucKR(t testing.TB, s string) []byte {
	t.Helper()
	b, err := korean.EUCKR.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encoding %q as EUC-KR error = %v", s, err)
	}
	return b
}

func decode(t testing.TB, raw []byte) textfile.File {
	t.Helper()
	f, err := textfile.Decode(raw)
	if err != nil {
		t.Fatalf("Decode(% x) error = %v", raw, err)
	}
	return f
}

// asciiTrailChar returns a CP949 character whose second byte is 'c'.
func asciiTrailChar(t testing.TB) string {
	t.Helper()
	r, err := korean.EUCKR.NewDecoder().Bytes([]byte{0x8c, 'c'})
	if err != nil || strings.ContainsRune(string(r), utf8.RuneError) {
		t.Fatalf("decoding 8c 63 = %q, %v, want a CP949 character", r, err)
	}
	return string(r)
}

// diskOffset is the offset in raw of the character at off in f.Text,
// computed by encoding the text before it, independently of the package.
func diskOffset(t testing.TB, raw []byte, f textfile.File, off int) int {
	t.Helper()
	if f.Encoding == textfile.EUCKR {
		return len(eucKR(t, string(f.Text[:off])))
	}
	if bytes.HasPrefix(raw, []byte(bom)) {
		return off + 3
	}
	return off
}

// checkReplace confirms out is raw with the edits applied: every byte outside
// the edited ranges is unchanged, the decoded text is the original text with
// only those ranges replaced, Hash is the SHA-256 of Content, and Lines are the
// lines the replacements start on.
func checkReplace(t testing.TB, raw []byte, f textfile.File, edits []rewrite.Edit, out rewrite.Output) {
	t.Helper()
	order := make([]int, len(edits))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return edits[a].Span.Start - edits[b].Span.Start })

	var content, text []byte
	wantLines := make([]int, len(edits))
	prevDisk, prevText := 0, 0
	for _, i := range order {
		e := edits[i]
		ds, de := diskOffset(t, raw, f, e.Span.Start), diskOffset(t, raw, f, e.Span.End)
		content = append(content, raw[prevDisk:ds]...)
		text = append(text, f.Text[prevText:e.Span.Start]...)
		wantLines[i] = 1 + bytes.Count(text, []byte{'\n'})
		content = append(content, e.With...)
		text = append(text, e.With...)
		prevDisk, prevText = de, e.Span.End
	}
	content = append(content, raw[prevDisk:]...)
	text = append(text, f.Text[prevText:]...)

	if !bytes.Equal(out.Content, content) {
		t.Errorf("Replace().Content = %q, want %q", out.Content, content)
	}
	got, err := textfile.Decode(out.Content)
	if err != nil {
		t.Errorf("Decode(Replace().Content) error = %v", err)
	} else if diff := cmp.Diff(string(text), string(got.Text)); diff != "" {
		t.Errorf("decoded Replace().Content mismatch (-want +got):\n%s", diff)
	}
	sum := sha256.Sum256(out.Content)
	if want := hex.EncodeToString(sum[:]); out.Hash != want {
		t.Errorf("Replace().Hash = %s, want %s", out.Hash, want)
	}
	if diff := cmp.Diff(wantLines, out.Lines); diff != "" {
		t.Errorf("Replace().Lines mismatch (-want +got):\n%s", diff)
	}
}

// target names a value: the first occurrence of value right after the next
// occurrence of anchor.
type target struct{ anchor, value string }

func spansOf(t *testing.T, text []byte, targets []target) []finding.Span {
	t.Helper()
	var out []finding.Span
	cursor := 0
	for _, tg := range targets {
		i := bytes.Index(text[cursor:], []byte(tg.anchor+tg.value))
		if i < 0 {
			t.Fatalf("%q%q not found in %q after %d", tg.anchor, tg.value, text, cursor)
		}
		start := cursor + i + len(tg.anchor)
		out = append(out, finding.Span{Start: start, End: start + len(tg.value)})
		cursor = start + len(tg.value)
	}
	return out
}

func TestSpan(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"one line", "a=1\npw=dev-only-fake-pw\nb=2\n", "dev-only-fake-pw"},
		{"after hangul", "비밀번호=dev-only-fake-pw\n", "dev-only-fake-pw"},
		{"hangul value", "이름=똠방각하\n", "똠방각하"},
		{"several lines", "key=-----BEGIN-----\r\nAAAA\r\n-----END-----\r\nx=1\n", "-----BEGIN-----\r\nAAAA\r\n-----END-----"},
		{"ends with the last character", "a=1\npw=dev-only-fake-pw", "dev-only-fake-pw"},
		{"whole text with its final line feed", "a=1\nb=2\n", "a=1\nb=2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := decode(t, []byte(tt.text))
			start := bytes.Index(f.Text, []byte(tt.want))
			end := start + len(tt.want)
			var l finding.Location
			l.StartLine, l.StartCol = f.Position(start)
			l.EndLine, l.EndCol = f.Position(end - 1)
			got, err := rewrite.Span(f, l)
			if err != nil {
				t.Fatalf("Span(%+v) error = %v", l, err)
			}
			if string(f.Text[got.Start:got.End]) != tt.want {
				t.Errorf("Span(%+v) = %v, text %q, want %q", l, got, f.Text[got.Start:got.End], tt.want)
			}
		})
	}

	f := decode(t, []byte("a=1\nbc"))
	for _, l := range []finding.Location{
		{StartLine: 3, StartCol: 1, EndLine: 3, EndCol: 1},
		{StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 3},
		{StartLine: 1, StartCol: 0, EndLine: 1, EndCol: 1},
		{StartLine: 2, StartCol: 1, EndLine: 1, EndCol: 1},
	} {
		if got, err := rewrite.Span(f, l); err == nil {
			t.Errorf("Span(%+v) = %v, nil, want an error", l, got)
		}
	}
}

func TestReplace(t *testing.T) {
	c := asciiTrailChar(t)
	tests := []struct {
		name    string
		raw     []byte
		targets []target
	}{
		{"utf-8 LF", []byte("db.url=x\npassword=dev-only-fake-pw\n"), []target{{"password=", "dev-only-fake-pw"}}},
		{"utf-8 BOM", []byte(bom + "key=값\r\npw=dev-only-fake-pw\r\n"), []target{{"key=", "값"}, {"pw=", "dev-only-fake-pw"}}},
		{"utf-8 CRLF", []byte("a=1\r\npw=dev-only-fake-pw\r\nz=2\r\n"), []target{{"pw=", "dev-only-fake-pw"}, {"z=", "2"}}},
		{
			"euc-kr with hangul and an ASCII second byte before the value",
			eucKR(t, "# 설정\n비밀번호=dev-only-fake-pw "+c+"Az=dev-only-fake-2\n"),
			[]target{{"비밀번호=", "dev-only-fake-pw"}, {c + "Az=", "dev-only-fake-2"}},
		},
		{"euc-kr CRLF", eucKR(t, "# 데이터베이스\r\n암호=dev-only-fake-pw\r\n이름=똠방\r\n"), []target{{"암호=", "dev-only-fake-pw"}, {"이름=", "똠방"}}},
		{"four values on one line", []byte("url=jdbc:mysql://a:b@c:d/x\n"), []target{{"//", "a"}, {":", "b"}, {"@", "c"}, {":", "d"}}},
		{"first and last characters", []byte("dev-only-fake-a=x=dev-only-fake-z"), []target{{"", "dev-only-fake-a"}, {"x=", "dev-only-fake-z"}}},
		{"no final line feed", eucKR(t, "키=값\npw=dev-only-fake-pw"), []target{{"pw=", "dev-only-fake-pw"}}},
		{
			"value after a three-line value",
			[]byte("key=-----BEGIN-----\r\nAAAA\r\n-----END-----\r\npw=dev-only-fake-pw\r\n"),
			[]target{{"key=", "-----BEGIN-----\r\nAAAA\r\n-----END-----"}, {"pw=", "dev-only-fake-pw"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := decode(t, tt.raw)
			var edits []rewrite.Edit
			for i, s := range spansOf(t, f.Text, tt.targets) {
				edits = append(edits, rewrite.Edit{Span: s, With: fmt.Sprintf("${SE_VALUE_%d}", i)})
			}
			out, err := rewrite.Replace(tt.raw, f, edits)
			if err != nil {
				t.Fatalf("Replace(%s) error = %v", tt.name, err)
			}
			checkReplace(t, tt.raw, f, edits, out)

			reversed := slices.Clone(edits)
			slices.Reverse(reversed)
			out2, err := rewrite.Replace(tt.raw, f, reversed)
			if err != nil {
				t.Fatalf("Replace(%s, reversed) error = %v", tt.name, err)
			}
			if !bytes.Equal(out2.Content, out.Content) {
				t.Errorf("Replace(%s, reversed).Content = %q, want %q", tt.name, out2.Content, out.Content)
			}
		})
	}

	t.Run("lines after a three-line value", func(t *testing.T) {
		raw := []byte("key=-----BEGIN-----\nAAAA\n-----END-----\npw=dev-only-fake-pw\n")
		f := decode(t, raw)
		spans := spansOf(t, f.Text, []target{{"key=", "-----BEGIN-----\nAAAA\n-----END-----"}, {"pw=", "dev-only-fake-pw"}})
		out, err := rewrite.Replace(raw, f, []rewrite.Edit{{Span: spans[1], With: "${B}"}, {Span: spans[0], With: "${A}"}})
		if err != nil {
			t.Fatalf("Replace error = %v", err)
		}
		if diff := cmp.Diff([]int{2, 1}, out.Lines); diff != "" {
			t.Errorf("Replace().Lines mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestReplace_Refuses(t *testing.T) {
	raw := eucKR(t, "키=값\npw=dev-only-fake-pw\n")
	f := decode(t, raw)
	pw := bytes.Index(f.Text, []byte("dev-only"))
	ok := finding.Span{Start: pw, End: pw + 4}
	tests := []struct {
		name    string
		raw     []byte
		edits   []rewrite.Edit
		wantErr error
	}{
		{"overlap", raw, []rewrite.Edit{{Span: ok, With: "x"}, {Span: finding.Span{Start: pw + 3, End: pw + 6}, With: "y"}}, rewrite.ErrOverlap},
		{"same range twice", raw, []rewrite.Edit{{Span: ok, With: "x"}, {Span: ok, With: "y"}}, rewrite.ErrOverlap},
		{"inside a character", raw, []rewrite.Edit{{Span: finding.Span{Start: 1, End: 4}, With: "x"}}, nil},
		{"ends inside a character", raw, []rewrite.Edit{{Span: finding.Span{Start: 0, End: 2}, With: "x"}}, nil},
		{"past the end", raw, []rewrite.Edit{{Span: finding.Span{Start: pw, End: len(f.Text) + 1}, With: "x"}}, nil},
		{"negative", raw, []rewrite.Edit{{Span: finding.Span{Start: -1, End: 2}, With: "x"}}, nil},
		{"empty range", raw, []rewrite.Edit{{Span: finding.Span{Start: pw, End: pw}, With: "x"}}, nil},
		{"reversed range", raw, []rewrite.Edit{{Span: finding.Span{Start: pw + 2, End: pw}, With: "x"}}, nil},
		{"non-ASCII replacement", raw, []rewrite.Edit{{Span: ok, With: "값"}}, nil},
		{"line feed in replacement", raw, []rewrite.Edit{{Span: ok, With: "a\nb"}}, nil},
		{"carriage return in replacement", raw, []rewrite.Edit{{Span: ok, With: "a\rb"}}, nil},
		{"tab in replacement", raw, []rewrite.Edit{{Span: ok, With: "a\tb"}}, nil},
		{"DEL in replacement", raw, []rewrite.Edit{{Span: ok, With: "a\x7f"}}, nil},
		{"empty replacement", raw, []rewrite.Edit{{Span: ok, With: ""}}, nil},
		{"raw is not what the file was decoded from", append(slices.Clone(raw), 'x'), []rewrite.Edit{{Span: ok, With: "x"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := rewrite.Replace(tt.raw, f, tt.edits)
			if err == nil {
				t.Fatalf("Replace(%s) error = nil, want an error", tt.name)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("Replace(%s) error = %v, want %v", tt.name, err, tt.wantErr)
			}
			if diff := cmp.Diff(rewrite.Output{}, out); diff != "" {
				t.Errorf("Replace(%s) output not empty (-want +got):\n%s", tt.name, diff)
			}
		})
	}
}

func FuzzReplace(f *testing.F) {
	// The pieces fuzzed text is made of. Every one can be written in EUC-KR;
	// "똠" has an ASCII second byte.
	fuzzPieces := []string{"a", "Z", "c", "=", " ", "\n", "가", "똠", "값", "漢"}
	f.Add([]byte{0, 6, 7, 5, 1, 2, 3}, []byte{1, 2, 0, 1}, true, true, false)
	f.Add([]byte{6, 6, 6, 5, 7, 7}, []byte{0, 1, 1, 1}, false, false, false)
	f.Add([]byte{7, 2, 5, 7, 2}, []byte{0, 5}, true, false, false)
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, []byte{0, 1, 0, 1, 0, 1, 0, 1}, false, true, true)
	f.Add([]byte{6, 5, 0, 5, 8, 1, 5}, []byte{0, 2, 1, 3}, false, false, true)
	f.Fuzz(func(t *testing.T, pieces, cuts []byte, euc, crlf, withBOM bool) {
		if len(pieces) > 2000 || len(cuts) > 64 {
			t.Skip("input larger than the property needs")
		}
		var sb strings.Builder
		for _, p := range pieces {
			sb.WriteString(fuzzPieces[int(p)%len(fuzzPieces)])
		}
		text := sb.String()
		if crlf {
			text = strings.ReplaceAll(text, "\n", "\r\n")
		}
		raw := []byte(text)
		switch {
		case euc:
			raw = eucKR(t, text)
		case withBOM:
			raw = append([]byte(bom), raw...)
		}
		file := decode(t, raw)

		var bounds []int
		for i := range file.Text {
			if utf8.RuneStart(file.Text[i]) {
				bounds = append(bounds, i)
			}
		}
		bounds = append(bounds, len(file.Text))
		var edits []rewrite.Edit
		at := 0
		for i := 0; i+1 < len(cuts); i += 2 {
			start := at + int(cuts[i])%4
			end := start + 1 + int(cuts[i+1])%5
			if end >= len(bounds) {
				break
			}
			edits = append(edits, rewrite.Edit{Span: finding.Span{Start: bounds[start], End: bounds[end]}, With: fmt.Sprintf("${V%d}", i)})
			at = end
		}
		if len(edits) == 0 {
			return
		}
		if len(cuts) > 0 && cuts[0]%2 == 1 {
			slices.Reverse(edits)
		}
		out, err := rewrite.Replace(raw, file, edits)
		if err != nil {
			t.Fatalf("Replace error = %v", err)
		}
		checkReplace(t, raw, file, edits, out)
	})
}
