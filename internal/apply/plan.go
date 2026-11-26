package apply

import (
	"bytes"
	"cmp"
	"path"
	"slices"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// Target is one location the plan changes. Text is what is written in its
// place; it is empty for a whole-file target. PEMOnly is set on a
// whole-file target whose file may be removed only if its text holds
// nothing but PEM blocks and white space.
type Target struct {
	Item        Item // Status planned, Reason empty
	Location    finding.Location
	Fingerprint string
	StoreKey    string // "ext.<name>" for a secret, empty for customer data
	Text        string
	WholeFile   bool
	PEMOnly     bool
}

// Plan is what a run would change, decided from stored data alone.
type Plan struct {
	Digest   string
	Blockers []Blocker // scan-not-latest, nothing-to-apply
	Targets  []Target  // ordered by file, then position
	Skipped  []Item    // not-allowed, overlap, holds-customer-data, key-holds-customer-data, key-in-source-file
}

// NewPlan decides from in alone, without reading a file, OpenBao or the
// database, what a run would change. Only bundles to externalize or redact
// whose decision their current members still allow give targets; every
// other bundle gives nothing, and a bundle whose decision is no longer
// allowed has each of its locations skipped as not-allowed. A secret is
// replaced by its placeholder and stored under "ext.<name>"; customer data
// is redacted with Marker and never stored. A key file is removed whole
// under the rules of keyFileReason. Two locations in one file whose ranges
// overlap are both skipped, since neither can be replaced without touching
// the other; a whole-file target does not count. A secret to replace whose
// range shares a character with a span in in.CustomerDataSpans is skipped
// as holds-customer-data, even when it also overlaps another target, since
// storing it would copy customer data into OpenBao; the other target keeps
// its own outcome. The plan is blocked when
// in.Scan is not the project's newest scan, or when there is no target and
// no applied item waiting for the manifest.
func NewPlan(in Input) Plan {
	p := Plan{Digest: Digest(in.Scan.ID, in.Bundles, in.CustomerDataSpans)}
	customer := customerDataFiles(in)
	type place struct {
		bundle                       triage.BundleID
		file                         string
		startLine, startCol, endLine int
		endCol                       int
	}
	seen := make(map[place]bool)
	var targets []Target
	for _, b := range in.Bundles {
		if b.Decision.Verdict != triage.VerdictExternalize && b.Decision.Verdict != triage.VerdictRedact {
			continue
		}
		allowed := allowedNow(b)
		for _, m := range b.Members {
			for _, l := range m.Locations {
				key := place{b.ID, m.File, l.StartLine, l.StartCol, l.EndLine, l.EndCol}
				if seen[key] {
					continue
				}
				seen[key] = true
				if !allowed {
					p.Skipped = append(p.Skipped, skippedItem(b, m, l, ReasonNotAllowed))
					continue
				}
				t := newTarget(in.Project.ID, b, m, l)
				if t.WholeFile {
					reason, pemOnly := keyFileReason(m.File, customer)
					if reason != "" {
						p.Skipped = append(p.Skipped, skipped(t.Item, reason))
						continue
					}
					t.PEMOnly = pemOnly
				}
				targets = append(targets, t)
			}
		}
	}
	slices.SortFunc(targets, compareTargets)
	spans := spansByFile(in.CustomerDataSpans)
	for _, t := range splitOverlaps(targets) {
		switch {
		case t.Item.Action == ActionReplace && holdsCustomerData(t.Location, spans[t.Item.File]):
			p.Skipped = append(p.Skipped, skipped(t.Item, ReasonHoldsCustomerData))
		case t.overlaps:
			p.Skipped = append(p.Skipped, skipped(t.Item, ReasonOverlap))
		default:
			p.Targets = append(p.Targets, t.Target)
		}
	}
	slices.SortFunc(p.Skipped, compareItems)

	if in.NewestScan != in.Scan.ID {
		p.Blockers = append(p.Blockers, BlockerScanNotLatest)
	}
	if len(p.Targets) == 0 && len(in.Unlisted) == 0 {
		p.Blockers = append(p.Blockers, BlockerNothingToApply)
	}
	return p
}

// allowedNow reports whether b's decision still holds against its current
// members, and whether its kind agrees with its category, so that customer
// data is never taken for a secret to store.
func allowedNow(b triage.Bundle) bool {
	if b.Check(b.Decision) != nil {
		return false
	}
	customer := b.Category == finding.CategoryCustomerData
	if customer != b.Kind.CustomerData() {
		return false
	}
	if customer {
		return b.Decision.Verdict == triage.VerdictRedact
	}
	return b.Decision.Verdict == triage.VerdictExternalize
}

// customerDataFiles returns the files in which the scan found customer
// data: those in.CustomerDataFiles lists and those of every customer-data
// bundle in in.Bundles.
func customerDataFiles(in Input) map[string]bool {
	files := make(map[string]bool, len(in.CustomerDataFiles))
	for _, f := range in.CustomerDataFiles {
		files[f] = true
	}
	for _, b := range in.Bundles {
		if b.Category != finding.CategoryCustomerData && !b.Kind.CustomerData() {
			continue
		}
		for _, m := range b.Members {
			files[m.File] = true
		}
	}
	return files
}

// item returns the planned item of b at location l of member m, without
// its action, name or store path.
func item(b triage.Bundle, m triage.Member, l finding.Location) Item {
	return Item{
		BundleID:  b.ID,
		FindingID: m.FindingID,
		File:      m.File,
		StartLine: l.StartLine,
		StartCol:  l.StartCol,
		Action:    action(b),
		Category:  b.Category,
		Kind:      b.Kind,
		Status:    StatusPlanned,
	}
}

// action returns what an apply does for b.
func action(b triage.Bundle) Action {
	switch {
	case b.Decision.Verdict == triage.VerdictRedact:
		return ActionRedact
	case b.Kind == finding.KindKeyFile:
		return ActionRemoveFile
	default:
		return ActionReplace
	}
}

// skippedItem returns the item of b at location l of member m, skipped for
// reason, with no name or store path.
func skippedItem(b triage.Bundle, m triage.Member, l finding.Location, reason Reason) Item {
	return skipped(item(b, m, l), reason)
}

// skipped returns it skipped for reason.
func skipped(it Item, reason Reason) Item {
	it.Status = StatusSkipped
	it.Reason = reason
	return it
}

// newTarget returns the target of b, whose decision is allowed, at location
// l of member m.
func newTarget(projectID project.ID, b triage.Bundle, m triage.Member, l finding.Location) Target {
	t := Target{Item: item(b, m, l), Location: l, Fingerprint: b.Fingerprint}
	switch t.Item.Action {
	case ActionRedact:
		t.Text = Marker(b.Kind)
		return t
	case ActionRemoveFile:
		t.WholeFile = true
	case ActionReplace:
		t.Item.CodeChange = strings.EqualFold(path.Ext(m.File), ".java")
	}
	name := b.Decision.Name
	t.StoreKey = name.Config()
	t.Item.Name = name.For(m.File)
	t.Item.StorePath = bao.StorePath(int64(projectID), t.StoreKey)
	if !t.WholeFile {
		t.Text = "${" + t.Item.Name + "}"
	}
	return t
}

// keyFileReason returns why the key file named file is not removed whole,
// or "" and whether the file may be removed only once its text is found
// to hold nothing but PEM blocks. A file that may hold customer data is
// never stored; a file with a key-store or certificate extension, or an SSH
// key name, is; a source or configuration file is not, since removing it
// would remove the code around the key; any other file is, if it is PEM
// only.
func keyFileReason(file string, customer map[string]bool) (reason Reason, pemOnly bool) {
	switch {
	case finding.CustomerDataPath(file) || customer[file]:
		return ReasonKeyHoldsCustomerData, false
	case keyFileName(file):
		return "", false
	case sourceFileName(file):
		return ReasonKeyInSourceFile, false
	default:
		return "", true
	}
}

// keyFileName reports whether file is named as a key store, key or
// certificate file. Case is ignored.
func keyFileName(file string) bool {
	base := strings.ToLower(path.Base(file))
	switch base {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return true
	}
	switch path.Ext(base) {
	case ".jks", ".p12", ".pfx", ".keystore", ".ppk", ".pem", ".key", ".crt", ".cer", ".der":
		return true
	}
	return false
}

// sourceFileName reports whether file is a source, configuration or page
// file, which a key in it does not make removable. Case is ignored.
func sourceFileName(file string) bool {
	if triage.ShellFile(file) {
		return true
	}
	switch strings.ToLower(path.Ext(file)) {
	case ".java", ".xml", ".properties", ".yml", ".yaml", ".jsp", ".jspf", ".js", ".html", ".htm":
		return true
	}
	return false
}

// checkedTarget is a target and whether it overlaps another.
type checkedTarget struct {
	Target
	overlaps bool
}

// splitOverlaps marks every target in sorted, ordered by compareTargets,
// whose range overlaps another target's range in the same file. Whole-file
// targets are not compared.
func splitOverlaps(sorted []Target) []checkedTarget {
	out := make([]checkedTarget, len(sorted))
	for i, t := range sorted {
		out[i].Target = t
	}
	for i := range out {
		a := out[i]
		if a.WholeFile {
			continue
		}
		for j := i + 1; j < len(out) && out[j].Item.File == a.Item.File; j++ {
			b := out[j]
			if b.WholeFile {
				continue
			}
			if comparePositions(b.Location.StartLine, b.Location.StartCol, a.Location.EndLine, a.Location.EndCol) > 0 {
				break
			}
			out[i].overlaps = true
			out[j].overlaps = true
		}
	}
	return out
}

// spansByFile groups spans by file.
func spansByFile(spans []CustomerSpan) map[string][]CustomerSpan {
	out := make(map[string][]CustomerSpan)
	for _, c := range spans {
		out[c.File] = append(out[c.File], c)
	}
	return out
}

// holdsCustomerData reports whether l shares at least one character with
// one of spans, all in l's file. Both ends are inclusive.
func holdsCustomerData(l finding.Location, spans []CustomerSpan) bool {
	for _, c := range spans {
		if comparePositions(c.StartLine, c.StartCol, l.EndLine, l.EndCol) <= 0 &&
			comparePositions(l.StartLine, l.StartCol, c.EndLine, c.EndCol) <= 0 {
			return true
		}
	}
	return false
}

// comparePositions compares line l1, column c1 with line l2, column c2.
func comparePositions(l1, c1, l2, c2 int) int {
	return cmp.Or(cmp.Compare(l1, l2), cmp.Compare(c1, c2))
}

// compareTargets orders targets by file, start, end, bundle and finding.
func compareTargets(x, y Target) int {
	return cmp.Or(
		cmp.Compare(x.Item.File, y.Item.File),
		compareLocations(x.Location, y.Location),
		cmp.Compare(x.Item.BundleID, y.Item.BundleID),
		cmp.Compare(x.Item.FindingID, y.Item.FindingID),
	)
}

// compareItems orders items by file, start, bundle and finding.
func compareItems(x, y Item) int {
	return cmp.Or(
		cmp.Compare(x.File, y.File),
		comparePositions(x.StartLine, x.StartCol, y.StartLine, y.StartCol),
		cmp.Compare(x.BundleID, y.BundleID),
		cmp.Compare(x.FindingID, y.FindingID),
	)
}

// Marker returns "[REDACTED:<kind>]" for a kind of customer data and
// "[REDACTED:data]" for any other kind.
func Marker(k finding.Kind) string {
	if !k.CustomerData() {
		return "[REDACTED:data]"
	}
	return "[REDACTED:" + string(k) + "]"
}

// PEMOnly reports whether text holds at least one PEM block and nothing
// else but white space. A block starts with a "-----BEGIN <label>-----"
// line and ends with the "-----END <label>-----" line of the same label;
// the lines between are its content: blank lines, RFC 1421 header lines
// ("Name: value") and base64 lines. A line may end in "\r".
func PEMOnly(text []byte) bool {
	const (
		begin = "-----BEGIN "
		end   = "-----END "
		dash  = "-----"
	)
	blocks := 0
	label := ""
	inside := false
	for line := range bytes.Lines(text) {
		s := strings.TrimRight(string(line), " \t\r\n")
		switch {
		case !inside && strings.TrimLeft(s, " \t") == "":
		case !inside && strings.HasPrefix(s, begin) && strings.HasSuffix(s, dash) && len(s) > len(begin)+len(dash):
			label = s[len(begin) : len(s)-len(dash)]
			inside = true
		case !inside:
			return false
		case s == end+label+dash:
			inside = false
			blocks++
		case !pemContent(strings.TrimSpace(s)):
			return false
		}
	}
	return !inside && blocks > 0
}

// pemContent reports whether s, a line inside a PEM block with white space
// trimmed, is blank, an RFC 1421 header line or a base64 line.
func pemContent(s string) bool {
	if s == "" {
		return true
	}
	if name, value, ok := strings.Cut(s, ": "); ok {
		return pemHeaderName(name) && value != ""
	}
	for _, r := range s {
		if !base64Rune(r) {
			return false
		}
	}
	return true
}

// pemHeaderName reports whether name is a header name: letters, digits and
// hyphens, starting with a letter.
func pemHeaderName(name string) bool {
	for i, r := range name {
		letter := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		if !letter && (i == 0 || r != '-' && (r < '0' || r > '9')) {
			return false
		}
	}
	return name != ""
}

// base64Rune reports whether r is a character of standard base64.
func base64Rune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '='
}
