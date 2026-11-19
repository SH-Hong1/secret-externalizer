// Package audit defines the record of what a user did to a project's
// findings: a verdict, a move of a finding between bundles, or a look at an
// original value. A record never holds a value a scan found.
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
)

// The keys an Event's Detail can hold.
const (
	DetailVerdict = "verdict"
	DetailReason  = "reason"
	DetailName    = "name"
	DetailNote    = "note"
	DetailTo      = "to"
)

// Event is one entry of the audit trail. BundleID is the bundle acted on, or
// for a move the bundle the finding left; FindingID is set only for a move.
// Detail holds only the verdict, hold reason, name and note of a decision and
// the bundle a finding moved to, under the Detail keys; never a value.
type Event struct {
	ID        int64
	At        time.Time
	Actor     Actor
	Action    Action
	ProjectID project.ID
	BundleID  triage.BundleID
	FindingID finding.ID
	Detail    map[string]string
}
