// Package chart computes the geometry of the server-rendered SVG charts:
// nice axis scales and the path data of line series. It returns numbers
// and SVG path strings only; labels are formatted by package format and
// written by the templates.
package chart

import (
	"strconv"
	"strings"
)

// Intervals is the number of equal steps on a value axis, so every axis
// has Intervals+1 ticks at fixed fractions of its height (0, ⅓, ⅔, 1) and
// tick labels can be laid out without per-tick positions.
const Intervals = 3

// Scale is a linear value axis from 0 to Max in Intervals steps of Step.
type Scale struct {
	Step int64
	Max  int64 // Step * Intervals
}

// NiceScale returns the smallest scale whose top is at least maxVal and
// whose step is a round number: 1, 2, 2.5 or 5 times a power of ten, and
// always a whole number (so 2.5 appears only from 25 on). A maxVal of 0
// or less gives the scale 0, 1, 2, 3.
func NiceScale(maxVal int64) Scale {
	if maxVal <= 0 {
		return Scale{Step: 1, Max: Intervals}
	}
	need := (maxVal + Intervals - 1) / Intervals // smallest step that fits
	for mag := int64(1); ; mag *= 10 {
		for _, m := range []struct{ num, den int64 }{{1, 1}, {2, 1}, {5, 2}, {5, 1}} {
			if m.den > 1 && mag < 10 {
				continue // 2.5 is not a whole step
			}
			step := mag * m.num / m.den
			if step >= need {
				return Scale{Step: step, Max: step * Intervals}
			}
		}
		if mag > (1<<62)/10 {
			return Scale{Step: need, Max: need * Intervals}
		}
	}
}

// Ticks returns the tick values from 0 to Max.
func (s Scale) Ticks() []int64 {
	out := make([]int64, Intervals+1)
	for i := range out {
		out[i] = s.Step * int64(i)
	}
	return out
}

// Plot is the drawing area of a chart, in SVG user units, with N equal
// slots along x (one per data point). Points sit in the middle of their
// slot.
type Plot struct {
	Width, Height float64
	N             int
}

// X is the x of the middle of slot i.
func (p Plot) X(i int) float64 {
	if p.N <= 0 {
		return 0
	}
	return (float64(i) + 0.5) / float64(p.N) * p.Width
}

// SlotX is the left edge of slot i, and SlotWidth the width of a slot.
func (p Plot) SlotX(i int) float64 {
	if p.N <= 0 {
		return 0
	}
	return float64(i) / float64(p.N) * p.Width
}

// SlotWidth is the width of one slot.
func (p Plot) SlotWidth() float64 {
	if p.N <= 0 {
		return p.Width
	}
	return p.Width / float64(p.N)
}

// Y is the y of v on scale s: 0 at the bottom, s.Max at the top. Values
// outside the scale are clamped.
func (p Plot) Y(v int64, s Scale) float64 {
	if s.Max <= 0 {
		return p.Height
	}
	f := float64(min(max(v, 0), s.Max)) / float64(s.Max)
	return p.Height - f*p.Height
}

// Line is the SVG path data of values on scale s. A single value is drawn
// as a zero-length segment, which a round line cap shows as a dot. No
// values is the empty path.
func (p Plot) Line(values []int64, s Scale) string {
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	for i, v := range values {
		if i == 0 {
			b.WriteString("M")
		} else {
			b.WriteString(" L")
		}
		b.WriteString(num(p.X(i)) + " " + num(p.Y(v, s)))
	}
	if len(values) == 1 {
		b.WriteString(" L" + num(p.X(0)) + " " + num(p.Y(values[0], s)))
	}
	return b.String()
}

// Area is the path data of the region between the line of values and the
// bottom of the plot. It is empty for fewer than two values.
func (p Plot) Area(values []int64, s Scale) string {
	if len(values) < 2 {
		return ""
	}
	h := num(p.Height)
	return p.Line(values, s) + " L" + num(p.X(len(values)-1)) + " " + h + " L" + num(p.X(0)) + " " + h + " Z"
}

// Num formats an SVG coordinate: at most one decimal, no trailing ".0".
func Num(v float64) string { return num(v) }

func num(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}
