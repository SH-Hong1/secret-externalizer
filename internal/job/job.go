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
)

// Job is a claimed job. ScanID is the ID of the scan the job runs.
type Job struct {
	ID       int64
	ScanID   int64
	Attempts int
}
