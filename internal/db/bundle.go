package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// The values of bundle.origin. A shared bundle takes every new finding of
// its fingerprint outside Frontend files and a frontend bundle those in
// Frontend files; an own bundle takes only what is put there by name.
const (
	originShared   = "shared"
	originFrontend = "frontend"
	originOwn      = "own"
)

// bundleForNew returns the bundle a finding that is stored for the first
// time joins, creating it when needed. Customer data and kinds that do not
// share a bundle get a new own bundle; a finding in a Frontend file joins the
// frontend bundle of its fingerprint, and any other the shared one.
func bundleForNew(ctx context.Context, tx pgx.Tx, projectID int64, f finding.Finding) (triage.BundleID, error) {
	var id triage.BundleID
	if f.Category == finding.CategoryCustomerData || !triage.SharesBundle(f.Kind) {
		err := tx.QueryRow(ctx,
			"INSERT INTO bundle (project_id, fingerprint, origin) VALUES ($1, $2, $3) RETURNING id",
			projectID, f.Fingerprint, originOwn).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("creating bundle: %w", err)
		}
		return id, nil
	}
	origin := originShared
	if triage.Frontend(f.File) {
		origin = originFrontend
	}
	err := tx.QueryRow(ctx,
		"SELECT id FROM bundle WHERE project_id = $1 AND fingerprint = $2 AND origin = $3",
		projectID, f.Fingerprint, origin).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("reading %s bundle: %w", origin, err)
	}
	if err := tx.QueryRow(ctx,
		"INSERT INTO bundle (project_id, fingerprint, origin) VALUES ($1, $2, $3) RETURNING id",
		projectID, f.Fingerprint, origin).Scan(&id); err != nil {
		return 0, fmt.Errorf("creating %s bundle: %w", origin, err)
	}
	return id, nil
}

// rowLock is the row lock lockBundle takes on a bundle.
type rowLock int

// The row locks lockBundle can take.
const (
	// lockNoKeyUpdate is for a change that keeps the bundle row.
	lockNoKeyUpdate rowLock = iota + 1
	// lockUpdate is for a change that may delete the bundle row.
	lockUpdate
)

// lockedBundle is a bundle read under a row lock.
type lockedBundle struct {
	// bundle has the decision and, as Members, the findings that the
	// project's latest succeeded scan found, without locations. Category,
	// Kind and Preview are those of the lowest of them.
	bundle triage.Bundle
	// findings counts every finding in the bundle, including those the
	// latest scan did not find.
	findings int
}

// lockBundle locks the bundle of the project with the given ID in tx and
// reads it. Its members are the findings the project's latest succeeded
// scan found, the same ones Bundle shows for that scan, so that a decision
// is checked against what the user sees. It returns ErrNotFound when the
// project has no such bundle.
func lockBundle(ctx context.Context, tx pgx.Tx, projectID project.ID, id triage.BundleID, lock rowLock) (lockedBundle, error) {
	query := "SELECT fingerprint, verdict, hold_reason, name, note FROM bundle WHERE id = $1 AND project_id = $2 FOR UPDATE"
	if lock == lockNoKeyUpdate {
		query = "SELECT fingerprint, verdict, hold_reason, name, note FROM bundle WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE"
	}
	lb := lockedBundle{bundle: triage.Bundle{ID: id, ProjectID: projectID}}
	b := &lb.bundle
	var name *string
	err := tx.QueryRow(ctx, query, id, projectID).
		Scan(&b.Fingerprint, &b.Decision.Verdict, &b.Decision.Reason, &name, &b.Decision.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedBundle{}, fmt.Errorf("reading bundle %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return lockedBundle{}, fmt.Errorf("reading bundle %d: %w", id, err)
	}
	if name != nil {
		b.Decision.Name = triage.Name(*name)
	}
	rows, err := tx.Query(ctx, `
		SELECT id, file_path, category, kind, preview, has_original,
			coalesce(last_scan_id = (
				SELECT id FROM scan WHERE project_id = $2 AND status = 'succeeded' ORDER BY id DESC LIMIT 1
			), false)
		FROM finding WHERE bundle_id = $1 ORDER BY id`, id, projectID)
	if err != nil {
		return lockedBundle{}, fmt.Errorf("listing members of bundle %d: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			m       triage.Member
			c       finding.Category
			k       finding.Kind
			preview string
			current bool
		)
		if err := rows.Scan(&m.FindingID, &m.File, &c, &k, &preview, &m.HasOriginal, &current); err != nil {
			return lockedBundle{}, fmt.Errorf("reading member row: %w", err)
		}
		lb.findings++
		if !current {
			continue
		}
		if len(b.Members) == 0 {
			b.Category, b.Kind, b.Preview = c, k, preview
		}
		b.Members = append(b.Members, m)
	}
	if err := rows.Err(); err != nil {
		return lockedBundle{}, fmt.Errorf("listing members of bundle %d: %w", id, err)
	}
	return lb, nil
}

// Decide gives every bundle in ids the decision d in one transaction and
// records one audit event per bundle. The zero Decision clears a verdict.
// A bundle is checked against the members the project's latest succeeded
// scan found. Nothing is stored when any bundle is not one of the project's
// or that scan found none of its members (ErrNotFound), when a bundle does not allow d (triage.ErrNotAllowed), or
// when the name is used by another bundle of the project (ErrDuplicate).
func (s *Store) Decide(ctx context.Context, projectID project.ID, ids []triage.BundleID, d triage.Decision, by audit.Actor) (err error) {
	if len(ids) == 0 {
		return fmt.Errorf("deciding bundles of project %d: no bundle given: %w", projectID, ErrNotFound)
	}
	// Locking in ID order keeps two calls from waiting on each other.
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	var name *string
	if d.Name != "" {
		n := string(d.Name)
		name = &n
	}
	decidedBy := by.Subject
	if d == (triage.Decision{}) {
		decidedBy = ""
	}
	for _, id := range sorted {
		// NO KEY UPDATE lets a scan add a finding to the bundle meanwhile,
		// unless the decision changes the name: name is in a unique key, so
		// the UPDATE then takes the full row lock and the scan waits.
		lb, err := lockBundle(ctx, tx, projectID, id, lockNoKeyUpdate)
		if err != nil {
			return err
		}
		if len(lb.bundle.Members) == 0 {
			return fmt.Errorf("deciding bundle %d: the latest scan found none of its members: %w", id, ErrNotFound)
		}
		if err := lb.bundle.Check(d); err != nil {
			return fmt.Errorf("deciding bundle %d: %w", id, err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE bundle SET verdict = $2, hold_reason = $3, name = $4, note = $5, decided_by = $6,
				decided_at = CASE WHEN $6 = '' THEN NULL ELSE now() END
			WHERE id = $1`,
			id, d.Verdict, d.Reason, name, d.Note, decidedBy)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return fmt.Errorf("naming bundle %d: %w", id, ErrDuplicate)
		}
		if err != nil {
			return fmt.Errorf("deciding bundle %d: %w", id, err)
		}
		if err := insertEvent(ctx, tx, audit.Event{
			Actor: by, Action: audit.ActionVerdict, ProjectID: projectID, BundleID: id, Detail: decisionDetail(d),
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing decision: %w", err)
	}
	return nil
}

// decisionDetail returns the audit detail of d: the verdict, and the reason,
// name and note when they are set.
func decisionDetail(d triage.Decision) map[string]string {
	detail := map[string]string{audit.DetailVerdict: string(d.Verdict)}
	for key, v := range map[string]string{
		audit.DetailReason: string(d.Reason), audit.DetailName: string(d.Name), audit.DetailNote: d.Note,
	} {
		if v != "" {
			detail[key] = v
		}
	}
	return detail
}

// MoveFinding moves the finding with the given ID to the bundle to, or to a
// new bundle of its own when to is zero, and returns the bundle it moved to.
// The finding then follows that bundle's decision. A bundle left with no
// finding is deleted, which frees its name. The move is recorded in the
// audit trail. Moving a finding to its own bundle, or the only finding of a
// bundle to a new one, changes nothing and returns the bundle it is in.
//
// It returns ErrNotFound when the project has no such finding or bundle, and
// triage.ErrNotAllowed, moving nothing, when the finding is customer data,
// which keeps one bundle per finding, or when the bundle has another
// fingerprint, holds customer data while the finding does not, or has a
// decision it would no longer allow with the finding in it.
// The decision is checked against the finding and the bundle's members that
// the project's latest succeeded scan found.
func (s *Store) MoveFinding(ctx context.Context, projectID project.ID, id finding.ID, to triage.BundleID, by audit.Actor) (_ triage.BundleID, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("starting transaction: %w", err)
	}
	defer rollback(ctx, tx, &err)

	var (
		from     triage.BundleID
		moving   triage.Member
		fp       string
		category finding.Category
	)
	err = tx.QueryRow(ctx, `
		SELECT bundle_id, fingerprint, file_path, category FROM finding
		WHERE id = $1 AND project_id = $2 FOR UPDATE`, id, projectID).
		Scan(&from, &fp, &moving.File, &category)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("reading finding %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("reading finding %d: %w", id, err)
	}
	moving.FindingID = id
	// Each customer data finding keeps a bundle of its own, so it never
	// moves.
	if category == finding.CategoryCustomerData {
		return 0, fmt.Errorf("moving customer data finding %d: %w", id, triage.ErrNotAllowed)
	}
	if to == from {
		return from, nil
	}

	// Locking in ID order keeps two calls from waiting on each other; a new
	// bundle has a higher ID than the source. The source may be deleted, so
	// both take the full lock.
	locked := map[triage.BundleID]lockedBundle{}
	order := []triage.BundleID{min(from, to), max(from, to)}
	if to == 0 {
		src, err := lockBundle(ctx, tx, projectID, from, lockUpdate)
		if err != nil {
			return 0, err
		}
		if src.findings == 1 {
			return from, nil
		}
		locked[from] = src
		if err := tx.QueryRow(ctx,
			"INSERT INTO bundle (project_id, fingerprint, origin) VALUES ($1, $2, $3) RETURNING id",
			projectID, fp, originOwn).Scan(&to); err != nil {
			return 0, fmt.Errorf("creating bundle: %w", err)
		}
		order = []triage.BundleID{to}
	}
	for _, b := range order {
		locked[b], err = lockBundle(ctx, tx, projectID, b, lockUpdate)
		if err != nil {
			return 0, err
		}
	}
	dest := locked[to].bundle
	switch {
	case dest.Fingerprint != fp:
		return 0, fmt.Errorf("moving finding %d to bundle %d of another value: %w", id, to, triage.ErrNotAllowed)
	case len(dest.Members) > 0 && dest.Category == finding.CategoryCustomerData:
		return 0, fmt.Errorf("moving finding %d into customer data: %w", id, triage.ErrNotAllowed)
	}
	dest.Members = append(dest.Members, moving)
	if err := dest.Check(dest.Decision); err != nil {
		return 0, fmt.Errorf("moving finding %d to bundle %d: %w", id, to, err)
	}

	if _, err := tx.Exec(ctx, "UPDATE finding SET bundle_id = $2 WHERE id = $1", id, to); err != nil {
		return 0, fmt.Errorf("moving finding %d: %w", id, err)
	}
	if locked[from].findings == 1 {
		if _, err := tx.Exec(ctx, "DELETE FROM bundle WHERE id = $1", from); err != nil {
			return 0, fmt.Errorf("deleting empty bundle %d: %w", from, err)
		}
	}
	if err := insertEvent(ctx, tx, audit.Event{
		Actor: by, Action: audit.ActionMove, ProjectID: projectID, BundleID: from, FindingID: id,
		Detail: map[string]string{audit.DetailTo: strconv.FormatInt(int64(to), 10)},
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing move: %w", err)
	}
	return to, nil
}

// UsedNames returns the names the project's bundles have, in order.
func (s *Store) UsedNames(ctx context.Context, projectID project.ID) ([]triage.Name, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT name FROM bundle WHERE project_id = $1 AND name IS NOT NULL ORDER BY name", projectID)
	if err != nil {
		return nil, fmt.Errorf("listing names of project %d: %w", projectID, err)
	}
	defer rows.Close()
	var out []triage.Name
	for rows.Next() {
		var n triage.Name
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("reading name row: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing names of project %d: %w", projectID, err)
	}
	return out, nil
}

// PastDecision returns the most recent decision given to a bundle of the
// fingerprint other than except, in any project. It reports false when there
// is none.
func (s *Store) PastDecision(ctx context.Context, fingerprint string, except triage.BundleID) (triage.Decision, bool, error) {
	var (
		d    triage.Decision
		name *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT verdict, hold_reason, name, note FROM bundle
		WHERE fingerprint = $1 AND id <> $2 AND verdict <> ''
		ORDER BY decided_at DESC, id DESC LIMIT 1`, fingerprint, except).
		Scan(&d.Verdict, &d.Reason, &name, &d.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return triage.Decision{}, false, nil
	}
	if err != nil {
		return triage.Decision{}, false, fmt.Errorf("reading past decision: %w", err)
	}
	if name != nil {
		d.Name = triage.Name(*name)
	}
	return d, true, nil
}
