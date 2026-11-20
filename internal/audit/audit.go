// Package audit defines the record of what a user did to a project's
// findings: a verdict, a move of a finding between bundles, a look at an
// original value, or the confirmation of an apply. A record never holds a
// value a scan found.
package audit

import (
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// Actor is the user who did something: the subject the identity provider
// gave and the name shown for it.
type Actor struct {
	Subject, Name string
}

// Action is what a user did.
type Action string

// The actions the audit trail records.
const (
	ActionVerdict Action = "verdict"
	ActionMove    Action = "move"
	ActionReveal  Action = "reveal"
	ActionApply   Action = "apply"
)

// The keys an Event's Detail can hold.
const (
	DetailVerdict = "verdict"
	DetailReason  = "reason"
	DetailName    = "name"
	DetailNote    = "note"
	DetailTo      = "to"
	// DetailApply is the ID of the apply run a user confirmed.
	DetailApply = "apply"
	// DetailIrreversible is "yes" when the user agreed to redact customer
	// data in untracked files, which cannot be undone, and "no" otherwise.
	DetailIrreversible = "irreversible"
)

// Event is one entry of the audit trail. BundleID is the bundle acted on, or
// for a move the bundle the finding left; FindingID is set only for a move.
// Detail holds only the verdict, hold reason, name and note of a decision and
// the bundle a finding moved to, or the apply run a user confirmed and
// whether the user agreed to irreversible redactions, under the Detail keys;
// never a value. A reveal has no detail, and an apply no bundle.
//
// ProjectName, Kind and File are filled in when the event is read back from
// the trail and are not stored with it. ProjectName is the project's name.
// Kind is the kind of the first finding the bundle holds now, and File that
// finding's path when it is customer data, whose bundle holds only it; both
// are empty when the bundle holds no finding any more. Neither is a value.
type Event struct {
	ID          int64
	At          time.Time
	Actor       Actor
	Action      Action
	ProjectID   project.ID
	ProjectName string
	Kind        finding.Kind
	File        string
	BundleID    triage.BundleID
	FindingID   finding.ID
	Detail      map[string]string
}
