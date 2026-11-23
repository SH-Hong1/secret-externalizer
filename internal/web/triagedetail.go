package web

import (
	"strconv"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

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
// the reason select with it. Cancel offers to clear the current verdict.
// Fixed disables every verdict and offers only to save the note with the
// verdict and name the bundle has, which is all an applied bundle allows.
// Locked disables the whole form, the note included.
type verdictForm struct {
	Action  string
	Return  string
	Buttons []verdictButton
	Hold    verdictButton
	Reasons []reasonOption
	Note    string
	Text    string
	Cancel  bool
	Fixed   bool
	Locked  bool
	Keep    triage.Verdict
}

// keep shows the note and hold reason a rejected form submitted again. A
// reason the form does not offer leaves the selection as it is.
func (f *verdictForm) keep(in verdictInput) {
	f.Note = in.Note
	if !knownReason(triage.HoldReason(in.Reason)) {
		return
	}
	for i := range f.Reasons {
		f.Reasons[i].Selected = f.Reasons[i].Value == in.Reason
	}
}

// nameField is the placeholder name input of a secret bundle. Config and
// Shell are the rendered forms of Value in the files of the bundle, set
// only when Value is a valid name. Fixed shows the name without letting it
// change.
type nameField struct {
	Value     string
	Prefix    string
	Config    string
	Shell     string
	Suggested bool
	Fixed     bool

	inConfig bool
	inShell  bool
}

// setValue puts value in the input and renders the forms the bundle's files
// take.
func (f *nameField) setValue(value string) {
	f.Value, f.Config, f.Shell = value, "", ""
	n, err := triage.ParseName(value)
	if err != nil {
		return
	}
	if f.inConfig {
		f.Config = "${" + n.Config() + "}"
	}
	if f.inShell {
		f.Shell = "${" + n.Shell() + "}"
	}
}

// bundleDetail is the detail of a secret bundle. From is its ID, which the
// move forms send so the screen can return to it; Targets are the other
// bundles of the same value a member can move to. Original is the slot of
// the original value, set only for an admin when a scan kept the value.
// Applied is set for a bundle whose value an apply stored.
type bundleDetail struct {
	Original      *originalView
	Applied       *appliedInfo
	From          string
	Targets       []moveTarget
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

// appliedInfo tells when an apply stored a bundle's value and where.
type appliedInfo struct {
	At        string
	StorePath string
}

// memberView is one member of a bundle. Key is the configuration key of its
// value, empty when it has none. MoveAction is where its move forms post;
// Split is set for every member after the first, which can be split off into
// a bundle of its own.
type memberView struct {
	MoveAction string
	Key        string
	Split      bool
	Locations  []locationView
}

// moveTarget is a bundle a member can move to.
type moveTarget struct {
	Value string
	Label string
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

// holdReasons returns the hold reasons in the order the form offers them.
func holdReasons() []triage.HoldReason {
	return []triage.HoldReason{triage.HoldStructure, triage.HoldConfirm, triage.HoldLater}
}

func knownReason(r triage.HoldReason) bool {
	for _, known := range holdReasons() {
		if r == known {
			return true
		}
	}
	return false
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

// newVerdictForm returns the verdict buttons of allowed with the hold
// button apart, marking the suggested verdict and choosing the reason of the
// current decision, or of the suggestion when there is none.
func newVerdictForm(action, ret string, allowed []triage.Verdict, current triage.Decision, s triage.Suggestion) verdictForm {
	f := verdictForm{Action: action, Return: ret, Note: current.Note, Cancel: current.Verdict != ""}
	for _, v := range allowed {
		b := verdictButton{verdictView: verdictOf(v), Suggested: s.Decision.Verdict == v}
		if b.Suggested {
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
	for _, r := range holdReasons() {
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
	if s.Source == triage.SourceHistory {
		return "제안: " + label + ". 같은 값을 다른 곳에서 " + verdictOf(s.Decision.Verdict).Label + "(으)로 판정했습니다. 버튼을 눌러야 저장됩니다."
	}
	return "제안: " + label + ". 규칙에 따른 제안입니다. 버튼을 눌러야 저장됩니다."
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
	shells := 0
	for _, m := range b.Members {
		if triage.ShellFile(m.File) {
			shells++
		}
	}
	if shells > 0 && shells == len(b.Members) {
		f.Prefix = "EXT_"
	}
	f.inConfig, f.inShell = shells < len(b.Members), shells > 0
	f.setValue(string(n))
	return f
}

// newBundleDetail shapes a secret bundle, its suggestion and the other
// bundles of its value for the detail.
func newBundleDetail(id project.ID, ret string, b triage.Bundle, s triage.Suggestion, same []triage.Entry) *bundleDetail {
	d := &bundleDetail{
		From:          strconv.FormatInt(int64(b.ID), 10),
		Targets:       newMoveTargets(same),
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
		mv := memberView{
			MoveAction: "/projects/" + pid + "/findings/" + strconv.FormatInt(int64(m.FindingID), 10) + "/move",
			Key:        m.Key,
			Split:      i > 0,
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
	if !b.AppliedAt.IsZero() {
		// The source holds the bundle's name, so only the note may change.
		d.Applied = &appliedInfo{At: formatTime(b.AppliedAt), StorePath: b.StorePath}
		d.Form.Fixed, d.Form.Cancel, d.Form.Keep, d.Form.Text = true, false, b.Decision.Verdict, ""
		if d.Name != nil {
			d.Name.Fixed, d.Name.Suggested = true, false
		}
	}
	return d
}

// lock takes away everything of d that changes the bundle: the verdict form
// and the name are disabled, and no member offers a move.
func (d *bundleDetail) lock() {
	d.Form.Locked, d.Form.Text, d.Targets = true, "", nil
	if d.Name != nil {
		d.Name.Fixed, d.Name.Suggested = true, false
	}
	for i := range d.Members {
		d.Members[i].Split = false
	}
}

// newMoveTargets labels the bundles a member can move to with their number
// and verdict, and their name or first place, followed by the key of that
// place when it has one.
func newMoveTargets(same []triage.Entry) []moveTarget {
	out := make([]moveTarget, 0, len(same))
	for _, e := range same {
		where := e.File + ":" + strconv.Itoa(e.FirstLine)
		if e.Name != "" {
			where = entryName(e)
		}
		if e.Key != "" {
			where += " · " + e.Key
		}
		id := strconv.FormatInt(int64(e.Bundle), 10)
		out = append(out, moveTarget{Value: id, Label: "묶음 #" + id + " · " + verdictOf(e.Verdict).Label + " · " + where})
	}
	return out
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
