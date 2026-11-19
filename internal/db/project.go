package db

import (
	"context"
	"errors"
	"fmt"

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

func scanProject(row pgx.Row) (project.Project, error) {
	var p project.Project
	err := row.Scan(&p.ID, &p.Name, &p.Path, &p.Excludes, &p.CreatedBy, &p.CreatedAt)
	return p, err
}
