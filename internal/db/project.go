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
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

var (
	// ErrDuplicate means a row with the same unique value already exists.
	ErrDuplicate = errors.New("already exists")
	// ErrNotFound means no row matches.
	ErrNotFound = errors.New("not found")
)

// uniqueViolation is the postgres error code for a unique constraint
// violation.
const uniqueViolation = "23505"

const projectColumns = "id, name, path, excludes, created_by, created_at"

// CreateProject stores p and returns its ID. It returns ErrDuplicate when a
// project with the same name or path exists. The ID and CreatedAt of p are
// ignored.
func (s *Store) CreateProject(ctx context.Context, p project.Project) (project.ID, error) {
	excludes := p.Excludes
	if excludes == nil {
		excludes = []string{}
	}
	var id project.ID
	err := s.pool.QueryRow(ctx,
		"INSERT INTO project (name, path, excludes, created_by) VALUES ($1, $2, $3, $4) RETURNING id",
		p.Name, p.Path, excludes, p.CreatedBy).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return 0, fmt.Errorf("creating project: %w", ErrDuplicate)
	}
	if err != nil {
		return 0, fmt.Errorf("creating project: %w", err)
	}
	return id, nil
}

// Project returns the project with the given ID, or ErrNotFound.
func (s *Store) Project(ctx context.Context, id project.ID) (project.Project, error) {
	row := s.pool.QueryRow(ctx, "SELECT "+projectColumns+" FROM project WHERE id = $1", id)
	p, err := scanProject(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Project{}, fmt.Errorf("reading project %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return project.Project{}, fmt.Errorf("reading project %d: %w", id, err)
	}
	return p, nil
}

// ProjectSummaries returns every project ordered by name, each with its
// newest scan and, for its newest succeeded scan, the lines of the triage
// list without a verdict and in all, counted as VerdictCounts counts them.
func (s *Store) ProjectSummaries(ctx context.Context) ([]project.Summary, error) {
	var out []project.Summary
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		var (
			okScans []scan.ID
			err     error
		)
		out, okScans, err = readSummaries(ctx, tx)
		if err != nil {
			return err
		}
		for i := range out {
			if !out[i].HasResult {
				continue
			}
			counts := map[triage.Verdict]int{}
			if err := readCounts(ctx, tx, out[i].Project.ID, okScans[i], counts); err != nil {
				return err
			}
			for v, n := range counts {
				out[i].Total += n
				if v == "" {
					out[i].Undecided = n
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// readSummaries returns every project ordered by name with its newest scan,
// and the ID of each one's newest succeeded scan, zero when there is none.
func readSummaries(ctx context.Context, tx pgx.Tx) ([]project.Summary, []scan.ID, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.name, p.path, p.excludes, p.created_by, p.created_at,
			last.id, last.status, last.requested_at, ok.id
		FROM project p
		LEFT JOIN LATERAL (
			SELECT id, status, requested_at FROM scan WHERE project_id = p.id ORDER BY id DESC LIMIT 1
		) last ON true
		LEFT JOIN LATERAL (
			SELECT id FROM scan WHERE project_id = p.id AND status = 'succeeded' ORDER BY id DESC LIMIT 1
		) ok ON true
		ORDER BY p.name`)
	if err != nil {
		return nil, nil, fmt.Errorf("listing project summaries: %w", err)
	}
	defer rows.Close()
	var (
		out     []project.Summary
		okScans []scan.ID
	)
	for rows.Next() {
		var (
			sum    project.Summary
			p      = &sum.Project
			lastID *int64
			status *string
			at     *time.Time
			okID   *int64
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.Excludes, &p.CreatedBy, &p.CreatedAt,
			&lastID, &status, &at, &okID); err != nil {
			return nil, nil, fmt.Errorf("reading project summary row: %w", err)
		}
		if lastID != nil {
			sum.LastScanID, sum.LastScanStatus, sum.LastScanAt = *lastID, *status, *at
		}
		var ok scan.ID
		if okID != nil {
			sum.HasResult, ok = true, scan.ID(*okID)
		}
		out = append(out, sum)
		okScans = append(okScans, ok)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("listing project summaries: %w", err)
	}
	return out, okScans, nil
}

func scanProject(row pgx.Row) (project.Project, error) {
	var p project.Project
	err := row.Scan(&p.ID, &p.Name, &p.Path, &p.Excludes, &p.CreatedBy, &p.CreatedAt)
	return p, err
}
