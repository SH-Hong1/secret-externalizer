package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SH-Hong1/secret-externalizer/internal/project"
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

// Projects returns every project ordered by name.
func (s *Store) Projects(ctx context.Context) ([]project.Project, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+projectColumns+" FROM project ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()
	var out []project.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("reading project row: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	return out, nil
}

// ProjectSummaries returns every project ordered by name, each with its
// newest scan and the bundle counts of its newest succeeded scan. A bundle
// counts when that scan found one of its members, as in Entries.
func (s *Store) ProjectSummaries(ctx context.Context) ([]project.Summary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, p.path, p.excludes, p.created_by, p.created_at,
			last.id, last.status, last.requested_at, ok.id IS NOT NULL,
			coalesce(c.undecided, 0)::int, coalesce(c.total, 0)::int
		FROM project p
		LEFT JOIN LATERAL (
			SELECT id, status, requested_at FROM scan WHERE project_id = p.id ORDER BY id DESC LIMIT 1
		) last ON true
		LEFT JOIN LATERAL (
			SELECT id FROM scan WHERE project_id = p.id AND status = 'succeeded' ORDER BY id DESC LIMIT 1
		) ok ON true
		LEFT JOIN LATERAL (
			SELECT count(*) FILTER (WHERE b.verdict = '') AS undecided, count(*) AS total
			FROM bundle b
			WHERE b.project_id = p.id AND EXISTS (
				SELECT 1 FROM finding f JOIN location l ON l.finding_id = f.id
				WHERE f.bundle_id = b.id AND l.scan_id = ok.id)
		) c ON true
		ORDER BY p.name`)
	if err != nil {
		return nil, fmt.Errorf("listing project summaries: %w", err)
	}
	defer rows.Close()
	var out []project.Summary
	for rows.Next() {
		var (
			sum    project.Summary
			p      = &sum.Project
			lastID *int64
			status *string
			at     *time.Time
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.Excludes, &p.CreatedBy, &p.CreatedAt,
			&lastID, &status, &at, &sum.HasResult, &sum.Undecided, &sum.Total); err != nil {
			return nil, fmt.Errorf("reading project summary row: %w", err)
		}
		if lastID != nil {
			sum.LastScanID, sum.LastScanStatus, sum.LastScanAt = *lastID, *status, *at
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing project summaries: %w", err)
	}
	return out, nil
}

func scanProject(row pgx.Row) (project.Project, error) {
	var p project.Project
	err := row.Scan(&p.ID, &p.Name, &p.Path, &p.Excludes, &p.CreatedBy, &p.CreatedAt)
	return p, err
}
