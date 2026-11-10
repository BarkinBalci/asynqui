package chart

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNiceScale(t *testing.T) {
	tests := []struct {
		max  int64
		want Scale
	}{
		{-5, Scale{1, 3}},
		{0, Scale{1, 3}},
		{1, Scale{1, 3}},
		{3, Scale{1, 3}},
		{4, Scale{2, 6}},
		{7, Scale{5, 15}}, // 2.5 is not a whole step
		{15, Scale{5, 15}},
		{16, Scale{10, 30}},
		{61, Scale{25, 75}},
		{211, Scale{100, 300}},
		{540, Scale{200, 600}},
		{70000, Scale{25000, 75000}},
		{75001, Scale{50000, 150000}},
		{231775, Scale{100000, 300000}},
		{math.MaxInt64 / 4, Scale{1e18, 3e18}},
	}
	for _, tt := range tests {
		got := NiceScale(tt.max)
		if got != tt.want {
			t.Errorf("NiceScale(%d) = %+v, want %+v", tt.max, got, tt.want)
		}
		if got.Max < tt.max {
			t.Errorf("NiceScale(%d) tops out at %d", tt.max, got.Max)
		}
	}
}

func TestTicks(t *testing.T) {
	if diff := cmp.Diff([]int64{0, 25000, 50000, 75000}, Scale{25000, 75000}.Ticks()); diff != "" {
		t.Error(diff)
	}
}

func TestPlot(t *testing.T) {
	p := Plot{Width: 1000, Height: 240, N: 4}
	s := Scale{Step: 10, Max: 30}
	tests := []struct {
		name   string
		values []int64
		line   string
		area   string
	}{
		{"none", nil, "", ""},
		{"single point is a dot", []int64{15}, "M125 120 L125 120", ""},
		{"zeros lie on the bottom", []int64{0, 0, 0, 0}, "M125 240 L375 240 L625 240 L875 240", "M125 240 L375 240 L625 240 L875 240 L875 240 L125 240 Z"},
		{"scaled and clamped", []int64{30, 10, -4, 99}, "M125 0 L375 160 L625 240 L875 0", "M125 0 L375 160 L625 240 L875 0 L875 240 L125 240 Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Line(tt.values, s); got != tt.line {
				t.Errorf("Line = %q, want %q", got, tt.line)
			}
			if got := p.Area(tt.values, s); got != tt.area {
				t.Errorf("Area = %q, want %q", got, tt.area)
			}
		})
	}
	if p.SlotX(1) != 250 || p.SlotWidth() != 250 || p.X(3) != 875 {
		t.Errorf("slots: %v %v %v", p.SlotX(1), p.SlotWidth(), p.X(3))
	}
	seven := Plot{Width: 1000, Height: 240, N: 7}
	if got := Num(seven.X(0)); got != "71.4" {
		t.Errorf("X(0) of 7 = %q", got)
	}
	empty := Plot{Width: 1000, Height: 240}
	if empty.X(0) != 0 || empty.SlotWidth() != 1000 || empty.Y(5, Scale{}) != 240 {
		t.Error("an empty plot must not divide by zero")
	}
}
