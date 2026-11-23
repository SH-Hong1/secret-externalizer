package web

import (
	"net/url"
	"strconv"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// dashboardPath is the address of the dashboard.
const dashboardPath = "/dashboard"

// noFigure stands for a number that does not exist, such as the share of
// done bundles of a project that has none done and none left.
const noFigure = "—"

// dashboardPage is what the dashboard shows. NoProjects is true when no
// project is registered, and then nothing else is shown. NoScans is true
// when no project of the filter has a succeeded scan: the tiles, the trend,
// the categories and the rules are left out and the project table stays.
// Trend is false when no scan of the period finished, and TrendMore the
// line that counts the scans the chart leaves out, empty when it leaves
// out none.
type dashboardPage struct {
	User        auth.User
	Projects    []filterOption
	Periods     []filterOption
	NoProjects  bool
	NoScans     bool
	Tiles       []dashboardTile
	Legend      []legendItem
	Trend       bool
	Chart       barChart
	TrendRows   []trendRow
	TrendMore   string
	Categories  []categoryRow
	CategoryMax int // the largest category, the end of every meter
	Rules       []ruleRow
	Rows        []dashboardRow
}

// filterOption is one choice of a select box of the filter.
type filterOption struct {
	Value, Label string
	Selected     bool
}

// dashboardTile is one number at the top of the dashboard: its label, the
// number, the line under it, the change since the scan before and the
// page it leads to, each empty when the tile has none.
type dashboardTile struct {
	Label  string
	Class  string
	Figure int
	Sub    string
	Delta  string
	Href   string
}

// legendItem names the colour of a category in the chart.
type legendItem struct {
	Class, Label string
}

// trendRow is a bar of the chart as a line of its table.
type trendRow struct {
	At      string
	Project string
	Scan    string
	Report  string
	Counts  []int // in the order of categoryOrder
	Total   int
}

// categoryRow is what is left in one category.
type categoryRow struct {
	ID, Label string
	N         int
	Split     string
}

// ruleRow is a rule with its false positives and their share, in percent
// rounded down.
type ruleRow struct {
	Tool, Rule             string
	FalsePositive, Bundles int
	Share                  string
}

// dashboardRow is one line of the project table. Last is empty when the
// project was never scanned; the numbers are noFigure when no scan of the
// project succeeded.
type dashboardRow struct {
	Href        string
	Open        string
	Name        string
	Current     bool
	Last        string
	Status      string
	StatusLabel string
	Remaining   string
	Rate        string
	Hold        string
}

// projectPath returns the address of project id.
func projectPath(id project.ID) string {
	return "/projects/" + strconv.FormatInt(int64(id), 10)
}

// dashboardHref returns the dashboard filtered to project id over period p.
func dashboardHref(id project.ID, p dashboard.Period) string {
	q := url.Values{"project": {strconv.FormatInt(int64(id), 10)}, "period": {string(p)}}
	return dashboardPath + "?" + q.Encode()
}

// newDashboardPage shapes d, the dashboard of the period p, for the
// template.
func newDashboardPage(u auth.User, d dashboard.Dashboard, p dashboard.Period) dashboardPage {
	page := dashboardPage{User: u, NoProjects: len(d.Rows) == 0, NoScans: d.Scanned == 0}
	page.Projects = append(page.Projects, filterOption{Value: "", Label: "전체 프로젝트", Selected: d.Project == 0})
	for _, r := range d.Rows {
		page.Projects = append(page.Projects, filterOption{
			Value: strconv.FormatInt(int64(r.Project.ID), 10), Label: r.Project.Name, Selected: r.Project.ID == d.Project,
		})
		page.Rows = append(page.Rows, newDashboardRow(r, d.Project, p))
	}
	for _, o := range []struct {
		p     dashboard.Period
		label string
	}{
		{dashboard.PeriodAll, "전체 기간"},
		{dashboard.PeriodWeek, "최근 7일"},
		{dashboard.PeriodMonth, "최근 30일"},
		{dashboard.PeriodQuarter, "최근 90일"},
	} {
		page.Periods = append(page.Periods, filterOption{Value: string(o.p), Label: o.label, Selected: o.p == p})
	}
	if page.NoProjects || page.NoScans {
		return page
	}

	page.Tiles = dashboardTiles(d)
	for _, c := range categoryOrder() {
		page.Legend = append(page.Legend, legendItem{Class: "chart-" + string(c), Label: categoryLabel(c)})
		s := d.Categories[c]
		page.Categories = append(page.Categories, categoryRow{ID: string(c), Label: categoryLabel(c), N: s.Total(), Split: splitText(s)})
		page.CategoryMax = max(page.CategoryMax, s.Total())
	}
	page.CategoryMax = max(page.CategoryMax, 1)

	page.Trend = len(d.Trend) > 0
	page.Chart = newBarChart(d.Trend, d.Project == 0)
	for _, s := range d.Trend {
		page.TrendRows = append(page.TrendRows, trendRow{
			At: formatTime(s.At), Project: s.Name, Scan: "#" + strconv.FormatInt(int64(s.Scan), 10),
			Report: reportPath(s.Scan), Counts: categoryCounts(s.Remaining), Total: s.Total,
		})
	}
	if d.TrendMore > 0 {
		page.TrendMore = "이전 스캔 " + strconv.Itoa(d.TrendMore) + "개는 싣지 않았습니다."
	}

	for _, r := range d.Rules {
		row := ruleRow{Tool: r.Tool, Rule: r.Rule, FalsePositive: r.FalsePositive, Bundles: r.Bundles, Share: noFigure}
		if r.Bundles > 0 {
			row.Share = strconv.Itoa(r.FalsePositive*100/r.Bundles) + "%"
		}
		page.Rules = append(page.Rules, row)
	}
	return page
}

func dashboardTiles(d dashboard.Dashboard) []dashboardTile {
	left := dashboardTile{Label: "남은 항목", Class: "untriaged", Figure: d.Remaining.Total(), Sub: splitText(d.Remaining), Delta: "이전 스캔 없음"}
	if d.HasDelta {
		left.Delta = "지난 스캔 대비 " + signed(d.Delta)
	}
	if d.Project != 0 {
		left.Href = triagePath(d.Project)
	}
	return []dashboardTile{
		left,
		{Label: "외부화 완료", Class: "externalize", Figure: d.Externalized.Bundles, Sub: "위치 " + strconv.Itoa(d.Externalized.Locations) + "곳"},
		{Label: "삭제·마스킹 완료", Class: "mask", Figure: d.Redacted.Bundles, Sub: "위치 " + strconv.Itoa(d.Redacted.Locations) + "곳"},
		{Label: "오탐", Class: "false-positive", Figure: d.FalsePositive.Bundles},
		{Label: "보류", Class: "hold", Figure: d.Remaining.Hold},
	}
}

// splitText words what is left of s by verdict.
func splitText(s dashboard.Split) string {
	return "미판정 " + strconv.Itoa(s.Undecided) + " · 보류 " + strconv.Itoa(s.Hold) + " · 적용 대기 " + strconv.Itoa(s.Pending)
}

func newDashboardRow(r dashboard.Row, current project.ID, p dashboard.Period) dashboardRow {
	row := dashboardRow{
		Href: dashboardHref(r.Project.ID, p), Open: projectPath(r.Project.ID), Name: r.Project.Name,
		Current: r.Project.ID == current, Remaining: noFigure, Rate: noFigure, Hold: noFigure,
	}
	if r.Last.ID != 0 {
		row.Last = formatTime(r.Last.RequestedAt)
		row.Status, row.StatusLabel = string(r.Last.Status), statusLabel(r.Last.Status)
	}
	if !r.Scanned {
		return row
	}
	row.Remaining, row.Hold = strconv.Itoa(r.Remaining), strconv.Itoa(r.Hold)
	if r.Rate >= 0 {
		row.Rate = strconv.Itoa(r.Rate) + "%"
	}
	return row
}

// categoryCounts returns the counts of m in the order of categoryOrder.
func categoryCounts(m map[finding.Category]int) []int {
	out := make([]int, 0, len(categoryOrder()))
	for _, c := range categoryOrder() {
		out = append(out, m[c])
	}
	return out
}
