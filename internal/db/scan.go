package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// ErrScanActive means the project already has a queued or running scan.
var ErrScanActive = errors.New("a scan of the project is already queued or running")

// foreignKeyViolation is the postgres error code for a foreign key
// violation.
const foreignKeyViolation = "23503"

// requestScanAttempts bounds how often RequestScan retries when the scan that
// blocked it finishes before it can be read.
const requestScanAttempts = 3

const scanColumns = `id, project_id, status, requested_by, requested_at, started_at, finished_at,
	commit_hash, rules_version, tool_versions, failure_reason, files, skipped, dropped, findings`

// RequestScan records a queued scan of the project and the job that runs it,
// in one transaction, and returns the scan ID. When the project already has
// a queued or running scan, it returns that scan's ID with ErrScanActive and
// stores nothing. It returns ErrNotFound when the project does not exist.
func (s *Store) RequestScan(ctx context.Context, projectID project.ID, requestedBy string) (scan.ID, error) {
	for range requestScanAttempts {
		id, err := s.insertScan(ctx, projectID, requestedBy)
		if !errors.Is(err, ErrScanActive) {
			return id, err
		}
		active, found, err := s.activeScan(ctx, projectID)
		if err != nil {
			return 0, err
		}
		if found {
			return active, fmt.Errorf("requesting scan of project %d: %w", projectID, ErrScanActive)
		}
	}
	return 0, fmt.Errorf("requesting scan of project %d: the active scan keeps changing", projectID)
}

// insertScan adds the scan and its job. It returns ErrScanActive, unwrapped,
// when the partial unique index refuses the scan.
func (s *Store) insertScan(ctx context.Context, projectID project.ID, requestedBy string) (_ scan.ID, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	var id scan.ID
	err = tx.QueryRow(ctx,
		"INSERT INTO scan (project_id, requested_by) VALUES ($1, $2) RETURNING id",
		projectID, requestedBy).Scan(&id)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == uniqueViolation:
		return 0, ErrScanActive
	case errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation:
		return 0, fmt.Errorf("requesting scan of project %d: %w", projectID, ErrNotFound)
	case err != nil:
		return 0, fmt.Errorf("creating scan: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO job (scan_id) VALUES ($1)", id); err != nil {
		return 0, fmt.Errorf("creating job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing scan request: %w", err)
	}
	return id, nil
}

// activeScan returns the ID of the queued or running scan of the project.
func (s *Store) activeScan(ctx context.Context, projectID project.ID) (id scan.ID, found bool, err error) {
	err = s.pool.QueryRow(ctx,
		"SELECT id FROM scan WHERE project_id = $1 AND status IN ('queued', 'running')",
		projectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading active scan of project %d: %w", projectID, err)
	}
	return id, true, nil
}

// rollback undoes tx unless it was committed. It is deferred right after the
// transaction starts and joins any rollback failure to *err.
func rollback(ctx context.Context, tx pgx.Tx, err *error) {
	// Rolling back after a commit returns pgx.ErrTxClosed, which is not a
	// problem.
	if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
		*err = errors.Join(*err, fmt.Errorf("rolling back: %w", rbErr))
	}
}

// Scan returns the scan with the given ID, or ErrNotFound.
func (s *Store) Scan(ctx context.Context, id scan.ID) (scan.Scan, error) {
	row := s.pool.QueryRow(ctx, "SELECT "+scanColumns+" FROM scan WHERE id = $1", id)
	sc, err := scanScan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return scan.Scan{}, fmt.Errorf("reading scan %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return scan.Scan{}, fmt.Errorf("reading scan %d: %w", id, err)
	}
	return sc, nil
}

// ScansByProject returns the scans of the project, newest first.
func (s *Store) ScansByProject(ctx context.Context, projectID project.ID) ([]scan.Scan, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+scanColumns+" FROM scan WHERE project_id = $1 ORDER BY id DESC", projectID)
	if err != nil {
		return nil, fmt.Errorf("listing scans of project %d: %w", projectID, err)
	}
	defer rows.Close()
	var out []scan.Scan
	for rows.Next() {
		sc, err := scanScan(rows)
		if err != nil {
			return nil, fmt.Errorf("reading scan row: %w", err)
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing scans of project %d: %w", projectID, err)
	}
	return out, nil
}

// StartScan marks the scan running and records the start time. It is safe to
// call again for a scan that is already running, as happens when a worker
// takes over a job. It returns ErrNotFound when the scan does not exist or
// has finished.
func (s *Store) StartScan(ctx context.Context, id scan.ID) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE scan SET status = 'running', started_at = now() WHERE id = $1 AND status IN ('queued', 'running')", id)
	if err != nil {
		return fmt.Errorf("starting scan %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("starting scan %d: %w", id, ErrNotFound)
	}
	return nil
}

func scanScan(row pgx.Row) (scan.Scan, error) {
	var (
		sc                scan.Scan
		started, finished *time.Time
		toolVersions      map[string]string
	)
	err := row.Scan(&sc.ID, &sc.ProjectID, &sc.Status, &sc.RequestedBy, &sc.RequestedAt, &started, &finished,
		&sc.CommitHash, &sc.RulesVersion, &toolVersions, &sc.FailureReason, &sc.Files, &sc.Skipped, &sc.Dropped, &sc.Findings)
	if err != nil {
		return scan.Scan{}, err
	}
	if started != nil {
		sc.StartedAt = *started
	}
	if finished != nil {
		sc.FinishedAt = *finished
	}
	if len(toolVersions) > 0 {
		sc.ToolVersions = toolVersions
	}
	return sc, nil
}

// LatestScan returns the project's newest succeeded scan, or ErrNotFound.
func (s *Store) LatestScan(ctx context.Context, projectID project.ID) (scan.Scan, error) {
	row := s.pool.QueryRow(ctx,
		"SELECT "+scanColumns+" FROM scan WHERE project_id = $1 AND status = 'succeeded' ORDER BY id DESC LIMIT 1", projectID)
	sc, err := scanScan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return scan.Scan{}, fmt.Errorf("reading latest scan of project %d: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return scan.Scan{}, fmt.Errorf("reading latest scan of project %d: %w", projectID, err)
	}
	return sc, nil
}
