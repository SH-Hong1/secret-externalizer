package web

import (
	"slices"
	"strconv"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// reportView is a report as its template block draws it: every text and
// address is ready, so the block needs no function and both the screen and
// the PDF document draw the same thing. Later is the project's newest
// succeeded scan when it is not this one. Unchecked is the line that counts
// what the scan could not look at, empty when it looked at everything.
// Previous tells whether a succeeded scan came before this one, so the
// summary can show how each number changed. Each list carries More, the
// line that says how many lines it leaves out, empty when it leaves out
// none.
type reportView struct {
	ProjectName   string
	ProjectHref   string
	ScanID        scan.ID
	ScanHref      string
	FinishedAt    string
	Commit        string
	RulesVersion  string
	Tools         []toolVersion
	GeneratedAt   string
	Later         scan.ID
	LaterHref     string
	Unchecked     string
	UncheckedHref string
	Previous      bool
	Summary       []summaryTile
	Externalized  entryList
	RedactedKinds []kindRow
	Redacted      redactionList
	Structure     lineList
	Remaining     lineList
	FalsePositive lineList
	History       eventList
}

// summaryTile is one number of the summary: its label, the bundles and
// locations it counts, and the change in both since the previous scan.
type summaryTile struct {
	Label string
	Class string
	Count string
	Delta string
}

type entryList struct {
	Rows []entryRow
	More string
}

// entryRow is one externalized name: the form it takes in the source, what
// it is, where its value went and the places it took in the source.
type entryRow struct {
	Name       string
	Category   string
	Kind       string
	Store      string
	Places     []string
	MorePlaces string
	CodeChange bool
	Reappeared bool
}

// kindRow is the redacted customer data of one kind.
type kindRow struct {
	Kind      string
	Bundles   int
	Locations int
	Files     int
}

type redactionList struct {
	Rows []redactionRow
	More string
}

// redactionRow is the redacted customer data of one kind in one file.
// Untracked marks a file git did not track, whose change cannot be undone.
type redactionRow struct {
	Kind      string
	File      string
	Untracked bool
	Bundles   int
	Locations int
}

type lineList struct {
	Rows []lineRow
	More string
}

// lineRow is one line of what the scan found: what it is, its first place,
// how many files and locations it covers, where it stands and the note a
// user wrote with its verdict.
type lineRow struct {
	Category    string
	Kind        string
	Place       string
	Count       string
	Status      string
	StatusClass string
	Note        string
	DecidedAt   string
}

type eventList struct {
	Rows []eventRow
	More string
}

// eventRow is one line of the history, worded as the audit trail words it.
type eventRow struct {
	At          string
	User        string
	ActionLabel string
	ActionClass string
	RunHref     string
	Target      string
	Name        string
	Reason      string
	Note        string
}

// printed returns v as the PDF document draws it: with no address of
// another screen, so the template block draws the same text without links.
func (v reportView) printed() reportView {
	v.LaterHref, v.UncheckedHref = "", ""
	v.History.Rows = slices.Clone(v.History.Rows)
	for i := range v.History.Rows {
		v.History.Rows[i].RunHref = ""
	}
	return v
}

// newReportView shapes r for its template block.
func newReportView(r report.Report) reportView {
	v := reportView{
		ProjectName:  r.Project.Name,
		ProjectHref:  "/projects/" + strconv.FormatInt(int64(r.Project.ID), 10),
		ScanID:       r.Scan.ID,
		ScanHref:     scanPath(r.Scan.ID),
		FinishedAt:   formatTime(r.Scan.FinishedAt),
		Commit:       r.Scan.CommitHash,
		RulesVersion: r.Scan.RulesVersion,
		Tools:        toolVersions(r.Scan.ToolVersions),
		GeneratedAt:  formatTime(r.GeneratedAt),
		Previous:     r.Previous != 0,
	}
	if r.Latest != 0 && r.Latest != r.Scan.ID {
		v.Later, v.LaterHref = r.Latest, reportPath(r.Latest)
	}
	v.Unchecked = uncheckedLine(r.Scan)
	if v.Unchecked != "" {
		v.UncheckedHref = scanPath(r.Scan.ID) + "#unchecked"
	}
	v.Summary = summaryTiles(r.Totals, r.Delta, v.Previous)

	v.Externalized.More = moreText(r.Externalized)
	for _, e := range r.Externalized.Rows {
		v.Externalized.Rows = append(v.Externalized.Rows, newEntryRow(e))
	}
	for _, k := range r.RedactedKinds {
		v.RedactedKinds = append(v.RedactedKinds, kindRow{Kind: kindLabel(k.Kind), Bundles: k.Bundles, Locations: k.Locations, Files: k.Files})
	}
	v.Redacted.More = moreText(r.Redacted)
	for _, x := range r.Redacted.Rows {
		v.Redacted.Rows = append(v.Redacted.Rows, redactionRow{
			Kind: kindLabel(x.Kind), File: x.File, Untracked: x.Untracked, Bundles: x.Bundles, Locations: x.Locations,
		})
	}
	v.Structure = newLineList(r.Structure)
	v.Remaining = newLineList(r.Remaining)
	v.FalsePositive = newLineList(r.FalsePositives)
	v.History.More = moreText(r.History)
	for _, e := range r.History.Rows {
		v.History.Rows = append(v.History.Rows, newEventRow(e))
	}
	return v
}

// moreText returns the line that ends a list cut at report.MaxRows, or ""
// when the list holds every line.
func moreText[T any](l report.List[T]) string {
	if l.More() <= 0 {
		return ""
	}
	return "처음 " + strconv.Itoa(report.MaxRows) + "줄만 싣습니다. 외 " + strconv.Itoa(l.More()) + "줄."
}

// uncheckedLine counts what sc could not look at, or returns "" when it
// looked at everything.
func uncheckedLine(sc scan.Scan) string {
	var parts []string
	if sc.Skipped > 0 {
		parts = append(parts, "건너뛴 파일 "+strconv.Itoa(sc.Skipped)+"개")
	}
	if sc.Dropped > 0 {
		parts = append(parts, "버린 결과 "+strconv.Itoa(sc.Dropped)+"건")
	}
	errs := 0
	for _, e := range scannerErrors(sc.ScannerErrors) {
		errs += e.N
	}
	if errs > 0 {
		parts = append(parts, "스캐너 오류 "+strconv.Itoa(errs)+"건")
	}
	if len(parts) == 0 {
		return ""
	}
	return "검사하지 못한 것이 있습니다: " + strings.Join(parts, " · ") + "."
}

func summaryTiles(t, delta report.Totals, previous bool) []summaryTile {
	tile := func(label, class string, c, d report.Count) summaryTile {
		s := summaryTile{Label: label, Class: class, Count: countText(c)}
		if previous {
			s.Delta = signed(d.Bundles) + "개 · 위치 " + signed(d.Locations) + "곳"
		}
		return s
	}
	return []summaryTile{
		tile("찾은 항목", "", t.Found, delta.Found),
		tile("외부화 완료", "externalize", t.Externalized, delta.Externalized),
		tile("삭제·마스킹 완료", "mask", t.Redacted, delta.Redacted),
		tile("오탐", "false-positive", t.FalsePositive, delta.FalsePositive),
		tile("보류", "hold", t.Hold, delta.Hold),
		tile("미판정", "untriaged", t.Undecided, delta.Undecided),
		tile("적용 대기", "", t.Pending, delta.Pending),
	}
}

func countText(c report.Count) string {
	return strconv.Itoa(c.Bundles) + "개 · 위치 " + strconv.Itoa(c.Locations) + "곳"
}

// signed writes n with its sign, and 0 without one.
func signed(n int) string {
	if n > 0 {
		return "+" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func newEntryRow(e report.Entry) entryRow {
	row := entryRow{
		Name:       "${" + e.Name + "}",
		Category:   categoryLabel(e.Category),
		Kind:       kindLabel(e.Kind),
		Store:      "openbao:" + e.StorePath,
		CodeChange: e.CodeChange,
		Reappeared: e.Reappeared,
	}
	for _, p := range e.Places {
		row.Places = append(row.Places, placeText(p))
	}
	if n := e.PlaceTotal - len(e.Places); n > 0 {
		row.MorePlaces = "외 " + strconv.Itoa(n) + "곳"
	}
	return row
}

func placeText(p report.Place) string {
	switch {
	case p.Removed:
		return p.File + " · 파일 제거"
	case p.Line > 0:
		return p.File + ":" + strconv.Itoa(p.Line)
	}
	return p.File
}

func newLineList(l report.List[report.Line]) lineList {
	out := lineList{More: moreText(l)}
	for _, x := range l.Rows {
		row := lineRow{
			Category:  categoryLabel(x.Category),
			Kind:      kindLabel(x.Kind),
			Place:     x.File,
			Count:     "파일 " + strconv.Itoa(x.Files) + "개 · 위치 " + strconv.Itoa(x.Locations) + "곳",
			Note:      x.Note,
			DecidedAt: formatTime(x.DecidedAt),
		}
		if x.FirstLine > 0 {
			row.Place += ":" + strconv.Itoa(x.FirstLine)
		}
		if x.Bundles > 1 {
			row.Count = "묶음 " + strconv.Itoa(x.Bundles) + "개 · " + row.Count
		}
		row.Status, row.StatusClass = lineStatus(x)
		out.Rows = append(out.Rows, row)
	}
	return out
}

// lineStatus words where l stands and picks the badge it is drawn with.
func lineStatus(l report.Line) (label, class string) {
	switch l.Applied {
	case report.AppliedBefore:
		return "적용됨 · 다시 발견", "failed"
	case report.AppliedAfter:
		return "이후 적용됨", "succeeded"
	}
	v := verdictOf(l.Verdict)
	switch l.Verdict { //nolint:exhaustive // a verdict not named is worded by its own label
	case triage.VerdictHold:
		return "보류 · " + reasonLabel(l.Reason), v.Class
	case triage.VerdictExternalize, triage.VerdictRedact:
		return v.Label + " 판정 · 적용 전", v.Class
	}
	return v.Label, v.Class
}

func newEventRow(e report.Event) eventRow {
	row := eventRow{At: formatTime(e.At), User: e.Actor.Name}
	if row.User == "" {
		row.User = e.Actor.Subject
	}
	switch e.Action { //nolint:exhaustive // the report holds only verdict, move and apply events
	case audit.ActionVerdict:
		v := verdictOf(triage.Verdict(e.Detail[audit.DetailVerdict]))
		row.ActionLabel, row.ActionClass = "판정 · "+v.Label, v.Class
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
	case audit.ActionMove:
		row.ActionLabel, row.ActionClass = "묶음 옮기기", "queued"
	case audit.ActionApply:
		row.ActionLabel, row.ActionClass = "적용", "queued"
		if n, err := strconv.ParseInt(e.Detail[audit.DetailApply], 10, 64); err == nil && n > 0 {
			row.ActionLabel, row.RunHref = "적용 #"+strconv.FormatInt(n, 10), runPath(apply.ID(n))
		}
	default:
		row.ActionLabel, row.ActionClass = string(e.Action), "queued"
	}
	row.Target = eventTarget(e.Kind, e.File, e.Bundles)
	return row
}

// eventTarget names what an event acted on: the kind and file of its first
// bundle, and how many bundles it acted on when there are more than one.
func eventTarget(k finding.Kind, file string, bundles int) string {
	var parts []string
	if k != "" {
		parts = append(parts, kindLabel(k))
	}
	if file != "" {
		parts = append(parts, file)
	}
	if bundles > 1 {
		parts = append(parts, strconv.Itoa(bundles)+"건")
	}
	return strings.Join(parts, " · ")
}
