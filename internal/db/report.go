package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

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

// frontendFile is true for the path file_path when triage.Frontend is,
// given the patterns of frontendPatterns as $7.
const frontendFile = `lower(file_path COLLATE "C") LIKE ANY ($7::text[])`

// frontendPatterns returns a LIKE pattern for each of
// triage.FrontendExtensions: any path that ends in the extension. No
// extension holds a character that LIKE treats specially.
func frontendPatterns() []string {
	exts := triage.FrontendExtensions()
	patterns := make([]string, len(exts))
	for i, ext := range exts {
		patterns[i] = "%" + ext
	}
	return patterns
}

// reportFound starts a query with common table expressions for project $1
// and scan $2, with frontendPatterns as $7, of which member and found are
// for the query to read. member is one row per finding the scan found.
// found is one row per bundle with such a member, as scanBundles has it:
// category and kind are those of the member with the lowest ID, file and
// first_line the first file and the first line in it, files the file of each member, frontend whether one of
// them is a triage.Frontend file, and locations the number of locations;
// before and after say whether an applied item of the bundle is in a run
// before the scan, or in a run after it. It reads no preview.
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
	), lowest AS (
		SELECT DISTINCT ON (bundle_id) bundle_id, category, kind FROM member ORDER BY bundle_id, id
	), head AS (
		SELECT DISTINCT ON (bundle_id) bundle_id, file_path AS file, first_line
		FROM member ORDER BY bundle_id, file_path, first_line
	), summed AS (
		SELECT bundle_id, array_agg(file_path) AS files,
			bool_or(` + frontendFile + `) AS frontend,
			sum(locations)::int AS locations
		FROM member GROUP BY bundle_id
	), grouped AS (
		SELECT b.id, b.verdict, b.hold_reason, b.note, b.name, b.decided_at,
			lo.category, lo.kind, h.file, h.first_line, su.files, su.frontend, su.locations
		FROM bundle b
		JOIN summed su ON su.bundle_id = b.id
		JOIN lowest lo ON lo.bundle_id = b.id
		JOIN head h ON h.bundle_id = b.id
		WHERE b.project_id = $1
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
	ids := []scan.ID{id}
	if d.Previous != 0 {
		ids = append(ids, d.Previous)
	}
	totals, err := readTotals(ctx, tx, ids...)
	if err != nil {
		return err
	}
	d.Totals, d.PreviousTotals = totals[id], totals[d.Previous]
	if err := readLines(ctx, tx, projectID, id, d); err != nil {
		return err
	}
	if d.Externalized, err = readExternalized(ctx, tx, projectID, id); err != nil {
		return err
	}
	if d.Redactions, err = readRedactions(ctx, tx, projectID, id); err != nil {
		return err
	}
	d.Events, d.EventTotal, err = readEvents(ctx, tx, projectID)
	return err
}

// readTotals returns the numbers of each of the scans ids, which may be of
// different projects. A scan with no row in the database has no entry.
func readTotals(ctx context.Context, tx pgx.Tx, ids ...scan.ID) (map[scan.ID]report.Totals, error) {
	rows, err := tx.Query(ctx, `
		WITH sc AS (
			SELECT id, project_id, requested_at, apply_id FROM scan WHERE id = ANY($1)
		), member AS (
			SELECT sc.id AS scan_id, f.bundle_id, count(*) AS locations
			FROM sc
			JOIN location l ON l.scan_id = sc.id
			JOIN finding f ON f.id = l.finding_id AND f.project_id = sc.project_id
			GROUP BY sc.id, f.bundle_id
		), found AS (
			SELECT m.scan_id,
				count(*)::int AS found, sum(m.locations)::int AS found_at,
				count(*) FILTER (WHERE b.verdict = 'false-positive')::int AS fp,
				coalesce(sum(m.locations) FILTER (WHERE b.verdict = 'false-positive'), 0)::int AS fp_at,
				count(*) FILTER (WHERE b.verdict = 'hold')::int AS hold,
				coalesce(sum(m.locations) FILTER (WHERE b.verdict = 'hold'), 0)::int AS hold_at,
				count(*) FILTER (WHERE b.verdict = '')::int AS undecided,
				coalesce(sum(m.locations) FILTER (WHERE b.verdict = ''), 0)::int AS undecided_at,
				count(*) FILTER (WHERE b.verdict IN ('externalize', 'redact'))::int AS pending,
				coalesce(sum(m.locations) FILTER (WHERE b.verdict IN ('externalize', 'redact')), 0)::int AS pending_at
			FROM member m
			JOIN sc ON sc.id = m.scan_id
			JOIN bundle b ON b.id = m.bundle_id AND b.project_id = sc.project_id
			GROUP BY m.scan_id
		), done AS (
			SELECT sc.id AS scan_id,
				count(DISTINCT i.bundle_id) FILTER (WHERE i.action IN ('replace', 'remove-file'))::int AS ext,
				count(*) FILTER (WHERE i.action IN ('replace', 'remove-file'))::int AS ext_at,
				count(DISTINCT i.bundle_id) FILTER (WHERE i.action = 'redact')::int AS red,
				count(*) FILTER (WHERE i.action = 'redact')::int AS red_at
			FROM sc
			JOIN apply_run r ON r.project_id = sc.project_id
			JOIN apply_item i ON i.apply_id = r.id
			WHERE i.status = 'applied' AND `+runBefore+`
			GROUP BY sc.id
		)
		SELECT sc.id,
			coalesce(f.found, 0), coalesce(f.found_at, 0), coalesce(f.fp, 0), coalesce(f.fp_at, 0),
			coalesce(f.hold, 0), coalesce(f.hold_at, 0), coalesce(f.undecided, 0), coalesce(f.undecided_at, 0),
			coalesce(f.pending, 0), coalesce(f.pending_at, 0),
			coalesce(d.ext, 0), coalesce(d.ext_at, 0), coalesce(d.red, 0), coalesce(d.red_at, 0)
		FROM sc
		LEFT JOIN found f ON f.scan_id = sc.id
		LEFT JOIN done d ON d.scan_id = sc.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("counting the scans %v: %w", ids, err)
	}
	var (
		id scan.ID
		t  report.Totals
	)
	totals := make(map[scan.ID]report.Totals, len(ids))
	_, err = pgx.ForEachRow(rows, []any{
		&id,
		&t.Found.Bundles, &t.Found.Locations,
		&t.FalsePositive.Bundles, &t.FalsePositive.Locations,
		&t.Hold.Bundles, &t.Hold.Locations,
		&t.Undecided.Bundles, &t.Undecided.Locations,
		&t.Pending.Bundles, &t.Pending.Locations,
		&t.Externalized.Bundles, &t.Externalized.Locations,
		&t.Redacted.Bundles, &t.Redacted.Locations,
	}, func() error {
		totals[id] = t
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("counting the scans %v: %w", ids, err)
	}
	return totals, nil
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

// The lists of what a scan found, as readLines numbers them.
const (
	sectionStructure = iota + 1
	sectionRemaining
	sectionFalsePositives
)

// reportLines lists what scan $2 of project $1 found: the first $6 lines
// of each list with the number of all its lines. $3, $4 and $5 are the
// numbers of the lists, and $7 is frontendPatterns.
const reportLines = reportFound + `, line AS (
		SELECT id AS bundle, category, kind, name, verdict, hold_reason, note, decided_at, file, first_line,
			files, 1 AS bundles, locations, before, after, frontend
		FROM found WHERE category <> 'customer-data'
		UNION ALL
		SELECT 0, f.category, f.kind, NULL, f.verdict, f.hold_reason, f.note, max(f.decided_at), f.file,
			min(f.first_line), array_agg(DISTINCT m.file_path), count(DISTINCT f.id)::int, sum(m.locations)::int,
			bool_or(f.before), bool_or(f.after), false
		FROM found f JOIN member m ON m.bundle_id = f.id
		WHERE f.category = 'customer-data'
		GROUP BY f.category, f.file, f.kind, f.verdict, f.hold_reason, f.note
	), sectioned AS (
		SELECT *, CASE
				WHEN verdict = 'false-positive' THEN $5::int
				WHEN frontend OR (verdict = 'hold' AND hold_reason = 'structure-change') THEN $3::int
				ELSE $4::int
			END AS section
		FROM line
	), ranked AS (
		SELECT *,
			row_number() OVER (
				PARTITION BY section
				ORDER BY file COLLATE "C", first_line, bundle, kind, verdict, hold_reason, note
			) AS n,
			count(*) OVER (PARTITION BY section)::int AS total
		FROM sectioned
	)
	SELECT section, total, bundle, category, kind, name, verdict, hold_reason, note, decided_at, file, first_line,
		files, bundles, locations, before, after, frontend
	FROM ranked WHERE n <= $6
	ORDER BY section, n`

// readLines fills the three lists of what scan id found, as report.Data
// describes them. A line is a secret bundle, or the customer data of one
// first file, kind, verdict, hold reason and note, whose Files counts the
// files of its members. Shell follows triage.ShellFile over the files of a
// secret bundle's members.
func readLines(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID, d *report.Data) error {
	rows, err := tx.Query(ctx, reportLines,
		projectID, id, sectionStructure, sectionRemaining, sectionFalsePositives, report.MaxRows, frontendPatterns())
	if err != nil {
		return fmt.Errorf("listing what scan %d found: %w", id, err)
	}
	var (
		section, total int
		l              report.Line
		name           *string
		decidedAt      *time.Time
		files          []string
		before, after  bool
	)
	_, err = pgx.ForEachRow(rows, []any{
		&section, &total, &l.Bundle, &l.Category, &l.Kind, &name, &l.Verdict, &l.Reason, &l.Note, &decidedAt,
		&l.File, &l.FirstLine, &files, &l.Bundles, &l.Locations, &before, &after, &l.Frontend,
	}, func() error {
		line := l
		if name != nil {
			line.Name = triage.Name(*name)
		}
		line.DecidedAt = timeOf(decidedAt)
		slices.Sort(files)
		line.Files = len(slices.Compact(files))
		line.Shell = line.Bundle != 0 && len(files) > 0 &&
			!slices.ContainsFunc(files, func(f string) bool { return !triage.ShellFile(f) })
		line.Applied = appliedOf(before, after)
		list := &d.Remaining
		switch section {
		case sectionStructure:
			list = &d.Structure
		case sectionFalsePositives:
			list = &d.FalsePositives
		}
		list.Rows, list.Total = append(list.Rows, line), total
		return nil
	})
	if err != nil {
		return fmt.Errorf("listing what scan %d found: %w", id, err)
	}
	return nil
}

// readExternalized returns the applied replace and remove-file items of the
// project's runs before scan id as entries, from the items' own columns:
// the first report.MaxRows entries, each with its first report.MaxPlaces
// places, and the number of all entries. An entry reappeared when the scan
// found its bundle as a secret and no run after the scan applied an item of
// the bundle.
func readExternalized(ctx context.Context, tx pgx.Tx, projectID project.ID, id scan.ID) (report.List[report.Entry], error) {
	rows, err := tx.Query(ctx, `
		WITH sc AS (
			SELECT requested_at, apply_id FROM scan WHERE id = $2
		), item AS (
			SELECT r.id AS run, i.id, i.bundle_id, i.name, i.file_path, i.line, i.action, i.category, i.kind,
				i.store_path, i.code_change
			`+reportItems+` AND i.action IN ('replace', 'remove-file')
		), entry AS (
			SELECT bundle_id, name, count(*)::int AS places, bool_or(code_change) AS any_code_change,
				row_number() OVER (ORDER BY name COLLATE "C", bundle_id) AS entry,
				count(*) OVER ()::int AS entries
			FROM item GROUP BY bundle_id, name
		), shown AS (
			SELECT * FROM (
				SELECT e.entry, e.entries, e.places, e.any_code_change, i.*,
					row_number() OVER (PARTITION BY e.entry ORDER BY i.file_path COLLATE "C", i.line, i.id) AS place
				FROM entry e JOIN item i ON i.bundle_id = e.bundle_id AND i.name = e.name
				WHERE e.entry <= $3
			) placed WHERE place <= $4
		), again AS (
			SELECT DISTINCT ON (f.bundle_id) f.bundle_id, f.category <> 'customer-data' AS secret
			FROM finding f
			WHERE f.project_id = $1 AND f.bundle_id IN (SELECT bundle_id FROM shown)
				AND EXISTS (SELECT 1 FROM location l WHERE l.finding_id = f.id AND l.scan_id = $2)
			ORDER BY f.bundle_id, f.id
		), later AS (
			SELECT DISTINCT i.bundle_id
			FROM apply_item i JOIN apply_run r ON r.id = i.apply_id CROSS JOIN sc
			WHERE r.project_id = $1 AND i.status = 'applied' AND `+runAfter+`
				AND i.bundle_id IN (SELECT bundle_id FROM shown)
		)
		SELECT s.place, s.bundle_id, s.name, s.category, s.kind, s.store_path, s.places, s.any_code_change,
			coalesce(a.secret, false) AND l.bundle_id IS NULL, s.file_path, s.line, s.action = 'remove-file', s.run,
			s.entries
		FROM shown s
		LEFT JOIN again a ON a.bundle_id = s.bundle_id
		LEFT JOIN later l ON l.bundle_id = s.bundle_id
		ORDER BY s.entry, s.place`, projectID, id, report.MaxRows, report.MaxPlaces)
	if err != nil {
		return report.List[report.Entry]{}, fmt.Errorf("listing the changes before scan %d: %w", id, err)
	}
	var (
		list  report.List[report.Entry]
		place int
		e     report.Entry
		p     report.Place
	)
	_, err = pgx.ForEachRow(rows, []any{
		&place, &e.Bundle, &e.Name, &e.Category, &e.Kind, &e.StorePath, &e.PlaceTotal, &e.CodeChange,
		&e.Reappeared, &p.File, &p.Line, &p.Removed, &p.Run, &list.Total,
	}, func() error {
		if place == 1 {
			list.Rows = append(list.Rows, e)
		}
		last := &list.Rows[len(list.Rows)-1]
		last.Places = append(last.Places, p)
		return nil
	})
	if err != nil {
		return report.List[report.Entry]{}, fmt.Errorf("listing the changes before scan %d: %w", id, err)
	}
	return list, nil
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
			coalesce(f.category, ''), coalesce(f.kind, ''), coalesce(f.file_path, ''), g.total
		FROM (
			SELECT *, count(*) OVER ()::int AS total FROM grp ORDER BY at DESC, last_id DESC LIMIT $2
		) g
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
