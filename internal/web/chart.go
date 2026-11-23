package web

import (
	"math"
	"strconv"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
)

// The box of the trend chart, in the units of its viewBox: the whole
// picture and the margins around the plot, which hold the numbers of the
// grid on the left and the names of the bars below.
const (
	chartWidth  = 960
	chartHeight = 300
	chartLeft   = 48
	chartBottom = 28
	chartTop    = 12
	chartRight  = 8
)

// The bars of the chart: never wider than chartMaxBar, a share of their
// slot so that a gap stays between them, and named one in two when there
// are more than chartMaxLabels.
const (
	chartMaxBar    = 40
	chartBarShare  = 0.6
	chartMaxLabels = 15
	chartMaxTicks  = 6
)

// chartSegment is one category's part of a bar.
type chartSegment struct {
	Class     string // "chart-" + the category name
	Y, Height int
}

// chartBar is one bar: its place, the label under it (empty when the
// label is left out), the text of its tooltip, and its segments from the
// bottom up.
type chartBar struct {
	X, Width int
	Label    string
	Title    string
	Segments []chartSegment
}

// chartTick is one horizontal grid line with its number.
type chartTick struct {
	Y     int
	Label string
}

// barChart is a stacked bar chart in the coordinates of its viewBox.
type barChart struct {
	Width, Height int
	// GridLeft and GridRight are the ends of the grid lines, TickX is where
	// the numbers of the grid end, and LabelY is the baseline of the names
	// of the bars.
	GridLeft, GridRight int
	TickX, LabelY       int
	Ticks               []chartTick
	Bars                []chartBar
}

// newBarChart lays out one bar per step, stacked by category in the
// order of categoryOrder.
// all says that the steps sum every project.
func newBarChart(steps []dashboard.Step, all bool) barChart {
	c := barChart{
		Width: chartWidth, Height: chartHeight,
		GridLeft: chartLeft, GridRight: chartWidth - chartRight,
		TickX: chartLeft - 6, LabelY: chartHeight - chartBottom + 20,
	}
	largest := 0
	for _, s := range steps {
		largest = max(largest, s.Total)
	}
	top := niceCeil(largest)
	bottom := chartHeight - chartBottom
	plot := float64(bottom - chartTop)
	// y places a number on the vertical scale. Every segment is placed by
	// the sum below it, so rounding never opens a gap or an overlap.
	y := func(v int) int {
		return bottom - int(math.Round(float64(v)*plot/float64(top)))
	}

	step := tickStep(top)
	for v := 0; v <= top; v += step {
		c.Ticks = append(c.Ticks, chartTick{Y: y(v), Label: strconv.Itoa(v)})
	}

	if len(steps) == 0 {
		return c
	}
	slot := float64(chartWidth-chartLeft-chartRight) / float64(len(steps))
	width := max(1, min(chartMaxBar, int(slot*chartBarShare)))
	for i, s := range steps {
		b := chartBar{
			X:     chartLeft + int(math.Round(slot*float64(i)+(slot-float64(width))/2)),
			Width: width,
			Title: barTitle(s, all),
		}
		if len(steps) <= chartMaxLabels || (len(steps)-1-i)%2 == 0 {
			b.Label = "#" + strconv.FormatInt(int64(s.Scan), 10)
		}
		sum := 0
		for _, cat := range categoryOrder() {
			n := s.Remaining[cat]
			if n <= 0 {
				continue
			}
			b.Segments = append(b.Segments, chartSegment{Class: "chart-" + string(cat), Y: y(sum + n), Height: y(sum) - y(sum+n)})
			sum += n
		}
		c.Bars = append(c.Bars, b)
	}
	return c
}

// barTitle is the tooltip of the bar of s: the scan, what was left once it
// finished, and the categories that left anything.
func barTitle(s dashboard.Step, all bool) string {
	total := "남은 항목 " + strconv.Itoa(s.Total) + "개"
	if all {
		total = "전체 " + total
	}
	parts := []string{s.Name, "스캔 #" + strconv.FormatInt(int64(s.Scan), 10), formatTime(s.At), total}
	for _, cat := range categoryOrder() {
		if n := s.Remaining[cat]; n > 0 {
			parts = append(parts, categoryLabel(cat)+" "+strconv.Itoa(n)+"개")
		}
	}
	return strings.Join(parts, " · ")
}

// niceSteps walks the numbers 1, 2, 5, 10, 20, 50, … and calls f with each
// until f returns true, and returns that number.
func niceSteps(f func(int) bool) int {
	for base := 1; ; base *= 10 {
		for _, m := range []int{1, 2, 5} {
			if n := m * base; f(n) {
				return n
			}
		}
	}
}

// niceCeil returns the smallest of 1, 2, 5 × 10ⁿ that is at least n, and 1
// when n is zero.
func niceCeil(n int) int {
	return niceSteps(func(v int) bool { return v >= n })
}

// tickStep returns the smallest of 1, 2, 5 × 10ⁿ that puts at most
// chartMaxTicks grid lines between 0 and top.
func tickStep(top int) int {
	return niceSteps(func(v int) bool { return top/v+1 <= chartMaxTicks })
}
