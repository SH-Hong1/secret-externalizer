// Package dashboard shapes the numbers of the dashboard: what the newest
// succeeded scan of each project leaves to do, and how that changed from
// scan to scan. Nothing here holds a value, a fingerprint, a preview or a
// line of source.
package dashboard

import (
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// The caps that keep the dashboard small enough to draw.
const (
	MaxPoints = 30 // the most scans the trend shows
	MaxRules  = 10 // the most rules the false-positive list shows
)

// ErrUnknownPeriod means a period name is not one of the Period constants.
var ErrUnknownPeriod = errors.New("unknown period")

// Period is the span of time the trend covers.
type Period string

// The values of Period.
const (
	PeriodAll     Period = "all"
	PeriodWeek    Period = "7d"
	PeriodMonth   Period = "30d"
	PeriodQuarter Period = "90d"
)

// ParsePeriod returns the period named s. The empty string is PeriodAll.
func ParsePeriod(s string) (Period, error) {
	switch p := Period(s); p {
	case "":
		return PeriodAll, nil
	case PeriodAll, PeriodWeek, PeriodMonth, PeriodQuarter:
		return p, nil
	}
	return "", ErrUnknownPeriod
}

// Since returns the start of the period that ends at now, or the zero
// time for PeriodAll.
func (p Period) Since(now time.Time) time.Time {
	const day = 24 * time.Hour
	switch p {
	case PeriodWeek:
		return now.Add(-7 * day)
	case PeriodMonth:
		return now.Add(-30 * day)
	case PeriodQuarter:
		return now.Add(-90 * day)
	case PeriodAll:
		return time.Time{}
	}
	return time.Time{}
}

// Filter selects what the dashboard shows: one project, or every project
// when Project is zero, and the trend of the scans that finished at or
// after Since, or of every scan when Since is zero.
type Filter struct {
	Project project.ID
	Since   time.Time
}

// Split is a number of bundles that are left, by the verdict they have
// now: none, hold, or externalize or redact.
type Split struct{ Undecided, Hold, Pending int }

// Total returns the bundles of s.
func (s Split) Total() int { return s.Undecided + s.Hold + s.Pending }

func (s Split) add(o Split) Split {
	return Split{s.Undecided + o.Undecided, s.Hold + o.Hold, s.Pending + o.Pending}
}

// Point is what one succeeded scan found and is not a false positive,
// by category. Baseline marks a scan that finished before the trend
// starts: it is not shown, but its project counts from the start.
type Point struct {
	Project   project.ID
	Scan      scan.ID
	At        time.Time
	Baseline  bool
	Remaining map[finding.Category]Split
}

// ProjectState is a project with its newest scan in any state (a zero ID
// when it was never scanned) and the numbers of its newest succeeded scan
// (Basis) and of the succeeded scan before that (Previous); each is zero
// when there is no such scan.
type ProjectState struct {
	Project        project.Project
	Last           scan.Scan
	Basis          scan.ID
	Previous       scan.ID
	Totals         report.Totals
	PreviousTotals report.Totals
}

// Rule counts the bundles a rule of a scanner found in the newest
// succeeded scans, and those of them that are false positives.
type Rule struct {
	Tool, Rule             string
	Bundles, FalsePositive int
}

// Data is what the database tells for a dashboard. Projects holds every
// project by name, whatever the filter. Points holds, for the projects of
// the filter, the newest MaxPoints succeeded scans of the period in the
// order they finished and, as baselines, the newest succeeded scan of
// each project that finished before the first of them. PointTotal counts
// the succeeded scans of the period. Rules is ordered by FalsePositive,
// largest first.
type Data struct {
	Projects   []ProjectState
	Points     []Point
	PointTotal int
	Rules      []Rule
}

// Step is one bar of the trend: the scan that finished, and what was left
// in the projects of the filter once it had.
type Step struct {
	Project   project.ID
	Name      string // the project's name
	Scan      scan.ID
	At        time.Time
	Remaining map[finding.Category]int
	Total     int
}

// Row is one line of the project table. Rate is the share of done
// bundles in percent, or -1 when the project has nothing done and
// nothing left. Scanned is false when no scan of the project succeeded.
type Row struct {
	Project   project.Project
	Last      scan.Scan
	Scanned   bool
	Remaining int
	Hold      int
	Rate      int
}

// Dashboard is the dashboard for one filter, ready to be shown.
type Dashboard struct {
	Project       project.ID // the filter's project, zero for all
	Scanned       int        // projects of the filter with a succeeded scan
	Remaining     Split
	Delta         int  // Remaining.Total() minus that of the scans before
	HasDelta      bool // false when no project of the filter has a scan before
	Externalized  report.Count
	Redacted      report.Count
	FalsePositive report.Count
	Trend         []Step
	TrendMore     int // scans of the period the trend leaves out
	Categories    map[finding.Category]Split
	Rules         []Rule
	Rows          []Row
}

var allCategories = []finding.Category{
	finding.CategoryPartnerSession, finding.CategoryStorage, finding.CategorySSH,
	finding.CategoryCustomerData, finding.CategoryOther,
}

func remainingOf(t report.Totals) Split {
	return Split{t.Undecided.Bundles, t.Hold.Bundles, t.Pending.Bundles}
}

func addCount(a, b report.Count) report.Count {
	return report.Count{Bundles: a.Bundles + b.Bundles, Locations: a.Locations + b.Locations}
}

// Build shapes d for the filter f. It keeps no slice or map of d.
func Build(d Data, f Filter) Dashboard {
	out := Dashboard{Project: f.Project}

	names := make(map[project.ID]string, len(d.Projects))
	for _, p := range d.Projects {
		names[p.Project.ID] = p.Project.Name
		out.Rows = append(out.Rows, row(p))
		if f.Project != 0 && p.Project.ID != f.Project {
			continue
		}
		if p.Basis == 0 {
			continue
		}
		out.Scanned++
		out.Remaining = out.Remaining.add(remainingOf(p.Totals))
		out.Externalized = addCount(out.Externalized, p.Totals.Externalized)
		out.Redacted = addCount(out.Redacted, p.Totals.Redacted)
		out.FalsePositive = addCount(out.FalsePositive, p.Totals.FalsePositive)
		if p.Previous != 0 {
			out.HasDelta = true
			out.Delta += remainingOf(p.Totals).Total() - remainingOf(p.PreviousTotals).Total()
		}
	}

	out.Trend, out.Categories = trend(d.Points, names)
	out.TrendMore = max(d.PointTotal-len(out.Trend), 0)
	out.Rules = slices.Clone(d.Rules[:min(len(d.Rules), MaxRules)])
	return out
}

func row(p ProjectState) Row {
	r := Row{Project: p.Project, Last: p.Last, Scanned: p.Basis != 0, Rate: -1}
	r.Project.Excludes = slices.Clone(r.Project.Excludes)
	r.Last.ToolVersions = maps.Clone(r.Last.ToolVersions)
	r.Last.SkippedFiles = slices.Clone(r.Last.SkippedFiles)
	r.Last.ScannerErrors = maps.Clone(r.Last.ScannerErrors)
	if !r.Scanned {
		return r
	}
	left := remainingOf(p.Totals)
	r.Remaining = left.Total()
	r.Hold = left.Hold
	done := p.Totals.Externalized.Bundles + p.Totals.Redacted.Bundles
	if done+r.Remaining > 0 {
		r.Rate = done * 100 / (done + r.Remaining)
	}
	return r
}

// trend replays the points: baselines first set what each project has,
// then every other point replaces its project's numbers and adds a step
// with the sum over all projects. It also returns, per category, the sum
// over the newest point of every project.
func trend(points []Point, names map[project.ID]string) ([]Step, map[finding.Category]Split) {
	current := map[project.ID]Point{}
	for _, p := range points {
		if p.Baseline {
			current[p.Project] = p
		}
	}
	var steps []Step
	for _, p := range points {
		if p.Baseline {
			continue
		}
		current[p.Project] = p
		sum := sumOf(current)
		s := Step{
			Project: p.Project, Name: names[p.Project], Scan: p.Scan, At: p.At,
			Remaining: make(map[finding.Category]int, len(sum)),
		}
		for c, v := range sum {
			s.Remaining[c] = v.Total()
			s.Total += v.Total()
		}
		steps = append(steps, s)
	}

	// The newest point of a project is the one with the largest scan ID,
	// whether or not it is a baseline.
	newest := map[project.ID]Point{}
	for _, p := range points {
		if n, ok := newest[p.Project]; !ok || p.Scan > n.Scan {
			newest[p.Project] = p
		}
	}
	return steps, sumOf(newest)
}

func sumOf(points map[project.ID]Point) map[finding.Category]Split {
	sum := make(map[finding.Category]Split, len(allCategories))
	for _, c := range allCategories {
		sum[c] = Split{}
	}
	for _, p := range points {
		for c, v := range p.Remaining {
			sum[c] = sum[c].add(v)
		}
	}
	return sum
}
