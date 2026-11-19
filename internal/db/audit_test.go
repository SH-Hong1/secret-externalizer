package db_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

func TestRecordReveal(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	other := newProject(t, s, "other")
	save(t, s, pid, mixed()...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")

	if err := s.RecordReveal(ctx, pid, shared, tester); err != nil {
		t.Fatalf("RecordReveal(%d, %d) error = %v", pid, shared, err)
	}
	e, err := s.LastAuditEvent(ctx)
	if err != nil {
		t.Fatalf("LastAuditEvent() error = %v", err)
	}
	if e.Action != audit.ActionReveal || e.Actor != tester || e.ProjectID != pid || e.BundleID != shared ||
		e.FindingID != 0 || len(e.Detail) != 0 {
		t.Errorf("audit event after RecordReveal = %+v, want a reveal of bundle %d with no detail", e, shared)
	}

	for _, tt := range []struct {
		name string
		pid  project.ID
		id   triage.BundleID
	}{
		{"other project", other, shared},
		{"unknown bundle", pid, 999999},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before, err := s.Count(ctx, "audit_event")
			if err != nil {
				t.Fatalf("counting audit_event: %v", err)
			}
			if err := s.RecordReveal(ctx, tt.pid, tt.id, tester); !errors.Is(err, db.ErrNotFound) {
				t.Errorf("RecordReveal(%d, %d) error = %v, want ErrNotFound", tt.pid, tt.id, err)
			}
			if n, err := s.Count(ctx, "audit_event"); err != nil || n != before {
				t.Errorf("rows in audit_event after a refused RecordReveal = %d, %v, want %d", n, err, before)
			}
		})
	}
}

func TestAuditEvents(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	save(t, s, pid, mixed()...)
	shared := bundleOf(t, s, pid, fpA, "conf/a.yml")
	hostA := bundleOf(t, s, pid, fpB, "conf/a.yml")
	xml := findingID(t, s, pid, fpA, "conf/b.xml")
	phoneX := bundleOf(t, s, pid, fpC, "logs/x.log")

	empty, err := s.AuditEvents(ctx, 0, 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("AuditEvents(0, 10) with no events = %v, %v; want none", empty, err)
	}

	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, phoneX)
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater, Note: "fake note"}, hostA)
	to, err := s.MoveFinding(ctx, pid, xml, 0, tester)
	if err != nil {
		t.Fatalf("MoveFinding() error = %v", err)
	}
	if err := s.RecordReveal(ctx, pid, shared, tester); err != nil {
		t.Fatalf("RecordReveal() error = %v", err)
	}

	got, err := s.AuditEvents(ctx, 0, 10)
	if err != nil {
		t.Fatalf("AuditEvents(0, 10) error = %v", err)
	}
	want := []audit.Event{
		{
			Actor: tester, Action: audit.ActionReveal, ProjectID: pid, ProjectName: "shop", BundleID: shared,
			Kind: finding.KindPassword, Detail: map[string]string{},
		},
		{
			Actor: tester, Action: audit.ActionMove, ProjectID: pid, ProjectName: "shop", BundleID: shared, FindingID: xml,
			Kind: finding.KindPassword, Detail: map[string]string{audit.DetailTo: strconv.FormatInt(int64(to), 10)},
		},
		{
			Actor: tester, Action: audit.ActionVerdict, ProjectID: pid, ProjectName: "shop", BundleID: hostA, Kind: finding.KindHost,
			Detail: map[string]string{audit.DetailVerdict: "hold", audit.DetailReason: "later", audit.DetailNote: "fake note"},
		},
		{
			Actor: tester, Action: audit.ActionVerdict, ProjectID: pid, ProjectName: "shop", BundleID: phoneX,
			Kind: finding.KindPhone, File: "logs/x.log", Detail: map[string]string{audit.DetailVerdict: "redact"},
		},
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(audit.Event{}, "ID", "At")); diff != "" {
		t.Errorf("AuditEvents(0, 10) mismatch (-want +got):\n%s", diff)
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID >= got[i-1].ID {
			t.Errorf("AuditEvents IDs = %d then %d, want newest first", got[i-1].ID, got[i].ID)
		}
	}
	for _, e := range got {
		if e.At.IsZero() || e.At.Location() != time.UTC {
			t.Errorf("AuditEvents event %d At = %v, want a time in UTC", e.ID, e.At)
		}
	}

	page, err := s.AuditEvents(ctx, got[0].ID, 1)
	if err != nil || len(page) != 1 || page[0].ID != got[1].ID {
		t.Errorf("AuditEvents(%d, 1) = %+v, %v; want only event %d", got[0].ID, page, err, got[1].ID)
	}
	last, err := s.AuditEvents(ctx, got[3].ID, 10)
	if err != nil || len(last) != 0 {
		t.Errorf("AuditEvents(%d, 10) past the oldest = %+v, %v; want none", got[3].ID, last, err)
	}
}
