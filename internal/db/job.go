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

// gaveUpReason is the failure reason of a scan whose job ran out of
// attempts.
const gaveUpReason = "worker stopped during the scan three times"

// The failure reasons of an apply run whose apply or check job ran out of
// attempts. The pages name them.
const (
	ApplyGaveUpReason = "worker stopped during the apply three times"
	CheckGaveUpReason = "worker stopped during the check three times"
)

// ClaimJob hands one job of the given kinds to the worker and leases it for
// lease; no kinds means every kind. The lease runs on the database clock. A
// job is claimable when it is queued or when its lease has run out. Jobs of
// the given kinds are handed out in the order they were made. It returns
// job.ErrNoJob when there is none.
//
// Jobs of every kind that have used their last attempt are given up, not
// only those of the given kinds: giving up a job only writes records.
//
// In the same transaction, a job whose lease has run out after its last
// attempt is marked failed together with its scan or apply run, so that a
// job that kills its worker does not run forever. The records an apply run
// wrote about its files are left as they are; the next check or apply of the
// project reconciles them with the disk.
func (s *Store) ClaimJob(ctx context.Context, workerID string, lease time.Duration, kinds ...job.Kind) (_ job.Job, err error) {
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
		maxJobAttempts, gaveUpReason, ApplyGaveUpReason, CheckGaveUpReason); err != nil {
		return job.Job{}, fmt.Errorf("failing exhausted jobs: %w", err)
	}

	var j job.Job
	err = tx.QueryRow(ctx, `
		UPDATE job SET state = 'running', attempts = attempts + 1,
			locked_by = $1, locked_until = now() + $2 * interval '1 second'
		WHERE id = (
			SELECT id FROM job
			WHERE attempts < $3 AND kind = ANY($4)
				AND (state = 'queued' OR (state = 'running' AND locked_until <= now()))
			ORDER BY id LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, coalesce(scan_id, 0), coalesce(apply_id, 0), attempts`,
		workerID, lease.Seconds(), maxJobAttempts, kindNames(kinds)).Scan(&j.ID, &j.Kind, &j.ScanID, &j.ApplyID, &j.Attempts)
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
// job.ErrClosed when the job is done or failed and the worker was the last
// to hold it, and job.ErrLeaseLost when the job is otherwise not running or
// is leased to another worker.
//
// A renewal that runs while the worker's own close of the job commits
// waits for that commit and then finds the job closed. The state is read
// again in a statement of its own because the update's snapshot was taken
// before that commit.
func (s *Store) ExtendJob(ctx context.Context, jobID int64, workerID string, lease time.Duration) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job SET locked_until = now() + $3 * interval '1 second'
		WHERE id = $1 AND state = 'running' AND locked_by = $2`,
		jobID, workerID, lease.Seconds())
	if err != nil {
		return fmt.Errorf("extending job %d: %w", jobID, err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var closed bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM job WHERE id = $1 AND locked_by = $2 AND state IN ('done', 'failed'))`,
		jobID, workerID).Scan(&closed); err != nil {
		return fmt.Errorf("reading state of job %d: %w", jobID, err)
	}
	if closed {
		return fmt.Errorf("extending job %d: %w", jobID, job.ErrClosed)
	}
	return fmt.Errorf("extending job %d: %w", jobID, job.ErrLeaseLost)
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
	return s.closeLeased(ctx, jobID, write, `
		UPDATE job SET state = $4
		WHERE id = $1 AND state = 'running' AND locked_by = $2 AND scan_id = $3`,
		jobID, workerID, int64(id), state)
}

// closeApplyJob is closeJob for an apply-check or apply job, which it finds
// by the ID of its apply run and its kind, so that a check cannot close as
// an apply or the reverse.
func (s *Store) closeApplyJob(ctx context.Context, jobID int64, workerID string, applyID int64, kind job.Kind, state string, write func(pgx.Tx) error) error {
	return s.closeLeased(ctx, jobID, write, `
		UPDATE job SET state = $4
		WHERE id = $1 AND state = 'running' AND locked_by = $2 AND apply_id = $3 AND kind = $5`,
		jobID, workerID, applyID, state, string(kind))
}

// closeLeased runs the lease-checking update query with args and then
// write, in one transaction. It returns job.ErrLeaseLost, writing nothing,
// when the query changes no row.
func (s *Store) closeLeased(ctx context.Context, jobID int64, write func(pgx.Tx) error, query string, args ...any) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	tag, err := tx.Exec(ctx, query, args...)
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

// Beat records that the worker is alive and asking for jobs of the given
// kinds; no kinds means every kind. It also removes the records of workers
// that have not been seen for a day, which are workers that no longer exist:
// every process picks a new ID.
func (s *Store) Beat(ctx context.Context, workerID string, kinds ...job.Kind) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)
	if _, err := tx.Exec(ctx, `
		INSERT INTO worker_beat (worker_id, seen_at, kinds) VALUES ($1, now(), $2)
		ON CONFLICT (worker_id) DO UPDATE SET seen_at = excluded.seen_at, kinds = excluded.kinds`,
		workerID, kindNames(kinds)); err != nil {
		return fmt.Errorf("recording beat of worker: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM worker_beat WHERE seen_at < now() - interval '1 day'`); err != nil {
		return fmt.Errorf("removing old beats: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing beat: %w", err)
	}
	return nil
}

// WorkerAlive reports whether a worker is serving the jobs of kind k: one
// that asks for that kind has asked for a job within the last within, or one
// holds a job of that kind under a lease that has not run out, which is a
// worker busy with a long job.
func (s *Store) WorkerAlive(ctx context.Context, k job.Kind, within time.Duration) (bool, error) {
	var alive bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM worker_beat
				WHERE seen_at > now() - $2 * interval '1 second' AND $1 = ANY(kinds))
			OR EXISTS (SELECT 1 FROM job WHERE kind = $1 AND state = 'running' AND locked_until > now())`,
		string(k), within.Seconds()).Scan(&alive)
	if err != nil {
		return false, fmt.Errorf("reading worker liveness: %w", err)
	}
	return alive, nil
}

// kindNames returns the names of kinds as the job table stores them, or the
// names of every kind when kinds is empty.
func kindNames(kinds []job.Kind) []string {
	if len(kinds) == 0 {
		kinds = job.Kinds()
	}
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return names
}
