package web

import (
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// auditPageSize is the number of events one page of the audit trail shows.
const auditPageSize = 100

// auditTimeLayout is the time of an event; the column head says it is UTC.
const auditTimeLayout = "2006-01-02 15:04:05"

// auditRow is one event of the audit trail as the page shows it. Move rows
// show the finding and the bundles it left and joined; verdict rows show the
// bundle with its kind, the file of customer data, and the name, hold reason
// and note of a decision; reveal rows show the bundle.
type auditRow struct {
	At          string
	User        string
	ActionLabel string
	ActionClass string
	ProjectHref string
	ProjectName string
	Move        bool
	Bundle      string
	Kind        string
	File        string
	Finding     string
	To          string
	Name        string
	Reason      string
	Note        string
}

// newAuditRows shapes events for the audit page.
func newAuditRows(events []audit.Event) []auditRow {
	rows := make([]auditRow, 0, len(events))
	for _, e := range events {
		row := auditRow{
			At:          e.At.UTC().Format(auditTimeLayout),
			User:        e.Actor.Name,
			ProjectHref: "/projects/" + strconv.FormatInt(int64(e.ProjectID), 10),
			ProjectName: e.ProjectName,
		}
		if e.BundleID != 0 {
			row.Bundle = "묶음 #" + strconv.FormatInt(int64(e.BundleID), 10)
		}
		switch e.Action {
		case audit.ActionReveal:
			row.ActionLabel, row.ActionClass = "원본 보기", "dark"
		case audit.ActionMove:
			row.ActionLabel, row.ActionClass, row.Move = "묶음 옮기기", "queued", true
			row.Finding = "항목 #" + strconv.FormatInt(int64(e.FindingID), 10)
			row.To = "#" + e.Detail[audit.DetailTo]
		case audit.ActionVerdict:
			v := verdictOf(triage.Verdict(e.Detail[audit.DetailVerdict]))
			row.ActionLabel, row.ActionClass = "판정 · "+v.Label, v.Class
			if e.Kind != "" {
				row.Kind = kindLabel(e.Kind)
			}
			row.File = e.File
			if v.Value == "" {
				row.ActionLabel = "판정 취소"
			}
			if n := e.Detail[audit.DetailName]; n != "" {
				row.Name = triage.Name(n).Config()
			}
			if r := e.Detail[audit.DetailReason]; r != "" {
				row.Reason = reasonLabel(triage.HoldReason(r))
			}
			row.Note = e.Detail[audit.DetailNote]
		default:
			row.ActionLabel, row.ActionClass = string(e.Action), "queued"
		}
		rows = append(rows, row)
	}
	return rows
}
