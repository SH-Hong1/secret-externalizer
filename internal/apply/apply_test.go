package apply_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

var (
	hashE = strings.Repeat("e", 64)
	hashF = strings.Repeat("f", 64)
)

func member(id finding.ID, file string, lines ...int) triage.Member {
	m := triage.Member{FindingID: id, File: file}
	for _, l := range lines {
		m.Locations = append(m.Locations, finding.Location{StartLine: l, StartCol: 5, EndLine: l, EndCol: 20, FileHash: hashF})
	}
	return m
}

// plan is two decided bundles: a password externalized under a name, with
// two members, and a phone number redacted.
func plan() []triage.Bundle {
	return []triage.Bundle{
		{
			ID:       3,
			Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"},
			Members:  []triage.Member{member(10, "conf/a.yml", 3, 9), member(11, "conf/b.xml", 4)},
		},
		{
			ID:       7,
			Decision: triage.Decision{Verdict: triage.VerdictRedact},
			Members:  []triage.Member{member(20, "logs/x.log", 1)},
		},
	}
}

// customer is the customer data the scan of plan found: the redacted phone
// number and an email on hold in a.yml.
func customer() []apply.CustomerSpan {
	return []apply.CustomerSpan{
		{File: "logs/x.log", StartLine: 1, StartCol: 5, EndLine: 1, EndCol: 20},
		{File: "conf/a.yml", StartLine: 3, StartCol: 12, EndLine: 3, EndCol: 18},
	}
}

func TestDigest(t *testing.T) {
	const sid scan.ID = 42
	base := apply.Digest(sid, plan(), customer())
	if len(base) != 64 || strings.Trim(base, "0123456789abcdef") != "" {
		t.Fatalf("Digest() = %q, want 64 lower-case hex digits", base)
	}

	same := map[string]func([]triage.Bundle) []triage.Bundle{
		"bundles reordered": func(b []triage.Bundle) []triage.Bundle { return []triage.Bundle{b[1], b[0]} },
		"members reordered": func(b []triage.Bundle) []triage.Bundle {
			b[0].Members = []triage.Member{b[0].Members[1], b[0].Members[0]}
			return b
		},
		"locations reordered": func(b []triage.Bundle) []triage.Bundle {
			l := b[0].Members[0].Locations
			b[0].Members[0].Locations = []finding.Location{l[1], l[0]}
			return b
		},
		"note, reason and times": func(b []triage.Bundle) []triage.Bundle {
			b[0].Decision.Note = "dev-only note"
			b[0].DecidedAt = time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
			b[0].DecidedBy = "fake-subject"
			b[0].AppliedAt = time.Date(2026, 11, 20, 4, 5, 6, 0, time.UTC)
			b[1].Preview = "010-****-****"
			return b
		},
	}
	for name, change := range same {
		t.Run("same/"+name, func(t *testing.T) {
			if got := apply.Digest(sid, change(plan()), customer()); got != base {
				t.Errorf("Digest(%s) = %s, want %s as before", name, got, base)
			}
		})
	}

	different := map[string]func([]triage.Bundle) []triage.Bundle{
		"verdict": func(b []triage.Bundle) []triage.Bundle {
			b[1].Decision.Verdict = triage.VerdictExternalize
			return b
		},
		"name": func(b []triage.Bundle) []triage.Bundle {
			b[0].Decision.Name = "db.main.pass"
			return b
		},
		"member removed": func(b []triage.Bundle) []triage.Bundle {
			b[0].Members = b[0].Members[:1]
			return b
		},
		"member added": func(b []triage.Bundle) []triage.Bundle {
			b[1].Members = append(b[1].Members, member(21, "logs/y.log", 1))
			return b
		},
		"bundle removed": func(b []triage.Bundle) []triage.Bundle { return b[:1] },
		"bundle ID": func(b []triage.Bundle) []triage.Bundle {
			b[1].ID = 8
			return b
		},
		"location line": func(b []triage.Bundle) []triage.Bundle {
			b[0].Members[0].Locations[0].StartLine = 4
			return b
		},
		"location end column": func(b []triage.Bundle) []triage.Bundle {
			b[0].Members[1].Locations[0].EndCol = 21
			return b
		},
		"file hash": func(b []triage.Bundle) []triage.Bundle {
			b[1].Members[0].Locations[0].FileHash = hashE
			return b
		},
		"file": func(b []triage.Bundle) []triage.Bundle {
			b[1].Members[0].File = "logs/z.log"
			return b
		},
		// Bundle 3 gives member 11 to bundle 7: the same IDs in the same
		// order, with the boundary between the bundles moved.
		"boundary moved": func(b []triage.Bundle) []triage.Bundle {
			moved := b[0].Members[1]
			b[0].Members = b[0].Members[:1]
			b[1].Members = []triage.Member{moved, b[1].Members[0]}
			return b
		},
	}
	for name, change := range different {
		t.Run("different/"+name, func(t *testing.T) {
			if got := apply.Digest(sid, change(plan()), customer()); got == base {
				t.Errorf("Digest(%s) = %s, want a digest other than before", name, got)
			}
		})
	}
	t.Run("different/scan", func(t *testing.T) {
		if got := apply.Digest(sid+1, plan(), customer()); got == base {
			t.Errorf("Digest(scan %d) = %s, want a digest other than scan %d's", sid+1, got, sid)
		}
	})

	t.Run("same/customer spans reordered", func(t *testing.T) {
		c := customer()
		if got := apply.Digest(sid, plan(), []apply.CustomerSpan{c[1], c[0]}); got != base {
			t.Errorf("Digest(customer spans reordered) = %s, want %s as before", got, base)
		}
	})
	spanChanges := map[string]func([]apply.CustomerSpan) []apply.CustomerSpan{
		"customer span removed": func(c []apply.CustomerSpan) []apply.CustomerSpan { return c[:1] },
		"customer span added": func(c []apply.CustomerSpan) []apply.CustomerSpan {
			return append(c, apply.CustomerSpan{File: "conf/b.xml", StartLine: 4, StartCol: 1, EndLine: 4, EndCol: 9})
		},
		"customer span end column": func(c []apply.CustomerSpan) []apply.CustomerSpan {
			c[1].EndCol = 19
			return c
		},
		"customer span file": func(c []apply.CustomerSpan) []apply.CustomerSpan {
			c[1].File = "conf/b.yml"
			return c
		},
		"no customer spans": func([]apply.CustomerSpan) []apply.CustomerSpan { return nil },
	}
	for name, change := range spanChanges {
		t.Run("different/"+name, func(t *testing.T) {
			if got := apply.Digest(sid, plan(), change(customer())); got == base {
				t.Errorf("Digest(%s) = %s, want a digest other than before", name, got)
			}
		})
	}

	t.Run("input untouched", func(t *testing.T) {
		in := []triage.Bundle{plan()[1], plan()[0]}
		in[1].Members = []triage.Member{in[1].Members[1], in[1].Members[0]}
		spans := customer()
		apply.Digest(sid, in, spans)
		if in[0].ID != 7 || in[1].Members[0].FindingID != 11 {
			t.Errorf("Digest() reordered its argument to bundles %d, %d, want it left as given", in[0].ID, in[1].ID)
		}
		if diff := cmp.Diff(customer(), spans); diff != "" {
			t.Errorf("Digest() changed its customer spans argument (-want +got):\n%s", diff)
		}
	})
}

func TestStateActive(t *testing.T) {
	tests := []struct {
		state apply.State
		want  bool
	}{
		{apply.StateChecking, true},
		{apply.StateQueued, true},
		{apply.StateRunning, true},
		{apply.StateReady, false},
		{apply.StateBlocked, false},
		{apply.StateSucceeded, false},
		{apply.StateFailed, false},
		{apply.StateSuperseded, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.state.Active(); got != tt.want {
			t.Errorf("State(%q).Active() = %v, want %v", tt.state, got, tt.want)
		}
	}
}
