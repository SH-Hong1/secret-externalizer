package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is the key of the advisory lock that serializes Migrate
// across processes.
const migrationLockID int64 = 0x5345_4d49_4752 // "SEMIGR"

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the embedded files in version order.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q does not match NNNN_name.sql", e.Name())
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("reading version of %q: %w", e.Name(), err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migration files %q and %q share version %d", other, e.Name(), version)
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(migrationFiles, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies every embedded migration that has not been applied yet, in
// version order. Each file runs in one transaction together with the row in
// schema_migration that records it. An advisory lock makes concurrent callers,
// in this or another process, take turns.
func (s *Store) Migrate(ctx context.Context) (err error) {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	// The lock belongs to a session, so one connection holds it from lock to
	// unlock.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("taking migration lock: %w", err)
	}
	defer func() {
		// The unlock gets a context of its own so that a cancelled ctx does
		// not leave the lock held. If it fails, the connection is hijacked and
		// closed, which ends the session and drops the lock.
		unlockCtx := context.WithoutCancel(ctx)
		if _, uerr := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID); uerr != nil {
			err = errors.Join(err, fmt.Errorf("releasing migration lock: %w", uerr))
			if cerr := conn.Hijack().Close(unlockCtx); cerr != nil {
				err = errors.Join(err, fmt.Errorf("closing connection: %w", cerr))
			}
		}
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migration (
		version    integer PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("creating schema_migration: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[int]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migration")
	if err != nil {
		return nil, fmt.Errorf("reading schema_migration: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scanning schema_migration: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading schema_migration: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) (err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction for %s: %w", m.name, err)
	}
	defer func() {
		// Rolling back after a commit returns pgx.ErrTxClosed, which is not a
		// problem.
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rolling back %s: %w", m.name, rbErr))
		}
	}()
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("applying %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migration (version) VALUES ($1)", m.version); err != nil {
		return fmt.Errorf("recording %s: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing %s: %w", m.name, err)
	}
	return nil
}
