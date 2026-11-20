// Package report builds the report of one scan: what the scan found, what
// the project's applies had changed before it, and the history of the
// decisions. Nothing here holds a value, a fingerprint, a preview or a
// line of source.
package report

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// The caps that keep a report small enough to draw and to convert.
const (
	MaxRows   = 1000 // the most lines a section lists
	MaxPlaces = 50   // the most places an externalized entry lists
)

// Count is a number of bundles and of their locations.
type Count struct{ Bundles, Locations int }

// Totals are the numbers of one scan. Found is what the scan found, and
// FalsePositive, Hold, Undecided and Pending split it by the verdict the
// bundles have now; Pending is externalize or redact. Externalized and
// Redacted count the applied items of the runs that started before the
// scan was requested.
type Totals struct {
	Found, FalsePositive, Hold, Undecided, Pending Count
	Externalized, Redacted                         Count
}

// Applied says how an apply changed places of a bundle the scan found.
// The zero value means none did.
type Applied string

// The values of Applied.
const (
	AppliedBefore Applied = "before" // a run before the scan: found again
	AppliedAfter  Applied = "after"  // a run after the scan
)

// Line is one line of what a scan found: a secret bundle (Bundles is 1),
// or the customer data of one kind in one file with one verdict, hold
// reason and note (Bundle is zero).
type Line struct {
	Bundle    triage.BundleID
	Category  finding.Category
	Kind      finding.Kind
	Name      triage.Name
	Verdict   triage.Verdict
	Reason    triage.HoldReason
	Note      string
	DecidedAt time.Time // the newest decision of the line, zero when undecided
	File      string    // the first file
	FirstLine int
	Files     int
	Bundles   int
	Locations int
	Frontend  bool // a secret bundle with a member the scan found in a triage.Frontend file; false for customer data
	Shell     bool // every member the scan found is in a triage.ShellFile
	Applied   Applied
}

// Change is one applied replace or remove-file item.
type Change struct {
	Run  apply.ID
	Item apply.Item
}

// Redaction is the redacted customer data of one kind in one file.
type Redaction struct {
	Kind      finding.Kind
	File      string
	Untracked bool // git did not track the file, so the change cannot be undone
	Bundles   int
	Locations int
}

// Event is one line of the history: the audit events one request left.
// Category, Kind and File are those of the first finding the first bundle
// holds now, empty when it holds none.
type Event struct {
	At       time.Time
	Actor    audit.Actor
	Action   audit.Action
	Detail   map[string]string
	Bundles  int
	Category finding.Category
	Kind     finding.Kind
	File     string
}

// Data is what the database tells about one scan. Redactions holds one row
// per kind, file and tracked state, so a file can appear twice for a kind. Latest is the project's
// newest succeeded scan and Previous the succeeded scan before Scan, zero
// when there is none. Events holds the newest MaxRows of EventTotal lines.
type Data struct {
	Project        project.Project
	Scan           scan.Scan
	Latest         scan.ID
	Previous       scan.ID
	Totals         Totals
	PreviousTotals Totals
	Lines          []Line
	Changes        []Change
	Redactions     []Redaction
	Events         []Event
	EventTotal     int
}

// List is the lines a section shows and the number it stands for.
type List[T any] struct {
	Rows  []T
	Total int
}

// More returns the number of lines the list leaves out.
func (l List[T]) More() int { return l.Total - len(l.Rows) }

// Place is one place an apply changed. Line is the line after the apply;
// Removed means the whole file was removed and Line is zero.
type Place struct {
	File    string
	Line    int
	Removed bool
	Run     apply.ID
}

// Entry is one externalized name as the source carries it.
type Entry struct {
	Bundle     triage.BundleID
	Name       string // as written: ext.db.main.password or EXT_DB_MAIN_PASSWORD
	Category   finding.Category
	Kind       finding.Kind
	StorePath  string
	Places     []Place // the first MaxPlaces of PlaceTotal
	PlaceTotal int
	CodeChange bool // a place is in code that has to read the value itself
	Reappeared bool // the scan still finds the bundle's value
}

// KindCount is the redacted customer data of one kind.
type KindCount struct {
	Kind      finding.Kind
	Bundles   int
	Locations int
	Files     int
}

// Report is the report of one scan, ready to be shown.
type Report struct {
	Project        project.Project
	Scan           scan.Scan
	Latest         scan.ID
	Previous       scan.ID
	GeneratedAt    time.Time
	Totals         Totals
	Delta          Totals // Totals minus the previous scan's; zero without one
	Externalized   List[Entry]
	RedactedKinds  []KindCount
	Redacted       List[Redaction]
	Structure      List[Line]
	Remaining      List[Line]
	FalsePositives List[Line]
	History        List[Event]
}

// kindRank is the place of a customer-data kind in the order phone, rrn,
// email, card, address. Kinds it does not name follow, ordered by name.
func kindRank(k finding.Kind) int {
	//nolint:exhaustive // the kinds not named share the last rank
	switch k {
	case finding.KindPhone:
		return 0
	case finding.KindRRN:
		return 1
	case finding.KindEmail:
		return 2
	case finding.KindCard:
		return 3
	case finding.KindAddress:
		return 4
	default:
		return 5
	}
}

func compareKind(a, b finding.Kind) int {
	return cmp.Or(cmp.Compare(kindRank(a), kindRank(b)), cmp.Compare(a, b))
}

// Build shapes d into the report made at now. It keeps no slice or map of d.
func Build(d Data, now time.Time) Report {
	r := Report{
		Project:     cloneProject(d.Project),
		Scan:        cloneScan(d.Scan),
		Latest:      d.Latest,
		Previous:    d.Previous,
		GeneratedAt: now,
		Totals:      d.Totals,
	}
	if d.Previous != 0 {
		r.Delta = subtract(d.Totals, d.PreviousTotals)
	}

	var structure, remaining, falsePositives []Line
	for _, l := range d.Lines {
		switch {
		case l.Verdict == triage.VerdictFalsePositive:
			falsePositives = append(falsePositives, l)
		case l.Frontend || (l.Verdict == triage.VerdictHold && l.Reason == triage.HoldStructure):
			structure = append(structure, l)
		default:
			remaining = append(remaining, l)
		}
	}
	r.Structure = lineList(structure)
	r.Remaining = lineList(remaining)
	r.FalsePositives = lineList(falsePositives)

	r.Externalized = externalized(d.Changes, d.Lines)
	r.RedactedKinds, r.Redacted = redacted(d.Redactions)

	events := make([]Event, len(d.Events))
	for i, e := range d.Events {
		e.Detail = maps.Clone(e.Detail)
		events[i] = e
	}
	r.History = List[Event]{Rows: capRows(events), Total: d.EventTotal}
	return r
}

func cloneProject(p project.Project) project.Project {
	p.Excludes = slices.Clone(p.Excludes)
	return p
}

func cloneScan(s scan.Scan) scan.Scan {
	s.ToolVersions = maps.Clone(s.ToolVersions)
	s.SkippedFiles = slices.Clone(s.SkippedFiles)
	s.ScannerErrors = maps.Clone(s.ScannerErrors)
	return s
}

func subtract(a, b Totals) Totals {
	sub := func(x, y Count) Count {
		return Count{Bundles: x.Bundles - y.Bundles, Locations: x.Locations - y.Locations}
	}
	return Totals{
		Found:         sub(a.Found, b.Found),
		FalsePositive: sub(a.FalsePositive, b.FalsePositive),
		Hold:          sub(a.Hold, b.Hold),
		Undecided:     sub(a.Undecided, b.Undecided),
		Pending:       sub(a.Pending, b.Pending),
		Externalized:  sub(a.Externalized, b.Externalized),
		Redacted:      sub(a.Redacted, b.Redacted),
	}
}

// capRows returns a copy of rows cut to MaxRows.
func capRows[T any](rows []T) []T {
	if len(rows) == 0 {
		return nil
	}
	return slices.Clone(rows[:min(len(rows), MaxRows)])
}

func lineList(lines []Line) List[Line] {
	slices.SortStableFunc(lines, func(a, b Line) int {
		return cmp.Or(
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.FirstLine, b.FirstLine),
			cmp.Compare(a.Bundle, b.Bundle),
		)
	})
	return List[Line]{Rows: capRows(lines), Total: len(lines)}
}

func externalized(changes []Change, lines []Line) List[Entry] {
	type key struct {
		bundle triage.BundleID
		name   string
	}
	reappeared := make(map[triage.BundleID]bool)
	for _, l := range lines {
		if l.Bundle != 0 && l.Applied == AppliedBefore {
			reappeared[l.Bundle] = true
		}
	}

	byKey := make(map[key]*Entry)
	places := make(map[key][]Place)
	for _, c := range changes {
		k := key{c.Item.BundleID, c.Item.Name}
		e, ok := byKey[k]
		if !ok {
			e = &Entry{
				Bundle:     c.Item.BundleID,
				Name:       c.Item.Name,
				Category:   c.Item.Category,
				Kind:       c.Item.Kind,
				StorePath:  c.Item.StorePath,
				Reappeared: reappeared[c.Item.BundleID],
			}
			byKey[k] = e
		}
		e.CodeChange = e.CodeChange || c.Item.CodeChange
		places[k] = append(places[k], Place{
			File:    c.Item.File,
			Line:    c.Item.Line,
			Removed: c.Item.Action == apply.ActionRemoveFile,
			Run:     c.Run,
		})
	}

	entries := make([]Entry, 0, len(byKey))
	for k, e := range byKey {
		p := places[k]
		slices.SortStableFunc(p, func(a, b Place) int {
			return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
		})
		e.PlaceTotal = len(p)
		e.Places = slices.Clone(p[:min(len(p), MaxPlaces)])
		entries = append(entries, *e)
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Bundle, b.Bundle))
	})
	return List[Entry]{Rows: capRows(entries), Total: len(entries)}
}

func redacted(redactions []Redaction) ([]KindCount, List[Redaction]) {
	rows := slices.Clone(redactions)
	slices.SortStableFunc(rows, func(a, b Redaction) int {
		return cmp.Or(compareKind(a.Kind, b.Kind), cmp.Compare(a.File, b.File))
	})

	var kinds []KindCount
	var files map[string]bool
	for _, x := range rows {
		if n := len(kinds); n == 0 || kinds[n-1].Kind != x.Kind {
			kinds = append(kinds, KindCount{Kind: x.Kind})
			files = make(map[string]bool)
		}
		k := &kinds[len(kinds)-1]
		k.Bundles += x.Bundles
		k.Locations += x.Locations
		files[x.File] = true
		k.Files = len(files)
	}
	return kinds, List[Redaction]{Rows: capRows(rows), Total: len(rows)}
}
