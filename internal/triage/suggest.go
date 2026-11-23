package triage

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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
	assignment = regexp.MustCompile("(?:^|[\\s<({\\[,;\"'])(?:(?:[A-Za-z_][A-Za-z0-9_-]*:)?([A-Za-z_][A-Za-z0-9_.-]*)\\s*=|([A-Za-z_][A-Za-z0-9_.-]*)\\s*:)\\s*[\"']?")
	targetForm = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

// environments maps a word of a file path or of a key to the environment it
// names, in the form a name carries.
var environments = map[string]string{
	"prod": "prod", "production": "prod", "prd": "prod", "real": "prod", "live": "prod",
	"stg": "stg", "stage": "stg", "staging": "stg",
	"uat":  "uat",
	"qa":   "qa",
	"test": "test", "tst": "test",
	"dev": "dev", "develop": "dev", "development": "dev",
	"local": "local",
}

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
// category.target.item, or category.environment.target.item when the files
// of b or the key name one environment. The target is taken from the key the
// detected value is assigned to and is "main" when the line gives none. A
// number is appended to the target when the name is taken.
func SuggestName(b Bundle, used []Name) Name {
	taken := make(map[Name]bool, len(used))
	for _, n := range used {
		taken[n] = true
	}
	env, target := envAndTarget(b)
	prefix := nameCategory(b.Category) + "."
	if env != "" {
		prefix += env + "."
	}
	for i := 1; ; i++ {
		t := target
		if i > 1 {
			t += strconv.Itoa(i)
		}
		n := Name(prefix + t + "." + nameItem(b.Kind))
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

// envAndTarget returns the environment and the target of a name for b. The
// environment is the one every file of b names. Without one, it is the
// environment the key starts with. A key that starts with the environment
// gives the word after it as the target.
func envAndTarget(b Bundle) (env, target string) {
	env = bundleEnv(b)
	words := keyWords(detectedKey(b))
	if len(words) > 0 {
		if e := environments[words[0]]; e != "" && (env == "" || env == e) {
			env = e
			words = words[1:]
		}
	}
	if len(words) > 0 && targetForm.MatchString(words[0]) {
		return env, words[0]
	}
	return env, mainTarget
}

// bundleEnv returns the environment that every file of b names, or "" when a
// file names none or two files name different ones.
func bundleEnv(b Bundle) string {
	env := ""
	for i, m := range b.Members {
		e := fileEnv(m.File)
		if e == "" || (i > 0 && e != env) {
			return ""
		}
		env = e
	}
	return env
}

// fileEnv returns the environment a slash-separated file path names, or "".
// It reads the file name without its extension first, from its last word,
// and then the folders from the nearest. A word is a run of letters and
// digits, so "LocalCache" and "realm" name no environment.
func fileEnv(file string) string {
	dir, base := path.Split(file)
	segments := []string{strings.TrimSuffix(base, path.Ext(base))}
	folders := strings.Split(strings.Trim(dir, "/"), "/")
	for _, folder := range slices.Backward(folders) {
		segments = append(segments, folder)
	}
	for _, seg := range segments {
		words := strings.FieldsFunc(strings.ToLower(seg), func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		})
		for _, w := range slices.Backward(words) {
			if e := environments[w]; e != "" {
				return e
			}
		}
	}
	return ""
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

// detectedKey returns the key the first detected value of b is assigned to,
// or "" when there is none.
func detectedKey(b Bundle) string {
	if len(b.Members) == 0 || len(b.Members[0].Locations) == 0 {
		return ""
	}
	loc := b.Members[0].Locations[0]
	i := loc.StartLine - loc.ContextFirstLine
	if i < 0 || i >= len(loc.Context) {
		return ""
	}
	return lineKey(loc.Context[i], loc.StartCol)
}

// keyWords returns the words of key that can tell one target from another:
// its words up to the first that does not have the form of a name segment,
// without the stop words. A key that holds "*" is the masked preview of a
// detected value, such as the account in "curl -u user:pass", and gives no
// words, so no part of a detected value reaches a name.
func keyWords(key string) []string {
	if strings.Contains(key, "*") {
		return nil
	}
	var words []string
	for _, w := range splitWords(key) {
		if stopWords[w] {
			continue
		}
		if !targetForm.MatchString(w) {
			break
		}
		words = append(words, w)
	}
	return words
}

// lineKey returns the key a value on line is assigned to: the value of a
// name attribute, or the key of the masked value nearest to column col, a
// 1-based character column, or the last field before the first "=" or ":".
// A col of zero means the column is not known.
func lineKey(line string, col int) string {
	if m := nameAttr.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	if key := keyNear(line, col); key != "" {
		return key
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

// keyNear returns the key of the assignment on line whose value is masked
// and starts nearest to column col, or "" when col is not known or no key is
// assigned a masked value. A key is the name before "=", without a namespace
// such as the "p" of p:password, or the name before ":".
//
// The column is the one in the file. The line is the stored one, where a
// masked value before the column may be shorter or longer than the value it
// hides, so the value is looked for near the column and not at it. Of two
// values at the same distance the later one wins.
func keyNear(line string, col int) string {
	if col < 1 {
		return ""
	}
	key, best := "", -1
	for _, m := range assignment.FindAllStringSubmatchIndex(line, -1) {
		name := m[2:4]
		if name[0] < 0 {
			name = m[4:6]
		}
		if !strings.Contains(assigned(line[m[1]:], line[m[1]-1]), "*") {
			continue
		}
		d := utf8.RuneCountInString(line[:m[1]]) + 1 - col
		if d < 0 {
			d = -d
		}
		if best < 0 || d <= best {
			key, best = line[name[0]:name[1]], d
		}
	}
	return key
}

// assigned returns the value at the start of rest: up to the quote that
// opened it, or up to the first white space when opener is not a quote.
func assigned(rest string, opener byte) string {
	end := strings.IndexAny(rest, " \t")
	if opener == '"' || opener == '\'' {
		end = strings.IndexByte(rest, opener)
	}
	if end < 0 {
		return rest
	}
	return rest[:end]
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
// allow it. The verdict and the hold reason are copied. The note is not, and
// neither is the name: an externalize suggestion carries the name SuggestName
// gives b, because the files and the category of b say which environment and
// which kind of store the name belongs to.
func Adapt(past Decision, b Bundle, used []Name) Suggestion {
	if past == (Decision{}) {
		return Default(b, used)
	}
	d := Decision{Verdict: past.Verdict, Reason: past.Reason, Name: past.Name}
	if err := b.Check(d); err != nil {
		return Default(b, used)
	}
	if d.Verdict == VerdictExternalize {
		d.Name = SuggestName(b, used)
	}
	return Suggestion{Decision: d, Source: SourceHistory}
}
