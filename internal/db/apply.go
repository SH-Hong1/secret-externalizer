package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// ErrApplyActive means the project has an apply that is being checked, is
// queued or runs, which keeps scans, verdicts, moves and other applies of the
// project out.
var ErrApplyActive = errors.New("an apply of the project is being checked, queued or running")

// ErrPlanChanged means the plan a user confirmed is not the plan the check
// showed, or the plan changed since the check.
var ErrPlanChanged = errors.New("the verdicts changed since the check")

// The project's scans, verdicts, moves and applies exclude each other through
// a lock on its project row. RequestApplyCheck and ConfirmApply hold it FOR
// UPDATE; inserting a scan takes FOR KEY SHARE through the foreign key, and
// Decide and MoveFinding take FOR SHARE. Each then checks, in a statement
// that starts after the lock is granted, for what would exclude it, so it
// sees whatever the transaction it waited for committed.

// lockProject locks the project's row in tx with the given clause, "FOR
// UPDATE" or "FOR SHARE". It returns ErrNotFound when there is no project.
func lockProject(ctx context.Context, tx pgx.Tx, projectID project.ID, clause string) error {
	var one int
	err := tx.QueryRow(ctx, "SELECT 1 FROM project WHERE id = $1 "+clause, projectID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("locking project %d: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("locking project %d: %w", projectID, err)
	}
	return nil
}

// refuseActiveApply returns ErrApplyActive when the project has an apply
// that is being checked, is queued or runs.
func refuseActiveApply(ctx context.Context, tx pgx.Tx, projectID project.ID) error {
	var active bool
	if err := tx.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM apply_run WHERE project_id = $1 AND state IN ('checking', 'queued', 'running'))",
		projectID).Scan(&active); err != nil {
		return fmt.Errorf("reading active apply of project %d: %w", projectID, err)
	}
	if active {
		return fmt.Errorf("project %d: %w", projectID, ErrApplyActive)
	}
	return nil
}

// refuseActiveScan returns ErrScanActive when the project has a queued or
// running scan.
func refuseActiveScan(ctx context.Context, tx pgx.Tx, projectID project.ID) error {
	var active bool
	if err := tx.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM scan WHERE project_id = $1 AND status IN ('queued', 'running'))",
		projectID).Scan(&active); err != nil {
		return fmt.Errorf("reading active scan of project %d: %w", projectID, err)
	}
	if active {
		return fmt.Errorf("project %d: %w", projectID, ErrScanActive)
	}
	return nil
}

// latestScanID returns the ID of the project's newest succeeded scan, or
// ErrNotFound.
func latestScanID(ctx context.Context, tx pgx.Tx, projectID project.ID) (scan.ID, error) {
	var id scan.ID
	err := tx.QueryRow(ctx,
		"SELECT id FROM scan WHERE project_id = $1 AND status = 'succeeded' ORDER BY id DESC LIMIT 1", projectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("reading latest scan of project %d: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("reading latest scan of project %d: %w", projectID, err)
	}
	return id, nil
}

// RequestApplyCheck creates a run in state checking with the digest of the
// current plan and its apply-check job, and marks the project's earlier
// ready and blocked runs superseded. The current plan is the bundles to
// externalize or redact that the project's latest succeeded scan found,
// together with the customer data it found that is not judged a false
// positive.
// It returns ErrScanActive when the project has a queued or running scan,
// ErrApplyActive when it has an active apply, and ErrNotFound when there is
// no project or it has no succeeded scan.
func (s *Store) RequestApplyCheck(ctx context.Context, projectID project.ID, by audit.Actor) (_ apply.ID, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	if err := lockProject(ctx, tx, projectID, "FOR UPDATE"); err != nil {
		return 0, err
	}
	if err := refuseActiveScan(ctx, tx, projectID); err != nil {
		return 0, fmt.Errorf("requesting apply check: %w", err)
	}
	if err := refuseActiveApply(ctx, tx, projectID); err != nil {
		return 0, fmt.Errorf("requesting apply check: %w", err)
	}
	scanID, err := latestScanID(ctx, tx, projectID)
	if err != nil {
		return 0, fmt.Errorf("requesting apply check: %w", err)
	}
	bundles, err := applyBundles(ctx, tx, projectID, scanID)
	if err != nil {
		return 0, err
	}
	spans, err := customerSpans(ctx, tx, projectID, scanID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		"UPDATE apply_run SET state = 'superseded' WHERE project_id = $1 AND state IN ('ready', 'blocked')",
		projectID); err != nil {
		return 0, fmt.Errorf("superseding earlier apply runs: %w", err)
	}
	var id apply.ID
	err = tx.QueryRow(ctx, `
		INSERT INTO apply_run (project_id, scan_id, plan_digest, requested_by, requester_name)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		projectID, scanID, apply.Digest(scanID, bundles, spans), by.Subject, by.Name).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return 0, fmt.Errorf("requesting apply check of project %d: %w", projectID, ErrApplyActive)
	}
	if err != nil {
		return 0, fmt.Errorf("creating apply run: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO job (kind, apply_id) VALUES ('apply-check', $1)", id); err != nil {
		return 0, fmt.Errorf("creating apply-check job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing apply check request: %w", err)
	}
	return id, nil
}

// ConfirmApply moves a ready run to queued, creates its apply job, and
// records the audit event, in one transaction. allowIrreversible is the
// user's consent to redact customer data in the untracked files the check
// listed. It returns ErrPlanChanged when digest is not the run's or not the
// current plan's, or when any scan of the project, in any state, is newer
// than the checked one; ErrNotFound when the run is not ready; ErrScanActive when
// the project has a queued or running scan.
func (s *Store) ConfirmApply(ctx context.Context, id apply.ID, digest string, allowIrreversible bool, by audit.Actor) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	var projectID project.ID
	err = tx.QueryRow(ctx, "SELECT project_id FROM apply_run WHERE id = $1", id).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("confirming apply %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("reading apply run %d: %w", id, err)
	}
	if err := lockProject(ctx, tx, projectID, "FOR UPDATE"); err != nil {
		return err
	}
	var (
		state      apply.State
		scanID     scan.ID
		planDigest string
	)
	if err := tx.QueryRow(ctx, "SELECT state, scan_id, plan_digest FROM apply_run WHERE id = $1 FOR UPDATE", id).
		Scan(&state, &scanID, &planDigest); err != nil {
		return fmt.Errorf("reading apply run %d: %w", id, err)
	}
	if state != apply.StateReady {
		return fmt.Errorf("confirming apply %d in state %s: %w", id, state, ErrNotFound)
	}
	if err := refuseActiveScan(ctx, tx, projectID); err != nil {
		return fmt.Errorf("confirming apply %d: %w", id, err)
	}
	if digest != planDigest {
		return fmt.Errorf("confirming apply %d: %w", id, ErrPlanChanged)
	}
	// Any scan after the checked one, whatever its state, means the check
	// no longer describes what the worker will meet.
	var newer bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM scan WHERE project_id = $1 AND id > $2)",
		projectID, scanID).Scan(&newer); err != nil {
		return fmt.Errorf("reading scans after scan %d: %w", scanID, err)
	}
	if newer {
		return fmt.Errorf("confirming apply %d: a scan followed the check: %w", id, ErrPlanChanged)
	}
	latest, err := latestScanID(ctx, tx, projectID)
	if err != nil {
		return fmt.Errorf("confirming apply %d: %w", id, err)
	}
	bundles, err := applyBundles(ctx, tx, projectID, latest)
	if err != nil {
		return err
	}
	spans, err := customerSpans(ctx, tx, projectID, latest)
	if err != nil {
		return err
	}
	if apply.Digest(latest, bundles, spans) != planDigest {
		return fmt.Errorf("confirming apply %d: %w", id, ErrPlanChanged)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE apply_run SET state = 'queued', confirmed_at = now(), allow_irreversible = $2 WHERE id = $1`,
		id, allowIrreversible); err != nil {
		return fmt.Errorf("queueing apply run %d: %w", id, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO job (kind, apply_id) VALUES ('apply', $1)", id); err != nil {
		return fmt.Errorf("creating apply job: %w", err)
	}
	irreversible := "no"
	if allowIrreversible {
		irreversible = "yes"
	}
	if err := insertEvent(ctx, tx, audit.Event{
		Actor: by, Action: audit.ActionApply, ProjectID: projectID,
		Detail: map[string]string{
			audit.DetailApply:        strconv.FormatInt(int64(id), 10),
			audit.DetailIrreversible: irreversible,
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing apply confirmation: %w", err)
	}
	return nil
}

// customerSpans returns the distinct locations of customer data that scanID
// found in the project and whose bundle is not judged a false positive,
// ordered by file, start and end.
func customerSpans(ctx context.Context, tx pgx.Tx, projectID project.ID, scanID scan.ID) ([]apply.CustomerSpan, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT f.file_path, l.start_line, l.start_col, l.end_line, l.end_col
		FROM finding f
			JOIN location l ON l.finding_id = f.id
			JOIN bundle b ON b.id = f.bundle_id
		WHERE f.project_id = $1 AND l.scan_id = $2 AND f.category = 'customer-data'
			AND b.verdict <> 'false-positive'
		ORDER BY f.file_path, l.start_line, l.start_col, l.end_line, l.end_col`, projectID, scanID)
	if err != nil {
		return nil, fmt.Errorf("listing customer data spans of scan %d: %w", scanID, err)
	}
	spans, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (apply.CustomerSpan, error) {
		var c apply.CustomerSpan
		err := row.Scan(&c.File, &c.StartLine, &c.StartCol, &c.EndLine, &c.EndCol)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing customer data spans of scan %d: %w", scanID, err)
	}
	return spans, nil
}

// applyBundles returns the project's bundles whose verdict is externalize or
// redact and which have a member scanID found, in ID order, each in the shape
// Bundle gives: the members scanID found ordered by file, each with the
// locations of that scan ordered by line and column.
func applyBundles(ctx context.Context, tx pgx.Tx, projectID project.ID, scanID scan.ID) ([]triage.Bundle, error) {
	rows, err := tx.Query(ctx, `
		SELECT b.id, b.fingerprint, b.verdict, b.hold_reason, b.name, b.note, b.decided_by, b.decided_at,
			b.applied_at, b.store_path,
			f.id, f.file_path, f.category, f.kind, f.preview, f.has_original,
			`+locationColumns+`
		FROM bundle b
			JOIN finding f ON f.bundle_id = b.id
			JOIN location l ON l.finding_id = f.id
		WHERE b.project_id = $1 AND l.scan_id = $2 AND b.verdict IN ('externalize', 'redact')
		ORDER BY b.id, f.file_path, f.id, l.start_line, l.start_col, l.id`, projectID, scanID)
	if err != nil {
		return nil, fmt.Errorf("listing bundles to apply: %w", err)
	}
	defer rows.Close()
	var (
		out    []triage.Bundle
		lowest finding.ID
	)
	for rows.Next() {
		var (
			b                    triage.Bundle
			name                 *string
			decidedAt, appliedAt *time.Time
			m                    triage.Member
			c                    finding.Category
			k                    finding.Kind
			preview              string
		)
		l, err := scanLocation(rows,
			&b.ID, &b.Fingerprint, &b.Decision.Verdict, &b.Decision.Reason, &name, &b.Decision.Note, &b.DecidedBy, &decidedAt,
			&appliedAt, &b.StorePath,
			&m.FindingID, &m.File, &c, &k, &preview, &m.HasOriginal)
		if err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != b.ID {
			b.ProjectID = projectID
			if name != nil {
				b.Decision.Name = triage.Name(*name)
			}
			if decidedAt != nil {
				b.DecidedAt = *decidedAt
			}
			if appliedAt != nil {
				b.AppliedAt = *appliedAt
			}
			out = append(out, b)
			lowest = 0
		}
		cur := &out[len(out)-1]
		if lowest == 0 || m.FindingID < lowest {
			lowest = m.FindingID
			cur.Category, cur.Kind, cur.Preview = c, k, preview
		}
		if n := len(cur.Members); n > 0 && cur.Members[n-1].FindingID == m.FindingID {
			cur.Members[n-1].Locations = append(cur.Members[n-1].Locations, l)
			continue
		}
		m.Locations = []finding.Location{l}
		cur.Members = append(cur.Members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing bundles to apply: %w", err)
	}
	return out, nil
}

const applyRunColumns = `id, project_id, scan_id, state, plan_digest, requested_by, requester_name, requested_at,
	checked_at, confirmed_at, started_at, finished_at, allow_irreversible, irreversible_files, blockers,
	dirty_files, dirty_count, failure_reason, applied, skipped, failed, rescan_id`

func scanApplyRun(row pgx.Row) (apply.Run, error) {
	var (
		r                                     apply.Run
		checked, confirmed, started, finished *time.Time
		irreversible, blockers, dirty         []string
		rescan                                *int64
	)
	err := row.Scan(&r.ID, &r.ProjectID, &r.ScanID, &r.State, &r.PlanDigest, &r.Requester.Subject, &r.Requester.Name,
		&r.RequestedAt, &checked, &confirmed, &started, &finished, &r.AllowIrreversible, &irreversible, &blockers,
		&dirty, &r.DirtyCount, &r.FailureReason, &r.Applied, &r.Skipped, &r.Failed, &rescan)
	if err != nil {
		return apply.Run{}, err
	}
	r.CheckedAt, r.ConfirmedAt, r.StartedAt, r.FinishedAt = timeOf(checked), timeOf(confirmed), timeOf(started), timeOf(finished)
	if len(irreversible) > 0 {
		r.IrreversibleFiles = irreversible
	}
	for _, b := range blockers {
		r.Blockers = append(r.Blockers, apply.Blocker(b))
	}
	if len(dirty) > 0 {
		r.DirtyFiles = dirty
	}
	if rescan != nil {
		r.RescanID = scan.ID(*rescan)
	}
	return r, nil
}

// timeOf returns *t, or the zero time when t is nil.
func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ApplyRun returns the apply run with the given ID, or ErrNotFound. Lists
// that are empty are nil.
func (s *Store) ApplyRun(ctx context.Context, id apply.ID) (apply.Run, error) {
	r, err := scanApplyRun(s.pool.QueryRow(ctx, "SELECT "+applyRunColumns+" FROM apply_run WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return apply.Run{}, fmt.Errorf("reading apply run %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return apply.Run{}, fmt.Errorf("reading apply run %d: %w", id, err)
	}
	return r, nil
}

// ApplyRunsByProject returns the apply runs of the project, newest first.
func (s *Store) ApplyRunsByProject(ctx context.Context, projectID project.ID) ([]apply.Run, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+applyRunColumns+" FROM apply_run WHERE project_id = $1 ORDER BY id DESC", projectID)
	if err != nil {
		return nil, fmt.Errorf("listing apply runs of project %d: %w", projectID, err)
	}
	defer rows.Close()
	var out []apply.Run
	for rows.Next() {
		r, err := scanApplyRun(rows)
		if err != nil {
			return nil, fmt.Errorf("reading apply run row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing apply runs of project %d: %w", projectID, err)
	}
	return out, nil
}

// ApplyItems returns the outcomes of the apply run with the given ID,
// ordered by file, line and column. It returns ErrNotFound when there is no
// such run.
func (s *Store) ApplyItems(ctx context.Context, id apply.ID) ([]apply.Item, error) {
	var out []apply.Item
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM apply_run WHERE id = $1)", id).Scan(&exists); err != nil {
			return fmt.Errorf("reading apply run %d: %w", id, err)
		}
		if !exists {
			return fmt.Errorf("reading items of apply run %d: %w", id, ErrNotFound)
		}
		rows, err := tx.Query(ctx, `
			SELECT `+itemColumns+` FROM apply_item WHERE apply_id = $1
			ORDER BY file_path, start_line, start_col, id`, id)
		if err != nil {
			return fmt.Errorf("listing items of apply run %d: %w", id, err)
		}
		defer rows.Close()
		for rows.Next() {
			it, err := scanItem(rows)
			if err != nil {
				return fmt.Errorf("reading apply item row: %w", err)
			}
			out = append(out, it)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listing items of apply run %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
