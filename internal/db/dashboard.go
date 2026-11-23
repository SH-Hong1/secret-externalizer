package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// scanEnd is when the scan sc finished. A succeeded scan always has a
// finish time when the worker saved it; a scan without one counts from its
// request. Scans are ordered by the pair (scanEnd, id).
const scanEnd = `coalesce(sc.finished_at, sc.requested_at)`

// dashboardScans is a common table expression of the succeeded scans of
// the dashboard's projects, $1 or every project when $1 is zero, with the
// time each finished as at.
const dashboardScans = `
	done AS (
		SELECT sc.id, sc.project_id, ` + scanEnd + ` AS at
		FROM scan sc
		WHERE sc.status = 'succeeded' AND ($1::bigint = 0 OR sc.project_id = $1)
	)`

// Dashboard returns what the database tells for the dashboard of f, read
// in one snapshot. It fails with an error matching ErrNotFound when
// f.Project is not zero and names no project.
func (s *Store) Dashboard(ctx context.Context, f dashboard.Filter) (dashboard.Data, error) {
	var d dashboard.Data
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		if f.Project != 0 {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM project WHERE id = $1)", f.Project).
				Scan(&exists); err != nil {
				return fmt.Errorf("reading project %d: %w", f.Project, err)
			}
			if !exists {
				return fmt.Errorf("reading project %d: %w", f.Project, ErrNotFound)
			}
		}
		var err error
		if d.Projects, err = readProjectStates(ctx, tx); err != nil {
			return err
		}
		if d.Points, d.PointTotal, err = readPoints(ctx, tx, f); err != nil {
			return err
		}
		d.Rules, err = readRules(ctx, tx, f.Project)
		return err
	})
	if err != nil {
		return dashboard.Data{}, err
	}
	return d, nil
}

// readProjectStates returns every project by name with its newest scan, its
// newest succeeded scan and the succeeded scan before that, and the numbers
// of the two.
func readProjectStates(ctx context.Context, tx pgx.Tx) ([]dashboard.ProjectState, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.name, p.path, p.excludes, p.created_by, p.created_at,
			coalesce(last.id, 0), coalesce(basis.id, 0), coalesce(prev.id, 0)
		FROM project p
		LEFT JOIN LATERAL (
			SELECT sc.id FROM scan sc WHERE sc.project_id = p.id
			ORDER BY `+scanEnd+` DESC, sc.id DESC LIMIT 1
		) last ON true
		LEFT JOIN LATERAL (
			SELECT sc.id FROM scan sc WHERE sc.project_id = p.id AND sc.status = 'succeeded'
			ORDER BY `+scanEnd+` DESC, sc.id DESC LIMIT 1
		) basis ON true
		LEFT JOIN LATERAL (
			SELECT max(sc.id) AS id FROM scan sc
			WHERE sc.project_id = p.id AND sc.status = 'succeeded' AND sc.id < basis.id
		) prev ON true
		ORDER BY p.name, p.id`)
	if err != nil {
		return nil, fmt.Errorf("listing the projects of the dashboard: %w", err)
	}
	defer rows.Close()
	type ids struct{ last, basis, previous scan.ID }
	var (
		states []dashboard.ProjectState
		scans  []ids
	)
	for rows.Next() {
		var (
			st dashboard.ProjectState
			i  ids
		)
		pr := &st.Project
		if err := rows.Scan(&pr.ID, &pr.Name, &pr.Path, &pr.Excludes, &pr.CreatedBy, &pr.CreatedAt,
			&i.last, &i.basis, &i.previous); err != nil {
			return nil, fmt.Errorf("listing the projects of the dashboard: %w", err)
		}
		states, scans = append(states, st), append(scans, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the projects of the dashboard: %w", err)
	}

	for n := range states {
		st, i := &states[n], scans[n]
		if i.last != 0 {
			sc, err := scanScan(tx.QueryRow(ctx, "SELECT "+scanColumns+" FROM scan WHERE id = $1", i.last))
			if err != nil {
				return nil, fmt.Errorf("reading scan %d: %w", i.last, err)
			}
			st.Last = sc
		}
		if i.basis == 0 {
			continue
		}
		var err error
		st.Basis, st.Previous = i.basis, i.previous
		if st.Totals, err = readTotals(ctx, tx, st.Project.ID, i.basis); err != nil {
			return nil, err
		}
		if i.previous == 0 {
			continue
		}
		if st.PreviousTotals, err = readTotals(ctx, tx, st.Project.ID, i.previous); err != nil {
			return nil, err
		}
	}
	return states, nil
}

// readPoints returns the points of the trend of f, baselines first and then
// in the order the scans finished, and the number of succeeded scans of the
// period.
func readPoints(ctx context.Context, tx pgx.Tx, f dashboard.Filter) ([]dashboard.Point, int, error) {
	var since *time.Time
	if !f.Since.IsZero() {
		since = &f.Since
	}
	rows, err := tx.Query(ctx, `
		WITH `+dashboardScans+`, period AS (
			SELECT * FROM done WHERE $2::timestamptz IS NULL OR at >= $2
		), picked AS (
			SELECT * FROM period ORDER BY at DESC, id DESC LIMIT $3
		), earliest AS (
			SELECT at, id FROM picked ORDER BY at, id LIMIT 1
		), baseline AS (
			SELECT DISTINCT ON (d.project_id) d.*
			FROM done d
			WHERE NOT EXISTS (SELECT 1 FROM earliest) OR (d.at, d.id) < (SELECT at, id FROM earliest)
			ORDER BY d.project_id, d.at DESC, d.id DESC
		), point AS (
			SELECT id, project_id, at, false AS baseline FROM picked
			UNION ALL
			SELECT id, project_id, at, true FROM baseline
		)
		SELECT id, project_id, at, baseline, (SELECT count(*) FROM period)::int
		FROM point ORDER BY baseline DESC, at, id`, f.Project, since, dashboard.MaxPoints)
	if err != nil {
		return nil, 0, fmt.Errorf("listing the scans of the trend: %w", err)
	}
	defer rows.Close()
	var (
		points []dashboard.Point
		total  int
		ids    []scan.ID
	)
	for rows.Next() {
		p := dashboard.Point{Remaining: map[finding.Category]dashboard.Split{}}
		if err := rows.Scan(&p.Scan, &p.Project, &p.At, &p.Baseline, &total); err != nil {
			return nil, 0, fmt.Errorf("listing the scans of the trend: %w", err)
		}
		p.At = p.At.UTC()
		points, ids = append(points, p), append(ids, p.Scan)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("listing the scans of the trend: %w", err)
	}
	if len(points) == 0 {
		return nil, total, nil
	}
	if err := readRemaining(ctx, tx, ids, points); err != nil {
		return nil, 0, err
	}
	return points, total, nil
}

// remainingCounts counts, for each scan of $1, the bundles it found that
// are not false positives, by the category of the member with the lowest ID
// that the scan found, as reportFound has it, and by verdict.
const remainingCounts = `
	WITH member AS (
		SELECT DISTINCT l.scan_id, sc.project_id, f.id, f.bundle_id, f.category
		FROM location l
		JOIN scan sc ON sc.id = l.scan_id
		JOIN finding f ON f.id = l.finding_id AND f.project_id = sc.project_id
		WHERE l.scan_id = ANY($1)
	), found AS (
		SELECT m.scan_id, b.verdict, (array_agg(m.category ORDER BY m.id))[1] AS category
		FROM member m JOIN bundle b ON b.id = m.bundle_id AND b.project_id = m.project_id
		GROUP BY m.scan_id, b.id
	)
	SELECT scan_id, category,
		count(*) FILTER (WHERE verdict = '')::int,
		count(*) FILTER (WHERE verdict = 'hold')::int,
		count(*) FILTER (WHERE verdict IN ('externalize', 'redact'))::int
	FROM found WHERE verdict <> 'false-positive'
	GROUP BY scan_id, category`

// readRemaining fills the Remaining of points, whose scans are ids, with
// remainingCounts.
func readRemaining(ctx context.Context, tx pgx.Tx, ids []scan.ID, points []dashboard.Point) error {
	rows, err := tx.Query(ctx, remainingCounts, ids)
	if err != nil {
		return fmt.Errorf("counting the scans of the trend: %w", err)
	}
	byScan := make(map[scan.ID]map[finding.Category]dashboard.Split, len(points))
	for _, p := range points {
		byScan[p.Scan] = p.Remaining
	}
	var (
		id       scan.ID
		category finding.Category
		split    dashboard.Split
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &category, &split.Undecided, &split.Hold, &split.Pending}, func() error {
		byScan[id][category] = split
		return nil
	})
	if err != nil {
		return fmt.Errorf("counting the scans of the trend: %w", err)
	}
	return nil
}

// readRules returns the rules that found a false positive among the
// bundles that the newest succeeded scans of the projects of projectID,
// zero for all, found, with the bundles each rule found.
func readRules(ctx context.Context, tx pgx.Tx, projectID project.ID) ([]dashboard.Rule, error) {
	rows, err := tx.Query(ctx, `
		WITH `+dashboardScans+`, basis AS (
			SELECT DISTINCT ON (project_id) id, project_id FROM done ORDER BY project_id, at DESC, id DESC
		), hit AS (
			SELECT DISTINCT s.project_id, f.bundle_id, d->>'tool' AS tool, d->>'rule' AS rule
			FROM basis s
			JOIN location l ON l.scan_id = s.id
			JOIN finding f ON f.id = l.finding_id AND f.project_id = s.project_id
			CROSS JOIN LATERAL jsonb_array_elements(l.detections) d
		)
		SELECT h.tool, h.rule, count(*)::int AS bundles,
			count(*) FILTER (WHERE b.verdict = 'false-positive')::int AS false_positive
		FROM hit h JOIN bundle b ON b.id = h.bundle_id AND b.project_id = h.project_id
		GROUP BY h.tool, h.rule
		HAVING count(*) FILTER (WHERE b.verdict = 'false-positive') > 0
		ORDER BY false_positive DESC, bundles, h.tool, h.rule
		LIMIT $2`, projectID, dashboard.MaxRules)
	if err != nil {
		return nil, fmt.Errorf("counting the false positives by rule: %w", err)
	}
	var (
		rules []dashboard.Rule
		r     dashboard.Rule
	)
	_, err = pgx.ForEachRow(rows, []any{&r.Tool, &r.Rule, &r.Bundles, &r.FalsePositive}, func() error {
		rules = append(rules, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("counting the false positives by rule: %w", err)
	}
	return rules, nil
}
