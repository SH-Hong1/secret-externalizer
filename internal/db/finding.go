package db

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// detection is how a finding.Detection is kept in location.detections.
type detection struct {
	Tool string `json:"tool"`
	Rule string `json:"rule"`
}

// SaveResult records a finished scan in one transaction: it checks that the
// worker holds the job, stores each finding and its locations, marks the
// scan succeeded with the counts, versions, skipped file paths and scanner
// error counts of r, and marks the job done.
// It returns job.ErrLeaseLost, storing nothing, when the worker does not hold
// the job, which is also the case when the result was already saved.
//
// It holds the project's row FOR NO KEY UPDATE while it stores the findings,
// so it waits for a Decide or MoveFinding of the project that has started and
// those wait for it. The findings and bundles it reads therefore stay as they
// were until it has stored the scan, and the two never wait on each other's
// finding and bundle rows. A scan can still be requested meanwhile.
//
// A finding is identified by project, fingerprint, file, and configuration
// key. A finding that an earlier scan stored keeps its ID, its bundle, and its
// first scan; its last scan becomes this one, and its category, kind,
// preview, and whether it has an original become what this scan found, since
// the rules or the masking may have changed in between. Locations belong to
// the scan that found them, so each scan adds its own.
func (s *Store) SaveResult(ctx context.Context, jobID int64, workerID string, id scan.ID, r scan.Result) error {
	return s.closeJob(ctx, jobID, workerID, id, "done", func(tx pgx.Tx) error {
		var projectID int64
		if err := tx.QueryRow(ctx, "SELECT project_id FROM scan WHERE id = $1", id).Scan(&projectID); err != nil {
			return fmt.Errorf("reading project of scan %d: %w", id, err)
		}
		if err := lockProject(ctx, tx, project.ID(projectID), "FOR NO KEY UPDATE"); err != nil {
			return err
		}
		ids, err := storeFindings(ctx, tx, projectID, id, r.Findings)
		if err != nil {
			return fmt.Errorf("storing findings of scan %d: %w", id, err)
		}
		if err := insertLocations(ctx, tx, id, ids, r.Findings); err != nil {
			return fmt.Errorf("storing locations of scan %d: %w", id, err)
		}
		toolVersions := r.ToolVersions
		if toolVersions == nil {
			toolVersions = map[string]string{}
		}
		skippedFiles := r.SkippedFiles
		if skippedFiles == nil {
			skippedFiles = []string{}
		}
		scannerErrors := r.ScannerErrors
		if scannerErrors == nil {
			scannerErrors = map[string]int{}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE scan SET status = 'succeeded', finished_at = now(), commit_hash = $2, rules_version = $3,
				tool_versions = $4, files = $5, skipped = $6, skipped_files = $7, scanner_errors = $8,
				dropped = $9, findings = $10
			WHERE id = $1`,
			id, r.CommitHash, r.RulesVersion, toolVersions, r.Files, r.Skipped, skippedFiles, scannerErrors,
			r.Dropped, len(r.Findings)); err != nil {
			return fmt.Errorf("finishing scan %d: %w", id, err)
		}
		return nil
	})
}

// saveBatch is the number of rows SaveResult sends to the database in one
// round trip.
const saveBatch = 1000

// storeFindings stores the findings of scan id of the project, in the rows
// and bundles planFindings decides, and returns the ID of each finding's
// row in the order of findings. It reads the project's findings and its
// shared and frontend bundles once and writes saveBatch rows in a round
// trip, so a finding does not cost round trips of its own. The caller
// holds the project's lock, so the rows it read stay as they were until it
// has written.
//
// A finding that an earlier scan stored keeps its ID, its bundle, and its
// first scan. A keyed finding that takes over a keyless row gives the row its
// key, so a finding stored before keys were read keeps its ID, and with it
// its bundle, decision and apply records, under the first key of its file
// that a scan reads.
func storeFindings(ctx context.Context, tx pgx.Tx, projectID int64, id scan.ID, findings []finding.Finding) ([]int64, error) {
	stored, err := readStoredFindings(ctx, tx, projectID, id)
	if err != nil {
		return nil, err
	}
	bundles, err := readAutoBundles(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	plan := planFindings(stored, bundles, findings)

	bundleIDs := make([]int64, 0, len(plan.bundles))
	for chunk := range slices.Chunk(plan.bundles, saveBatch) {
		created, err := insertBundles(ctx, tx, projectID, chunk)
		if err != nil {
			return nil, err
		}
		bundleIDs = append(bundleIDs, created...)
	}

	// A row that is taken over gets its key before any row is inserted, so
	// that a new keyless finding of the same file does not meet it.
	var updates, inserts []int
	rowIDs := make([]int64, len(plan.rows))
	for i, r := range plan.rows {
		if r.id == 0 {
			inserts = append(inserts, i)
			continue
		}
		rowIDs[i] = r.id
		updates = append(updates, i)
	}
	for chunk := range slices.Chunk(updates, saveBatch) {
		if err := updateFindings(ctx, tx, id, plan.rows, chunk); err != nil {
			return nil, err
		}
	}
	for chunk := range slices.Chunk(inserts, saveBatch) {
		if err := insertFindings(ctx, tx, projectID, id, plan.rows, chunk, bundleIDs, rowIDs); err != nil {
			return nil, err
		}
	}

	ids := make([]int64, len(findings))
	for i, slot := range plan.slots {
		ids[i] = rowIDs[slot]
	}
	return ids, nil
}

// insertBundles creates bundles in the project, in one statement, and
// returns their IDs in the order of bundles. The statement does not promise
// the order of the IDs it returns. Bundles of one fingerprint and origin are
// alike until a finding joins them, so each takes the lowest ID left among
// those of its fingerprint and origin.
func insertBundles(ctx context.Context, tx pgx.Tx, projectID int64, bundles []plannedBundle) ([]int64, error) {
	fingerprints := make([]string, len(bundles))
	origins := make([]string, len(bundles))
	for i, b := range bundles {
		fingerprints[i], origins[i] = b.fingerprint, b.origin
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO bundle (project_id, fingerprint, origin)
		SELECT $1, u.fingerprint, u.origin FROM unnest($2::text[], $3::text[]) AS u (fingerprint, origin)
		RETURNING id, fingerprint, origin`,
		projectID, fingerprints, origins)
	if err != nil {
		return nil, fmt.Errorf("creating bundles: %w", err)
	}
	defer rows.Close()
	created := make(map[plannedBundle][]int64, len(bundles))
	for rows.Next() {
		var (
			id int64
			b  plannedBundle
		)
		if err := rows.Scan(&id, &b.fingerprint, &b.origin); err != nil {
			return nil, fmt.Errorf("reading created bundle: %w", err)
		}
		created[b] = append(created[b], id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("creating bundles: %w", err)
	}
	for _, ids := range created {
		slices.Sort(ids)
	}
	out := make([]int64, len(bundles))
	for i, b := range bundles {
		out[i], created[b] = created[b][0], created[b][1:]
	}
	return out, nil
}

// findingColumns holds the values of some finding rows, one slice a column,
// as a statement takes them to unnest.
type findingColumns struct {
	fingerprints, files, keys, categories, kinds, previews []string
	hasOriginal                                            []bool
}

// columnsOf returns the columns of the rows of rows that chunk indexes.
func columnsOf(rows []plannedRow, chunk []int) findingColumns {
	var c findingColumns
	for _, i := range chunk {
		f := rows[i].finding
		c.fingerprints = append(c.fingerprints, f.Fingerprint)
		c.files = append(c.files, f.File)
		c.keys = append(c.keys, f.Key)
		c.categories = append(c.categories, string(f.Category))
		c.kinds = append(c.kinds, string(f.Kind))
		c.previews = append(c.previews, f.Preview)
		c.hasOriginal = append(c.hasOriginal, f.HasOriginal)
	}
	return c
}

// updateFindings gives the stored rows of rows that chunk indexes their key
// and the values scan id found, in one statement.
func updateFindings(ctx context.Context, tx pgx.Tx, id scan.ID, rows []plannedRow, chunk []int) error {
	ids := make([]int64, len(chunk))
	for n, i := range chunk {
		ids[n] = rows[i].id
	}
	c := columnsOf(rows, chunk)
	_, err := tx.Exec(ctx, `
		UPDATE finding f SET key = u.key, category = u.category, kind = u.kind, preview = u.preview,
			has_original = u.has_original, last_scan_id = $1
		FROM unnest($2::bigint[], $3::text[], $4::text[], $5::text[], $6::text[], $7::boolean[])
			AS u (id, key, category, kind, preview, has_original)
		WHERE f.id = u.id`,
		id, ids, c.keys, c.categories, c.kinds, c.previews, c.hasOriginal)
	if err != nil {
		return fmt.Errorf("updating findings: %w", err)
	}
	return nil
}

// insertFindings inserts the rows of rows that chunk indexes as findings of
// the project first found by scan id, in one statement, and puts the ID of
// each in rowIDs at the row's index. bundleIDs are the IDs of the bundles the
// plan created. The statement does not promise the order of the IDs it
// returns, so each is matched to its row by the finding's identity.
func insertFindings(ctx context.Context, tx pgx.Tx, projectID int64, id scan.ID, rows []plannedRow, chunk []int, bundleIDs, rowIDs []int64) error {
	bundles := make([]int64, len(chunk))
	index := make(map[identity]int, len(chunk))
	for n, i := range chunk {
		r := rows[i]
		bundles[n] = r.bundle.id
		if r.bundle.planned != 0 {
			bundles[n] = bundleIDs[r.bundle.planned-1]
		}
		index[identity{fileOfValue{r.finding.Fingerprint, r.finding.File}, r.finding.Key}] = i
	}
	c := columnsOf(rows, chunk)
	inserted, err := tx.Query(ctx, `
		INSERT INTO finding (project_id, fingerprint, file_path, key, category, kind, preview, has_original,
			first_scan_id, last_scan_id, bundle_id)
		SELECT $1, u.fingerprint, u.file_path, u.key, u.category, u.kind, u.preview, u.has_original, $2, $2, u.bundle_id
		FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::boolean[], $10::bigint[])
			AS u (fingerprint, file_path, key, category, kind, preview, has_original, bundle_id)
		RETURNING id, fingerprint, file_path, key`,
		projectID, id, c.fingerprints, c.files, c.keys, c.categories, c.kinds, c.previews, c.hasOriginal, bundles)
	if err != nil {
		return fmt.Errorf("inserting findings: %w", err)
	}
	defer inserted.Close()
	for inserted.Next() {
		var (
			rowID int64
			of    identity
		)
		if err := inserted.Scan(&rowID, &of.fingerprint, &of.file, &of.key); err != nil {
			return fmt.Errorf("reading inserted finding: %w", err)
		}
		rowIDs[index[of]] = rowID
	}
	if err := inserted.Err(); err != nil {
		return fmt.Errorf("inserting findings: %w", err)
	}
	return nil
}

// readStoredFindings returns every finding of the project, each marked as
// seen when scan id stored it.
func readStoredFindings(ctx context.Context, tx pgx.Tx, projectID int64, id scan.ID) ([]storedFinding, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, fingerprint, file_path, key, bundle_id, last_scan_id = $2
		FROM finding WHERE project_id = $1`, projectID, id)
	if err != nil {
		return nil, fmt.Errorf("reading stored findings: %w", err)
	}
	defer rows.Close()
	var out []storedFinding
	for rows.Next() {
		var f storedFinding
		if err := rows.Scan(&f.id, &f.fingerprint, &f.file, &f.key, &f.bundle, &f.seen); err != nil {
			return nil, fmt.Errorf("reading stored finding: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading stored findings: %w", err)
	}
	return out, nil
}

// readAutoBundles returns the shared and frontend bundles of the project.
func readAutoBundles(ctx context.Context, tx pgx.Tx, projectID int64) ([]autoBundle, error) {
	rows, err := tx.Query(ctx,
		"SELECT id, fingerprint, origin FROM bundle WHERE project_id = $1 AND origin <> $2", projectID, originOwn)
	if err != nil {
		return nil, fmt.Errorf("reading bundles: %w", err)
	}
	defer rows.Close()
	var out []autoBundle
	for rows.Next() {
		var b autoBundle
		if err := rows.Scan(&b.id, &b.fingerprint, &b.origin); err != nil {
			return nil, fmt.Errorf("reading bundle: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading bundles: %w", err)
	}
	return out, nil
}

// insertLocations adds the locations the scan found for findings, whose
// stored IDs are ids in the same order. It copies them to the database in
// one stream, so their number adds no round trips.
func insertLocations(ctx context.Context, tx pgx.Tx, id scan.ID, ids []int64, findings []finding.Finding) error {
	i, next := 0, 0
	source := pgx.CopyFromFunc(func() ([]any, error) {
		for i < len(findings) && next == len(findings[i].Locations) {
			i, next = i+1, 0
		}
		if i == len(findings) {
			return nil, nil
		}
		l := findings[i].Locations[next]
		next++
		detections := make([]detection, 0, len(l.Detections))
		for _, d := range l.Detections {
			detections = append(detections, detection(d))
		}
		encoded, err := json.Marshal(detections)
		if err != nil {
			return nil, fmt.Errorf("encoding detections: %w", err)
		}
		lines := l.Context
		if lines == nil {
			lines = []string{}
		}
		return []any{
			ids[i], id, l.StartLine, l.StartCol, l.EndLine, l.EndCol, lines, l.ContextFirstLine, l.FileHash, encoded,
		}, nil
	})
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"location"}, []string{
		"finding_id", "scan_id", "start_line", "start_col", "end_line", "end_col",
		"context", "context_first_line", "file_hash", "detections",
	}, source)
	if err != nil {
		return fmt.Errorf("inserting locations: %w", err)
	}
	return nil
}

// locationColumns are the columns of location l that scanLocation reads.
const locationColumns = `l.start_line, l.start_col, l.end_line, l.end_col, l.context, l.context_first_line,
	l.file_hash, l.detections`

// scanLocation scans the current row into dest followed by the
// locationColumns, and returns the location.
func scanLocation(rows pgx.Rows, dest ...any) (finding.Location, error) {
	var (
		l          finding.Location
		detections []byte
	)
	dest = append(dest, &l.StartLine, &l.StartCol, &l.EndLine, &l.EndCol, &l.Context, &l.ContextFirstLine, &l.FileHash, &detections)
	if err := rows.Scan(dest...); err != nil {
		return finding.Location{}, fmt.Errorf("reading location row: %w", err)
	}
	var ds []detection
	if err := json.Unmarshal(detections, &ds); err != nil {
		return finding.Location{}, fmt.Errorf("decoding detections: %w", err)
	}
	for _, d := range ds {
		l.Detections = append(l.Detections, finding.Detection(d))
	}
	if len(l.Context) == 0 {
		l.Context = nil
	}
	return l, nil
}

// FindingsByScan returns the findings the scan found, each with its ID and
// the locations of that scan only. Findings are ordered by file and then by
// their first location; locations by line and column.
func (s *Store) FindingsByScan(ctx context.Context, id scan.ID) ([]finding.Finding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.fingerprint, f.file_path, f.key, f.category, f.kind, f.preview, f.has_original,
			`+locationColumns+`
		FROM location l JOIN finding f ON f.id = l.finding_id
		WHERE l.scan_id = $1
		ORDER BY f.file_path, l.start_line, l.start_col, f.id, l.id`, id)
	if err != nil {
		return nil, fmt.Errorf("listing findings of scan %d: %w", id, err)
	}
	defer rows.Close()

	var out []finding.Finding
	index := map[finding.ID]int{}
	for rows.Next() {
		var f finding.Finding
		l, err := scanLocation(rows, &f.ID, &f.Fingerprint, &f.File, &f.Key, &f.Category, &f.Kind, &f.Preview, &f.HasOriginal)
		if err != nil {
			return nil, err
		}
		i, seen := index[f.ID]
		if !seen {
			i = len(out)
			index[f.ID] = i
			out = append(out, f)
		}
		out[i].Locations = append(out[i].Locations, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing findings of scan %d: %w", id, err)
	}
	return out, nil
}

// CategoryCounts returns, for each category the scan found something in,
// the number of findings and of their locations in that scan, ordered by
// category. A scan that found nothing, or that does not exist, gives none.
func (s *Store) CategoryCounts(ctx context.Context, id scan.ID) ([]scan.CategoryCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.category, count(DISTINCT f.id), count(*)
		FROM location l JOIN finding f ON f.id = l.finding_id
		WHERE l.scan_id = $1
		GROUP BY f.category
		ORDER BY f.category`, id)
	if err != nil {
		return nil, fmt.Errorf("counting findings of scan %d: %w", id, err)
	}
	defer rows.Close()
	var out []scan.CategoryCount
	for rows.Next() {
		var c scan.CategoryCount
		if err := rows.Scan(&c.Category, &c.Findings, &c.Locations); err != nil {
			return nil, fmt.Errorf("reading category count: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting findings of scan %d: %w", id, err)
	}
	return out, nil
}

// Reappeared returns the number of findings the scan found that are in a
// bundle an apply had stored the value of before the scan was requested:
// values of applied bundles that the scan still finds in the source,
// whether at a place an apply replaced or at one it never replaced (a
// member the apply skipped or that was found after it). A bundle applied
// after the scan was requested does not count, so the number of a scan
// never changes. A bundle's apply time is set once, by its first apply.
func (s *Store) Reappeared(ctx context.Context, id scan.ID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT f.id)
		FROM scan s
			JOIN location l ON l.scan_id = s.id
			JOIN finding f ON f.id = l.finding_id
			JOIN bundle b ON b.id = f.bundle_id
		WHERE s.id = $1 AND b.applied_at < s.requested_at`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting reappeared values of scan %d: %w", id, err)
	}
	return n, nil
}
