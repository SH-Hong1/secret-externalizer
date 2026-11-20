package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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
// cancelled. A job's lease is renewed while h runs, and h's context is
// cancelled when the lease is lost or runs out without a successful
// renewal. A job that h returns an error for is logged and left to its
// lease, so it is retried when the lease runs out. On cancellation Run
// cancels the running job's context and waits for h to return. When h then
// returns an error, so that it did not finish the job, and the lease was
// not lost, Run gives the job back to the queue with ReleaseJob under a
// context of its own bounded by opts.ReleaseTimeout. Run then returns nil.
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
		claimed := time.Now()
		j, err := q.ClaimJob(ctx, opts.WorkerID, opts.Lease)
		switch {
		case err == nil:
			runJob(ctx, logger, q, h, opts, j, claimed)
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

// runJob runs h for j, claimed at the time claimed, and renews the lease
// meanwhile. It returns after h has returned, the renewals have stopped,
// and, when the shutdown stopped h, the job has been released.
func runJob(ctx context.Context, logger *slog.Logger, q Queue, h Handler, opts Options, j Job, claimed time.Time) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// stopped is set by the handler's goroutine and leaseLost by the
	// renewals', when the lease is lost or runs out; both are read only
	// after Wait.
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
				logger.InfoContext(ctx, "job stopped before it finished", attrs(j)...)
				return nil
			}
			logger.ErrorContext(ctx, "job failed", attrs(j, "error", err.Error())...)
		}
		return nil
	})
	g.Go(func() error {
		renewLease(jobCtx, logger, func() {
			leaseLost = true
			cancel()
		}, q, opts, j, claimed)
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
		logger.InfoContext(releaseCtx, "job released", attrs(j)...)
	case errors.Is(err, ErrLeaseLost):
		// The job was closed or taken over meanwhile: nothing to give back.
	default:
		logger.ErrorContext(releaseCtx, "releasing the job failed", attrs(j, "error", err.Error())...)
	}
}

// renewLease extends the lease on j every opts.Heartbeat until ctx ends. It
// calls lost when the lease belongs to another worker, and when opts.Lease
// has passed without a successful renewal since claimed or since the start
// of the last successful renewal: the lease has then run out in the
// database as well, and another worker may claim the job.
//
// A claim or renewal is counted from the moment it was asked for, which is
// no later than the database clock started the lease, so the handler is
// stopped no later than the lease ends.
func renewLease(ctx context.Context, logger *slog.Logger, lost func(), q Queue, opts Options, j Job, claimed time.Time) {
	deadline := claimed.Add(opts.Lease)
	expiry := time.NewTimer(time.Until(deadline))
	defer expiry.Stop()
	beat := time.NewTimer(opts.Heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			logger.WarnContext(ctx, "job lease ran out without a renewal", attrs(j)...)
			lost()
			return
		case <-beat.C:
		}
		asked := time.Now()
		err := extend(ctx, q, opts, j, deadline)
		switch {
		case err == nil:
			deadline = asked.Add(opts.Lease)
			expiry.Reset(time.Until(deadline))
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrLeaseLost):
			logger.WarnContext(ctx, "job lease lost", attrs(j)...)
			lost()
			return
		case !time.Now().Before(deadline):
			logger.WarnContext(ctx, "job lease ran out without a renewal", attrs(j, "error", err.Error())...)
			lost()
			return
		default:
			// A renewal that fails for another reason is tried again at the
			// next beat, until the lease runs out.
			logger.WarnContext(ctx, "extending the job lease failed", attrs(j, "error", err.Error())...)
		}
		beat.Reset(opts.Heartbeat)
	}
}

// extend renews the lease on j. A renewal that has not answered when the
// lease runs out at deadline is abandoned.
func extend(ctx context.Context, q Queue, opts Options, j Job, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return q.ExtendJob(ctx, j.ID, opts.WorkerID, opts.Lease)
}

// Dispatch returns a Handler that passes each job to the handler for its
// kind and returns what that handler returns. A job of a kind with no
// handler is an error, which leaves the job to its lease like any other
// handler error.
func Dispatch(handlers map[Kind]Handler) Handler {
	handlers = maps.Clone(handlers)
	return func(ctx context.Context, j Job) error {
		h, ok := handlers[j.Kind]
		if !ok {
			return fmt.Errorf("running job %d: no handler for kind %q", j.ID, j.Kind)
		}
		return h(ctx, j)
	}
}

// attrs returns the log attributes that name j.
func attrs(j Job, more ...any) []any {
	return append([]any{"job_id", j.ID, "kind", string(j.Kind), "scan_id", j.ScanID, "apply_id", j.ApplyID}, more...)
}
