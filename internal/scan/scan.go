// Package scan defines a scan of a project and the states it passes through.
package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// ID identifies a scan.
type ID int64

// Status is the state of a scan.
type Status string

// The states of a scan. A scan is queued until the worker starts it, running
// until the worker finishes it, and then succeeded or failed.
const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Active reports whether the scan has not finished yet.
func (s Status) Active() bool { return s == StatusQueued || s == StatusRunning }

// Scan is one run of the scanner over a project. The time fields that do not
// apply yet are zero. FailureReason is fixed wording chosen by the code that
// fails the scan, never text from an error. Dropped counts scanner results
// that could not be placed in a file the scan read.
type Scan struct {
	ID            ID
	ProjectID     project.ID
	Status        Status
	RequestedBy   string
	RequestedAt   time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	CommitHash    string
	RulesVersion  string
	ToolVersions  map[string]string
	FailureReason string
	Files         int
	Skipped       int
	Dropped       int
	Findings      int
}

// Result is what a scan found, ready to be stored. It holds no value a
// scanner found: each finding carries only a fingerprint, a masked preview,
// its locations and, for secrets, masked surrounding lines.
//
// Files counts the regular files the scan listed. Skipped counts the files
// it could not read as text and the directories it could not read. Dropped
// counts scanner results that were discarded: a value not at the place the
// scanner reported, a file the scan did not read, or a result the scanner
// itself could not place.
type Result struct {
	CommitHash   string
	RulesVersion string
	ToolVersions map[string]string
	Files        int
	Skipped      int
	Dropped      int
	Findings     []finding.Finding
}

// ErrScannerFailed means a scanner did not finish with a usable report. The
// error says which tool failed and its exit code or a fixed reason, and never
// holds anything the scanner wrote.
var ErrScannerFailed = errors.New("scanner failed")

const redacted = "[redacted]"

// Value holds the bytes of a value a scanner found. fmt (every verb), slog,
// and encoding/json print a redacted form, also when the Value is a field of
// a Raw being printed. Convert it with []byte(v) where the bytes are needed.
type Value []byte

// String returns the redacted form.
func (Value) String() string { return redacted }

// Format prints the redacted form for every fmt verb, including %x and %#v.
func (Value) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns the redacted form for slog.
func (Value) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON returns the redacted form as a JSON string.
func (Value) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Raw is one result as a scanner reported it, before it is checked against
// the file and merged with the results of the other scanners.
//
// File is relative to the scanned folder, with forward slashes. A scanner
// that reports byte offsets sets Offset and EndOffset, the half-open byte
// range of the value, and leaves Value empty. A scanner that reports a line
// and a column sets Offset and EndOffset to -1, Line and ByteCol to the
// 1-based line and 1-based byte column where the value starts, and Value to
// the value. A result for a whole file has Offset 0 and EndOffset -1.
type Raw struct {
	Tool, Rule        string
	Category          finding.Category
	Kind              finding.Kind
	File              string
	Offset, EndOffset int
	Line, ByteCol     int
	Value             Value
}

// Counts tells what a scanner run reported besides its results. Dropped is
// the number of results that were discarded because they could not be
// placed: a file outside the scanned folder, or a value that is not inside
// the text the scanner says it matched. Errors is the number of errors the
// scanner reported for files it could analyse only in part.
type Counts struct {
	Dropped int
	Errors  int
}

// Scanner runs one detection tool over a folder.
type Scanner interface {
	// Name returns the tool's name, which is also Raw.Tool.
	Name() string
	// Version returns the version of the tool.
	Version(ctx context.Context) (string, error)
	// Scan scans the folder dir, an absolute path, and returns what it found.
	Scan(ctx context.Context, dir string) ([]Raw, error)
}

// RelPath returns the path p, which a scanner reported for a file it found
// while scanning dir, relative to dir and with forward slashes. A relative p
// is taken as relative to dir. ok is false when p is not a file inside dir.
func RelPath(dir, p string) (rel string, ok bool) {
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	r, err := filepath.Rel(dir, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", false
	}
	return filepath.ToSlash(r), true
}
