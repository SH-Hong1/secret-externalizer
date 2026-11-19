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
	// ReleaseJob queues again a job workerID holds but did not finish, and
	// gives back the attempt it used, or returns ErrLeaseLost.
	ReleaseJob(ctx context.Context, jobID int64, workerID string) error
}

// DefaultReleaseTimeout is the default of Options.ReleaseTimeout.
const DefaultReleaseTimeout = 5 * time.Second

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
	// ReleaseTimeout bounds giving back a job that was stopped by the
	// shutdown. Zero means DefaultReleaseTimeout.
	ReleaseTimeout time.Duration
}

func (o Options) validate() error {
	switch {
	case o.WorkerID == "":
		return errors.New("worker ID is empty")
	case o.Poll <= 0 || o.Lease <= 0 || o.Heartbeat <= 0:
		return errors.New("poll, lease, and heartbeat must be positive")
	case o.ReleaseTimeout < 0:
		return errors.New("release timeout is negative")
	case o.Heartbeat >= o.Lease:
		return errors.New("heartbeat must be shorter than the lease")
	}
	return nil
}

// Run claims jobs from q one at a time and passes each to h, until ctx is
// cancelled. A job's lease is renewed while h runs. A job that h returns an
// error for is logged and left to its lease, so it is retried when the lease
// runs out. On cancellation Run cancels the running job's context and waits
// for h to return. When h then returns an error, so that it did not finish
// the job, and the lease was not lost, Run gives the job back to the queue
// with ReleaseJob under a context of its own bounded by
// opts.ReleaseTimeout. Run then returns nil.
func Run(ctx context.Context, logger *slog.Logger, q Queue, h Handler, opts Options) error {
	if err := opts.validate(); err != nil {
		return fmt.Errorf("checking options: %w", err)
	}
	if opts.ReleaseTimeout == 0 {
		opts.ReleaseTimeout = DefaultReleaseTimeout
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
// returned, the renewals have stopped, and, when the shutdown stopped h, the
// job has been released.
func runJob(ctx context.Context, logger *slog.Logger, q Queue, h Handler, opts Options, j Job) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// stopped is set by the handler's goroutine and leaseLost by the
	// renewals'; both are read only after Wait.
	var stopped, leaseLost bool
	var g errgroup.Group
	g.Go(func() error {
		defer cancel() // ends the renewals
		err := h(jobCtx, j)
		// The handler finished the job when it returned nil; otherwise the
		// job is released only if it was the shutdown that stopped it.
		stopped = err != nil && ctx.Err() != nil
		if err != nil {
			if jobCtx.Err() != nil {
				logger.InfoContext(ctx, "job stopped before it finished", "job_id", j.ID, "scan_id", j.ScanID)
				return nil
			}
			logger.ErrorContext(ctx, "job failed", "job_id", j.ID, "scan_id", j.ScanID, "error", err.Error())
		}
		return nil
	})
	g.Go(func() error {
		renewLease(jobCtx, logger, func() {
			leaseLost = true
			cancel()
		}, q, opts, j)
		return nil
	})
	_ = g.Wait() // both functions return nil; they report through the logger

	if stopped && !leaseLost {
		releaseJob(ctx, logger, q, opts, j)
	}
}

// releaseJob gives j back to the queue after the shutdown stopped its
// handler. ctx is already cancelled, so the call runs under a context of its
// own. A job that is no longer this worker's is left as it is.
func releaseJob(ctx context.Context, logger *slog.Logger, q Queue, opts Options, j Job) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opts.ReleaseTimeout)
	defer cancel()
	err := q.ReleaseJob(releaseCtx, j.ID, opts.WorkerID)
	switch {
	case err == nil:
		logger.InfoContext(releaseCtx, "job released", "job_id", j.ID, "scan_id", j.ScanID)
	case errors.Is(err, ErrLeaseLost):
		// The job was closed or taken over meanwhile: nothing to give back.
	default:
		logger.ErrorContext(releaseCtx, "releasing the job failed", "job_id", j.ID, "scan_id", j.ScanID, "error", err.Error())
	}
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
