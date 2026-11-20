// Package triage defines bundles, the verdicts a user gives them, and the
// rules for which verdicts a bundle allows. A bundle is a set of findings in
// one project that share a fingerprint; it is the unit of a verdict and of a
// placeholder name.
package triage

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// ErrNotAllowed means a decision is not one the bundle allows, or a finding
// may not move to the bundle it was asked to move to.
var ErrNotAllowed = errors.New("decision not allowed")

// BundleID identifies a bundle.
type BundleID int64

// Verdict is what a user decided about a bundle. The zero value means not
// decided.
type Verdict string

// The verdicts a bundle can have.
const (
	VerdictExternalize   Verdict = "externalize"
	VerdictRedact        Verdict = "redact"
	VerdictFalsePositive Verdict = "false-positive"
	VerdictHold          Verdict = "hold"
)

// HoldReason says why a bundle is on hold.
type HoldReason string

// The reasons a bundle can be on hold.
const (
	HoldStructure HoldReason = "structure-change"
	HoldConfirm   HoldReason = "needs-confirmation"
	HoldLater     HoldReason = "later"
)

// MaxNote is the longest note, in characters, a decision can carry.
const MaxNote = 500

// Decision is what a user chose for a bundle. The zero Decision clears a
// verdict. Name is set only for VerdictExternalize and Reason only for
// VerdictHold.
type Decision struct {
	Verdict Verdict
	Reason  HoldReason
	Name    Name
	Note    string
}

// Member is one finding of a bundle: the value in one file, with the
// locations of one scan.
type Member struct {
	FindingID   finding.ID
	File        string
	HasOriginal bool
	Locations   []finding.Location
}

// Bundle is a set of findings in one project that share a fingerprint, with
// the decision a user gave it. Category, Kind and Preview are those of the
// member with the lowest finding ID. DecidedBy is the subject of the user who
// made the decision; it and DecidedAt are zero when nothing is decided.
// AppliedAt is when an apply changed the source for the bundle, zero when
// none has, and StorePath the path in OpenBao its value went to, empty for a
// bundle whose value was not stored.
type Bundle struct {
	ID          BundleID
	ProjectID   project.ID
	Fingerprint string
	Category    finding.Category
	Kind        finding.Kind
	Preview     string
	Decision    Decision
	DecidedBy   string
	DecidedAt   time.Time
	AppliedAt   time.Time
	StorePath   string
	Members     []Member
}

// Frontend reports whether file is a JSP, JavaScript or HTML file, where a
// value cannot be read from outside configuration. Case is ignored.
func Frontend(file string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".jsp", ".jspf", ".js", ".html", ".htm":
		return true
	}
	return false
}

// SharesBundle reports whether findings of kind k with the same fingerprint
// share one bundle when they are first stored. A host, port or account value
// is common to unrelated settings, and customer data is decided per finding,
// so those kinds start in a bundle of their own.
func SharesBundle(k finding.Kind) bool {
	switch k {
	case finding.KindPassword, finding.KindToken, finding.KindURL, finding.KindPrivateKey, finding.KindKeyFile, finding.KindOther:
		return true
	case finding.KindHost, finding.KindPort, finding.KindAccount,
		finding.KindPhone, finding.KindRRN, finding.KindEmail, finding.KindCard, finding.KindAddress:
		return false
	}
	return false
}

// Allowed returns the verdicts b allows. Customer data can be redacted; a
// secret can be externalized unless a member is in a Frontend file. Every
// bundle can be a false positive or put on hold.
func (b Bundle) Allowed() []Verdict {
	if b.Category == finding.CategoryCustomerData {
		return []Verdict{VerdictRedact, VerdictFalsePositive, VerdictHold}
	}
	for _, m := range b.Members {
		if Frontend(m.File) {
			return []Verdict{VerdictFalsePositive, VerdictHold}
		}
	}
	return []Verdict{VerdictExternalize, VerdictFalsePositive, VerdictHold}
}

// Check returns an error wrapping ErrNotAllowed when b does not allow d: the
// verdict is not in Allowed, an externalized bundle has no valid name, a name
// is given for another verdict, a hold has no known reason, a reason is given
// for another verdict, or the note is longer than MaxNote characters or holds
// a control character or invalid UTF-8. The zero Decision is always allowed.
func (b Bundle) Check(d Decision) error {
	if d == (Decision{}) {
		return nil
	}
	if d.Verdict == "" {
		return fmt.Errorf("%w: a note, name or reason without a verdict", ErrNotAllowed)
	}
	allowed := false
	for _, v := range b.Allowed() {
		allowed = allowed || v == d.Verdict
	}
	if !allowed {
		return fmt.Errorf("%w: verdict %q for a %s bundle", ErrNotAllowed, d.Verdict, b.Category)
	}
	switch {
	case d.Verdict == VerdictExternalize && d.Name == "":
		return fmt.Errorf("%w: externalize without a name", ErrNotAllowed)
	case d.Verdict != VerdictExternalize && d.Name != "":
		return fmt.Errorf("%w: a name for verdict %q", ErrNotAllowed, d.Verdict)
	case d.Verdict == VerdictHold && !knownReason(d.Reason):
		return fmt.Errorf("%w: hold reason %q", ErrNotAllowed, d.Reason)
	case d.Verdict != VerdictHold && d.Reason != "":
		return fmt.Errorf("%w: a hold reason for verdict %q", ErrNotAllowed, d.Verdict)
	}
	if d.Name != "" {
		if _, err := ParseName(string(d.Name)); err != nil {
			return fmt.Errorf("%w: %w", ErrNotAllowed, err)
		}
	}
	switch {
	case !utf8.ValidString(d.Note):
		return fmt.Errorf("%w: the note is not valid UTF-8", ErrNotAllowed)
	case utf8.RuneCountInString(d.Note) > MaxNote:
		return fmt.Errorf("%w: the note is longer than %d characters", ErrNotAllowed, MaxNote)
	case strings.IndexFunc(d.Note, unicode.IsControl) >= 0:
		return fmt.Errorf("%w: the note holds a control character", ErrNotAllowed)
	}
	return nil
}

func knownReason(r HoldReason) bool {
	switch r {
	case HoldStructure, HoldConfirm, HoldLater:
		return true
	}
	return false
}

// PageSize is the number of entries on one page of the triage list.
const PageSize = 50

// GroupLines is the most line numbers a Group lists.
const GroupLines = 50

// The values of Filter.Status besides the verdicts.
const (
	StatusUndecided = "undecided"
	StatusAll       = "all"
)

// Filter selects entries of the triage list. An empty Category means every
// category. Status is StatusUndecided, StatusAll, or a verdict. Page counts
// from one; a page past the last gives the last.
type Filter struct {
	Category finding.Category
	Status   string
	Page     int
}

// GroupKey names the customer data of one kind in one file that has one
// verdict.
type GroupKey struct {
	File    string
	Kind    finding.Kind
	Verdict Verdict
}

// Entry is one line of the triage list. A secret bundle has Bundle set and
// Bundles equal to one. Customer data is listed by group: Bundle is zero,
// Group is set, and Bundles counts the bundles in the group. File is the
// first file of the line and Count the number of locations in the scan.
// Shell reports that every member the scan found is in a ShellFile, so the
// name takes its shell form there. Applied reports that an apply stored the
// bundle's value and changed the source for it.
type Entry struct {
	Bundle    BundleID
	Group     GroupKey
	Name      Name
	Category  finding.Category
	Kind      finding.Kind
	Preview   string
	File      string
	FirstLine int
	Count     int
	Bundles   int
	Verdict   Verdict
	Shell     bool
	Applied   bool
}

// AppliedPageSize is the number of lines on one page of the table of
// applied bundles.
const AppliedPageSize = 50

// Applied is one line of the table of a project's applied bundles. Category,
// Kind and Preview are those of the bundle's member with the lowest ID.
// Locations counts the places applies changed for the bundle, and Shell and
// Config tell whether the name went into a ShellFile and into any other
// file. CodeChange reports that a changed place is in a file whose code has
// to read the value from the secret store. LastRun is the ID of the newest
// apply run that changed a place of the bundle. Reappeared reports that the
// project's latest succeeded scan, requested after that run started, still
// finds the bundle's value.
type Applied struct {
	Bundle     BundleID
	Name       Name
	Category   finding.Category
	Kind       finding.Kind
	Preview    string
	StorePath  string
	AppliedAt  time.Time
	Locations  int
	Shell      bool
	Config     bool
	CodeChange bool
	LastRun    int64
	Reappeared bool
}

// AppliedPage is one page of the table of applied bundles, newest first.
// Total is the number of applied bundles and Pages the number of pages they
// fill.
type AppliedPage struct {
	Entries []Applied
	Total   int
	Page    int
	Pages   int
}

// Page is one page of the triage list. Total is the number of entries that
// match the filter and Pages the number of pages they fill. Counts holds the
// number of bundles in the scan for each verdict, the zero verdict meaning
// undecided, whatever the filter.
type Page struct {
	Entries []Entry
	Total   int
	Page    int
	Pages   int
	Counts  map[Verdict]int
}

// Group is the customer data a GroupKey names: its bundles, the number of
// locations, and the first GroupLines line numbers in ascending order.
type Group struct {
	Key     GroupKey
	Bundles []BundleID
	Count   int
	Lines   []int
}

// ShellFile reports whether file is a shell script, whose placeholder takes
// the shell form. Case is ignored.
func ShellFile(file string) bool {
	return strings.EqualFold(path.Ext(file), ".sh")
}
