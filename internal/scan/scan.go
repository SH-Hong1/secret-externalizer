// Package scan defines a scan of a project and the states it passes through.
package scan

import (
	"time"

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
// fails the scan, never text from an error.
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
	Findings      int
}
