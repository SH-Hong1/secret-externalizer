package web

import (
	"path"
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
// the reason select with it.
type verdictForm struct {
	Action  string
	Return  string
	Buttons []verdictButton
	Hold    verdictButton
	Reasons []reasonOption
	Note    string
	Text    string
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
	f := verdictForm{Action: action, Return: ret, Note: current.Note}
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
	if s.Source == triage.SourceHistory {
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
		mv := memberView{}
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
