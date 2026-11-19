package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
)

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
