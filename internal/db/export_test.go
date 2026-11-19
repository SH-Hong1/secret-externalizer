package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// NewTestStore returns a Store whose pool uses a schema of its own, created
// for this test and dropped when it ends. It skips the test when
// TEST_DATABASE_URL is not set.
func NewTestStore(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to run against postgres")
	}
	ctx := t.Context()

	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(admin.Close)

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	schema := "test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("CREATE SCHEMA %s error = %v", schema, err)
	}
	t.Cleanup(func() {
		// The test context is already cancelled during cleanup.
		if _, err := admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("DROP SCHEMA %s error = %v", schema, err)
		}
	})

	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() error = %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return &Store{pool: pool}
}

// NewTestStoreLike returns a second Store on the same schema as s.
func NewTestStoreLike(t *testing.T, s *Store) *Store {
	t.Helper()
	cfg := s.pool.Config()
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return &Store{pool: pool}
}

// Count returns the number of rows in the given table, which must be a
// constant of the test.
func (s *Store) Count(ctx context.Context, table string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n)
	return n, err
}

// MigrationCount returns the number of embedded migration files.
func MigrationCount() int {
	m, err := loadMigrations()
	if err != nil {
		panic(err)
	}
	return len(m)
}

// JobState returns the state of the job that runs the scan with the given ID.
func (s *Store) JobState(ctx context.Context, scanID int64) (string, error) {
	var state string
	err := s.pool.QueryRow(ctx, "SELECT state FROM job WHERE scan_id = $1", scanID).Scan(&state)
	return state, err
}

// FindingIDs are the identifying columns of a finding row.
type FindingIDs struct {
	ID, FirstScanID, LastScanID int64
}

// FindingRow returns the identifying columns of the finding of the project
// with the given fingerprint and file.
func (s *Store) FindingRow(ctx context.Context, projectID project.ID, fingerprint, file string) (FindingIDs, error) {
	var f FindingIDs
	err := s.pool.QueryRow(ctx,
		"SELECT id, first_scan_id, last_scan_id FROM finding WHERE project_id = $1 AND fingerprint = $2 AND file_path = $3",
		projectID, fingerprint, file).Scan(&f.ID, &f.FirstScanID, &f.LastScanID)
	return f, err
}

// JobRow is the lease columns of a job row.
type JobRow struct {
	State       string
	Attempts    int
	LockedBy    *string
	LockedUntil *time.Time
}

// Job returns the lease columns of the job that runs the scan with the given
// ID.
func (s *Store) Job(ctx context.Context, scanID int64) (JobRow, error) {
	var j JobRow
	err := s.pool.QueryRow(ctx, "SELECT state, attempts, locked_by, locked_until FROM job WHERE scan_id = $1", scanID).
		Scan(&j.State, &j.Attempts, &j.LockedBy, &j.LockedUntil)
	return j, err
}
