// Package job defines a unit of work that the worker claims from the job
// queue, and the errors a claim can end in.
package job

import "errors"

var (
	// ErrNoJob means no job is waiting to be run.
	ErrNoJob = errors.New("no job to run")
	// ErrLeaseLost means the caller no longer holds the lease on the job, so
	// the job is not the caller's to change.
	ErrLeaseLost = errors.New("job lease lost")
	// ErrClosed means the job is done or failed and the caller was the last
	// worker to hold it, which is how a job looks once the caller has
	// closed it itself.
	ErrClosed = errors.New("job closed")
)

// Kind is what a job does.
type Kind string

// The kinds of job the worker runs.
const (
	// KindScan scans a project.
	KindScan Kind = "scan"
	// KindApplyCheck checks whether the verdicts of a project can be
	// applied, without changing anything.
	KindApplyCheck Kind = "apply-check"
	// KindApply applies the verdicts of a project to its source.
	KindApply Kind = "apply"
)

// Kinds returns every kind of job, in a new slice.
func Kinds() []Kind {
	return []Kind{KindScan, KindApplyCheck, KindApply}
}

// Job is a claimed job. ScanID is the ID of the scan a scan job runs, and 0
// for the other kinds; ApplyID is the ID of the apply run an apply-check or
// apply job works on, and 0 for a scan job.
type Job struct {
	ID       int64
	Kind     Kind
	ScanID   int64
	ApplyID  int64
	Attempts int
}
