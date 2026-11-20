package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// The records an apply keeps about its files follow one rule: before a file
// changes, BeginFile writes the change it is about to make (the file's hash
// before and after, the temporary file it goes through) and marks the file's
// items writing; after the change, EndFile marks the record written, the
// items applied and their bundles applied. A record that is still open when
// a run stops is settled by the next check or apply of the project through
// OpenFiles and ResolveFile.

// ErrItemSettled means an item given to BeginFile is already applied, or
// failed for a reason other than an interruption, so the file must not
// change for it.
var ErrItemSettled = errors.New("an item of the file is already applied or failed")

// ErrSuperseded means the record of a file was announced again, with
// another hash or temporary file, since the change being confirmed was
// announced.
var ErrSuperseded = errors.New("the file was announced again")

const itemColumns = `bundle_id, finding_id, file_path, start_line, start_col, line, action, category, kind, name,
	store_path, store_version, tracked, code_change, status, reason`

func scanItem(row pgx.Row) (apply.Item, error) {
	var it apply.Item
	err := row.Scan(&it.BundleID, &it.FindingID, &it.File, &it.StartLine, &it.StartCol, &it.Line, &it.Action,
		&it.Category, &it.Kind, &it.Name, &it.StorePath, &it.StoreVersion, &it.Tracked, &it.CodeChange,
		&it.Status, &it.Reason)
	return it, err
}

// The rows of apply_item that a later write may replace: replaceByCheck for
// a check, which starts from an empty list; replaceByBegin for BeginFile,
// which may also reopen an item an earlier attempt of the run was
// interrupted on before its file changed; replaceByFinish for FinishApply,
// which never replaces an item that is writing or applied, or failed for a
// reason other than an interruption: an interrupted item's file did not
// change, so the outcome the attempt that finishes the run found for it
// stands.
const (
	replaceByCheck  = "false"
	replaceByBegin  = "apply_item.status IN ('planned', 'skipped', 'writing') OR (apply_item.status = 'failed' AND apply_item.reason = 'interrupted')"
	replaceByFinish = "apply_item.status IN ('planned', 'skipped') OR (apply_item.status = 'failed' AND apply_item.reason = 'interrupted')"
)

// upsertItems stores items as rows of the run. An item whose location the
// run already has replaces that row only when the row meets replaceable, a
// condition from the constants above; status, when not empty, is stored in
// place of each item's. With strict, an item that could not be stored is
// ErrItemSettled.
func upsertItems(ctx context.Context, tx pgx.Tx, id apply.ID, items []apply.Item, status apply.Status, replaceable string, strict bool) error {
	for _, it := range items {
		if status != "" {
			it.Status, it.Reason = status, ""
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO apply_item (apply_id, `+itemColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (apply_id, finding_id, start_line, start_col) DO UPDATE SET
				bundle_id = excluded.bundle_id, file_path = excluded.file_path, line = excluded.line,
				action = excluded.action, category = excluded.category, kind = excluded.kind, name = excluded.name,
				store_path = excluded.store_path, store_version = excluded.store_version, tracked = excluded.tracked,
				code_change = excluded.code_change, status = excluded.status, reason = excluded.reason
			WHERE `+replaceable,
			id, it.BundleID, it.FindingID, it.File, it.StartLine, it.StartCol, it.Line, it.Action, it.Category, it.Kind,
			it.Name, it.StorePath, it.StoreVersion, it.Tracked, it.CodeChange, it.Status, it.Reason)
		if err != nil {
			return fmt.Errorf("storing item of apply %d: %w", id, err)
		}
		if strict && tag.RowsAffected() == 0 {
			return fmt.Errorf("storing item of finding %d in apply %d: %w", it.FindingID, id, ErrItemSettled)
		}
	}
	return nil
}

// ApplyInput returns what the worker needs to plan the run with the given
// ID, read from one snapshot. It returns ErrNotFound when there is no such
// run.
func (s *Store) ApplyInput(ctx context.Context, id apply.ID) (apply.Input, error) {
	var in apply.Input
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		r, err := scanApplyRun(tx.QueryRow(ctx, "SELECT "+applyRunColumns+" FROM apply_run WHERE id = $1", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reading apply run %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading apply run %d: %w", id, err)
		}
		in.Run = r
		if in.Project, err = scanProject(tx.QueryRow(ctx,
			"SELECT "+projectColumns+" FROM project WHERE id = $1", r.ProjectID)); err != nil {
			return fmt.Errorf("reading project %d: %w", r.ProjectID, err)
		}
		if in.Scan, err = scanScan(tx.QueryRow(ctx, "SELECT "+scanColumns+" FROM scan WHERE id = $1", r.ScanID)); err != nil {
			return fmt.Errorf("reading scan %d: %w", r.ScanID, err)
		}
		if err := tx.QueryRow(ctx, "SELECT max(id) FROM scan WHERE project_id = $1", r.ProjectID).
			Scan(&in.NewestScan); err != nil {
			return fmt.Errorf("reading newest scan of project %d: %w", r.ProjectID, err)
		}
		if in.Bundles, err = applyBundles(ctx, tx, r.ProjectID, r.ScanID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT f.file_path FROM finding f JOIN location l ON l.finding_id = f.id
			WHERE f.project_id = $1 AND l.scan_id = $2 AND f.category = 'customer-data'
			ORDER BY f.file_path`, r.ProjectID, r.ScanID)
		if err != nil {
			return fmt.Errorf("listing customer data files of scan %d: %w", r.ScanID, err)
		}
		if in.CustomerDataFiles, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return fmt.Errorf("listing customer data files of scan %d: %w", r.ScanID, err)
		}
		if in.CustomerDataSpans, err = customerSpans(ctx, tx, r.ProjectID, r.ScanID); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `
			SELECT `+itemColumns+` FROM apply_item
			WHERE apply_id IN (SELECT id FROM apply_run WHERE project_id = $1)
				AND status = 'applied' AND NOT in_manifest AND action IN ('replace', 'remove-file')
				AND category <> 'customer-data'
			ORDER BY apply_id, file_path, start_line, start_col, id`, r.ProjectID)
		if err != nil {
			return fmt.Errorf("listing unlisted items of project %d: %w", r.ProjectID, err)
		}
		if in.Unlisted, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (apply.Item, error) {
			return scanItem(row)
		}); err != nil {
			return fmt.Errorf("listing unlisted items of project %d: %w", r.ProjectID, err)
		}
		return nil
	})
	if err != nil {
		return apply.Input{}, err
	}
	return in, nil
}

// StartApply records that a job of kind phase begins work on the run. A
// check leaves its run checking; an apply moves a queued or running run to
// running with a new start time. It returns ErrNotFound when the run is not
// in a state the phase works on.
func (s *Store) StartApply(ctx context.Context, id apply.ID, phase job.Kind) error {
	var query string
	switch phase {
	case job.KindApplyCheck:
		query = "SELECT 1 FROM apply_run WHERE id = $1 AND state = 'checking'"
	case job.KindApply:
		query = `UPDATE apply_run SET state = 'running', started_at = now()
			WHERE id = $1 AND state IN ('queued', 'running') RETURNING 1`
	case job.KindScan:
		return fmt.Errorf("starting apply %d: a scan is not a phase of an apply", id)
	default:
		return fmt.Errorf("starting apply %d: unknown phase %q", id, phase)
	}
	var one int
	err := s.pool.QueryRow(ctx, query, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("starting %s of apply %d: %w", phase, id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("starting %s of apply %d: %w", phase, id, err)
	}
	return nil
}

// OpenFiles returns the project's apply_file rows that are not written,
// of every run, ordered by run and file.
func (s *Store) OpenFiles(ctx context.Context, projectID project.ID) ([]apply.OpenFile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.apply_id, f.file_path, f.after_hash, f.temp_name
		FROM apply_file f JOIN apply_run r ON r.id = f.apply_id
		WHERE r.project_id = $1 AND NOT f.written
		ORDER BY f.apply_id, f.file_path`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing open files of project %d: %w", projectID, err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (apply.OpenFile, error) {
		var f apply.OpenFile
		err := row.Scan(&f.Run, &f.File, &f.AfterHash, &f.TempName)
		return f, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing open files of project %d: %w", projectID, err)
	}
	return out, nil
}

// ResolveFile closes an open file. done marks it written, its writing
// items applied and their bundles applied; otherwise the items become
// failed with reason interrupted and the record is removed, or, when it
// announced again a change the run had already written, goes back to that
// written change. A record that is already closed, or that was announced
// again with another hash or temporary file since f was read, is left as
// it is.
func (s *Store) ResolveFile(ctx context.Context, f apply.OpenFile, done bool) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	if done {
		if _, err := markWritten(ctx, tx, f.Run, f.File, f.AfterHash, f.TempName); err != nil {
			return err
		}
	} else {
		closed, err := undoAnnouncement(ctx, tx, f)
		if err != nil {
			return err
		}
		if closed {
			if _, err := tx.Exec(ctx, `
				UPDATE apply_item SET status = 'failed', reason = 'interrupted'
				WHERE apply_id = $1 AND file_path = $2 AND status = 'writing'`, f.Run, f.File); err != nil {
				return fmt.Errorf("failing interrupted items of apply %d: %w", f.Run, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing file record of apply %d: %w", f.Run, err)
	}
	return nil
}

// undoAnnouncement closes the open record f that did not happen: a record
// that announced again a change the run had written goes back to that
// change, any other is removed. It reports whether there was such an open
// record.
func undoAnnouncement(ctx context.Context, tx pgx.Tx, f apply.OpenFile) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE apply_file SET written = true, after_hash = prev_after_hash, prev_after_hash = NULL, temp_name = ''
		WHERE apply_id = $1 AND file_path = $2 AND NOT written AND after_hash = $3 AND temp_name = $4
			AND prev_after_hash IS NOT NULL`,
		f.Run, f.File, f.AfterHash, f.TempName)
	if err != nil {
		return false, fmt.Errorf("restoring file record of apply %d: %w", f.Run, err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	tag, err = tx.Exec(ctx, `
		DELETE FROM apply_file
		WHERE apply_id = $1 AND file_path = $2 AND NOT written AND after_hash = $3 AND temp_name = $4`,
		f.Run, f.File, f.AfterHash, f.TempName)
	if err != nil {
		return false, fmt.Errorf("removing file record of apply %d: %w", f.Run, err)
	}
	return tag.RowsAffected() > 0, nil
}

// markWritten marks the open record of file in run id that announced
// afterHash through tempName written, the file's writing items applied, and
// the bundles of the project's items that stored a value applied with their
// store path. It reports whether there was such an open record.
func markWritten(ctx context.Context, tx pgx.Tx, id apply.ID, file, afterHash, tempName string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE apply_file SET written = true, prev_after_hash = NULL
		WHERE apply_id = $1 AND file_path = $2 AND NOT written AND after_hash = $3 AND temp_name = $4`,
		id, file, afterHash, tempName)
	if err != nil {
		return false, fmt.Errorf("marking file of apply %d written: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		WITH done AS (
			UPDATE apply_item SET status = 'applied'
			WHERE apply_id = $1 AND file_path = $2 AND status = 'writing'
			RETURNING bundle_id, action, store_path
		)
		UPDATE bundle b SET applied_at = coalesce(b.applied_at, now()), store_path = d.store_path
		FROM done d
		WHERE b.id = d.bundle_id AND d.action IN ('replace', 'remove-file') AND d.store_path <> ''
			AND b.project_id = (SELECT project_id FROM apply_run WHERE id = $1)`,
		id, file); err != nil {
		return false, fmt.Errorf("marking items of apply %d applied: %w", id, err)
	}
	return true, nil
}

// WrittenFiles returns, by file, every change the project's applies made
// and confirmed, in the order of the runs.
func (s *Store) WrittenFiles(ctx context.Context, projectID project.ID) (map[string][]apply.Written, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.file_path, f.after_hash
		FROM apply_file f JOIN apply_run r ON r.id = f.apply_id
		WHERE r.project_id = $1 AND f.written
		ORDER BY f.apply_id, f.file_path`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing written files of project %d: %w", projectID, err)
	}
	defer rows.Close()
	out := map[string][]apply.Written{}
	for rows.Next() {
		var (
			file string
			w    apply.Written
		)
		if err := rows.Scan(&file, &w.AfterHash); err != nil {
			return nil, fmt.Errorf("reading written file row: %w", err)
		}
		out[file] = append(out[file], w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing written files of project %d: %w", projectID, err)
	}
	return out, nil
}

// SaveCheck closes the apply-check job the worker holds for run id and
// stores what the check found, in one transaction: the run becomes ready,
// or blocked when there are blockers, with the check time, the count of
// dirty and its first apply.MaxDirtyShown paths, and irreversible as the
// files the confirmation covers; items, each planned or skipped, replace the
// run's items. The file list is written only by the statement that moves a
// checking run on, so a run that has left checking never changes it. It
// returns job.ErrLeaseLost, saving nothing, when the worker does not hold
// the job, and ErrNotFound when the run is not checking.
func (s *Store) SaveCheck(ctx context.Context, jobID int64, workerID string, id apply.ID, blockers []apply.Blocker,
	dirty, irreversible []string, items []apply.Item,
) error {
	type location struct {
		finding             finding.ID
		startLine, startCol int
	}
	seen := make(map[location]bool, len(items))
	for _, it := range items {
		if it.Status != apply.StatusPlanned && it.Status != apply.StatusSkipped {
			return fmt.Errorf("saving check of apply %d: item status %q is neither planned nor skipped", id, it.Status)
		}
		l := location{it.FindingID, it.StartLine, it.StartCol}
		if seen[l] {
			return fmt.Errorf("saving check of apply %d: two items of finding %d at %d:%d", id, it.FindingID, it.StartLine, it.StartCol)
		}
		seen[l] = true
	}
	state := apply.StateReady
	if len(blockers) > 0 {
		state = apply.StateBlocked
	}
	codes := make([]string, 0, len(blockers))
	for _, b := range blockers {
		codes = append(codes, string(b))
	}
	shown := dirty[:min(len(dirty), apply.MaxDirtyShown)]
	return s.closeApplyJob(ctx, jobID, workerID, int64(id), job.KindApplyCheck, "done", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apply_run SET state = $2, checked_at = now(), blockers = $3, dirty_count = $4, dirty_files = $5,
				irreversible_files = $6
			WHERE id = $1 AND state = 'checking'`,
			id, state, codes, len(dirty), nonNil(shown), nonNil(irreversible))
		if err != nil {
			return fmt.Errorf("saving check of apply %d: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("saving check of apply %d: %w", id, ErrNotFound)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM apply_item WHERE apply_id = $1", id); err != nil {
			return fmt.Errorf("clearing items of apply %d: %w", id, err)
		}
		return upsertItems(ctx, tx, id, items, "", replaceByCheck, false)
	})
}

// nonNil returns list, or an empty list for nil, for a NOT NULL array.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// BeginFile writes down, before the change, that run id is about to change
// file from beforeHash to afterHash through the temporary file tempName,
// and marks items, which must all be in file, writing, in one transaction.
// beforeHash is empty for a file the run creates; afterHash and tempName are
// empty for a removal. Announcing the same file again replaces the record;
// when the run had already written a change of the file, the record keeps
// that change's hash, and ResolveFile goes back to it if the new change does
// not happen. An item an earlier attempt of the run was interrupted on is
// reopened. It returns ErrItemSettled, writing nothing, when an item is
// already applied or failed for another reason, since the file must not
// change unless every item it is changed for is writing; and ErrNotFound
// when the run is not running.
func (s *Store) BeginFile(ctx context.Context, id apply.ID, file, beforeHash, afterHash, tempName string, items []apply.Item) (err error) {
	for _, it := range items {
		if it.File != file {
			return fmt.Errorf("announcing file of apply %d: an item of another file", id)
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	var running bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM apply_run WHERE id = $1 AND state = 'running')", id).
		Scan(&running); err != nil {
		return fmt.Errorf("reading apply run %d: %w", id, err)
	}
	if !running {
		return fmt.Errorf("announcing file of apply %d: %w", id, ErrNotFound)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO apply_file (apply_id, file_path, before_hash, after_hash, temp_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (apply_id, file_path) DO UPDATE SET
			before_hash = excluded.before_hash, after_hash = excluded.after_hash, temp_name = excluded.temp_name,
			written = false,
			prev_after_hash = CASE WHEN apply_file.written THEN apply_file.after_hash ELSE apply_file.prev_after_hash END`,
		id, file, beforeHash, afterHash, tempName); err != nil {
		return fmt.Errorf("announcing file of apply %d: %w", id, err)
	}
	if err := upsertItems(ctx, tx, id, items, apply.StatusWriting, replaceByBegin, true); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing file announcement of apply %d: %w", id, err)
	}
	return nil
}

// EndFile records, after the change, that run id changed file as announced
// with afterHash through tempName, the values given to BeginFile: the
// record is written, the file's writing items applied, and their bundles
// applied with their store path, in one transaction. Calling it again for
// the same announcement does nothing. It returns ErrSuperseded, changing
// nothing, when the file's record now holds another announcement, and
// ErrNotFound when the run announced no change of file.
func (s *Store) EndFile(ctx context.Context, id apply.ID, file, afterHash, tempName string) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	marked, err := markWritten(ctx, tx, id, file, afterHash, tempName)
	if err != nil {
		return err
	}
	if !marked {
		var same bool
		err := tx.QueryRow(ctx, `
			SELECT written AND after_hash = $3 AND temp_name = $4 FROM apply_file WHERE apply_id = $1 AND file_path = $2`,
			id, file, afterHash, tempName).Scan(&same)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("confirming file of apply %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading file record of apply %d: %w", id, err)
		}
		if !same {
			return fmt.Errorf("confirming file of apply %d: %w", id, ErrSuperseded)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing file confirmation of apply %d: %w", id, err)
	}
	return nil
}

// MarkInManifest records that every applied secret item of the project's
// runs is in the manifest.
func (s *Store) MarkInManifest(ctx context.Context, projectID project.ID) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE apply_item SET in_manifest = true
		WHERE apply_id IN (SELECT id FROM apply_run WHERE project_id = $1)
			AND status = 'applied' AND NOT in_manifest AND action IN ('replace', 'remove-file')
			AND category <> 'customer-data'`, projectID); err != nil {
		return fmt.Errorf("marking items of project %d in the manifest: %w", projectID, err)
	}
	return nil
}

// SettledFingerprints returns the fingerprints whose bundles in the
// project all have the verdict false-positive, in ascending order.
func (s *Store) SettledFingerprints(ctx context.Context, projectID project.ID) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT fingerprint FROM bundle WHERE project_id = $1
		GROUP BY fingerprint HAVING bool_and(verdict = 'false-positive')
		ORDER BY fingerprint`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing settled fingerprints of project %d: %w", projectID, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("listing settled fingerprints of project %d: %w", projectID, err)
	}
	return out, nil
}

// finishRun moves run id from one of the states in from to state with the
// finish time, the reason and blockers given, and the counts of its items.
func finishRun(ctx context.Context, tx pgx.Tx, id apply.ID, from []string, state apply.State, reason string, blockers []apply.Blocker) error {
	codes := make([]string, 0, len(blockers))
	for _, b := range blockers {
		codes = append(codes, string(b))
	}
	tag, err := tx.Exec(ctx, `
		UPDATE apply_run r SET state = $3, finished_at = now(), failure_reason = $4, blockers = $5,
			applied = c.applied, skipped = c.skipped, failed = c.failed
		FROM (SELECT count(*) FILTER (WHERE status = 'applied') AS applied,
				count(*) FILTER (WHERE status = 'skipped') AS skipped,
				count(*) FILTER (WHERE status = 'failed') AS failed
			FROM apply_item WHERE apply_id = $1) c
		WHERE r.id = $1 AND r.state = ANY($2)`,
		id, from, state, reason, codes)
	if err != nil {
		return fmt.Errorf("closing apply %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("closing apply %d: %w", id, ErrNotFound)
	}
	return nil
}

// FinishApply closes the apply job the worker holds for run id, in one
// transaction: items are merged into the run's items, never replacing one
// that is writing or applied, or failed for a reason other than an
// interruption; the run becomes succeeded with its
// finish time and counts; the job is done. It returns job.ErrLeaseLost,
// saving nothing, when the worker does not hold the job, and ErrNotFound
// when the run is not running.
func (s *Store) FinishApply(ctx context.Context, jobID int64, workerID string, id apply.ID, items []apply.Item) error {
	return s.closeApplyJob(ctx, jobID, workerID, int64(id), job.KindApply, "done", func(tx pgx.Tx) error {
		if err := upsertItems(ctx, tx, id, items, "", replaceByFinish, false); err != nil {
			return err
		}
		return finishRun(ctx, tx, id, []string{"running"}, apply.StateSucceeded, "", nil)
	})
}

// FailApply closes the job the worker holds for run id as failed, in one
// transaction, and the run with it: a checking run's apply-check job, or a
// queued or running run's apply job. The run keeps reason, which must be
// one of the fixed failure reasons (knownFailureReason), and blockers. Its
// items and file records stay as they are. It returns job.ErrLeaseLost, changing nothing,
// when the worker does not hold the job, and ErrNotFound when the run is in
// no state a job works on.
func (s *Store) FailApply(ctx context.Context, jobID int64, workerID string, id apply.ID, reason string, blockers []apply.Blocker) error {
	if !knownFailureReason(reason) {
		return fmt.Errorf("failing apply %d: the reason is not one of the fixed failure reasons", id)
	}
	var state apply.State
	err := s.pool.QueryRow(ctx, "SELECT state FROM apply_run WHERE id = $1", id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failing apply %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("reading apply run %d: %w", id, err)
	}
	var (
		kind job.Kind
		from []string
	)
	switch state {
	case apply.StateChecking:
		kind, from = job.KindApplyCheck, []string{"checking"}
	case apply.StateQueued, apply.StateRunning:
		kind, from = job.KindApply, []string{"queued", "running"}
	case apply.StateReady, apply.StateBlocked, apply.StateSucceeded, apply.StateFailed, apply.StateSuperseded:
		return fmt.Errorf("failing apply %d in state %s: %w", id, state, ErrNotFound)
	default:
		return fmt.Errorf("failing apply %d: unknown state %q", id, state)
	}
	return s.closeApplyJob(ctx, jobID, workerID, int64(id), kind, "failed", func(tx pgx.Tx) error {
		return finishRun(ctx, tx, id, from, apply.StateFailed, reason, blockers)
	})
}

// knownFailureReason reports whether reason is one of the fixed reasons a
// check or an apply fails with, the only ones FailApply stores.
func knownFailureReason(reason string) bool {
	switch reason {
	case apply.FailureDirty,
		apply.FailureNotRepository,
		apply.FailureUnsafeConfig,
		apply.FailureManifest,
		apply.FailurePlanChanged,
		apply.FailureSecretStore,
		apply.FailureTimeLimit,
		apply.FailureOther,
		apply.FailureManifestWrite:
		return true
	}
	return false
}
