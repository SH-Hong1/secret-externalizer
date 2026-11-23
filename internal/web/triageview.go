package web

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// selNone is the verdict part of a customer data selection that has no
// verdict.
const selNone = "none"

// triageQuery is the filter, page and selection of a triage screen request.
type triageQuery struct {
	Category finding.Category
	Status   string
	Page     int
	Sel      string
}

// values returns q as query parameters. The status is always set; an empty
// category and selection are left out, and so is page 1 unless withPage is
// set.
func (q triageQuery) values(withPage bool) url.Values {
	v := url.Values{}
	if q.Category != "" {
		v.Set("category", string(q.Category))
	}
	v.Set("status", q.Status)
	if withPage || q.Page > 1 {
		v.Set("page", strconv.Itoa(q.Page))
	}
	if q.Sel != "" {
		v.Set("sel", q.Sel)
	}
	return v
}

// href returns the address of the triage screen of project id for q.
func (q triageQuery) href(id project.ID, withPage bool) string {
	return triagePath(id) + "?" + q.values(withPage).Encode()
}

func triagePath(id project.ID) string {
	return "/projects/" + strconv.FormatInt(int64(id), 10) + "/triage"
}

// triagePage is what the triage screen shows. Exactly one of NoScan, Empty,
// Bundle and Customer describes the detail, unless Applied is set: then the
// screen shows the table of applied bundles instead of the list and detail.
// Error is the reason a verdict or a move was refused, shown above the
// detail, and ErrorLink a page that tells more. ApplyHref is the apply page
// of the project.
type triagePage struct {
	Error     string
	ErrorLink string
	ApplyHref string
	Locked    bool
	Applied   *appliedTable
	User      auth.User
	Project   project.Project
	NoScan    bool
	ScanID    scan.ID
	ScanAt    string
	Commit    string
	Undecided int
	Total     int
	Statuses  []statusChip
	Status    string
	Options   []categoryOption
	Rows      []listRow
	ShowState bool
	Page      int
	Pages     int
	Entries   int
	PrevHref  string
	NextHref  string
	Empty     *emptyState
	Bundle    *bundleDetail
	Customer  *customerDetail
}

// statusChip is one status filter link with its bundle count.
type statusChip struct {
	Label   string
	Href    string
	N       int
	Current bool
}

type categoryOption struct {
	Value, Label string
	Selected     bool
}

// listRow is one line of the triage list. Applied marks a bundle whose
// value an apply stored.
type listRow struct {
	Href         string
	Current      bool
	KindLabel    string
	CategoryText string
	CountText    string
	Sub          string
	Verdict      verdictView
	Applied      bool
}

// appliedTable is one page of the table of applied bundles.
type appliedTable struct {
	ApplyHref string
	Rows      []appliedRow
	Page      int
	Pages     int
	Total     int
	PrevHref  string
	NextHref  string
}

// appliedRow is one applied bundle: the forms its name took in the source,
// what it is, where its value went, whether a scan has looked since the
// apply (Checked) and found the value again, with the link to the bundle on
// the triage screen then.
type appliedRow struct {
	Names      []string
	What       string
	Preview    string
	StorePath  string
	AppliedAt  string
	Locations  int
	CodeChange bool
	RunHref    string
	Run        string
	Checked    bool
	Reappeared bool
	BundleHref string
}

// verdictView names a verdict for a label: Class completes "badge-" and
// "btn-".
type verdictView struct {
	Value triage.Verdict
	Label string
	Class string
}

// emptyState is the detail of a screen without entries.
type emptyState struct {
	Title    string
	Text     string
	LinkText string
	LinkHref string
}

// verdictOf returns the label and style of v; the zero verdict is undecided.
func verdictOf(v triage.Verdict) verdictView {
	switch v {
	case triage.VerdictExternalize:
		return verdictView{v, "외부화", "externalize"}
	case triage.VerdictRedact:
		return verdictView{v, "삭제·마스킹", "mask"}
	case triage.VerdictFalsePositive:
		return verdictView{v, "오탐", "false-positive"}
	case triage.VerdictHold:
		return verdictView{v, "보류", "hold"}
	}
	return verdictView{v, "미판정", "untriaged"}
}

// statusLabelOf returns the label of a Filter.Status value.
func statusLabelOf(status string) string {
	switch status {
	case triage.StatusAll:
		return "전체"
	case triage.StatusUndecided:
		return "미판정"
	}
	return verdictOf(triage.Verdict(status)).Label
}

// entryName returns the form the name of e takes in the list: the shell
// form when every member is in a shell script.
func entryName(e triage.Entry) string {
	if e.Shell {
		return e.Name.Shell()
	}
	return e.Name.Config()
}

// newAppliedTable shapes one page of applied bundles of project id.
func newAppliedTable(id project.ID, p triage.AppliedPage) *appliedTable {
	t := &appliedTable{ApplyHref: applyPath(id), Page: p.Page, Pages: p.Pages, Total: p.Total}
	for _, a := range p.Entries {
		row := appliedRow{
			What:      categoryLabel(a.Category) + " · " + kindLabel(a.Kind),
			Preview:   a.Preview,
			StorePath: a.StorePath,
			AppliedAt: formatTime(a.AppliedAt),
			Locations: a.Locations, CodeChange: a.CodeChange, Checked: a.Checked, Reappeared: a.Reappeared,
		}
		if a.Name != "" {
			if a.Config || !a.Shell {
				row.Names = append(row.Names, a.Name.Config())
			}
			if a.Shell {
				row.Names = append(row.Names, a.Name.Shell())
			}
		}
		if a.LastRun > 0 {
			row.Run, row.RunHref = "#"+strconv.FormatInt(a.LastRun, 10), runPath(apply.ID(a.LastRun))
		}
		if a.Reappeared {
			row.BundleHref = triageQuery{Status: triage.StatusAll, Sel: bundleSel(a.Bundle)}.href(id, false)
		}
		t.Rows = append(t.Rows, row)
	}
	if p.Page > 1 {
		t.PrevHref = triageQuery{Status: statusApplied, Page: p.Page - 1}.href(id, true)
	}
	if p.Page < p.Pages {
		t.NextHref = triageQuery{Status: statusApplied, Page: p.Page + 1}.href(id, true)
	}
	return t
}

func validStatus(status string) bool {
	switch triage.Verdict(status) {
	case triage.VerdictExternalize, triage.VerdictRedact, triage.VerdictFalsePositive, triage.VerdictHold:
		return true
	}
	return status == triage.StatusUndecided || status == triage.StatusAll
}

func validCategory(c finding.Category) bool {
	for _, known := range categoryOrder() {
		if c == known {
			return true
		}
	}
	return false
}

// customerKind reports whether k is a kind of customer data.
func customerKind(k finding.Kind) bool {
	switch k {
	case finding.KindPhone, finding.KindRRN, finding.KindEmail, finding.KindCard, finding.KindAddress:
		return true
	case finding.KindPassword, finding.KindToken, finding.KindAccount, finding.KindHost, finding.KindPort,
		finding.KindURL, finding.KindPrivateKey, finding.KindKeyFile, finding.KindOther:
		return false
	}
	return false
}

// bundleSel returns the selection of a secret bundle.
func bundleSel(id triage.BundleID) string {
	return "b" + strconv.FormatInt(int64(id), 10)
}

// groupSel returns the selection of a customer data group.
func groupSel(k triage.GroupKey) string {
	v := string(k.Verdict)
	if v == "" {
		v = selNone
	}
	return "c." + v + "." + string(k.Kind) + "." + k.File
}

// selection is a parsed sel parameter: a bundle, or a customer data group.
type selection struct {
	Bundle triage.BundleID
	Group  triage.GroupKey
}

// parseSel parses "b<bundle ID>" or "c.<verdict|none>.<kind>.<file>". The
// file is the rest after the third dot and may hold dots of its own.
func parseSel(sel string) (selection, bool) {
	if rest, ok := strings.CutPrefix(sel, "b"); ok {
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || n < 1 {
			return selection{}, false
		}
		return selection{Bundle: triage.BundleID(n)}, true
	}
	rest, ok := strings.CutPrefix(sel, "c.")
	if !ok {
		return selection{}, false
	}
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) != 3 || parts[2] == "" {
		return selection{}, false
	}
	v := triage.Verdict(parts[0])
	switch {
	case parts[0] == selNone:
		v = ""
	case v == "" || !validStatus(parts[0]) || parts[0] == triage.StatusUndecided || parts[0] == triage.StatusAll:
		return selection{}, false
	}
	k := finding.Kind(parts[1])
	if !customerKind(k) || !validText(parts[2]) {
		return selection{}, false
	}
	return selection{Group: triage.GroupKey{File: parts[2], Kind: k, Verdict: v}}, true
}

// newListRows shapes the entries of a page into list lines. The line whose
// selection is sel is the current one. Each line carries its verdict; the
// template shows it only under the status filter "all" (ShowState), where
// lines of different verdicts meet.
func newListRows(id project.ID, q triageQuery, entries []triage.Entry, sel string) []listRow {
	out := make([]listRow, 0, len(entries))
	for _, e := range entries {
		r := listRow{
			KindLabel:    kindLabel(e.Kind),
			CategoryText: categoryLabel(e.Category),
			Verdict:      verdictOf(e.Verdict),
		}
		var s string
		if e.Bundle != 0 {
			s = bundleSel(e.Bundle)
			r.CountText = strconv.Itoa(e.Count) + "곳"
			r.Sub = e.File + ":" + strconv.Itoa(e.FirstLine)
			if e.Name != "" {
				r.Sub = entryName(e)
			}
			r.Applied = e.Applied
		} else {
			s = groupSel(e.Group)
			r.CountText = strconv.Itoa(e.Count) + "건"
			r.Sub = e.File
		}
		lq := q
		lq.Sel = s
		r.Href = lq.href(id, false)
		r.Current = s == sel
		out = append(out, r)
	}
	return out
}

// newStatusChips returns the status filter links with their counts, and
// last the link to the applied bundles with their number. Each link keeps
// the category and starts at page 1.
func newStatusChips(id project.ID, q triageQuery, counts map[triage.Verdict]int, applied int) []statusChip {
	total := 0
	for _, n := range counts {
		total += n
	}
	statuses := []string{
		triage.StatusUndecided, string(triage.VerdictExternalize), string(triage.VerdictRedact),
		string(triage.VerdictFalsePositive), string(triage.VerdictHold), triage.StatusAll,
	}
	out := make([]statusChip, 0, len(statuses))
	for _, s := range statuses {
		n := total
		switch s {
		case triage.StatusUndecided:
			n = counts[""]
		case triage.StatusAll:
		default:
			n = counts[triage.Verdict(s)]
		}
		cq := triageQuery{Category: q.Category, Status: s}
		out = append(out, statusChip{Label: statusLabelOf(s), Href: cq.href(id, false), N: n, Current: s == q.Status})
	}
	aq := triageQuery{Status: statusApplied}
	return append(out, statusChip{Label: "적용됨", Href: aq.href(id, false), N: applied, Current: q.Status == statusApplied})
}

func newCategoryOptions(current finding.Category) []categoryOption {
	out := []categoryOption{{Value: "", Label: "전체", Selected: current == ""}}
	for _, c := range categoryOrder() {
		out = append(out, categoryOption{Value: string(c), Label: categoryLabel(c), Selected: c == current})
	}
	return out
}

// newEmptyState returns the detail of a page without entries: a scan that
// found nothing, everything decided, or a filter that matches nothing.
func newEmptyState(id project.ID, q triageQuery, scanID scan.ID, counts map[triage.Verdict]int) *emptyState {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return &emptyState{
			Title:    "찾은 항목이 없습니다",
			Text:     "스캔 #" + strconv.FormatInt(int64(scanID), 10) + "에서 비밀 값이나 고객정보를 찾지 못했습니다.",
			LinkText: "프로젝트로 돌아가기",
			LinkHref: "/projects/" + strconv.FormatInt(int64(id), 10),
		}
	}
	if q.Status == triage.StatusUndecided && q.Category == "" {
		return &emptyState{
			Title: "미판정 항목이 없습니다",
			Text: "스캔 #" + strconv.FormatInt(int64(scanID), 10) + "에서 찾은 " + strconv.Itoa(total) + "개를 모두 판정했습니다. " +
				"외부화 " + strconv.Itoa(counts[triage.VerdictExternalize]) +
				", 삭제·마스킹 " + strconv.Itoa(counts[triage.VerdictRedact]) +
				", 오탐 " + strconv.Itoa(counts[triage.VerdictFalsePositive]) +
				", 보류 " + strconv.Itoa(counts[triage.VerdictHold]) + ".",
			LinkText: "전체 보기",
			LinkHref: triagePath(id) + "?status=" + triage.StatusAll,
		}
	}
	text := "상태 \"" + statusLabelOf(q.Status) + "\""
	if q.Category != "" {
		text += ", 분류 \"" + categoryLabel(q.Category) + "\""
	}
	return &emptyState{
		Title:    "이 조건에 맞는 항목이 없습니다",
		Text:     text + "로 걸렀습니다.",
		LinkText: "필터 지우기",
		LinkHref: triagePath(id),
	}
}

// verdictTile is one verdict count on the project page, linking to the
// triage screen filtered by it. The first tile, undecided, leads.
type verdictTile struct {
	Label string
	Class string
	Href  string
	N     int
}

// newVerdictTiles returns the tiles of the undecided bundles and of each
// verdict.
func newVerdictTiles(id project.ID, counts map[triage.Verdict]int) []verdictTile {
	out := []verdictTile{{Label: "미판정", Class: "untriaged", Href: triagePath(id), N: counts[""]}}
	for _, v := range []triage.Verdict{triage.VerdictExternalize, triage.VerdictRedact, triage.VerdictFalsePositive, triage.VerdictHold} {
		vv := verdictOf(v)
		out = append(out, verdictTile{Label: vv.Label, Class: vv.Class, Href: triagePath(id) + "?status=" + string(v), N: counts[v]})
	}
	return out
}
