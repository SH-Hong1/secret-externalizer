package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// readOnly runs read in a read-only transaction that sees one snapshot.
func (s *Store) readOnly(ctx context.Context, read func(pgx.Tx) error) (err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)
	if err := read(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing read: %w", err)
	}
	return nil
}

// Bundle returns the project's bundle with the given ID and the members that
// scanID found, each with the locations of that scan. Members are ordered by
// file, locations by line and column. It returns ErrNotFound when the project
// has no such bundle or scanID found none of its members.
func (s *Store) Bundle(ctx context.Context, projectID project.ID, scanID scan.ID, id triage.BundleID) (triage.Bundle, error) {
	b := triage.Bundle{ID: id, ProjectID: projectID}
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		var (
			name                 *string
			decidedAt, appliedAt *time.Time
		)
		err := tx.QueryRow(ctx, `
			SELECT fingerprint, verdict, hold_reason, name, note, decided_by, decided_at, applied_at, store_path FROM bundle
			WHERE id = $1 AND project_id = $2`, id, projectID).
			Scan(&b.Fingerprint, &b.Decision.Verdict, &b.Decision.Reason, &name, &b.Decision.Note, &b.DecidedBy, &decidedAt,
				&appliedAt, &b.StorePath)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reading bundle %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading bundle %d: %w", id, err)
		}
		if name != nil {
			b.Decision.Name = triage.Name(*name)
		}
		b.DecidedAt, b.AppliedAt = timeOf(decidedAt), timeOf(appliedAt)
		return readMembers(ctx, tx, scanID, &b)
	})
	if err != nil {
		return triage.Bundle{}, err
	}
	if len(b.Members) == 0 {
		return triage.Bundle{}, fmt.Errorf("reading bundle %d in scan %d: %w", id, scanID, ErrNotFound)
	}
	return b, nil
}

// readMembers adds to b the members scanID found, with their locations, and
// sets b's category, kind and preview from the member with the lowest ID.
func readMembers(ctx context.Context, tx pgx.Tx, scanID scan.ID, b *triage.Bundle) error {
	rows, err := tx.Query(ctx, `
		SELECT f.id, f.file_path, f.key, f.category, f.kind, f.preview, f.has_original,
			`+locationColumns+`
		FROM finding f JOIN location l ON l.finding_id = f.id
		WHERE f.bundle_id = $1 AND l.scan_id = $2
		ORDER BY f.file_path, f.id, l.start_line, l.start_col, l.id`, b.ID, scanID)
	if err != nil {
		return fmt.Errorf("listing members of bundle %d: %w", b.ID, err)
	}
	defer rows.Close()
	var lowest finding.ID
	for rows.Next() {
		var (
			m       triage.Member
			c       finding.Category
			k       finding.Kind
			preview string
		)
		dest := []any{&m.FindingID, &m.File, &m.Key, &c, &k, &preview, &m.HasOriginal}
		l, err := scanLocation(rows, dest...)
		if err != nil {
			return err
		}
		if lowest == 0 || m.FindingID < lowest {
			lowest = m.FindingID
			b.Category, b.Kind, b.Preview = c, k, preview
		}
		if n := len(b.Members); n > 0 && b.Members[n-1].FindingID == m.FindingID {
			b.Members[n-1].Locations = append(b.Members[n-1].Locations, l)
			continue
		}
		m.Locations = []finding.Location{l}
		b.Members = append(b.Members, m)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing members of bundle %d: %w", b.ID, err)
	}
	return nil
}

// scanBundles starts a query with the common table expression scan_bundle:
// one row per bundle of project $1 with a member that scan $2 found. Its
// category, kind and preview are those of the member with the lowest ID; its
// file and first_line are the first file and the first line in it; locations
// counts the locations of the scan; shell is true when every member is in a
// shell script and applied when an apply stored the bundle's value.
const scanBundles = `
	WITH member AS (
		SELECT f.id, f.bundle_id, f.file_path, f.category, f.kind, f.preview,
			min(l.start_line) AS first_line, count(*) AS locations
		FROM finding f JOIN location l ON l.finding_id = f.id
		WHERE f.project_id = $1 AND l.scan_id = $2
		GROUP BY f.id
	), scan_bundle AS (
		SELECT b.id, b.verdict, b.name,
			(array_agg(m.category ORDER BY m.id))[1] AS category,
			(array_agg(m.kind ORDER BY m.id))[1] AS kind,
			(array_agg(m.preview ORDER BY m.id))[1] AS preview,
			(array_agg(m.file_path ORDER BY m.file_path, m.first_line))[1] AS file,
			(array_agg(m.first_line ORDER BY m.file_path, m.first_line))[1] AS first_line,
			sum(m.locations)::int AS locations,
			bool_and(lower(m.file_path) LIKE '%.sh') AS shell,
			b.applied_at IS NOT NULL AS applied
		FROM bundle b JOIN member m ON m.bundle_id = b.id
		WHERE b.project_id = $1
		GROUP BY b.id
	)`

// scanEntries extends scanBundles with entry: one row per line of the
// triage list. A secret bundle is a line of its own; customer data is one
// line per file, kind and verdict, with bundle zero.
const scanEntries = scanBundles + `, entry AS (
		SELECT id AS bundle, category, kind, preview, name, file, first_line, verdict,
			locations AS count, 1 AS bundles, shell, applied
		FROM scan_bundle WHERE category <> 'customer-data'
		UNION ALL
		SELECT 0, 'customer-data', kind, (array_agg(preview ORDER BY id))[1], NULL, file, min(first_line), verdict,
			sum(locations)::int, count(*)::int, false, false
		FROM scan_bundle WHERE category = 'customer-data'
		GROUP BY file, kind, verdict
	)`

// entryFilter is the condition on entry for category $3 (empty for every
// category), verdict $4, and $5, true for every verdict.
const entryFilter = ` WHERE ($3 = '' OR category = $3) AND ($5 OR verdict = $4)`

// statusVerdict returns the verdict a Filter.Status selects, and whether it
// selects every verdict.
func statusVerdict(status string) (v triage.Verdict, all bool, err error) {
	switch v := triage.Verdict(status); v {
	case triage.VerdictExternalize, triage.VerdictRedact, triage.VerdictFalsePositive, triage.VerdictHold:
		return v, false, nil
	}
	switch status {
	case triage.StatusAll:
		return "", true, nil
	case triage.StatusUndecided:
		return "", false, nil
	}
	return "", false, fmt.Errorf("unknown status %q", status)
}

// Entries returns one page of the triage list of the project for scanID:
// the bundles that scan found, ordered by category, file, and first line.
// A page past the last gives the last page and a page before the first the
// first. Counts holds the number of bundles for each verdict, whatever the
// filter.
func (s *Store) Entries(ctx context.Context, projectID project.ID, scanID scan.ID, f triage.Filter) (triage.Page, error) {
	verdict, all, err := statusVerdict(f.Status)
	if err != nil {
		return triage.Page{}, fmt.Errorf("listing entries: %w", err)
	}
	p := triage.Page{Counts: zeroCounts()}
	err = s.readOnly(ctx, func(tx pgx.Tx) error {
		if err := readCounts(ctx, tx, projectID, scanID, p.Counts); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, scanEntries+" SELECT count(*) FROM entry"+entryFilter,
			projectID, scanID, f.Category, verdict, all).Scan(&p.Total); err != nil {
			return fmt.Errorf("counting entries: %w", err)
		}
		p.Pages = max(1, (p.Total+triage.PageSize-1)/triage.PageSize)
		p.Page = min(max(f.Page, 1), p.Pages)
		entries, err := readEntries(ctx, tx, scanEntries+" SELECT bundle, category, kind, preview, name, file, first_line, verdict, count, bundles, shell, applied FROM entry"+
			entryFilter+" ORDER BY category, file, first_line, bundle, kind, verdict LIMIT $6 OFFSET $7",
			projectID, scanID, f.Category, verdict, all, triage.PageSize, (p.Page-1)*triage.PageSize)
		p.Entries = entries
		return err
	})
	if err != nil {
		return triage.Page{}, err
	}
	return p, nil
}

// zeroCounts returns verdict counts with a key for each verdict, all zero.
func zeroCounts() map[triage.Verdict]int {
	return map[triage.Verdict]int{
		"": 0, triage.VerdictExternalize: 0, triage.VerdictRedact: 0, triage.VerdictFalsePositive: 0, triage.VerdictHold: 0,
	}
}

// VerdictCounts returns the number of bundles of each verdict that the
// scan found, as the Counts of Entries has it. The key of the bundles
// without a verdict is the empty verdict.
func (s *Store) VerdictCounts(ctx context.Context, projectID project.ID, scanID scan.ID) (map[triage.Verdict]int, error) {
	counts := zeroCounts()
	if err := s.readOnly(ctx, func(tx pgx.Tx) error {
		return readCounts(ctx, tx, projectID, scanID, counts)
	}); err != nil {
		return nil, err
	}
	return counts, nil
}

// readCounts adds to counts the number of bundles of each verdict that
// scanID found.
func readCounts(ctx context.Context, tx pgx.Tx, projectID project.ID, scanID scan.ID, counts map[triage.Verdict]int) error {
	rows, err := tx.Query(ctx, scanBundles+" SELECT verdict, count(*) FROM scan_bundle GROUP BY verdict", projectID, scanID)
	if err != nil {
		return fmt.Errorf("counting bundles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			v triage.Verdict
			n int
		)
		if err := rows.Scan(&v, &n); err != nil {
			return fmt.Errorf("reading count row: %w", err)
		}
		counts[v] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("counting bundles: %w", err)
	}
	return nil
}

func readEntries(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]triage.Entry, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing entries: %w", err)
	}
	defer rows.Close()
	var out []triage.Entry
	for rows.Next() {
		var (
			e    triage.Entry
			name *string
		)
		if err := rows.Scan(&e.Bundle, &e.Category, &e.Kind, &e.Preview, &name, &e.File, &e.FirstLine,
			&e.Verdict, &e.Count, &e.Bundles, &e.Shell, &e.Applied); err != nil {
			return nil, fmt.Errorf("reading entry row: %w", err)
		}
		if name != nil {
			e.Name = triage.Name(*name)
		}
		if e.Bundle == 0 {
			e.Group = triage.GroupKey{File: e.File, Kind: e.Kind, Verdict: e.Verdict}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing entries: %w", err)
	}
	return out, nil
}

// CustomerGroup returns the customer data of the project that scanID found
// and that key names: the bundles whose first file and kind are key's and
// whose verdict is key's, the number of their locations, and the first
// triage.GroupLines lines in key's file. A key that names nothing gives a
// Group with no bundles.
func (s *Store) CustomerGroup(ctx context.Context, projectID project.ID, scanID scan.ID, key triage.GroupKey) (triage.Group, error) {
	var (
		ids   []int64
		lines []int32
		g     = triage.Group{Key: key}
	)
	err := s.pool.QueryRow(ctx, scanBundles+`, grp AS (
			SELECT id, locations FROM scan_bundle
			WHERE category = 'customer-data' AND file = $3 AND kind = $4 AND verdict = $5
		)
		SELECT
			coalesce((SELECT array_agg(id ORDER BY id) FROM grp), '{}'),
			coalesce((SELECT sum(locations) FROM grp), 0)::int,
			coalesce((SELECT array_agg(start_line ORDER BY start_line) FROM (
				SELECT DISTINCT l.start_line FROM location l JOIN finding f ON f.id = l.finding_id
				WHERE l.scan_id = $2 AND f.file_path = $3 AND f.bundle_id IN (SELECT id FROM grp)
				ORDER BY l.start_line LIMIT $6) AS first_lines), '{}')`,
		projectID, scanID, key.File, key.Kind, key.Verdict, triage.GroupLines).Scan(&ids, &g.Count, &lines)
	if err != nil {
		return triage.Group{}, fmt.Errorf("reading customer data group: %w", err)
	}
	for _, id := range ids {
		g.Bundles = append(g.Bundles, triage.BundleID(id))
	}
	for _, l := range lines {
		g.Lines = append(g.Lines, int(l))
	}
	return g, nil
}

// SameValueBundles returns, as list entries, the other bundles of the
// project whose fingerprint is that of bundle id and which scanID found,
// ordered by ID. Customer data is left out, since it is decided by group.
// It returns no entries when the project has no bundle id.
func (s *Store) SameValueBundles(ctx context.Context, projectID project.ID, scanID scan.ID, id triage.BundleID) ([]triage.Entry, error) {
	var out []triage.Entry
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readEntries(ctx, tx, scanBundles+`
			SELECT sb.id, sb.category, sb.kind, sb.preview, sb.name, sb.file, sb.first_line, sb.verdict, sb.locations, 1,
				sb.shell, sb.applied
			FROM scan_bundle sb
				JOIN bundle o ON o.id = sb.id
				JOIN bundle me ON me.id = $3 AND me.project_id = $1
			WHERE o.fingerprint = me.fingerprint AND sb.id <> $3 AND sb.category <> 'customer-data'
			ORDER BY sb.id`, projectID, scanID, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("listing bundles of the same value as %d: %w", id, err)
	}
	return out, nil
}

// AppliedCount returns the number of the project's applied bundles.
func (s *Store) AppliedCount(ctx context.Context, projectID project.ID) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM bundle WHERE project_id = $1 AND applied_at IS NOT NULL",
		projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting applied bundles of project %d: %w", projectID, err)
	}
	return n, nil
}

// AppliedBundles returns one page of the project's applied bundles, newest
// apply first, triage.AppliedPageSize to a page. A page past the last gives
// the last and a page before the first the first. A place counts as changed
// when its apply item is applied, in any run of the project.
func (s *Store) AppliedBundles(ctx context.Context, projectID project.ID, page int) (triage.AppliedPage, error) {
	var p triage.AppliedPage
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM bundle WHERE project_id = $1 AND applied_at IS NOT NULL",
			projectID).Scan(&p.Total); err != nil {
			return fmt.Errorf("counting applied bundles of project %d: %w", projectID, err)
		}
		p.Pages = max(1, (p.Total+triage.AppliedPageSize-1)/triage.AppliedPageSize)
		p.Page = min(max(page, 1), p.Pages)
		rows, err := tx.Query(ctx, `
			WITH latest AS (
				SELECT id, requested_at FROM scan WHERE project_id = $1 AND status = 'succeeded'
				ORDER BY id DESC LIMIT 1
			)
			SELECT b.id, b.name, coalesce(f.category, ''), coalesce(f.kind, ''), coalesce(f.preview, ''),
				b.store_path, b.applied_at, coalesce(i.locations, 0), coalesce(i.shell, false), coalesce(i.config, false),
				coalesce(i.code_change, false), coalesce(i.last_run, 0),
				EXISTS (SELECT 1 FROM latest WHERE latest.requested_at > coalesce(i.last_start, b.applied_at)),
				EXISTS (SELECT 1 FROM latest
					JOIN location l ON l.scan_id = latest.id
					JOIN finding m ON m.id = l.finding_id
					WHERE m.bundle_id = b.id AND latest.requested_at > coalesce(i.last_start, b.applied_at))
			FROM bundle b
				LEFT JOIN LATERAL (
					SELECT category, kind, preview FROM finding WHERE bundle_id = b.id ORDER BY id LIMIT 1
				) f ON true
				LEFT JOIN LATERAL (
					SELECT count(*)::int AS locations,
						bool_or(lower(i.file_path) LIKE '%.sh') AS shell,
						bool_or(lower(i.file_path) NOT LIKE '%.sh') AS config,
						bool_or(i.code_change) AS code_change,
						max(i.apply_id) AS last_run,
						max(r.started_at) AS last_start
					FROM apply_item i JOIN apply_run r ON r.id = i.apply_id
					WHERE i.bundle_id = b.id AND r.project_id = $1 AND i.status = 'applied'
				) i ON true
			WHERE b.project_id = $1 AND b.applied_at IS NOT NULL
			ORDER BY b.applied_at DESC, b.id DESC
			LIMIT $2 OFFSET $3`, projectID, triage.AppliedPageSize, (p.Page-1)*triage.AppliedPageSize)
		if err != nil {
			return fmt.Errorf("listing applied bundles of project %d: %w", projectID, err)
		}
		p.Entries, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (triage.Applied, error) {
			var (
				a    triage.Applied
				name *string
			)
			err := row.Scan(&a.Bundle, &name, &a.Category, &a.Kind, &a.Preview, &a.StorePath, &a.AppliedAt, &a.Locations,
				&a.Shell, &a.Config, &a.CodeChange, &a.LastRun, &a.Checked, &a.Reappeared)
			if name != nil {
				a.Name = triage.Name(*name)
			}
			return a, err
		})
		if err != nil {
			return fmt.Errorf("listing applied bundles of project %d: %w", projectID, err)
		}
		return nil
	})
	if err != nil {
		return triage.AppliedPage{}, err
	}
	return p, nil
}
