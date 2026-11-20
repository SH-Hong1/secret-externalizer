package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// runTime is the time of the apply run r: its start, or its finish when it
// has no start time, which happens when a worker gave the run back and it
// closed without starting again. It is NULL for a run that has neither.
const runTime = `coalesce(r.started_at, r.finished_at)`

// runBefore is true when the apply run r counts as before the scan sc: its
// time is before sc was requested, or sc is the scan that confirms it. A run
// that closed without a start time requests its confirmation scan in the
// transaction that closes it, so the two times are equal.
const runBefore = `(coalesce(` + runTime + ` < sc.requested_at, false) OR r.id IS NOT DISTINCT FROM sc.apply_id)`

// runAfter is true when the apply run r has a time and does not count as
// before the scan sc.
const runAfter = `(` + runTime + ` IS NOT NULL AND NOT ` + runBefore + `)`

// reportItems is the FROM and WHERE of the applied apply items of project
// $1 in runs before scan $2, as i, with their run r.
const reportItems = `
	FROM apply_item i JOIN apply_run r ON r.id = i.apply_id
		CROSS JOIN (SELECT requested_at, apply_id FROM scan WHERE id = $2) sc
	WHERE r.project_id = $1 AND i.status = 'applied' AND ` + runBefore

// reportFound starts a query with common table expressions for project $1
// and scan $2, of which member and found are for the query to read. member is one row per finding the scan
// found. found is one row per bundle with such a member, as scanBundles
// has it: category and kind are those of the member with the lowest ID,
// file and first_line the first file and the first line in it, files the
// files of its members and locations the number of locations; before and
// after say whether an applied item of the bundle is in a run before the
// scan, or in a run after it. It reads no preview.
const reportFound = `
	WITH sc AS (
		SELECT requested_at, apply_id FROM scan WHERE id = $2
	), member AS (
		SELECT f.id, f.bundle_id, f.file_path, f.category, f.kind,
			min(l.start_line) AS first_line, count(*) AS locations
		FROM finding f JOIN location l ON l.finding_id = f.id
		WHERE f.project_id = $1 AND l.scan_id = $2
		GROUP BY f.id
	), run_mark AS (
		SELECT i.bundle_id, bool_or(` + runBefore + `) AS before, bool_or(` + runAfter + `) AS after
		FROM apply_item i JOIN apply_run r ON r.id = i.apply_id CROSS JOIN sc
		WHERE r.project_id = $1 AND i.status = 'applied'
		GROUP BY i.bundle_id
	), grouped AS (
		SELECT b.id, b.verdict, b.hold_reason, b.note, b.name, b.decided_at,
			(array_agg(m.category ORDER BY m.id))[1] AS category,
			(array_agg(m.kind ORDER BY m.id))[1] AS kind,
			(array_agg(m.file_path ORDER BY m.file_path, m.first_line))[1] AS file,
			(array_agg(m.first_line ORDER BY m.file_path, m.first_line))[1] AS first_line,
			array_agg(DISTINCT m.file_path) AS files,
			sum(m.locations)::int AS locations
		FROM bundle b JOIN member m ON m.bundle_id = b.id
		WHERE b.project_id = $1
		GROUP BY b.id
	), found AS (
		SELECT g.*, coalesce(a.before, false) AS before, coalesce(a.after, false) AS after
		FROM grouped g LEFT JOIN run_mark a ON a.bundle_id = g.id
	)`

// Report returns what the database tells about the scan with the given ID
// for its report, read in one snapshot. It fails with an error matching
// ErrNotFound when the scan is unknown. For a scan that has not succeeded
// it returns only Project, Scan and Latest.
func (s *Store) Report(ctx context.Context, id scan.ID) (report.Data, error) {
	var d report.Data
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		sc, err := scanScan(tx.QueryRow(ctx, "SELECT "+scanColumns+" FROM scan WHERE id = $1", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reading scan %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading scan %d: %w", id, err)
		}
		d.Scan = sc
		d.Project, err = scanProject(tx.QueryRow(ctx, "SELECT "+projectColumns+" FROM project WHERE id = $1", sc.ProjectID))
		if err != nil {
			return fmt.Errorf("reading project %d: %w", sc.ProjectID, err)
		}
		var previous scan.ID
		if err := tx.QueryRow(ctx, `
			SELECT
				coalesce((SELECT max(id) FROM scan WHERE project_id = $1 AND status = 'succeeded'), 0),
				coalesce((SELECT max(id) FROM scan WHERE project_id = $1 AND status = 'succeeded' AND id < $2), 0)`,
			sc.ProjectID, id).Scan(&d.Latest, &previous); err != nil {
			return fmt.Errorf("reading the scans around scan %d: %w", id, err)
		}
		if sc.Status != scan.StatusSucceeded {
			return nil
		}
		d.Previous = previous
		return readReport(ctx, tx, sc.ProjectID, id, &d)
	})
	if err != nil {
		return report.Data{}, err
	}
	return d, nil
}

// readReport fills everything of d but the project, the scans and Latest.
func readReport(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID, d *report.Data) error {
	var err error
	if d.Totals, err = readTotals(ctx, tx, projectID, id); err != nil {
		return err
	}
	if d.Previous != 0 {
		if d.PreviousTotals, err = readTotals(ctx, tx, projectID, d.Previous); err != nil {
			return err
		}
	}
	if d.Lines, err = readSecretLines(ctx, tx, projectID, id); err != nil {
		return err
	}
	customer, err := readCustomerLines(ctx, tx, projectID, id)
	if err != nil {
		return err
	}
	d.Lines = append(d.Lines, customer...)
	if d.Changes, err = readChanges(ctx, tx, projectID, id); err != nil {
		return err
	}
	if d.Redactions, err = readRedactions(ctx, tx, projectID, id); err != nil {
		return err
	}
	d.Events, d.EventTotal, err = readEvents(ctx, tx, projectID)
	return err
}

// readTotals returns the numbers of scan id of the project.
func readTotals(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID) (report.Totals, error) {
	var t report.Totals
	err := tx.QueryRow(ctx, reportFound+`, done AS (
			SELECT i.bundle_id, i.action `+reportItems+`
		)
		SELECT f.*, d.* FROM (
			SELECT count(*)::int, coalesce(sum(locations), 0)::int,
				count(*) FILTER (WHERE verdict = 'false-positive')::int,
				coalesce(sum(locations) FILTER (WHERE verdict = 'false-positive'), 0)::int,
				count(*) FILTER (WHERE verdict = 'hold')::int,
				coalesce(sum(locations) FILTER (WHERE verdict = 'hold'), 0)::int,
				count(*) FILTER (WHERE verdict = '')::int,
				coalesce(sum(locations) FILTER (WHERE verdict = ''), 0)::int,
				count(*) FILTER (WHERE verdict IN ('externalize', 'redact'))::int,
				coalesce(sum(locations) FILTER (WHERE verdict IN ('externalize', 'redact')), 0)::int
			FROM found
		) f CROSS JOIN (
			SELECT count(DISTINCT bundle_id) FILTER (WHERE action IN ('replace', 'remove-file'))::int,
				count(*) FILTER (WHERE action IN ('replace', 'remove-file'))::int,
				count(DISTINCT bundle_id) FILTER (WHERE action = 'redact')::int,
				count(*) FILTER (WHERE action = 'redact')::int
			FROM done
		) d`, projectID, id).Scan(
		&t.Found.Bundles, &t.Found.Locations,
		&t.FalsePositive.Bundles, &t.FalsePositive.Locations,
		&t.Hold.Bundles, &t.Hold.Locations,
		&t.Undecided.Bundles, &t.Undecided.Locations,
		&t.Pending.Bundles, &t.Pending.Locations,
		&t.Externalized.Bundles, &t.Externalized.Locations,
		&t.Redacted.Bundles, &t.Redacted.Locations)
	if err != nil {
		return report.Totals{}, fmt.Errorf("counting the report of scan %d: %w", id, err)
	}
	return t, nil
}

// appliedOf returns how an apply changed the places of a line: after wins
// over before.
func appliedOf(before, after bool) report.Applied {
	switch {
	case after:
		return report.AppliedAfter
	case before:
		return report.AppliedBefore
	}
	return ""
}

// readSecretLines returns one line per secret bundle that scan id found.
// Frontend and Shell follow triage.Frontend and triage.ShellFile over the
// files of the bundle's members.
func readSecretLines(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID) ([]report.Line, error) {
	rows, err := tx.Query(ctx, reportFound+`
		SELECT id, category, kind, name, verdict, hold_reason, note, decided_at, file, first_line, files, locations,
			before, after
		FROM found WHERE category <> 'customer-data'
		ORDER BY file, first_line, id`, projectID, id)
	if err != nil {
		return nil, fmt.Errorf("listing the secrets of scan %d: %w", id, err)
	}
	lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (report.Line, error) {
		var (
			l             report.Line
			name          *string
			decidedAt     *time.Time
			files         []string
			before, after bool
		)
		if err := row.Scan(&l.Bundle, &l.Category, &l.Kind, &name, &l.Verdict, &l.Reason, &l.Note, &decidedAt,
			&l.File, &l.FirstLine, &files, &l.Locations, &before, &after); err != nil {
			return report.Line{}, err
		}
		if name != nil {
			l.Name = triage.Name(*name)
		}
		l.DecidedAt = timeOf(decidedAt)
		l.Files, l.Bundles = len(files), 1
		l.Frontend = slices.ContainsFunc(files, triage.Frontend)
		l.Shell = len(files) > 0 && !slices.ContainsFunc(files, func(f string) bool { return !triage.ShellFile(f) })
		l.Applied = appliedOf(before, after)
		return l, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the secrets of scan %d: %w", id, err)
	}
	return lines, nil
}

// readCustomerLines returns one line per file, kind, verdict, hold reason
// and note of the customer data that scan id found. Its file is the first
// file of each bundle, as in the triage list; Files counts the files of
// the members.
func readCustomerLines(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID) ([]report.Line, error) {
	rows, err := tx.Query(ctx, reportFound+`
		SELECT f.kind, f.verdict, f.hold_reason, f.note, max(f.decided_at), f.file, min(f.first_line),
			count(DISTINCT m.file_path)::int, count(DISTINCT f.id)::int, sum(m.locations)::int,
			bool_or(f.before), bool_or(f.after)
		FROM found f JOIN member m ON m.bundle_id = f.id
		WHERE f.category = 'customer-data'
		GROUP BY f.file, f.kind, f.verdict, f.hold_reason, f.note
		ORDER BY f.file, f.kind, f.verdict, f.hold_reason, f.note`, projectID, id)
	if err != nil {
		return nil, fmt.Errorf("listing the customer data of scan %d: %w", id, err)
	}
	lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (report.Line, error) {
		var (
			l             = report.Line{Category: finding.CategoryCustomerData}
			decidedAt     *time.Time
			before, after bool
		)
		if err := row.Scan(&l.Kind, &l.Verdict, &l.Reason, &l.Note, &decidedAt, &l.File, &l.FirstLine,
			&l.Files, &l.Bundles, &l.Locations, &before, &after); err != nil {
			return report.Line{}, err
		}
		l.DecidedAt = timeOf(decidedAt)
		l.Applied = appliedOf(before, after)
		return l, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the customer data of scan %d: %w", id, err)
	}
	return lines, nil
}

// readChanges returns the applied replace and remove-file items of the
// project's runs before scan id, from the items' own columns, ordered by
// name, file and line.
func readChanges(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID) ([]report.Change, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id, i.bundle_id, i.finding_id, i.file_path, i.start_line, i.start_col, i.line, i.action,
			i.category, i.kind, i.name, i.store_path, i.store_version, i.tracked, i.code_change, i.status, i.reason
		`+reportItems+` AND i.action IN ('replace', 'remove-file')
		ORDER BY i.name, i.file_path, i.line, i.id`, projectID, id)
	if err != nil {
		return nil, fmt.Errorf("listing the changes before scan %d: %w", id, err)
	}
	changes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (report.Change, error) {
		var (
			c  report.Change
			it = &c.Item
		)
		err := row.Scan(&c.Run, &it.BundleID, &it.FindingID, &it.File, &it.StartLine, &it.StartCol, &it.Line,
			&it.Action, &it.Category, &it.Kind, &it.Name, &it.StorePath, &it.StoreVersion, &it.Tracked,
			&it.CodeChange, &it.Status, &it.Reason)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing the changes before scan %d: %w", id, err)
	}
	return changes, nil
}

// readRedactions returns the applied redact items of the project's runs
// before scan id, one row per kind, file and tracked state.
func readRedactions(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID) ([]report.Redaction, error) {
	rows, err := tx.Query(ctx, `
		SELECT i.kind, i.file_path, NOT i.tracked, count(DISTINCT i.bundle_id)::int, count(*)::int
		`+reportItems+` AND i.action = 'redact'
		GROUP BY i.kind, i.file_path, i.tracked
		ORDER BY i.kind, i.file_path, i.tracked`, projectID, id)
	if err != nil {
		return nil, fmt.Errorf("listing the redactions before scan %d: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (report.Redaction, error) {
		var x report.Redaction
		err := row.Scan(&x.Kind, &x.File, &x.Untracked, &x.Bundles, &x.Locations)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing the redactions before scan %d: %w", id, err)
	}
	return out, nil
}

// readEvents returns the newest report.MaxRows lines of the project's
// history and the number of all its lines. A line is the verdict, move and
// apply events with the same time, actor, action and detail, which is what
// one request leaves, since a transaction has one now(). Its category, kind
// and file are those of the first finding that the line's lowest bundle
// holds now. Times are in UTC.
func readEvents(ctx context.Context, tx pgx.Tx, projectID project.ID) ([]report.Event, int, error) {
	rows, err := tx.Query(ctx, `
		WITH grp AS (
			SELECT at, actor_subject, actor_name, action, detail, count(*)::int AS bundles,
				min(bundle_id) AS first_bundle, max(id) AS last_id
			FROM audit_event
			WHERE project_id = $1 AND action IN ('verdict', 'move', 'apply')
			GROUP BY at, actor_subject, actor_name, action, detail
		)
		SELECT g.at, g.actor_subject, g.actor_name, g.action, g.detail, g.bundles,
			coalesce(f.category, ''), coalesce(f.kind, ''), coalesce(f.file_path, ''),
			(SELECT count(*) FROM grp)::int
		FROM (SELECT * FROM grp ORDER BY at DESC, last_id DESC LIMIT $2) g
		LEFT JOIN LATERAL (
			SELECT category, kind, file_path FROM finding
			WHERE bundle_id = g.first_bundle AND project_id = $1
			ORDER BY id LIMIT 1
		) f ON true
		ORDER BY g.at DESC, g.last_id DESC`, projectID, report.MaxRows)
	if err != nil {
		return nil, 0, fmt.Errorf("listing the history of project %d: %w", projectID, err)
	}
	var total int
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (report.Event, error) {
		var e report.Event
		err := row.Scan(&e.At, &e.Actor.Subject, &e.Actor.Name, &e.Action, &e.Detail, &e.Bundles,
			&e.Category, &e.Kind, &e.File, &total)
		e.At = e.At.UTC()
		return e, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing the history of project %d: %w", projectID, err)
	}
	return events, total, nil
}
