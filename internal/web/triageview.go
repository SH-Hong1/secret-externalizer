package web

import (
	"net/url"
	"path"
	"strconv"
	"strings"

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

// values returns q as query parameters, leaving out the default status, an
// empty category and selection, and page 1 unless withPage is set.
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
// Bundle and Customer describes the detail.
type triagePage struct {
	User      auth.User
	Project   project.Project
	NoScan    bool
	ScanID    scan.ID
	ScanAt    string
	Commit    string
	Undecided int
	Total     int
	Statuses  []statusChip
	Category  finding.Category
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

// listRow is one line of the triage list.
type listRow struct {
	Href         string
	Current      bool
	KindLabel    string
	CategoryText string
	CountText    string
	Sub          string
	Verdict      verdictView
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

// verdictButton is one submit button of a verdict form.
type verdictButton struct {
	verdictView
	Suggested bool
}

type reasonOption struct {
	Value, Label string
	Selected     bool
}

// verdictForm is the form that records a verdict. The hold button submits
// the reason select with it.
type verdictForm struct {
	Action    string
	Return    string
	Buttons   []verdictButton
	Hold      verdictButton
	Reasons   []reasonOption
	Note      string
	Suggested bool
	Source    string
	Text      string
}

// nameField is the placeholder name input of a secret bundle.
type nameField struct {
	Value     string
	Prefix    string
	Config    string
	Shell     string
	Suggested bool
}

// bundleDetail is the detail of a secret bundle.
type bundleDetail struct {
	ID            triage.BundleID
	KindLabel     string
	CategoryLabel string
	Preview       string
	Verdict       verdictView
	Frontend      bool
	Locations     int
	Members       []memberView
	Name          *nameField
	Form          verdictForm
}

// memberView is one member of a bundle. MoveAction is set for every member
// after the first, which can be split off into a bundle of its own.
type memberView struct {
	FindingID  finding.ID
	MoveAction string
	Locations  []locationView
}

type locationView struct {
	Where      string
	Detections []finding.Detection
	Context    []contextLine
}

// customerDetail is the detail of the customer data of one kind in one file.
type customerDetail struct {
	Kind       finding.Kind
	KindLabel  string
	File       string
	From       string
	Preview    string
	Verdict    verdictView
	Count      int
	Lines      string
	Shown      int
	MoreLines  bool
	Detections []finding.Detection
	Form       verdictForm
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

// categoryShort returns the short category name of a list line.
func categoryShort(c finding.Category) string {
	switch c {
	case finding.CategoryStorage:
		return "스토리지"
	case finding.CategorySSH:
		return "ssh"
	case finding.CategoryPartnerSession:
		return "제휴사 세션"
	case finding.CategoryOther:
		return "그 밖의 비밀 값"
	case finding.CategoryCustomerData:
		return "고객정보"
	}
	return string(c)
}

func reasonLabel(r triage.HoldReason) string {
	switch r {
	case triage.HoldStructure:
		return "구조 변경 필요"
	case triage.HoldConfirm:
		return "확인 필요"
	case triage.HoldLater:
		return "나중에 처리"
	}
	return string(r)
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
// selection is sel is the current one. The verdict of each line is shown
// when the list holds more than one verdict.
func newListRows(id project.ID, q triageQuery, entries []triage.Entry, sel string) []listRow {
	out := make([]listRow, 0, len(entries))
	for _, e := range entries {
		r := listRow{
			KindLabel:    kindLabel(e.Kind),
			CategoryText: categoryShort(e.Category),
			Verdict:      verdictOf(e.Verdict),
		}
		var s string
		if e.Bundle != 0 {
			s = bundleSel(e.Bundle)
			r.CountText = strconv.Itoa(e.Count) + "곳"
			r.Sub = e.File + ":" + strconv.Itoa(e.FirstLine)
			if e.Name != "" {
				r.Sub = e.Name.Config()
			}
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

// newStatusChips returns the status filter links with their counts. Each
// link keeps the category and starts at page 1.
func newStatusChips(id project.ID, q triageQuery, counts map[triage.Verdict]int) []statusChip {
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
	return out
}

func newCategoryOptions(current finding.Category) []categoryOption {
	out := []categoryOption{{Value: "", Label: "전체", Selected: current == ""}}
	for _, c := range categoryOrder() {
		out = append(out, categoryOption{Value: string(c), Label: categoryLabel(c), Selected: c == current})
	}
	return out
}

// newVerdictForm returns the verdict buttons of allowed with the hold
// button apart, marking the suggested verdict and choosing the reason of the
// current decision, or of the suggestion when there is none.
func newVerdictForm(action, ret string, allowed []triage.Verdict, current triage.Decision, s triage.Suggestion) verdictForm {
	f := verdictForm{Action: action, Return: ret, Note: current.Note}
	for _, v := range allowed {
		b := verdictButton{verdictView: verdictOf(v), Suggested: s.Decision.Verdict == v}
		if b.Suggested {
			f.Suggested, f.Source = true, s.Source
			f.Text = suggestionText(s)
		}
		if v == triage.VerdictHold {
			f.Hold = b
			continue
		}
		f.Buttons = append(f.Buttons, b)
	}
	reason := current.Reason
	if reason == "" {
		reason = s.Decision.Reason
	}
	if reason == "" {
		reason = triage.HoldConfirm
	}
	for _, r := range []triage.HoldReason{triage.HoldStructure, triage.HoldConfirm, triage.HoldLater} {
		f.Reasons = append(f.Reasons, reasonOption{Value: string(r), Label: reasonLabel(r), Selected: r == reason})
	}
	return f
}

// suggestionText explains where a suggested verdict came from.
func suggestionText(s triage.Suggestion) string {
	label := verdictOf(s.Decision.Verdict).Label
	if s.Decision.Verdict == triage.VerdictHold {
		label += "(" + reasonLabel(s.Decision.Reason) + ")"
	}
	if s.Source == "history" {
		return "제안: " + label + ". 같은 값을 다른 곳에서 " + verdictOf(s.Decision.Verdict).Label + "(으)로 판정했습니다. 버튼을 눌러야 저장됩니다."
	}
	return "제안: " + label + ". 규칙에 따른 제안입니다. 버튼을 눌러야 저장됩니다."
}

// shellFile reports whether file is a shell script, whose placeholder takes
// the shell form.
func shellFile(file string) bool {
	return strings.EqualFold(path.Ext(file), ".sh")
}

// newNameField returns the name input of b: its stored name, or the
// suggested one. The prefix is the shell one when every member is a shell
// script; Config and Shell are the rendered forms the bundle's files take.
func newNameField(b triage.Bundle, s triage.Suggestion) *nameField {
	n := b.Decision.Name
	f := &nameField{Prefix: "ext."}
	if n == "" {
		n = s.Decision.Name
		f.Suggested = n != ""
	}
	f.Value = string(n)
	shells := 0
	for _, m := range b.Members {
		if shellFile(m.File) {
			shells++
		}
	}
	if shells > 0 && shells == len(b.Members) {
		f.Prefix = "EXT_"
	}
	if n == "" {
		return f
	}
	if shells < len(b.Members) {
		f.Config = "${" + n.Config() + "}"
	}
	if shells > 0 {
		f.Shell = "${" + n.Shell() + "}"
	}
	return f
}

// newBundleDetail shapes a secret bundle and its suggestion for the detail.
func newBundleDetail(id project.ID, ret string, b triage.Bundle, s triage.Suggestion) *bundleDetail {
	d := &bundleDetail{
		ID:            b.ID,
		KindLabel:     kindLabel(b.Kind),
		CategoryLabel: categoryLabel(b.Category),
		Preview:       b.Preview,
		Verdict:       verdictOf(b.Decision.Verdict),
	}
	allowed := b.Allowed()
	for _, v := range allowed {
		if v == triage.VerdictExternalize {
			d.Name = newNameField(b, s)
		}
	}
	for _, m := range b.Members {
		d.Frontend = d.Frontend || triage.Frontend(m.File)
	}
	pid := strconv.FormatInt(int64(id), 10)
	for i, m := range b.Members {
		mv := memberView{FindingID: m.FindingID}
		if i > 0 {
			mv.MoveAction = "/projects/" + pid + "/findings/" + strconv.FormatInt(int64(m.FindingID), 10) + "/move"
		}
		for _, l := range m.Locations {
			mv.Locations = append(mv.Locations, locationView{
				Where:      m.File + ":" + lineRange(l),
				Detections: l.Detections,
				Context:    contextLines(l),
			})
			d.Locations++
		}
		d.Members = append(d.Members, mv)
	}
	action := "/projects/" + pid + "/bundles/" + strconv.FormatInt(int64(b.ID), 10) + "/verdict"
	d.Form = newVerdictForm(action, ret, allowed, b.Decision, s)
	return d
}

// newCustomerDetail shapes a customer data group for the detail. first is
// the group's first bundle, which gives the preview and the detections.
func newCustomerDetail(id project.ID, ret string, g triage.Group, first triage.Bundle) *customerDetail {
	d := &customerDetail{
		Kind:      g.Key.Kind,
		KindLabel: kindLabel(g.Key.Kind),
		File:      g.Key.File,
		From:      string(g.Key.Verdict),
		Preview:   first.Preview,
		Verdict:   verdictOf(g.Key.Verdict),
		Count:     g.Count,
		Shown:     len(g.Lines),
		MoreLines: len(g.Lines) >= triage.GroupLines,
	}
	lines := make([]string, 0, len(g.Lines))
	for _, l := range g.Lines {
		lines = append(lines, strconv.Itoa(l))
	}
	d.Lines = strings.Join(lines, ", ")
	seen := map[finding.Detection]bool{}
	for _, m := range first.Members {
		for _, l := range m.Locations {
			for _, det := range l.Detections {
				if !seen[det] {
					seen[det] = true
					d.Detections = append(d.Detections, det)
				}
			}
		}
	}
	action := "/projects/" + strconv.FormatInt(int64(id), 10) + "/customer-data/verdict"
	d.Form = newVerdictForm(action, ret, first.Allowed(), triage.Decision{Verdict: g.Key.Verdict}, triage.Suggestion{})
	return d
}

// newEmptyState returns the detail of a page without entries: everything
// decided, or a filter that matches nothing.
func newEmptyState(id project.ID, q triageQuery, scanID scan.ID, counts map[triage.Verdict]int) *emptyState {
	if q.Status == triage.StatusUndecided && q.Category == "" {
		total := 0
		for _, n := range counts {
			total += n
		}
		return &emptyState{
			Title: "미판정 항목이 없습니다",
			Text: "스캔 #" + strconv.FormatInt(int64(scanID), 10) + "가 찾은 " + strconv.Itoa(total) + "개를 모두 판정했습니다. " +
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
