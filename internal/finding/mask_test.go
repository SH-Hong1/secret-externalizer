package finding_test

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

func file(text string) textfile.File {
	return textfile.File{Encoding: textfile.UTF8, Text: []byte(text)}
}

// spanOf returns the span of the first occurrence of sub in text.
func spanOf(t *testing.T, text, sub string) finding.Span {
	t.Helper()
	i := strings.Index(text, sub)
	if i < 0 {
		t.Fatalf("%q not found in %q", sub, text)
	}
	return finding.Span{Start: i, End: i + len(sub)}
}

func TestContext_MasksTarget(t *testing.T) {
	text := "a\nb\npassword = dev-only-fake-pw;\nc\nd\ne\n"
	got, first := finding.Context(file(text), spanOf(t, text, "dev-only-fake-pw"), nil)
	want := []string{"a", "b", "password = ****;", "c", "d"}
	if first != 1 || !cmp.Equal(got, want) {
		t.Errorf("Context() = %q, %d, want %q, 1\n%s", got, first, want, cmp.Diff(want, got))
	}
}

func TestContext_MasksNeighbours(t *testing.T) {
	text := "one dev-only-fake-aaa\ntwo\nx=dev-only-fake-bbb y=dev-only-fake-ccc\nfour\n"
	target := spanOf(t, text, "dev-only-fake-bbb")
	all := []finding.Span{
		spanOf(t, text, "dev-only-fake-aaa"),
		target,
		spanOf(t, text, "dev-only-fake-ccc"),
	}
	got, first := finding.Context(file(text), target, all)
	want := []string{"one ****", "two", "x=**** y=****", "four"}
	if first != 1 || !cmp.Equal(got, want) {
		t.Errorf("Context() = %q, %d, want %q, 1", got, first, want)
	}
}

func TestContext_MultiLineValue(t *testing.T) {
	pem := "-----BEGIN FAKE-----\nAAAA\nBBBB\n-----END FAKE-----"
	text := "l1\nl2\nkey = " + pem + " # done\nl7\nl8\nl9\n"
	got, first := finding.Context(file(text), spanOf(t, text, pem), nil)
	want := []string{"l1", "l2", "key = ****", "", "", " # done", "l7", "l8"}
	if first != 1 || !cmp.Equal(got, want) {
		t.Errorf("Context() = %q, %d, want %q, 1", got, first, want)
	}
}

func TestContext_VeryLongMultiLineValueIsCapped(t *testing.T) {
	value := strings.Repeat("dev-only-fake-line\n", 20) + "end"
	text := "l1\nl2\nl3\nk=" + value + "\nafter\n"
	got, first := finding.Context(file(text), spanOf(t, text, value), nil)
	want := []string{"l2", "l3", "k=****", "", ""}
	if first != 2 || !cmp.Equal(got, want) {
		t.Errorf("Context() = %q, %d, want %q, 2", got, first, want)
	}
}

func TestContext_OverlappingSpans(t *testing.T) {
	text := "x=abcdefghij;\n"
	target := finding.Span{Start: 2, End: 8}
	for name, all := range map[string][]finding.Span{
		"overlapping": {{Start: 2, End: 8}, {Start: 5, End: 12}},
		"adjacent":    {{Start: 8, End: 12}, {Start: 2, End: 8}},
		"unsorted":    {{Start: 5, End: 12}, {Start: 2, End: 8}},
	} {
		got, first := finding.Context(file(text), target, all)
		want := []string{"x=****;"}
		if first != 1 || !cmp.Equal(got, want) {
			t.Errorf("%s: Context() = %q, %d, want %q, 1", name, got, first, want)
		}
	}
}

func TestContext_TruncatesAfterMasking(t *testing.T) {
	text := strings.Repeat("a", 190) + " dev-only-fake-secret-value " + strings.Repeat("b", 5000)
	// The target begins before the 200th character and runs past it.
	target := spanOf(t, text, "dev-only-fake-secret-value")
	got, _ := finding.Context(file(text), target, nil)
	if len(got) != 1 {
		t.Fatalf("Context() = %d lines, want 1", len(got))
	}
	want := strings.Repeat("a", 190) + " ****" + " " + strings.Repeat("b", 4)
	if got[0] != want {
		t.Errorf("Context() = %q, want %q", got[0], want)
	}
	// A second value far beyond the cut must not survive either.
	text2 := strings.Repeat("x", 300) + "dev-only-fake-far"
	got, _ = finding.Context(file(text2), spanOf(t, text2, "dev-only-fake-far"), nil)
	if strings.Contains(got[0], "dev-only") || len([]rune(got[0])) != 200 {
		t.Errorf("Context() = %q, want 200 characters without the value", got[0])
	}
}

func TestContext_TruncatesByCharacters(t *testing.T) {
	text := strings.Repeat("가", 300)
	got, _ := finding.Context(file(text), finding.Span{Start: 0, End: 0}, nil)
	if len(got) != 1 || got[0] != strings.Repeat("가", 200) {
		t.Errorf("Context() = %q, want 200 Hangul characters", got)
	}
}

func TestContext_FileEdges(t *testing.T) {
	text := "dev-only-fake-first\nmid\ndev-only-fake-last"
	tests := []struct {
		name      string
		target    finding.Span
		want      []string
		wantFirst int
	}{
		{"first line", spanOf(t, text, "dev-only-fake-first"), []string{"****", "mid", "****"}, 1},
		{"last line", spanOf(t, text, "dev-only-fake-last"), []string{"****", "mid", "****"}, 1},
	}
	all := []finding.Span{spanOf(t, text, "dev-only-fake-first"), spanOf(t, text, "dev-only-fake-last")}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, first := finding.Context(file(text), tt.target, all)
			if first != tt.wantFirst || !cmp.Equal(got, tt.want) {
				t.Errorf("Context() = %q, %d, want %q, %d", got, first, tt.want, tt.wantFirst)
			}
		})
	}
	long := "1\n2\n3\n4\n5\n6\n7\nsecret-fake-value\n"
	got, first := finding.Context(file(long), spanOf(t, long, "secret-fake-value"), nil)
	if first != 6 || !cmp.Equal(got, []string{"6", "7", "****"}) {
		t.Errorf("Context() = %q, %d, want [6 7 ****], 6", got, first)
	}
}

func TestContext_Degenerate(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		target finding.Span
		all    []finding.Span
		want   []string
	}{
		{"empty file", "", finding.Span{Start: 0, End: 3}, nil, nil},
		{"zero-length target", "abc\n", finding.Span{Start: 1, End: 1}, nil, []string{"abc"}},
		{"span at offset zero", "secret-fake\nx\n", finding.Span{Start: 0, End: 11}, nil, []string{"****", "x"}},
		{"span to end of text", "k=secret-fake", finding.Span{Start: 2, End: 13}, nil, []string{"k=****"}},
		{"end past text", "k=secret-fake", finding.Span{Start: 2, End: 999}, nil, []string{"k=****"}},
		{"negative start", "secret-fake;", finding.Span{Start: -5, End: 11}, nil, []string{"****;"}},
		{"reversed span", "k=secret-fake;", finding.Span{Start: 12, End: 2}, nil, []string{"k=secret-fake;"}},
		{"out of range in all", "k=secret-fake;", finding.Span{Start: 2, End: 13}, []finding.Span{{Start: 500, End: 600}, {Start: -9, End: -1}}, []string{"k=****;"}},
		{"crlf", "a\r\nk=secret-fake\r\nc\r\n", finding.Span{Start: 5, End: 16}, nil, []string{"a", "k=****", "c"}},
		{"span covers line break exactly", "k=secret-fake\nnext\n", finding.Span{Start: 2, End: 14}, nil, []string{"k=****", "next"}},
		{"span covers crlf exactly", "k=secret-fake\r\nnext\n", finding.Span{Start: 2, End: 15}, nil, []string{"k=****", "next"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := finding.Context(file(tt.text), tt.target, tt.all)
			if !cmp.Equal(got, tt.want) {
				t.Errorf("Context(%q, %v, %v) = %q, want %q", tt.text, tt.target, tt.all, got, tt.want)
			}
		})
	}
}

func TestContext_WidensToCharacterBoundaries(t *testing.T) {
	text := "k=가나다;\n"
	// Bytes 3..7 start in the middle of 가 and end in the middle of 나.
	got, _ := finding.Context(file(text), finding.Span{Start: 3, End: 7}, nil)
	want := []string{"k=****다;"}
	if !cmp.Equal(got, want) {
		t.Errorf("Context(%q, 3..7) = %q, want %q", text, got, want)
	}
}

func TestContext_DoesNotModifyText(t *testing.T) {
	text := "k=dev-only-fake-secret\nx\n"
	f := file(text)
	finding.Context(f, spanOf(t, text, "dev-only-fake-secret"), nil)
	if string(f.Text) != text {
		t.Errorf("Context changed Text to %q, want %q", f.Text, text)
	}
}

func TestContext_DoesNotModifyAll(t *testing.T) {
	text := "abcdefghij\n"
	all := []finding.Span{{Start: 6, End: 9}, {Start: 1, End: 4}}
	want := []finding.Span{{Start: 6, End: 9}, {Start: 1, End: 4}}
	finding.Context(file(text), finding.Span{Start: 1, End: 4}, all)
	if !cmp.Equal(all, want) {
		t.Errorf("Context changed all to %v, want %v", all, want)
	}
}

func TestContext_NoMaskedContentSurvives(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11)) //nolint:gosec // G404: a fixed seed makes the generated cases repeatable
	alphabet := []rune("abcdefghijklmnopqrstuvwxyz0123456789가나다라마바사아자차")
	for iter := range 300 {
		var sb strings.Builder
		var tokens []finding.Span
		for i := range 5 + rng.IntN(30) {
			if i > 0 {
				sb.WriteString([]string{" ", " ", "\n", "\r\n", "="}[rng.IntN(5)])
			}
			start := sb.Len()
			for range 4 + rng.IntN(8) {
				sb.WriteRune(alphabet[rng.IntN(len(alphabet))])
			}
			tokens = append(tokens, finding.Span{Start: start, End: sb.Len()})
		}
		text := sb.String()
		var all []finding.Span
		for _, tok := range tokens {
			if rng.IntN(3) == 0 {
				all = append(all, tok)
			}
		}
		target := tokens[rng.IntN(len(tokens))]
		got, _ := finding.Context(file(text), target, all)
		out := strings.Join(got, "\n")
		for _, s := range append(all, target) {
			content := text[s.Start:s.End]
			if strings.Count(text, content) != 1 {
				continue
			}
			if strings.Contains(out, content) {
				t.Fatalf("iteration %d: output %q contains masked %q (text %q)", iter, out, content, text)
			}
		}
	}
}
