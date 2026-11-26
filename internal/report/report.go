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
	MaxRows   = 100 // the most lines a section lists
	MaxPlaces = 50  // the most places an externalized entry lists
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
// reason, note and Applied (Bundle is zero).
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

// Data is what the database tells about one scan. A line the scan found is
// in one of three lists: FalsePositives when its verdict is false-positive;
// otherwise Structure when it is a secret bundle with a member in a
// triage.Frontend file or is on hold for a structure change; otherwise
// Remaining. Each list is ordered by file, first line and bundle, and holds
// its first MaxRows rows next to the number of all of them. Externalized is
// the applied replace and remove-file items of the runs before the scan,
// one entry per bundle and name, ordered by name and bundle, cut the same
// way; the places of an entry are ordered by file and line. Redactions
// holds one row per kind, file and tracked state, so a file can appear twice
// for a kind. Latest is the project's newest succeeded scan and Previous the
// succeeded scan before Scan, zero when there is none. Events holds the
// newest MaxRows of EventTotal lines.
type Data struct {
	Project        project.Project
	Scan           scan.Scan
	Latest         scan.ID
	Previous       scan.ID
	Totals         Totals
	PreviousTotals Totals
	Structure      List[Line]
	Remaining      List[Line]
	FalsePositives List[Line]
	Externalized   List[Entry]
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

// Entry is one externalized name as the source carries it. Category, Kind
// and StorePath are those of its first place.
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
	switch k { //nolint:exhaustive // the kinds not named share the last rank
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

// Build shapes d into the report made at now. It keeps no slice or map of d
// and shows no more than the caps, whatever d holds.
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

	r.Structure = capList(d.Structure)
	r.Remaining = capList(d.Remaining)
	r.FalsePositives = capList(d.FalsePositives)
	r.Externalized = capList(d.Externalized)
	for i, e := range r.Externalized.Rows {
		r.Externalized.Rows[i].Places = slices.Clone(e.Places[:min(len(e.Places), MaxPlaces)])
	}
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

// capList returns l with a copy of its rows cut to MaxRows.
func capList[T any](l List[T]) List[T] {
	return List[T]{Rows: capRows(l.Rows), Total: l.Total}
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
