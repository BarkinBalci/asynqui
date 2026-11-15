package components

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/a-h/templ"
	"github.com/google/go-cmp/cmp"
)

func label(s string) templ.Component { return templ.Raw(s) }

// withChildren renders c with s as its children.
func withChildren(c templ.Component, s string) templ.Component { return withChild(c, label(s)) }

// withChild renders c with child as its children.
func withChild(c, child templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return c.Render(templ.WithChildren(ctx, child), w)
	})
}

// wrapIn wraps c in the markup it needs to parse, such as a table row.
func wrapIn(open, closing string, c templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, open); err != nil {
			return err
		}
		if err := c.Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, closing)
		return err
	})
}

func inHead(c templ.Component) templ.Component {
	return wrapIn("<table><thead><tr>", "</tr></thead></table>", c)
}
func inRow(c templ.Component) templ.Component {
	return wrapIn("<table><tbody><tr>", "</tr></tbody></table>", c)
}

func TestSortHeader(t *testing.T) {
	tests := []struct {
		dir       SortDir
		num       bool
		wantAria  string
		wantClass string
		wantPath  string
		on        bool
	}{
		{SortNone, false, "none", "", "M2 4.5L5 1.5L8 4.5M2 7.5L5 10.5L8 7.5", false},
		{SortAsc, true, "ascending", "num", "M2 7.5L5 4.5L8 7.5", true},
		{SortDesc, true, "descending", "num", "M2 4.5L5 7.5L8 4.5", true},
	}
	for _, tt := range tests {
		doc := render(t, inHead(withChildren(SortHeader(SortHead{ID: "sort-size", URL: "/c/main?sort=size&dir=asc", Dir: tt.dir, Num: tt.num}), "Size")))
		th := doc.Find("th")
		if a, _ := th.Attr("aria-sort"); a != tt.wantAria {
			t.Errorf("%v: aria-sort = %q", tt.dir, a)
		}
		if c, _ := th.Attr("class"); c != tt.wantClass {
			t.Errorf("%v: class = %q", tt.dir, c)
		}
		a := th.Find("a#sort-size.sort")
		for attr, want := range map[string]string{"href": "/c/main?sort=size&dir=asc", "hx-get": "/c/main?sort=size&dir=asc", "hx-swap": "none", "hx-push-url": "true"} {
			if got, _ := a.Attr(attr); got != want {
				t.Errorf("%v: %s = %q", tt.dir, attr, got)
			}
		}
		if text(a) != "Size" {
			t.Errorf("label = %q", text(a))
		}
		if d, _ := a.Find("svg path").Attr("d"); d != tt.wantPath {
			t.Errorf("%v: icon = %q", tt.dir, d)
		}
		if a.Find("svg").HasClass("sort-on") != tt.on {
			t.Errorf("%v: sort-on = %v", tt.dir, !tt.on)
		}
	}
}

func TestSelectCells(t *testing.T) {
	doc := render(t, inRow(withChildren(SelectCell(RowSelect{ID: HexID("qsel-", "a b"), Form: "f", Name: "queue", Value: "a b"}), "Select queue a b")))
	in := doc.Find("td.cb-col input#qsel-612062.cb[type=checkbox]")
	for attr, want := range map[string]string{"form": "f", "name": "queue", "value": "a b", "hx-preserve": "true"} {
		if got, _ := in.Attr(attr); got != want {
			t.Errorf("%s = %q", attr, got)
		}
	}
	if l := doc.Find(`label.sr-only[for=qsel-612062]`); text(l) != "Select queue a b" {
		t.Errorf("label = %q", text(l))
	}
	doc = render(t, inRow(withChildren(SelectCell(RowSelect{ID: "x", Form: "f", Name: "id", Value: "v", Fresh: true}), "Select")))
	if _, ok := doc.Find("input#x").Attr("hx-preserve"); ok {
		t.Error("a fresh checkbox is preserved")
	}
	doc = render(t, inHead(withChildren(SelectAllHeader("qsel-all", "f"), "Select all queues")))
	if v, _ := doc.Find("th.cb-col input#qsel-all").Attr("data-select-all"); v != "f" {
		t.Errorf("select-all form = %q", v)
	}
	doc = render(t, wrapIn("<table><tbody>", "</tbody></table>", withChildren(EmptyRow(13), "No queues")))
	if td := doc.Find("tr.empty-row td.empty"); td.AttrOr("colspan", "") != "13" || text(td) != "No queues" {
		t.Errorf("empty row = %q", text(td))
	}
}

func TestBadges(t *testing.T) {
	for paused, want := range map[bool]string{false: "badge-ok Running", true: "badge-warn Paused"} {
		b := render(t, QueueStateBadge(paused)).Find("span.badge")
		cls, _ := b.Attr("class")
		if got := strings.TrimPrefix(cls, "badge ") + " " + text(b); got != want {
			t.Errorf("paused=%v: %q", paused, got)
		}
	}
	if c, _ := render(t, withChildren(Badge(ToneNeutral), "x")).Find("span").Attr("class"); c != "badge badge-neutral" {
		t.Errorf("neutral = %q", c)
	}
}

// TestActionButton: a role that may not act gets a disabled button with
// only a native title (no lock icon, no text); an allowed one submits the
// selection form, through htmx unless it confirms first.
func TestActionButton(t *testing.T) {
	base := Action{ID: "act-pause", URL: "/c/main/queues/pause", Form: "queues-form", Need: RoleOperator, Enable: EnableState, State: "running"}
	tests := []struct {
		name  string
		a     func(Action) Action
		attrs map[string]string
		gone  []string
	}{
		{"operator allowed", func(a Action) Action { a.Allowed = true; return a },
			map[string]string{"type": "submit", "form": "queues-form", "formaction": "/c/main/queues/pause", "hx-post": "/c/main/queues/pause",
				"hx-swap": "none", "data-enable": "state", "data-state": "running", "class": "btn sm"},
			[]string{"disabled", "title", "data-confirm", "aria-label"}},
		{"viewer", func(a Action) Action { return a },
			map[string]string{"type": "button", "disabled": "", "title": "Requires operator", "aria-label": "Pause (requires operator)", "class": "btn sm"},
			[]string{"hx-post", "formaction", "data-enable", "form"}},
		{"operator on an admin action", func(a Action) Action {
			a.Need, a.Danger, a.Confirm, a.Enable = RoleAdmin, true, "dlg", EnableAny
			return a
		}, map[string]string{"type": "button", "disabled": "", "title": "Requires admin", "aria-label": "Pause (requires admin)", "class": "btn sm danger"},
			[]string{"hx-post", "data-confirm", "data-enable"}},
		{"admin with confirmation", func(a Action) Action {
			a.Allowed, a.Need, a.Danger, a.Confirm, a.Enable = true, RoleAdmin, true, "dlg", EnableAny
			return a
		}, map[string]string{"type": "submit", "data-confirm": "dlg", "data-enable": "any", "formaction": "/c/main/queues/pause", "class": "btn sm danger"},
			[]string{"hx-post", "disabled", "title", "data-state"}},
		{"task page: large, primary, named connection", func(a Action) Action {
			a.Large, a.Primary, a.Conn, a.Need = true, true, "main", RoleAdmin
			return a
		}, map[string]string{"type": "button", "title": "Requires admin on main", "aria-label": "Pause (requires admin on main)", "class": "btn primary"},
			[]string{"hx-post"}},
		{"enabled only when every selected row has a flag", func(a Action) Action {
			a.Allowed, a.Enable, a.Flag, a.Blocked = true, EnableEvery, "empty", "Queue is not empty"
			return a
		}, map[string]string{"data-enable": "every", "data-flag": "empty", "data-blocked-title": "Queue is not empty"},
			[]string{"data-state", "title", "disabled"}},
		{"select all N confirmation", func(a Action) Action {
			a.Allowed, a.ConfirmAll = true, "all-dlg"
			return a
		}, map[string]string{"data-confirm-all": "all-dlg", "hx-post": "/c/main/queues/pause"}, []string{"data-confirm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := render(t, ActionButton(tt.a(base), IconPause("14"), "Pause"))
			b := doc.Find("button#act-pause")
			for k, want := range tt.attrs {
				if got, ok := b.Attr(k); !ok || got != want {
					t.Errorf("%s = %q (%v), want %q", k, got, ok, want)
				}
			}
			for _, k := range tt.gone {
				if _, ok := b.Attr(k); ok {
					t.Errorf("unexpected %s", k)
				}
			}
			if text(b) != "Pause" || b.Find("svg").Length() != 1 {
				t.Errorf("content = %q, %d icons", text(b), b.Find("svg").Length())
			}
		})
	}
}

func TestSelectionToolbar(t *testing.T) {
	doc := render(t, withChildren(SelectionToolbar("queues-form", FilterInput("queues-filter", "queues-rows", "Filter queues")), "<button>x</button>"))
	bar := doc.Find(".tbar[data-toolbar=queues-form]")
	if in := bar.Find("input#queues-filter"); in.AttrOr("data-filter", "") != "#queues-rows" || in.AttrOr("placeholder", "") != "Filter queues" {
		t.Error("filter input wrong")
	}
	sel := bar.Find(".selc[data-sel-label]")
	if _, hidden := sel.Attr("hidden"); !hidden || text(sel) != "All 0 selected" || sel.Find("[data-sel-count]").Length() != 1 ||
		sel.Find("[data-sel-all][hidden]").Length() != 1 {
		t.Errorf("selected label = %q", text(sel))
	}
	if bar.Find(".tacts button").Length() != 1 || bar.Find(".selc-live[role=status] > .selc[data-sel-label]").Length() != 1 {
		t.Error("children missing")
	}
}

func TestConfirmDialog(t *testing.T) {
	d := Dialog{ID: "queues-delete", ConfirmURL: "/c/main/queues/delete", Form: "queues-form", Danger: true}
	doc := render(t, withChildren(ConfirmDialog(d, label("Delete queues"), label("Delete")), `<ul data-sel-names></ul>`))
	dlg := doc.Find("dialog#queues-delete.dialog")
	if l, _ := dlg.Attr("aria-labelledby"); l != "queues-delete-title" || text(dlg.Find("h2#queues-delete-title")) != "Delete queues" {
		t.Error("dialog title")
	}
	if dlg.Find("[data-dialog-close]").Length() != 2 || text(dlg.Find("button[autofocus]")) != "Cancel" || dlg.Find("ul[data-sel-names]").Length() != 1 {
		t.Error("close buttons or body missing")
	}
	b := dlg.Find("button#queues-delete-confirm")
	for k, want := range map[string]string{"type": "submit", "form": "queues-form", "formaction": "/c/main/queues/delete", "hx-post": "/c/main/queues/delete",
		"hx-swap": "none", "name": "confirm", "value": "1", "class": "btn danger"} {
		if got, _ := b.Attr(k); got != want {
			t.Errorf("confirm %s = %q, want %q", k, got, want)
		}
	}
}

func TestToasts(t *testing.T) {
	doc := render(t, ToastRegion(nil))
	region := doc.Find("#toasts.toasts[role=status]")
	if region.Length() != 1 || region.Children().Length() != 0 {
		t.Error("region")
	}
	if doc.Find("template#toast-request-failed").Length() != 1 {
		t.Error("request-failed template missing")
	}
	doc = render(t, withChild(ToastsOOB(), withChildren(Toast(), "Queue x is not empty")))
	oob := doc.Find(`div[hx-swap-oob="beforeend:#toasts"]`)
	if text(oob.Find(".toast .toast-body")) != "Queue x is not empty" || oob.Find("button.js-only[data-toast-close][aria-label=Dismiss]").Length() != 1 {
		t.Errorf("toast = %q", text(oob))
	}
}

func chartFixture(n int) Chart {
	c := Chart{ID: "tp", ViewW: "1000", ViewH: "240", ProcessedTicks: []int64{0, 25000, 50000, 75000}, FailedTicks: []int64{0, 100, 200, 300},
		GridY: []string{"160", "80", "0"}, ProcessedLine: "M1 2 L3 4", ProcessedArea: "M1 2 L3 4 Z", FailedLine: "M1 3 L3 3", Peak: -1}
	end := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for i := range n {
		c.Days = append(c.Days, ChartDay{Date: end.AddDate(0, 0, i-n+1), Processed: int64(70000 + i), Failed: int64(160 * i), Today: i == n-1, Label: i == n-1 || i%2 == 1, BandX: "0", BandW: "10"})
		if i > 0 {
			c.Peak = i
		}
	}
	return c
}

func TestThroughputChart(t *testing.T) {
	doc := render(t, ThroughputChart(chartFixture(7)))
	svg := doc.Find("svg.chart-svg")
	if v, _ := svg.Attr("viewBox"); v != "0 0 1000 240" {
		t.Errorf("viewBox = %q", v)
	}
	if v, _ := svg.Attr("preserveAspectRatio"); v != "none" {
		t.Errorf("preserveAspectRatio = %q", v)
	}
	if r, _ := svg.Attr("role"); r != "img" || svg.AttrOr("aria-labelledby", "") != "tp-title tp-desc" {
		t.Error("svg has no accessible name")
	}
	if got := text(svg.ChildrenFiltered("title")); got != "Throughput" {
		t.Errorf("title = %q", got)
	}
	desc := text(svg.Find("desc#tp-desc"))
	if desc != "Processed and failed tasks per day, last 7 days. Processed on the left axis, failed on the right axis. Failures peak at 960 on Nov 8." {
		t.Errorf("desc = %q", desc)
	}
	for sel, d := range map[string]string{"path.area-processed": "M1 2 L3 4 Z", "path.line.line-processed": "M1 2 L3 4", "path.line.line-failed": "M1 3 L3 3"} {
		if got, _ := svg.Find(sel).Attr("d"); got != d {
			t.Errorf("%s d = %q", sel, got)
		}
	}
	if svg.Find("line.grid").Length() != 3 || svg.Find("line.baseline").Length() != 1 {
		t.Error("grid lines")
	}
	// Color comes from classes only.
	svg.Find("*").Each(func(_ int, s *goquery.Selection) {
		for _, a := range []string{"fill", "stroke", "style", "color"} {
			if _, ok := s.Attr(a); ok {
				t.Errorf("<%s> has a %s attribute", goquery.NodeName(s), a)
			}
		}
	})
	bands := svg.Find("rect.band")
	if bands.Length() != 7 {
		t.Fatalf("%d bands", bands.Length())
	}
	if got := bands.Last().Find("title").Text(); got != "Today, Nov 8\nProcessed 70,006\nFailed 960\nFailure rate 1.37%" {
		t.Errorf("last tooltip = %q", got)
	}
	if got := bands.First().Find("title").Text(); !strings.HasPrefix(got, "Mon, Nov 2\n") {
		t.Errorf("first tooltip = %q", got)
	}
	var left, right, xl []string
	doc.Find(".yaxis-processed span").Each(func(_ int, s *goquery.Selection) { left = append(left, s.Text()) })
	doc.Find(".yaxis-failed span").Each(func(_ int, s *goquery.Selection) { right = append(right, s.Text()) })
	doc.Find(".xaxis .xl").Each(func(_ int, s *goquery.Selection) { xl = append(xl, s.Text()) })
	if diff := cmp.Diff([]string{"0", "25k", "50k", "75k"}, left); diff != "" {
		t.Errorf("left ticks (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"0", "100", "200", "300"}, right); diff != "" {
		t.Errorf("right ticks (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"Nov 3", "Nov 5", "Nov 7", "Today"}, xl); diff != "" {
		t.Errorf("x labels (-want +got):\n%s", diff)
	}
	if doc.Find(".xaxis .xcell").Length() != 7 || doc.Find(".xl-last").Length() != 1 {
		t.Error("x cells")
	}
	// The same data for screen readers.
	tbl := doc.Find("table#tp-data.sr-only")
	var head []string
	tbl.Find("thead th").Each(func(_ int, s *goquery.Selection) { head = append(head, s.Text()) })
	if strings.Join(head, "|") != "Date|Processed|Failed|Failure rate" || tbl.Find("tbody tr").Length() != 7 {
		t.Errorf("data table head %q rows %d", head, tbl.Find("tbody tr").Length())
	}
	var last []string
	tbl.Find("tbody tr").Last().Children().Each(func(_ int, s *goquery.Selection) { last = append(last, text(s)) })
	if diff := cmp.Diff([]string{"Today, Nov 8", "70,006", "960", "1.37%"}, last); diff != "" {
		t.Errorf("last data row (-want +got):\n%s", diff)
	}
	if doc.Find(".chart-legend .lg-processed").Length() != 1 || doc.Find(".chart-legend .lg-failed").Length() != 1 {
		t.Error("legend")
	}

	// No failures: no peak sentence.
	c := chartFixture(1)
	c.Peak = -1
	if d := text(render(t, ThroughputChart(c)).Find("desc")); strings.Contains(d, "peak") {
		t.Errorf("desc = %q", d)
	}
}

func TestHexID(t *testing.T) {
	if got := HexID("qsel-", "a/b c"); got != "qsel-612f622063" {
		t.Errorf("HexID = %q", got)
	}
	if HexID("q-", "ab") == HexID("q-", "a") || HexID("q-", "") != "q-" {
		t.Error("collision")
	}
}
