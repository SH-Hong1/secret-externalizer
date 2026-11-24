package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
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
// with the given fingerprint and file and no key.
func (s *Store) FindingRow(ctx context.Context, projectID project.ID, fingerprint, file string) (FindingIDs, error) {
	return s.FindingRowOfKey(ctx, projectID, fingerprint, file, "")
}

// FindingRowOfKey returns the identifying columns of the finding of the
// project with the given fingerprint, file and key.
func (s *Store) FindingRowOfKey(ctx context.Context, projectID project.ID, fingerprint, file, key string) (FindingIDs, error) {
	var f FindingIDs
	err := s.pool.QueryRow(ctx, `
		SELECT id, first_scan_id, last_scan_id FROM finding
		WHERE project_id = $1 AND fingerprint = $2 AND file_path = $3 AND key = $4`,
		projectID, fingerprint, file, key).Scan(&f.ID, &f.FirstScanID, &f.LastScanID)
	return f, err
}

// CopyFinding stores a copy of the finding with the given ID under key, with
// no locations.
func (s *Store) CopyFinding(ctx context.Context, id int64, key string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO finding (project_id, fingerprint, file_path, key, category, kind, preview, has_original,
			first_scan_id, last_scan_id, bundle_id)
		SELECT project_id, fingerprint, file_path, $2, category, kind, preview, has_original,
			first_scan_id, last_scan_id, bundle_id
		FROM finding WHERE id = $1`, id, key)
	return err
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

// InsertRawScan stores a queued scan of the project requested by
// fake-subject, with SQL that needs only the schema of migration 0002.
func (s *Store) InsertRawScan(ctx context.Context, projectID project.ID) (scan.ID, error) {
	var id scan.ID
	err := s.pool.QueryRow(ctx,
		"INSERT INTO scan (project_id, requested_by) VALUES ($1, 'fake-subject') RETURNING id", projectID).Scan(&id)
	return id, err
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
// fingerprint and file and no key.
func (s *Store) BundleOf(ctx context.Context, projectID project.ID, fingerprint, file string) (triage.BundleID, error) {
	return s.BundleOfKey(ctx, projectID, fingerprint, file, "")
}

// BundleOfKey returns the bundle of the finding of the project with the
// given fingerprint, file and key.
func (s *Store) BundleOfKey(ctx context.Context, projectID project.ID, fingerprint, file, key string) (triage.BundleID, error) {
	var id triage.BundleID
	err := s.pool.QueryRow(ctx,
		"SELECT bundle_id FROM finding WHERE project_id = $1 AND fingerprint = $2 AND file_path = $3 AND key = $4",
		projectID, fingerprint, file, key).Scan(&id)
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

// InsertRawScanJob stores a scan of the project with the given status and a
// job for it in jobState, with SQL that needs only the schema of migration
// 0002. A running job has a lease that runs for another hour.
func (s *Store) InsertRawScanJob(ctx context.Context, projectID project.ID, status, jobState string) (scan.ID, error) {
	var id scan.ID
	err := s.pool.QueryRow(ctx,
		"INSERT INTO scan (project_id, requested_by, status) VALUES ($1, 'fake-subject', $2) RETURNING id",
		projectID, status).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO job (scan_id, state, attempts, locked_by, locked_until)
		SELECT $1, $2::text, CASE WHEN $2::text = 'queued' THEN 0 ELSE 1 END,
			CASE WHEN $2::text = 'running' THEN 'fake-worker' END,
			CASE WHEN $2::text = 'running' THEN now() + interval '1 hour' END`,
		id, jobState)
	return id, err
}

// JobKind returns the kind of the job that runs the scan with the given ID.
func (s *Store) JobKind(ctx context.Context, scanID int64) (string, error) {
	var kind string
	err := s.pool.QueryRow(ctx, "SELECT kind FROM job WHERE scan_id = $1", scanID).Scan(&kind)
	return kind, err
}

// InsertApplyRun stores an apply run of the project for the scan, in state,
// with a fake plan digest. A run in state running has a start time.
func (s *Store) InsertApplyRun(ctx context.Context, projectID project.ID, scanID scan.ID, state string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO apply_run (project_id, scan_id, state, plan_digest, requested_by, requester_name, started_at)
		VALUES ($1, $2, $3, $4, 'fake-subject', 'fake-user', CASE WHEN $3::text = 'running' THEN now() END)
		RETURNING id`,
		projectID, scanID, state, strings.Repeat("a", 64)).Scan(&id)
	return id, err
}

// InsertApplyJob queues a job of the given kind for the apply run.
func (s *Store) InsertApplyJob(ctx context.Context, applyID int64, kind string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		"INSERT INTO job (kind, apply_id) VALUES ($1, $2) RETURNING id", kind, applyID).Scan(&id)
	return id, err
}

// ApplyRunRow is the state columns of an apply_run row.
type ApplyRunRow struct {
	State         string
	FailureReason string
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

// ApplyRunState returns the state columns of the apply run with the given ID.
func (s *Store) ApplyRunState(ctx context.Context, id int64) (ApplyRunRow, error) {
	var r ApplyRunRow
	err := s.pool.QueryRow(ctx,
		"SELECT state, failure_reason, started_at, finished_at FROM apply_run WHERE id = $1", id).
		Scan(&r.State, &r.FailureReason, &r.StartedAt, &r.FinishedAt)
	return r, err
}

// ApplyJob returns the lease columns of the job of the given kind for the
// apply run.
func (s *Store) ApplyJob(ctx context.Context, applyID int64, kind string) (JobRow, error) {
	var j JobRow
	err := s.pool.QueryRow(ctx,
		"SELECT state, attempts, locked_by, locked_until FROM job WHERE apply_id = $1 AND kind = $2", applyID, kind).
		Scan(&j.State, &j.Attempts, &j.LockedBy, &j.LockedUntil)
	return j, err
}

// InsertApplyRecords stores, for the apply run, one apply_item in status
// writing and one apply_file that is not written yet, both for file.
func (s *Store) InsertApplyRecords(ctx context.Context, applyID int64, file string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO apply_item (apply_id, bundle_id, finding_id, file_path, start_line, start_col, action, category, kind, status)
		VALUES ($1, 1, 1, $2, 1, 1, 'replace', 'other-secret', 'password', 'writing')`, applyID, file); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO apply_file (apply_id, file_path, before_hash, after_hash, temp_name)
		VALUES ($1, $2, $3, $4, $2 || '.se-apply-0000000000000000.tmp')`,
		applyID, file, strings.Repeat("b", 64), strings.Repeat("c", 64))
	return err
}

// ApplyRecords returns the status of the apply_item rows and the written
// flag of the apply_file rows of the apply run, in file order.
func (s *Store) ApplyRecords(ctx context.Context, applyID int64) (statuses []string, written []bool, err error) {
	rows, err := s.pool.Query(ctx, "SELECT status FROM apply_item WHERE apply_id = $1 ORDER BY file_path", applyID)
	if err != nil {
		return nil, nil, err
	}
	statuses, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	rows, err = s.pool.Query(ctx, "SELECT written FROM apply_file WHERE apply_id = $1 ORDER BY file_path", applyID)
	if err != nil {
		return nil, nil, err
	}
	written, err = pgx.CollectRows(rows, pgx.RowTo[bool])
	return statuses, written, err
}

// SetPrevAfterHash stores hash as the prev_after_hash of the apply_file
// row of the run for file.
func (s *Store) SetPrevAfterHash(ctx context.Context, applyID int64, file, hash string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE apply_file SET prev_after_hash = $3 WHERE apply_id = $1 AND file_path = $2", applyID, file, hash)
	return err
}

// CloseApplyJob runs closeApplyJob.
func (s *Store) CloseApplyJob(ctx context.Context, jobID int64, workerID string, applyID int64, kind job.Kind, state string, write func(pgx.Tx) error) error {
	return s.closeApplyJob(ctx, jobID, workerID, applyID, kind, state, write)
}

// InsertScanJob queues a job for the existing scan with the given ID.
func (s *Store) InsertScanJob(ctx context.Context, scanID scan.ID) error {
	_, err := s.pool.Exec(ctx, "INSERT INTO job (scan_id) VALUES ($1)", scanID)
	return err
}

// ExhaustJobs makes every job one whose lease has run out after its last
// attempt, so the next ClaimJob gives them all up in one statement.
func (s *Store) ExhaustJobs(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE job SET state = 'running', attempts = 3, locked_by = 'fake-worker',
			locked_until = now() - interval '1 second'`)
	return err
}

// ScanStatus returns the status and failure reason of the scan with the
// given ID.
func (s *Store) ScanStatus(ctx context.Context, id scan.ID) (status, reason string, err error) {
	err = s.pool.QueryRow(ctx, "SELECT status, failure_reason FROM scan WHERE id = $1", id).Scan(&status, &reason)
	return status, reason, err
}

// SetApplyState puts the apply run with the given ID in state and, unless
// the state is checking, marks its apply-check job done, as a finished check
// would.
func (s *Store) SetApplyState(ctx context.Context, id apply.ID, state apply.State) error {
	if _, err := s.pool.Exec(ctx, "UPDATE apply_run SET state = $2 WHERE id = $1", id, state); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		"UPDATE job SET state = 'done' WHERE apply_id = $1 AND kind = 'apply-check' AND $2::text <> 'checking'", id, state)
	return err
}

// MoveApplyRun puts the apply run with the given ID in state and changes
// nothing else, its jobs included.
func (s *Store) MoveApplyRun(ctx context.Context, id apply.ID, state apply.State) error {
	_, err := s.pool.Exec(ctx, "UPDATE apply_run SET state = $2 WHERE id = $1", id, state)
	return err
}

// SetApplyRun stores every column of r but its ID, project, scan and
// requester on the apply run r.ID, as a check and an apply would.
func (s *Store) SetApplyRun(ctx context.Context, r apply.Run) error {
	blockers := make([]string, 0, len(r.Blockers))
	for _, b := range r.Blockers {
		blockers = append(blockers, string(b))
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE apply_run SET state = $2, plan_digest = $3, requested_at = $4, checked_at = $5, confirmed_at = $6,
			started_at = $7, finished_at = $8, allow_irreversible = $9, irreversible_files = $10, blockers = $11,
			dirty_files = $12, dirty_count = $13, failure_reason = $14, applied = $15, skipped = $16, failed = $17,
			rescan_id = $18
		WHERE id = $1`,
		r.ID, r.State, r.PlanDigest, r.RequestedAt, r.CheckedAt, r.ConfirmedAt, r.StartedAt, r.FinishedAt,
		r.AllowIrreversible, r.IrreversibleFiles, blockers, r.DirtyFiles, r.DirtyCount, r.FailureReason,
		r.Applied, r.Skipped, r.Failed, nullID(r.RescanID))
	return err
}

// InsertApplyItem stores it as an apply_item of the apply run.
func (s *Store) InsertApplyItem(ctx context.Context, id apply.ID, it apply.Item) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO apply_item (apply_id, bundle_id, finding_id, file_path, start_line, start_col, line, action,
			category, kind, name, store_path, store_version, tracked, code_change, status, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		id, it.BundleID, it.FindingID, it.File, it.StartLine, it.StartCol, it.Line, it.Action,
		it.Category, it.Kind, it.Name, it.StorePath, it.StoreVersion, it.Tracked, it.CodeChange, it.Status, it.Reason)
	return err
}

// MarkApplied records that an apply changed the source for the bundle and
// stored its value at storePath.
func (s *Store) MarkApplied(ctx context.Context, id triage.BundleID, storePath string) error {
	_, err := s.pool.Exec(ctx, "UPDATE bundle SET applied_at = now(), store_path = $2 WHERE id = $1", id, storePath)
	return err
}

// JobsOfKind returns the number of jobs of the given kind.
func (s *Store) JobsOfKind(ctx context.Context, kind string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind = $1", kind).Scan(&n)
	return n, err
}

// HeldLock is a transaction a test keeps open while it holds a lock.
type HeldLock struct {
	tx  pgx.Tx
	pid int32
}

// LockProject starts a transaction that locks the project's row in mode,
// "FOR UPDATE" or "FOR SHARE", and keeps it until Commit.
func (s *Store) LockProject(ctx context.Context, id project.ID, mode string) (*HeldLock, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	h := &HeldLock{tx: tx}
	if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&h.pid); err != nil {
		return nil, errors.Join(err, tx.Rollback(ctx))
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM project WHERE id = $1 "+mode, id); err != nil {
		return nil, errors.Join(err, tx.Rollback(ctx))
	}
	return h, nil
}

// Exec runs a statement of the test in the held transaction.
func (h *HeldLock) Exec(ctx context.Context, sql string, args ...any) error {
	_, err := h.tx.Exec(ctx, sql, args...)
	return err
}

// Commit ends the held transaction.
func (h *HeldLock) Commit(ctx context.Context) error {
	return h.tx.Commit(ctx)
}

// Blocking reports whether another backend waits for a lock the held
// transaction has.
func (h *HeldLock) Blocking(ctx context.Context, s *Store) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))", h.pid).Scan(&n)
	return n > 0, err
}

// SetScan puts the scan with the given ID in status, with the request time
// at and applyID, zero for none, as the apply it confirms.
func (s *Store) SetScan(ctx context.Context, id scan.ID, status scan.Status, at time.Time, applyID apply.ID) error {
	_, err := s.pool.Exec(ctx, "UPDATE scan SET status = $2, requested_at = $3, apply_id = $4 WHERE id = $1",
		id, status, at, nullID(applyID))
	return err
}

// SetScanFinished stores at, nil for NULL, as the finish time of the scan
// with the given ID.
func (s *Store) SetScanFinished(ctx context.Context, id scan.ID, at *time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE scan SET finished_at = $2 WHERE id = $1", id, at)
	return err
}

// SetApplyRunTimes stores started and finished, nil for NULL, as the start
// and finish times of the apply run with the given ID.
func (s *Store) SetApplyRunTimes(ctx context.Context, id apply.ID, started, finished *time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE apply_run SET started_at = $2, finished_at = $3 WHERE id = $1",
		id, started, finished)
	return err
}

// InsertAuditEvents adds events to the audit trail with the times they
// carry, in one batch. Their IDs are ignored.
func (s *Store) InsertAuditEvents(ctx context.Context, events ...audit.Event) error {
	var b pgx.Batch
	for _, e := range events {
		detail := e.Detail
		if detail == nil {
			detail = map[string]string{}
		}
		b.Queue(`
			INSERT INTO audit_event (at, actor_subject, actor_name, action, project_id, bundle_id, finding_id, detail)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			e.At, e.Actor.Subject, e.Actor.Name, e.Action, e.ProjectID, nullID(e.BundleID), nullID(e.FindingID), detail)
	}
	return s.pool.SendBatch(ctx, &b).Close()
}

// DeleteBundleRows removes the bundle with the given ID, its findings and
// their locations, leaving apply items and audit events as they are.
func (s *Store) DeleteBundleRows(ctx context.Context, id triage.BundleID) error {
	if _, err := s.pool.Exec(ctx,
		"DELETE FROM location WHERE finding_id IN (SELECT id FROM finding WHERE bundle_id = $1)", id); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM finding WHERE bundle_id = $1", id); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, "DELETE FROM bundle WHERE id = $1", id)
	return err
}

// StoredFinding is a finding row given to PlanFindings.
type StoredFinding struct {
	ID          int64
	Fingerprint string
	File        string
	Key         string
	Bundle      int64
	Seen        bool
}

// AutoBundle is a shared or frontend bundle row given to PlanFindings.
type AutoBundle struct {
	ID          int64
	Fingerprint string
	Origin      string
}

// Planned is what the plan of a scan's findings decided for one of them.
type Planned struct {
	// ID is the stored finding the scan's finding becomes, or 0 for a new
	// one.
	ID int64
	// Key is the key the row has after the scan.
	Key string
	// Bundle is the stored bundle a new finding joins, or 0.
	Bundle int64
	// NewBundle is the 1-based position of the new bundle a new finding
	// joins, or 0, and Origin that bundle's origin.
	NewBundle int
	Origin    string
	// Preview is the preview the row gets. It is compared only by tests
	// that set one.
	Preview string
}

// PlanFindings plans the storing of findings over the given rows and returns
// the decision for each finding, the number of finding rows the plan writes,
// and the number of bundles it creates.
func PlanFindings(stored []StoredFinding, bundles []AutoBundle, findings []finding.Finding) (decided []Planned, rows, created int) {
	sf := make([]storedFinding, len(stored))
	for i, f := range stored {
		sf[i] = storedFinding{id: f.ID, fingerprint: f.Fingerprint, file: f.File, key: f.Key, bundle: f.Bundle, seen: f.Seen}
	}
	ab := make([]autoBundle, len(bundles))
	for i, b := range bundles {
		ab[i] = autoBundle{id: b.ID, fingerprint: b.Fingerprint, origin: b.Origin}
	}
	p := planFindings(sf, ab, findings)
	decided = make([]Planned, len(p.slots))
	for i, slot := range p.slots {
		r := p.rows[slot]
		decided[i] = Planned{ID: r.id, Key: r.finding.Key, Preview: r.finding.Preview}
		if r.id != 0 {
			continue
		}
		decided[i].Bundle, decided[i].NewBundle = r.bundle.id, r.bundle.planned
		if r.bundle.planned != 0 {
			decided[i].Origin = p.bundles[r.bundle.planned-1].origin
		}
	}
	return decided, len(p.rows), len(p.bundles)
}

// HeldClose is a transaction that has closed a job as done and not yet
// committed, so that it holds the lock on the job's row.
type HeldClose struct {
	tx  pgx.Tx
	pid int
}

// HoldJobClose closes the job with the given ID as done in a transaction it
// leaves open. The caller ends it with Commit.
func (s *Store) HoldJobClose(ctx context.Context, jobID int64) (*HeldClose, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	h := &HeldClose{tx: tx}
	if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&h.pid); err != nil {
		return nil, errors.Join(err, tx.Rollback(ctx))
	}
	if _, err := tx.Exec(ctx, "UPDATE job SET state = 'done' WHERE id = $1", jobID); err != nil {
		return nil, errors.Join(err, tx.Rollback(ctx))
	}
	return h, nil
}

// WaitBlocked returns once another session waits for a lock that h holds,
// or when ctx ends.
func (h *HeldClose) WaitBlocked(ctx context.Context, s *Store) error {
	for {
		var blocked bool
		if err := s.pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", h.pid).
			Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return nil
		}
	}
}

// Commit commits the close.
func (h *HeldClose) Commit(ctx context.Context) error {
	return h.tx.Commit(ctx)
}
