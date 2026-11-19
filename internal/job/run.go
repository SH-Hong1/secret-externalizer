package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
)

// Queue is the job queue as the worker loop uses it.
type Queue interface {
	// ClaimJob leases one job to workerID for lease, or returns ErrNoJob.
	ClaimJob(ctx context.Context, workerID string, lease time.Duration) (Job, error)
	// ExtendJob renews the lease on a job workerID holds, or returns
	// ErrLeaseLost.
	ExtendJob(ctx context.Context, jobID int64, workerID string, lease time.Duration) error
}

// Handler runs one claimed job. Its ctx is cancelled when the worker shuts
// down or loses the lease, and the handler then returns without finishing
// the job.
type Handler func(ctx context.Context, j Job) error

// Options sets how Run claims jobs. WorkerID must be unique among the
// workers that share a queue.
type Options struct {
	// WorkerID names this worker as the holder of the leases it takes.
	WorkerID string
	// Poll is how long to wait before asking an empty queue again.
	Poll time.Duration
	// Lease is how long a claim or a renewal holds a job.
	Lease time.Duration
	// Heartbeat is how often the lease is renewed while a job runs. It must
	// be shorter than Lease.
	Heartbeat time.Duration
}

func (o Options) validate() error {
	switch {
	case o.WorkerID == "":
		return errors.New("worker ID is empty")
	case o.Poll <= 0 || o.Lease <= 0 || o.Heartbeat <= 0:
		return errors.New("poll, lease, and heartbeat must be positive")
	case o.Heartbeat >= o.Lease:
		return errors.New("heartbeat must be shorter than the lease")
	}
	return nil
}

// Run claims jobs from q one at a time and passes each to h, until ctx is
// cancelled. A job's lease is renewed while h runs. A job that h returns an
// error for is logged and left to its lease, so it is retried when the lease
// runs out. On cancellation Run cancels the running job's context, waits for
// h to return, and returns nil.
func Run(ctx context.Context, logger *slog.Logger, q Queue, h Handler, opts Options) error {
	if err := opts.validate(); err != nil {
		return fmt.Errorf("checking options: %w", err)
	}
	timer := time.NewTimer(opts.Poll)
	defer timer.Stop()
	for ctx.Err() == nil {
		j, err := q.ClaimJob(ctx, opts.WorkerID, opts.Lease)
		switch {
		case err == nil:
			runJob(ctx, logger, q, h, opts, j)
			continue
		case ctx.Err() != nil:
			return nil
		case !errors.Is(err, ErrNoJob):
			logger.ErrorContext(ctx, "claiming a job failed", "error", err.Error())
		}

		timer.Reset(opts.Poll)
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
	return nil
}

// runJob runs h for j and renews the lease meanwhile. It returns after h has
// returned and the renewals have stopped.
func runJob(ctx context.Context, logger *slog.Logger, q Queue, h Handler, opts Options, j Job) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var g errgroup.Group
	g.Go(func() error {
		defer cancel() // ends the renewals
		if err := h(jobCtx, j); err != nil {
			if jobCtx.Err() != nil {
				logger.InfoContext(ctx, "job stopped before it finished", "job_id", j.ID, "scan_id", j.ScanID)
				return nil
			}
			logger.ErrorContext(ctx, "job failed", "job_id", j.ID, "scan_id", j.ScanID, "error", err.Error())
		}
		return nil
	})
	g.Go(func() error {
		renewLease(jobCtx, logger, cancel, q, opts, j)
		return nil
	})
	_ = g.Wait() // both functions return nil; they report through the logger
}

// renewLease extends the lease on j every opts.Heartbeat until ctx ends. It
// calls lost when the lease belongs to another worker.
func renewLease(ctx context.Context, logger *slog.Logger, lost context.CancelFunc, q Queue, opts Options, j Job) {
	timer := time.NewTimer(opts.Heartbeat)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		err := q.ExtendJob(ctx, j.ID, opts.WorkerID, opts.Lease)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrLeaseLost):
			logger.WarnContext(ctx, "job lease lost", "job_id", j.ID, "scan_id", j.ScanID)
			lost()
			return
		default:
			// A renewal that fails for another reason is tried again at the
			// next beat; the lease only runs out if every beat fails.
			logger.WarnContext(ctx, "extending the job lease failed", "job_id", j.ID, "error", err.Error())
		}
		timer.Reset(opts.Heartbeat)
	}
}
