package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
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
// A finding is keyed by project, fingerprint, and file. A finding that an
// earlier scan stored keeps its ID, its bundle, and its first scan; its last scan becomes
// this one, and its category, kind, preview, and whether it has an original
// become what this scan found, since the rules or the masking may have
// changed in between. Locations belong to the scan that found them, so each
// scan adds its own.
func (s *Store) SaveResult(ctx context.Context, jobID int64, workerID string, id scan.ID, r scan.Result) error {
	return s.closeJob(ctx, jobID, workerID, id, "done", func(tx pgx.Tx) error {
		var projectID int64
		if err := tx.QueryRow(ctx, "SELECT project_id FROM scan WHERE id = $1", id).Scan(&projectID); err != nil {
			return fmt.Errorf("reading project of scan %d: %w", id, err)
		}
		for i, f := range r.Findings {
			if err := insertFinding(ctx, tx, projectID, id, f); err != nil {
				return fmt.Errorf("storing finding %d of scan %d: %w", i, id, err)
			}
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

// insertFinding stores f, or updates it when an earlier scan stored it, and
// adds its locations for the scan. A finding stored for the first time joins
// the bundle bundleForNew gives it; a stored finding keeps its bundle.
func insertFinding(ctx context.Context, tx pgx.Tx, projectID int64, id scan.ID, f finding.Finding) error {
	var findingID int64
	err := tx.QueryRow(ctx, `
		UPDATE finding SET category = $4, kind = $5, preview = $6, has_original = $7, last_scan_id = $8
		WHERE project_id = $1 AND fingerprint = $2 AND file_path = $3
		RETURNING id`,
		projectID, f.Fingerprint, f.File, f.Category, f.Kind, f.Preview, f.HasOriginal, id).Scan(&findingID)
	if errors.Is(err, pgx.ErrNoRows) {
		bundleID, berr := bundleForNew(ctx, tx, projectID, f)
		if berr != nil {
			return berr
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO finding (project_id, fingerprint, file_path, category, kind, preview, has_original,
				first_scan_id, last_scan_id, bundle_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9)
			RETURNING id`,
			projectID, f.Fingerprint, f.File, f.Category, f.Kind, f.Preview, f.HasOriginal, id, bundleID).Scan(&findingID)
	}
	if err != nil {
		return fmt.Errorf("storing finding: %w", err)
	}
	batch := &pgx.Batch{}
	for _, l := range f.Locations {
		detections := make([]detection, 0, len(l.Detections))
		for _, d := range l.Detections {
			detections = append(detections, detection(d))
		}
		encoded, err := json.Marshal(detections)
		if err != nil {
			return fmt.Errorf("encoding detections: %w", err)
		}
		lines := l.Context
		if lines == nil {
			lines = []string{}
		}
		batch.Queue(`
			INSERT INTO location (finding_id, scan_id, start_line, start_col, end_line, end_col,
				context, context_first_line, file_hash, detections)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			findingID, id, l.StartLine, l.StartCol, l.EndLine, l.EndCol, lines, l.ContextFirstLine, l.FileHash, encoded)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
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
		SELECT f.id, f.fingerprint, f.file_path, f.category, f.kind, f.preview, f.has_original,
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
		l, err := scanLocation(rows, &f.ID, &f.Fingerprint, &f.File, &f.Category, &f.Kind, &f.Preview, &f.HasOriginal)
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
// bundle an apply externalized: values an apply replaced that the scan
// still finds in the source.
func (s *Store) Reappeared(ctx context.Context, id scan.ID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT f.id)
		FROM location l JOIN finding f ON f.id = l.finding_id JOIN bundle b ON b.id = f.bundle_id
		WHERE l.scan_id = $1 AND b.applied_at IS NOT NULL`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting reappeared values of scan %d: %w", id, err)
	}
	return n, nil
}
