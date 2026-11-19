package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// maxJobAttempts is how many times a job is handed out before it is given up.
const maxJobAttempts = 3

// gaveUpReason is the failure reason of a scan whose job ran out of attempts.
const gaveUpReason = "worker stopped during the scan three times"

// ClaimJob hands one job to the worker and leases it for lease. The lease
// runs on the database clock. A job is claimable when it is queued or when
// its lease has run out. It returns job.ErrNoJob when there is none.
//
// In the same transaction, a job whose lease has run out after its last
// attempt is marked failed together with its scan, so that a scan that kills
// its worker does not run forever.
func (s *Store) ClaimJob(ctx context.Context, workerID string, lease time.Duration) (_ job.Job, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return job.Job{}, fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	if _, err := tx.Exec(ctx, `
		WITH given_up AS (
			UPDATE job SET state = 'failed'
			WHERE state = 'running' AND locked_until <= now() AND attempts >= $1
			RETURNING scan_id
		)
		UPDATE scan SET status = 'failed', finished_at = now(), failure_reason = $2
		WHERE id IN (SELECT scan_id FROM given_up) AND status IN ('queued', 'running')`,
		maxJobAttempts, gaveUpReason); err != nil {
		return job.Job{}, fmt.Errorf("failing exhausted jobs: %w", err)
	}

	var j job.Job
	err = tx.QueryRow(ctx, `
		UPDATE job SET state = 'running', attempts = attempts + 1,
			locked_by = $1, locked_until = now() + $2 * interval '1 second'
		WHERE id = (
			SELECT id FROM job
			WHERE state = 'queued' OR (state = 'running' AND locked_until <= now())
			ORDER BY id LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, scan_id, attempts`,
		workerID, lease.Seconds()).Scan(&j.ID, &j.ScanID, &j.Attempts)
	noJob := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !noJob {
		return job.Job{}, fmt.Errorf("claiming job: %w", err)
	}
	// Commit also when there is no job, to keep the jobs failed above.
	if err := tx.Commit(ctx); err != nil {
		return job.Job{}, fmt.Errorf("committing job claim: %w", err)
	}
	if noJob {
		return job.Job{}, job.ErrNoJob
	}
	return j, nil
}

// ExtendJob renews the lease on a job the worker holds. It returns
// job.ErrLeaseLost when the job is not running or is leased to another
// worker.
func (s *Store) ExtendJob(ctx context.Context, jobID int64, workerID string, lease time.Duration) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job SET locked_until = now() + $3 * interval '1 second'
		WHERE id = $1 AND state = 'running' AND locked_by = $2`,
		jobID, workerID, lease.Seconds())
	if err != nil {
		return fmt.Errorf("extending job %d: %w", jobID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("extending job %d: %w", jobID, job.ErrLeaseLost)
	}
	return nil
}

// FailScan marks the job and its scan failed, in one transaction. The reason
// is stored and shown as given, so it must be fixed wording and never text
// from an error. It returns job.ErrLeaseLost, changing nothing, when the
// worker does not hold the job.
func (s *Store) FailScan(ctx context.Context, jobID int64, workerID string, id scan.ID, reason string) error {
	return s.closeJob(ctx, jobID, workerID, id, "failed", `
		UPDATE scan SET status = 'failed', finished_at = now(), failure_reason = $2
		WHERE id = $1`, reason)
}

// FinishEmptyScan marks the scan succeeded with the given counts and no
// findings, and the job done, in one transaction. It returns job.ErrLeaseLost,
// changing nothing, when the worker does not hold the job.
func (s *Store) FinishEmptyScan(ctx context.Context, jobID int64, workerID string, id scan.ID, files, skipped int) error {
	return s.closeJob(ctx, jobID, workerID, id, "done", `
		UPDATE scan SET status = 'succeeded', finished_at = now(), files = $2, skipped = $3
		WHERE id = $1`, files, skipped)
}

// closeJob moves the job the worker holds to state and runs scanSQL, which
// takes the scan ID as $1 and args as $2 onward, in one transaction.
func (s *Store) closeJob(ctx context.Context, jobID int64, workerID string, id scan.ID, state, scanSQL string, args ...any) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	tag, err := tx.Exec(ctx, `
		UPDATE job SET state = $4
		WHERE id = $1 AND state = 'running' AND locked_by = $2 AND scan_id = $3`,
		jobID, workerID, id, state)
	if err != nil {
		return fmt.Errorf("closing job %d: %w", jobID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("closing job %d: %w", jobID, job.ErrLeaseLost)
	}
	if _, err := tx.Exec(ctx, scanSQL, append([]any{id}, args...)...); err != nil {
		return fmt.Errorf("closing scan %d: %w", id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing close of job %d: %w", jobID, err)
	}
	return nil
}
