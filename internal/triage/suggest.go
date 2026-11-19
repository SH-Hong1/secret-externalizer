package triage

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// Suggestion is what the tool proposes for a bundle. It is never stored.
// Source says where the verdict came from.
type Suggestion struct {
	Decision Decision
	Source   Source
}

// Source names where a suggestion came from.
type Source string

// The sources of a suggestion.
const (
	// SourceRule means the rules of this package made the suggestion.
	SourceRule Source = "rule"
	// SourceHistory means an earlier verdict on the same value made it.
	SourceHistory Source = "history"
)

const mainTarget = "main"

var (
	nameAttr   = regexp.MustCompile(`\bname\s*=\s*["']([^"']*)["']`)
	targetForm = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

// stopWords are the words that say what a key is or which kind of store it
// belongs to, so they do not tell one target from another.
var stopWords = map[string]bool{
	"jdbc": true, "db": true, "database": true, "datasource": true, "spring": true,
	"ssh": true, "partner": true,
	"password": true, "passwd": true, "pwd": true, "pass": true, "username": true,
	"user": true, "id": true, "account": true, "url": true, "host": true,
	"port": true, "token": true, "key": true, "secret": true, "value": true,
}

// SuggestName returns a name for b that is not in used. It has the form
// category.target.item, where the target is taken from the key on the
// detected line and is "main" when the line gives none. A number is appended
// to the target when the name is taken.
func SuggestName(b Bundle, used []Name) Name {
	taken := make(map[Name]bool, len(used))
	for _, n := range used {
		taken[n] = true
	}
	target := targetOf(detectedLine(b))
	for i := 1; ; i++ {
		t := target
		if i > 1 {
			t += strconv.Itoa(i)
		}
		n := Name(nameCategory(b.Category) + "." + t + "." + nameItem(b.Kind))
		if _, err := ParseName(string(n)); err != nil {
			if target == mainTarget {
				return n
			}
			target = mainTarget
			i = 0
			continue
		}
		if !taken[n] {
			return n
		}
	}
}

func nameCategory(c finding.Category) string {
	switch c {
	case finding.CategoryStorage:
		return "db"
	case finding.CategorySSH:
		return "ssh"
	case finding.CategoryPartnerSession:
		return "partner"
	case finding.CategoryCustomerData, finding.CategoryOther:
		return "secret"
	}
	return "secret"
}

func nameItem(k finding.Kind) string {
	switch k {
	case finding.KindPassword:
		return "password"
	case finding.KindToken:
		return "token"
	case finding.KindAccount:
		return "account"
	case finding.KindHost:
		return "host"
	case finding.KindPort:
		return "port"
	case finding.KindURL:
		return "url"
	case finding.KindPrivateKey, finding.KindKeyFile:
		return "key"
	case finding.KindPhone, finding.KindRRN, finding.KindEmail, finding.KindCard, finding.KindAddress, finding.KindOther:
		return "value"
	}
	return "value"
}

// detectedLine returns the context line of the first location of the first
// member that holds the detection, or "" when there is none.
func detectedLine(b Bundle) string {
	if len(b.Members) == 0 || len(b.Members[0].Locations) == 0 {
		return ""
	}
	loc := b.Members[0].Locations[0]
	i := loc.StartLine - loc.ContextFirstLine
	if i < 0 || i >= len(loc.Context) {
		return ""
	}
	return loc.Context[i]
}

// targetOf returns the first word of the key on line that is not a stop
// word, or "main" when there is none. A key that holds "*" is the masked
// preview of a detected value, such as the account in "curl -u user:pass",
// and gives "main" too, so no part of a detected value reaches a name.
func targetOf(line string) string {
	key := lineKey(line)
	if strings.Contains(key, "*") {
		return mainTarget
	}
	for _, w := range splitWords(key) {
		if stopWords[w] {
			continue
		}
		if targetForm.MatchString(w) {
			return w
		}
		break
	}
	return mainTarget
}

// lineKey returns the key a value on line is assigned to: the value of a
// name attribute, or the last field before the first "=" or ":".
func lineKey(line string) string {
	if m := nameAttr.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	i := strings.IndexAny(line, "=:")
	if i < 0 {
		return ""
	}
	fields := strings.Fields(line[:i])
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], `"'`)
}

// splitWords splits s into lower-case ASCII words at every other character
// and at camel-case boundaries.
func splitWords(s string) []string {
	var words []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	isUpper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	isLower := func(c byte) bool { return c >= 'a' && c <= 'z' }
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isUpper(c) && !isLower(c) && !isDigit(c) {
			flush()
			continue
		}
		if isUpper(c) && len(cur) > 0 {
			prev := cur[len(cur)-1]
			nextLower := i+1 < len(s) && isLower(s[i+1])
			if isLower(prev) || isDigit(prev) || (isUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, c)
	}
	flush()
	return words
}

// Default returns the suggestion the rules give for b without looking at
// earlier verdicts: a name for a secret, and a hold for a frontend file.
// Customer data gets no suggestion.
func Default(b Bundle, used []Name) Suggestion {
	if b.Category == finding.CategoryCustomerData {
		return Suggestion{}
	}
	for _, m := range b.Members {
		if Frontend(m.File) {
			return Suggestion{
				Decision: Decision{Verdict: VerdictHold, Reason: HoldStructure},
				Source:   SourceRule,
			}
		}
	}
	return Suggestion{Decision: Decision{Name: SuggestName(b, used)}, Source: SourceRule}
}

// Adapt turns a decision taken earlier for the same value elsewhere into a
// suggestion for b. It falls back to Default when past is zero or b does not
// allow it. The note is not copied, and an externalize name that is already
// in used is replaced by SuggestName.
func Adapt(past Decision, b Bundle, used []Name) Suggestion {
	if past == (Decision{}) {
		return Default(b, used)
	}
	d := Decision{Verdict: past.Verdict, Reason: past.Reason, Name: past.Name}
	if err := b.Check(d); err != nil {
		return Default(b, used)
	}
	if d.Verdict == VerdictExternalize {
		for _, n := range used {
			if n == d.Name {
				d.Name = SuggestName(b, used)
				break
			}
		}
	}
	return Suggestion{Decision: d, Source: SourceHistory}
}
