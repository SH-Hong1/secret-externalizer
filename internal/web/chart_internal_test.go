package web

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// chartStep is a step of project 3 for scan id with the given counts.
func chartStep(id scan.ID, counts map[finding.Category]int) dashboard.Step {
	s := dashboard.Step{
		Project: 3, Name: "shop", Scan: id,
		At:        time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Hour),
		Remaining: counts,
	}
	for _, n := range counts {
		s.Total += n
	}
	return s
}

func tickLabels(c barChart) []string {
	var out []string
	for _, t := range c.Ticks {
		out = append(out, t.Label)
	}
	return out
}

// barTop returns the top of b, or the bottom of the plot when b has no
// segment.
func barTop(b barChart, i int) int {
	top := chartHeight - chartBottom
	for _, s := range b.Bars[i].Segments {
		top = min(top, s.Y)
	}
	return top
}

func TestNewBarChart_Stacks(t *testing.T) {
	steps := []dashboard.Step{
		chartStep(1, map[finding.Category]int{finding.CategoryStorage: 6, finding.CategorySSH: 4}),
		chartStep(2, map[finding.Category]int{finding.CategoryStorage: 5, finding.CategoryOther: 7, finding.CategoryCustomerData: 3}),
		chartStep(3, map[finding.Category]int{finding.CategoryPartnerSession: 8, finding.CategorySSH: 0}),
	}
	c := newBarChart(steps, false)
	if len(c.Bars) != len(steps) {
		t.Fatalf("newBarChart(3 steps) has %d bars, want 3", len(c.Bars))
	}
	bottom := chartHeight - chartBottom
	for i, b := range c.Bars {
		sum := 0
		segs := slices.Clone(b.Segments)
		slices.SortFunc(segs, func(a, b chartSegment) int { return a.Y - b.Y })
		for j, s := range segs {
			sum += s.Height
			if j > 0 && segs[j-1].Y+segs[j-1].Height > s.Y {
				t.Errorf("bar %d: segment %v overlaps %v", i, segs[j-1], s)
			}
		}
		if want := bottom - barTop(c, i); sum != want {
			t.Errorf("bar %d: segment heights sum to %d, want %d", i, sum, want)
		}
	}
	if len(c.Bars[2].Segments) != 1 {
		t.Errorf("bar 2 has %d segments, want 1 (a zero count draws none)", len(c.Bars[2].Segments))
	}
	// Totals 10, 15, 8: the bar of 15 is the tallest, the bar of 8 the
	// shortest.
	if barTop(c, 1) >= barTop(c, 0) || barTop(c, 0) >= barTop(c, 2) {
		t.Errorf("bar tops = %d, %d, %d, want the second highest and the third lowest", barTop(c, 0), barTop(c, 1), barTop(c, 2))
	}
	if diff := cmp.Diff([]string{"0", "5", "10", "15", "20"}, tickLabels(c)); diff != "" {
		t.Errorf("newBarChart(totals 10, 15, 8) ticks (-want +got):\n%s", diff)
	}
	if got, want := c.Ticks[len(c.Ticks)-1].Y, chartTop; got != want {
		t.Errorf("newBarChart top tick Y = %d, want %d", got, want)
	}

	for _, tt := range []struct {
		total int
		want  []string
	}{
		{1, []string{"0", "1"}},
		{5, []string{"0", "1", "2", "3", "4", "5"}},
		{10, []string{"0", "2", "4", "6", "8", "10"}},
		{11, []string{"0", "5", "10", "15", "20"}},
	} {
		c := newBarChart([]dashboard.Step{chartStep(1, map[finding.Category]int{finding.CategoryOther: tt.total})}, false)
		if diff := cmp.Diff(tt.want, tickLabels(c)); diff != "" {
			t.Errorf("newBarChart(total %d) ticks (-want +got):\n%s", tt.total, diff)
		}
	}
}

func TestNewBarChart_Empty(t *testing.T) {
	c := newBarChart(nil, false)
	if len(c.Bars) != 0 {
		t.Errorf("newBarChart(nil) has %d bars, want 0", len(c.Bars))
	}
	if diff := cmp.Diff([]string{"0", "1"}, tickLabels(c)); diff != "" {
		t.Errorf("newBarChart(nil) ticks (-want +got):\n%s", diff)
	}

	c = newBarChart([]dashboard.Step{
		chartStep(1, map[finding.Category]int{finding.CategoryOther: 0}),
		chartStep(2, nil),
	}, true)
	if diff := cmp.Diff([]string{"0", "1"}, tickLabels(c)); diff != "" {
		t.Errorf("newBarChart(zero totals) ticks (-want +got):\n%s", diff)
	}
	for i, b := range c.Bars {
		if len(b.Segments) != 0 {
			t.Errorf("newBarChart(zero totals) bar %d has segments %v, want none", i, b.Segments)
		}
	}
}

func TestNewBarChart_Large(t *testing.T) {
	var steps []dashboard.Step
	for i := range 30 {
		steps = append(steps, chartStep(scan.ID(i+1), map[finding.Category]int{
			finding.CategoryStorage: 40000 + i, finding.CategoryCustomerData: 8731 - i,
		}))
	}
	c := newBarChart(steps, true)
	if got := c.Ticks[len(c.Ticks)-1].Label; got != "50000" {
		t.Errorf("newBarChart(total 48731) top tick = %q, want %q", got, "50000")
	}
	inside := func(what string, x, y int) {
		t.Helper()
		if x < 0 || x > c.Width || y < 0 || y > c.Height {
			t.Errorf("%s at (%d, %d) is outside %dx%d", what, x, y, c.Width, c.Height)
		}
	}
	for _, tk := range c.Ticks {
		inside("tick "+tk.Label, 0, tk.Y)
	}
	labels := 0
	for i, b := range c.Bars {
		inside("bar left", b.X, 0)
		inside("bar right", b.X+b.Width, 0)
		if b.Width < 1 {
			t.Errorf("bar %d width = %d, want at least 1", i, b.Width)
		}
		if i > 0 && c.Bars[i-1].X+c.Bars[i-1].Width >= b.X {
			t.Errorf("bar %d (x %d) overlaps bar %d (x %d, width %d)", i, b.X, i-1, c.Bars[i-1].X, c.Bars[i-1].Width)
		}
		for _, s := range b.Segments {
			inside("segment top", b.X, s.Y)
			inside("segment bottom", b.X, s.Y+s.Height)
		}
		if b.Label != "" {
			labels++
		}
	}
	if labels != 15 {
		t.Errorf("newBarChart(30 steps) labels %d bars, want 15", labels)
	}
	if got := c.Bars[29].Label; got != "#30" {
		t.Errorf("newBarChart(30 steps) last label = %q, want %q", got, "#30")
	}
}

func TestNewBarChart_Title(t *testing.T) {
	steps := []dashboard.Step{chartStep(7, map[finding.Category]int{
		finding.CategoryStorage: 3, finding.CategoryCustomerData: 2, finding.CategorySSH: 0,
	})}
	got := newBarChart(steps, false).Bars[0].Title
	for _, want := range []string{"shop", "스캔 #7", "남은 항목 5개", "저장소 접속 정보 3개", "고객정보 2개", formatTime(steps[0].At)} {
		if !strings.Contains(got, want) {
			t.Errorf("newBarChart(scan 7).Title = %q, want it to hold %q", got, want)
		}
	}
	if strings.Contains(got, categoryLabel(finding.CategorySSH)) || strings.Contains(got, "전체") {
		t.Errorf("newBarChart(scan 7).Title = %q, want no zero category and no 전체", got)
	}
	if got := newBarChart(steps, true).Bars[0].Title; !strings.Contains(got, "전체 남은 항목 5개") {
		t.Errorf("newBarChart(scan 7, all).Title = %q, want it to hold %q", got, "전체 남은 항목 5개")
	}
}

func TestNewBarChart_GuideCoordinates(t *testing.T) {
	c := newBarChart(nil, false)
	got := []int{c.GridLeft, c.GridRight, c.TickX, c.LabelY}
	want := []int{chartLeft, chartWidth - chartRight, chartLeft - 6, chartHeight - chartBottom + 20}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("newBarChart(nil) GridLeft, GridRight, TickX, LabelY mismatch (-want +got):\n%s", diff)
	}
	if want := []int{48, 952, 42, 292}; !slices.Equal(got, want) {
		t.Errorf("newBarChart(nil) guides = %v, want %v", got, want)
	}
}
