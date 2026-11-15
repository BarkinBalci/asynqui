package web

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/chart"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// Chart geometry, in SVG user units; the SVG stretches to its box.
const (
	chartW = 1000
	chartH = 240
)

// throughput builds the chart card for state v: the range buttons, the
// queue picker and the chart from stats, the history read in parallel
// with the queues. A history that missed the page budget shows
// "Checking"; another failure shows its error.
func (d *deps) throughput(ctx context.Context, name string, v queuesView, rows []conn.Queue, stats []conn.DailyStat, err error) pages.Throughput {
	t := pages.Throughput{Show: true, Queue: v.queue, FormURL: d.url("c", name)}
	for _, q := range rows {
		t.Queues = append(t.Queues, q.Name)
	}
	for _, days := range rangeChoices() {
		t.Ranges = append(t.Ranges, pages.RangeButton{
			ID: "range-" + strconv.Itoa(days), Days: days, Value: rangeParam(days), On: days == v.days,
		})
	}
	// Each control submits its own field; the rest of the state rides
	// along in hidden fields.
	noRange, noQueue := v, v
	noRange.days, noQueue.queue = defaultRange, ""
	t.RangeHidden = params(noRange.values(), "range")
	t.PickerHidden = params(noQueue.values(), "queue")

	switch {
	case err != nil && ctx.Err() != nil:
		t.State = pages.DataChecking
	case err != nil:
		t.State, t.Error = pages.DataFailed, errText(err)
	default:
		t.Chart = buildChart(pages.ChartID, stats)
	}
	return t
}

// params lists q as hidden fields, sorted, without the field skip.
func params(q url.Values, skip string) []pages.Param {
	var out []pages.Param
	for k, vs := range q {
		if k != skip {
			out = append(out, pages.Param{Name: k, Value: vs[0]})
		}
	}
	slices.SortFunc(out, func(a, b pages.Param) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// buildChart lays out the daily counters, oldest first and ending today.
func buildChart(id string, stats []conn.DailyStat) components.Chart {
	n := len(stats)
	plot := chart.Plot{Width: chartW, Height: chartH, N: n}
	processed, failed := make([]int64, n), make([]int64, n)
	var maxP, maxF int64
	peak := -1
	for i, s := range stats {
		processed[i], failed[i] = int64(s.Processed), int64(s.Failed)
		maxP = max(maxP, processed[i])
		if failed[i] > maxF {
			maxF, peak = failed[i], i
		}
	}
	ps, fs := chart.NiceScale(maxP), chart.NiceScale(maxF)
	c := components.Chart{
		ID:             id,
		ViewW:          chart.Num(chartW),
		ViewH:          chart.Num(chartH),
		ProcessedTicks: ps.Ticks(),
		FailedTicks:    fs.Ticks(),
		ProcessedLine:  plot.Line(processed, ps),
		ProcessedArea:  plot.Area(processed, ps),
		FailedLine:     plot.Line(failed, fs),
		Peak:           peak,
	}
	for _, v := range ps.Ticks()[1:] {
		c.GridY = append(c.GridY, chart.Num(plot.Y(v, ps)))
	}
	every := labelEvery(n)
	for i, s := range stats {
		back := n - 1 - i
		c.Days = append(c.Days, components.ChartDay{
			Date:      s.Date,
			Processed: processed[i],
			Failed:    failed[i],
			Today:     back == 0,
			Label:     back == 0 || (back%every == 0 && back >= every),
			BandX:     chart.Num(plot.SlotX(i)),
			BandW:     chart.Num(plot.SlotWidth()),
		})
	}
	return c
}

// labelEvery is how many days apart the x labels are, counted back from
// today, so each range shows about seven.
func labelEvery(days int) int {
	switch {
	case days <= 7:
		return 1
	case days <= 14:
		return 2
	case days <= 30:
		return 5
	}
	return max(days/6, 1)
}
