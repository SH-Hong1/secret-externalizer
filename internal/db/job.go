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

// The failure reasons of a scan or apply run whose job ran out of attempts.
const (
	gaveUpReason      = "worker stopped during the scan three times"
	applyGaveUpReason = "worker stopped during the apply three times"
	checkGaveUpReason = "worker stopped during the check three times"
)

// ClaimJob hands one job to the worker and leases it for lease. The lease
// runs on the database clock. A job is claimable when it is queued or when
// its lease has run out. Jobs are handed out in the order they were made,
// whatever their kind. It returns job.ErrNoJob when there is none.
//
// In the same transaction, a job whose lease has run out after its last
// attempt is marked failed together with its scan or apply run, so that a
// job that kills its worker does not run forever. The records an apply run
// wrote about its files are left as they are; the next check or apply of the
// project reconciles them with the disk.
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
			RETURNING kind, scan_id, apply_id
		), failed_scans AS (
			UPDATE scan SET status = 'failed', finished_at = now(), failure_reason = $2
			WHERE id IN (SELECT scan_id FROM given_up WHERE kind = 'scan') AND status IN ('queued', 'running')
		)
		UPDATE apply_run r SET state = 'failed', finished_at = now(),
			failure_reason = CASE g.kind WHEN 'apply' THEN $3 ELSE $4 END
		FROM given_up g
		WHERE r.id = g.apply_id
			AND ((g.kind = 'apply' AND r.state IN ('queued', 'running'))
				OR (g.kind = 'apply-check' AND r.state = 'checking'))`,
		maxJobAttempts, gaveUpReason, applyGaveUpReason, checkGaveUpReason); err != nil {
		return job.Job{}, fmt.Errorf("failing exhausted jobs: %w", err)
	}

	var j job.Job
	err = tx.QueryRow(ctx, `
		UPDATE job SET state = 'running', attempts = attempts + 1,
			locked_by = $1, locked_until = now() + $2 * interval '1 second'
		WHERE id = (
			SELECT id FROM job
			WHERE attempts < $3
				AND (state = 'queued' OR (state = 'running' AND locked_until <= now()))
			ORDER BY id LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, coalesce(scan_id, 0), coalesce(apply_id, 0), attempts`,
		workerID, lease.Seconds(), maxJobAttempts).Scan(&j.ID, &j.Kind, &j.ScanID, &j.ApplyID, &j.Attempts)
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

// ReleaseJob gives back a job the worker holds but did not finish, in one
// transaction: the job is queued again without its lease, and the attempt it
// used is given back (never going below zero). A scan job's scan is queued
// again with no start time, an apply job's run likewise, and an apply-check
// job's run stays checking. It returns job.ErrLeaseLost, changing nothing,
// when the job is not running or is leased to another worker.
func (s *Store) ReleaseJob(ctx context.Context, jobID int64, workerID string) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	var (
		kind            job.Kind
		scanID, applyID int64
	)
	err = tx.QueryRow(ctx, `
		UPDATE job SET state = 'queued', attempts = greatest(attempts - 1, 0),
			locked_by = NULL, locked_until = NULL
		WHERE id = $1 AND state = 'running' AND locked_by = $2
		RETURNING kind, coalesce(scan_id, 0), coalesce(apply_id, 0)`,
		jobID, workerID).Scan(&kind, &scanID, &applyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("releasing job %d: %w", jobID, job.ErrLeaseLost)
	}
	if err != nil {
		return fmt.Errorf("releasing job %d: %w", jobID, err)
	}
	switch kind {
	case job.KindScan:
		if _, err := tx.Exec(ctx, `
			UPDATE scan SET status = 'queued', started_at = NULL
			WHERE id = $1 AND status IN ('queued', 'running')`, scanID); err != nil {
			return fmt.Errorf("requeuing scan %d: %w", scanID, err)
		}
	case job.KindApply:
		if _, err := tx.Exec(ctx, `
			UPDATE apply_run SET state = 'queued', started_at = NULL
			WHERE id = $1 AND state IN ('queued', 'running')`, applyID); err != nil {
			return fmt.Errorf("requeuing apply run %d: %w", applyID, err)
		}
	case job.KindApplyCheck:
		// A check changes its run only when it closes, so the run is still
		// checking.
	default:
		return fmt.Errorf("releasing job %d: unknown kind %q", jobID, kind)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing release of job %d: %w", jobID, err)
	}
	return nil
}

// FailScan marks the job and its scan failed, in one transaction. The reason
// is stored and shown as given, so it must be fixed wording and never text
// from an error. It returns job.ErrLeaseLost, changing nothing, when the
// worker does not hold the job.
func (s *Store) FailScan(ctx context.Context, jobID int64, workerID string, id scan.ID, reason string) error {
	return s.closeJob(ctx, jobID, workerID, id, "failed", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE scan SET status = 'failed', finished_at = now(), failure_reason = $2
			WHERE id = $1`, id, reason); err != nil {
			return fmt.Errorf("failing scan %d: %w", id, err)
		}
		return nil
	})
}

// closeJob moves the scan job the worker holds for scan id to state and
// then runs write, in one transaction. Nothing is written, and it returns
// job.ErrLeaseLost, when the worker does not hold the job; an error from
// write undoes everything.
func (s *Store) closeJob(ctx context.Context, jobID int64, workerID string, id scan.ID, state string, write func(pgx.Tx) error) error {
	return s.closeLeased(ctx, `
		UPDATE job SET state = $4
		WHERE id = $1 AND state = 'running' AND locked_by = $2 AND scan_id = $3`,
		jobID, workerID, int64(id), state, write)
}

// closeApplyJob is closeJob for an apply-check or apply job, which it finds
// by the ID of its apply run.
func (s *Store) closeApplyJob(ctx context.Context, jobID int64, workerID string, applyID int64, state string, write func(pgx.Tx) error) error {
	return s.closeLeased(ctx, `
		UPDATE job SET state = $4
		WHERE id = $1 AND state = 'running' AND locked_by = $2 AND apply_id = $3`,
		jobID, workerID, applyID, state, write)
}

// closeLeased runs the lease-checking update query, which takes the job ID,
// the worker ID, the ID of the job's record, and the new state, and then
// write, in one transaction. It returns job.ErrLeaseLost, writing nothing,
// when the query changes no row.
func (s *Store) closeLeased(ctx context.Context, query string, jobID int64, workerID string, recordID int64, state string, write func(pgx.Tx) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	tag, err := tx.Exec(ctx, query, jobID, workerID, recordID, state)
	if err != nil {
		return fmt.Errorf("closing job %d: %w", jobID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("closing job %d: %w", jobID, job.ErrLeaseLost)
	}
	if err := write(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing close of job %d: %w", jobID, err)
	}
	return nil
}
