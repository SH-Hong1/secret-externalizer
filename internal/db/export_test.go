package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
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
	m, err := loadMigrations(migrationFiles)
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

// MigrateFrom runs Migrate with the migration files under migrations/ in
// fsys in place of the embedded ones.
func (s *Store) MigrateFrom(ctx context.Context, fsys fs.FS) error {
	return s.migrate(ctx, fsys)
}

// MigrateThrough applies the embedded migrations up to and including
// version.
func (s *Store) MigrateThrough(ctx context.Context, version int) error {
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	fsys := fstest.MapFS{}
	for _, m := range all {
		if m.version <= version {
			fsys["migrations/"+m.name] = &fstest.MapFile{Data: []byte(m.sql)}
		}
	}
	return s.migrate(ctx, fsys)
}

// InsertRawFinding stores a finding of the scan with one location at line,
// with SQL that needs only the schema of migration 0004.
func (s *Store) InsertRawFinding(ctx context.Context, projectID project.ID, scanID int64, f finding.Finding, line int) error {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO finding (project_id, fingerprint, file_path, category, kind, preview, has_original, first_scan_id, last_scan_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8) RETURNING id`,
		projectID, f.Fingerprint, f.File, f.Category, f.Kind, f.Preview, f.HasOriginal, scanID).Scan(&id)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO location (finding_id, scan_id, start_line, start_col, end_line, end_col, file_hash, detections)
		VALUES ($1, $2, $3, 1, $3, 4, $4, '[]')`, id, scanID, line, strings.Repeat("f", 64))
	return err
}

// BundleOf returns the bundle of the finding of the project with the given
// fingerprint and file.
func (s *Store) BundleOf(ctx context.Context, projectID project.ID, fingerprint, file string) (triage.BundleID, error) {
	var id triage.BundleID
	err := s.pool.QueryRow(ctx,
		"SELECT bundle_id FROM finding WHERE project_id = $1 AND fingerprint = $2 AND file_path = $3",
		projectID, fingerprint, file).Scan(&id)
	return id, err
}

// LastAuditEvent returns the newest row of audit_event.
func (s *Store) LastAuditEvent(ctx context.Context) (audit.Event, error) {
	var (
		e                   audit.Event
		bundleID, findingID *int64
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, at, actor_subject, actor_name, action, project_id, bundle_id, finding_id, detail
		FROM audit_event ORDER BY id DESC LIMIT 1`).
		Scan(&e.ID, &e.At, &e.Actor.Subject, &e.Actor.Name, &e.Action, &e.ProjectID, &bundleID, &findingID, &e.Detail)
	if bundleID != nil {
		e.BundleID = triage.BundleID(*bundleID)
	}
	if findingID != nil {
		e.FindingID = finding.ID(*findingID)
	}
	return e, err
}
