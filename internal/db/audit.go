package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// RecordReveal records in the audit trail that by asked to see the original
// value of bundle id of the project. It records no value and no detail. It
// returns ErrNotFound, recording nothing, when the project has no such
// bundle.
func (s *Store) RecordReveal(ctx context.Context, projectID project.ID, id triage.BundleID, by audit.Actor) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO audit_event (actor_subject, actor_name, action, project_id, bundle_id)
		SELECT $1, $2, $3, b.project_id, b.id FROM bundle b WHERE b.id = $4 AND b.project_id = $5`,
		by.Subject, by.Name, audit.ActionReveal, id, projectID)
	if err != nil {
		return fmt.Errorf("recording reveal of bundle %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("recording reveal of bundle %d of project %d: %w", id, projectID, ErrNotFound)
	}
	return nil
}

// AuditEvents returns at most limit events of the audit trail, newest first,
// with the name of each event's project. When before is not zero it returns
// only events with a smaller ID, so the ID of the last event of one page
// gives the next. Times are in UTC.
func (s *Store) AuditEvents(ctx context.Context, before int64, limit int) ([]audit.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.at, e.actor_subject, e.actor_name, e.action, e.project_id, p.name, e.bundle_id, e.finding_id, e.detail
		FROM audit_event e JOIN project p ON p.id = e.project_id
		WHERE $1 = 0 OR e.id < $1
		ORDER BY e.id DESC
		LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}
	defer rows.Close()
	events := []audit.Event{}
	for rows.Next() {
		var (
			e                   audit.Event
			bundleID, findingID *int64
		)
		if err := rows.Scan(&e.ID, &e.At, &e.Actor.Subject, &e.Actor.Name, &e.Action, &e.ProjectID, &e.ProjectName,
			&bundleID, &findingID, &e.Detail); err != nil {
			return nil, fmt.Errorf("reading audit event: %w", err)
		}
		e.At = e.At.UTC()
		if bundleID != nil {
			e.BundleID = triage.BundleID(*bundleID)
		}
		if findingID != nil {
			e.FindingID = finding.ID(*findingID)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}
	return events, nil
}

// insertEvent adds e to the audit trail in tx. Its ID and At are ignored;
// the database sets them. A zero BundleID or FindingID is stored as NULL.
func insertEvent(ctx context.Context, tx pgx.Tx, e audit.Event) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]string{}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_event (actor_subject, actor_name, action, project_id, bundle_id, finding_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.Actor.Subject, e.Actor.Name, e.Action, e.ProjectID, nullID(e.BundleID), nullID(e.FindingID), detail); err != nil {
		return fmt.Errorf("recording %s event: %w", e.Action, err)
	}
	return nil
}

// nullID returns nil for a zero ID, so that it is stored as NULL.
func nullID[T ~int64](id T) *int64 {
	if id == 0 {
		return nil
	}
	v := int64(id)
	return &v
}
