// Package apply defines an apply of a project's verdicts to its source: the
// run that checks the conditions and, once a user confirms it, applies them,
// the outcome for each location, and the digest that binds a confirmation to
// the plan the check showed. Nothing here holds a value a scan found.
package apply

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// ID identifies an apply run.
type ID int64

// State is where an apply run is.
type State string

// The states of an apply run. A check runs while the run is checking and
// leaves it ready or blocked; a confirmed run is queued, then running, then
// succeeded or failed. A newer check supersedes a ready or blocked run.
const (
	StateChecking   State = "checking"
	StateReady      State = "ready"
	StateBlocked    State = "blocked"
	StateQueued     State = "queued"
	StateRunning    State = "running"
	StateSucceeded  State = "succeeded"
	StateFailed     State = "failed"
	StateSuperseded State = "superseded"
)

// Active reports whether a run in state s keeps scans, verdicts and other
// applies of its project waiting: it is being checked, is queued, or runs.
func (s State) Active() bool {
	switch s {
	case StateChecking, StateQueued, StateRunning:
		return true
	case StateReady, StateBlocked, StateSucceeded, StateFailed, StateSuperseded:
		return false
	}
	return false
}

// Blocker is a condition that keeps a project from being applied.
type Blocker string

// The conditions a check can find.
const (
	BlockerNotRepository      Blocker = "not-repository"
	BlockerNoCommit           Blocker = "no-commit"
	BlockerUnsafeGitConfig    Blocker = "unsafe-git-config"
	BlockerGitMissing         Blocker = "git-missing"
	BlockerGitFailed          Blocker = "git-failed"
	BlockerDirty              Blocker = "dirty"
	BlockerScanNotLatest      Blocker = "scan-not-latest"
	BlockerNothingToApply     Blocker = "nothing-to-apply"
	BlockerManifestUnreadable Blocker = "manifest-unreadable"
	BlockerProjectPath        Blocker = "project-path"
	BlockerWorkspaceReadOnly  Blocker = "workspace-read-only"
)

// Action is what an apply does at one location.
type Action string

// The actions of an apply.
const (
	ActionReplace    Action = "replace"
	ActionRedact     Action = "redact"
	ActionRemoveFile Action = "remove-file"
)

// Status is where the change at one location is.
type Status string

// The statuses of an Item.
const (
	StatusPlanned Status = "planned"
	StatusWriting Status = "writing"
	StatusApplied Status = "applied"
	StatusSkipped Status = "skipped"
	StatusFailed  Status = "failed"
)

// Reason says why a location was skipped or failed.
type Reason string

// The reasons an Item can carry.
const (
	ReasonFileChanged           Reason = "file-changed"
	ReasonFileMissing           Reason = "file-missing"
	ReasonNotText               Reason = "not-text"
	ReasonValueMismatch         Reason = "value-mismatch"
	ReasonValueUnclear          Reason = "value-unclear"
	ReasonValuesDiffer          Reason = "values-differ"
	ReasonNotAllowed            Reason = "not-allowed"
	ReasonOverlap               Reason = "overlap"
	ReasonUntrackedIrreversible Reason = "untracked-irreversible"
	ReasonNestedRepository      Reason = "nested-repository"
	ReasonKeyInSourceFile       Reason = "key-in-source-file"
	ReasonKeyHoldsCustomerData  Reason = "key-holds-customer-data"
	ReasonHoldsCustomerData     Reason = "holds-customer-data"
	ReasonFileRemoved           Reason = "file-removed"
	ReasonTooLarge              Reason = "too-large"
	ReasonStoreFailed           Reason = "store-failed"
	ReasonWriteFailed           Reason = "write-failed"
	ReasonInterrupted           Reason = "interrupted"
)

// MaxDirtyShown is the number of changed paths a run keeps for the page.
const MaxDirtyShown = 50

// Run is one check of a project and, once a user confirms it, one apply.
// PlanDigest is the Digest of the plan when the check was requested.
// IrreversibleFiles are the untracked files holding customer data that the
// check showed; an apply redacts customer data in an untracked file only
// when the file is listed here and AllowIrreversible is set. Times that have
// not happened are zero.
type Run struct {
	ID                ID
	ProjectID         project.ID
	ScanID            scan.ID
	State             State
	PlanDigest        string
	Requester         audit.Actor
	RequestedAt       time.Time
	CheckedAt         time.Time
	ConfirmedAt       time.Time
	StartedAt         time.Time
	FinishedAt        time.Time
	AllowIrreversible bool
	IrreversibleFiles []string
	Blockers          []Blocker
	// DirtyFiles are the first MaxDirtyShown of DirtyCount changed paths.
	DirtyFiles    []string
	DirtyCount    int
	FailureReason string
	Applied       int
	Skipped       int
	Failed        int
	RescanID      scan.ID
}

// Item is the outcome for one location. Line is the line after the apply,
// 0 for a removed file. Name is the placeholder name as written, empty
// for a redaction. It never holds a value.
type Item struct {
	BundleID            triage.BundleID
	FindingID           finding.ID
	File                string
	StartLine, StartCol int
	Line                int
	Action              Action
	Category            finding.Category
	Kind                finding.Kind
	Name                string
	StorePath           string
	StoreVersion        int
	Tracked             bool
	CodeChange          bool
	Status              Status
	Reason              Reason
}

// CustomerSpan is a location of customer data that a scan found and that is
// not judged a false positive: the file and the first and last character.
type CustomerSpan struct {
	File                string
	StartLine, StartCol int
	EndLine, EndCol     int
}

// Digest identifies a plan: the SHA-256, in hex, of the scan ID; for each
// bundle in ID order, its ID, verdict, name and its members in ascending ID
// order, each member with its file and each of its locations (start and
// end, file hash) in order; and each customer-data span in order of file,
// start and end; one field per line. Two plans that differ in any of these
// give different digests; the order of bundles, members, locations and
// spans in the arguments, a note, a hold reason and the times do not change
// it. A file name is quoted, so no field can run into the next.
func Digest(scanID scan.ID, bundles []triage.Bundle, customer []CustomerSpan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scan %d\n", scanID)
	sorted := slices.Clone(bundles)
	slices.SortFunc(sorted, func(x, y triage.Bundle) int { return cmp.Compare(x.ID, y.ID) })
	for _, bundle := range sorted {
		fmt.Fprintf(&b, "bundle %d\nverdict %s\nname %s\n", bundle.ID, bundle.Decision.Verdict, bundle.Decision.Name)
		members := slices.Clone(bundle.Members)
		slices.SortFunc(members, func(x, y triage.Member) int { return cmp.Compare(x.FindingID, y.FindingID) })
		for _, m := range members {
			fmt.Fprintf(&b, "member %d\nfile %s\n", m.FindingID, strconv.Quote(m.File))
			locations := slices.Clone(m.Locations)
			slices.SortFunc(locations, compareLocations)
			for _, l := range locations {
				fmt.Fprintf(&b, "location %d %d %d %d %s\n", l.StartLine, l.StartCol, l.EndLine, l.EndCol, strconv.Quote(l.FileHash))
			}
		}
	}
	spans := slices.Clone(customer)
	slices.SortFunc(spans, compareSpans)
	for _, c := range spans {
		fmt.Fprintf(&b, "customer %s %d %d %d %d\n", strconv.Quote(c.File), c.StartLine, c.StartCol, c.EndLine, c.EndCol)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func compareSpans(x, y CustomerSpan) int {
	return cmp.Or(
		cmp.Compare(x.File, y.File),
		cmp.Compare(x.StartLine, y.StartLine),
		cmp.Compare(x.StartCol, y.StartCol),
		cmp.Compare(x.EndLine, y.EndLine),
		cmp.Compare(x.EndCol, y.EndCol),
	)
}

func compareLocations(x, y finding.Location) int {
	return cmp.Or(
		cmp.Compare(x.StartLine, y.StartLine),
		cmp.Compare(x.StartCol, y.StartCol),
		cmp.Compare(x.EndLine, y.EndLine),
		cmp.Compare(x.EndCol, y.EndCol),
		cmp.Compare(x.FileHash, y.FileHash),
	)
}

// Count is the number of bundles with one verdict in one category that a
// scan found, and the number of their locations in that scan. The zero
// Verdict means undecided.
type Count struct {
	Verdict   triage.Verdict
	Category  finding.Category
	Bundles   int
	Locations int
}

// Summary is what the database tells about applying a project before any
// check reads its source. Scan is the project's latest succeeded scan, zero
// when it has none. Digest is the Digest of the current plan of that scan.
// Counts holds a Count for each verdict and category the scan found, ordered
// by verdict and category. Blockers holds the conditions the database shows:
// BlockerScanNotLatest when a scan of any state is newer than Scan, then
// BlockerNothingToApply when no bundle is to be externalized or redacted and
// no applied value waits for the manifest. ScanActive reports a queued or
// running scan. Check is the project's newest run that is not superseded, or
// nil.
type Summary struct {
	Scan       scan.Scan
	Digest     string
	Counts     []Count
	Blockers   []Blocker
	ScanActive bool
	Check      *Run
}

// Input is what the worker needs to plan a run.
type Input struct {
	Run        Run
	Project    project.Project
	Scan       scan.Scan // the scan the run refers to
	NewestScan scan.ID   // the project's newest scan in any state
	// Bundles are the project's bundles to externalize or redact that Scan
	// found, each with the members and locations of Scan.
	Bundles []triage.Bundle
	// CustomerDataFiles holds every file in which Scan found customer data,
	// whatever its verdict.
	CustomerDataFiles []string
	// CustomerDataSpans holds every location of customer data that Scan
	// found and whose bundle is not judged a false positive: undecided, on
	// hold or to redact. No secret whose range shares a character with one
	// of them is stored.
	CustomerDataSpans []CustomerSpan
	// Unlisted holds the project's applied secret items, of any run, that
	// are not in the manifest yet.
	Unlisted []Item
}

// OpenFile is a change to a file that was announced and not confirmed.
// TempName is relative to the project folder.
type OpenFile struct {
	Run       ID
	File      string
	AfterHash string // empty: the file was to be removed
	TempName  string
}

// Written is a change the project's applies made to a file.
type Written struct {
	AfterHash string // empty: the file was removed
}
