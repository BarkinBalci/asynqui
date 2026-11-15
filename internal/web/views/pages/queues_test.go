package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

func renderDoc(t *testing.T, m Queues) *goquery.Document {
	t.Helper()
	return renderC(t, QueuesPoll(m))
}

func text(s *goquery.Selection) string { return strings.Join(strings.Fields(s.Text()), " ") }

func tableModel() Queues {
	m := Queues{Health: components.HealthUp, Next: "/c/main", Live: Live{PollURL: "/c/main"}, Throughput: Throughput{Show: false}}
	for c := range NumQueueColumns {
		m.Heads[c] = components.SortHead{ID: "sort-" + string(rune('a'+c)), URL: "/c/main?x", Num: c >= ColSize}
	}
	m.Heads[ColSize].Dir = components.SortDesc
	m.Rows = []QueueRow{
		{Name: "default", URL: "/c/main/queues/default", LinkID: "qlink-1", Select: components.RowSelect{ID: "qsel-1", Form: QueuesFormID, Name: "queue", Value: "default"},
			Size: 1402, Pending: 880, Failed: 540, Memory: 41 << 20, Latency: 12600 * time.Millisecond, LatencyHigh: false},
		{Name: "slow", URL: "/c/main/queues/slow", LinkID: "qlink-2", Select: components.RowSelect{ID: "qsel-2", Form: QueuesFormID, Name: "queue", Value: "slow"},
			Size: 3, Pending: 3, Latency: 2 * time.Minute, LatencyHigh: true},
		{Name: "held", URL: "/c/main/queues/held", LinkID: "qlink-3", Select: components.RowSelect{ID: "qsel-3", Form: QueuesFormID, Name: "queue", Value: "held"},
			Paused: true, Size: 1, Pending: 1, Latency: time.Hour},
	}
	m.Total = QueueRow{Size: 1406, Pending: 884, Failed: 540, Memory: 41 << 20}
	return m
}

func TestQueuesTable(t *testing.T) {
	doc := renderDoc(t, tableModel())
	tbl := doc.Find("#queues-table table#queues.tbl")
	if tbl.Length() != 1 {
		t.Fatal("no table")
	}
	if tbl.Find(`th[aria-sort=descending] a#sort-c`).Length() != 1 || tbl.Find(`th[aria-sort=none]`).Length() != 11 {
		t.Error("sort state")
	}
	if tbl.Find("thead th.num").Length() != 10 {
		t.Errorf("%d numeric headers", tbl.Find("thead th.num").Length())
	}
	rows := tbl.Find("tbody tr")
	if rows.Length() != 3 {
		t.Fatalf("%d rows", rows.Length())
	}
	if s, _ := rows.Eq(2).Attr("data-state"); s != "paused" || text(rows.Eq(2).Find("td").Eq(9)) != "—" {
		t.Error("paused row")
	}
	if !rows.Eq(1).Find("td").Eq(9).HasClass("c-failed") || rows.Eq(0).Find("td").Eq(9).HasClass("c-failed") {
		t.Error("latency highlight")
	}
	if text(rows.Eq(0).Find("td").Eq(9)) != "12.6s" || text(rows.Eq(0).Find("td").Eq(10)) != "41.0 MB" {
		t.Error("latency or memory format")
	}
	var foot []string
	tbl.Find("tfoot tr").Children().Each(func(_ int, s *goquery.Selection) { foot = append(foot, text(s)) })
	if got := strings.Join(foot, "|"); got != "|Total||1,406|884|0|0|0|0|—|41.0 MB|0|540" {
		t.Errorf("total = %q", got)
	}
	if !tbl.Find("tfoot td").Last().HasClass("c-failed") {
		t.Error("total failed not highlighted")
	}
}

func TestQueuesTableEmpty(t *testing.T) {
	m := tableModel()
	m.Rows, m.Total = nil, QueueRow{}
	doc := renderDoc(t, m)
	if got := text(doc.Find("#queues-rows td.empty")); got != "No queues" {
		t.Errorf("empty = %q", got)
	}
	if c, _ := doc.Find("#queues-rows td.empty").Attr("colspan"); c != "13" {
		t.Errorf("colspan = %q", c)
	}
	if doc.Find("tfoot").Length() != 0 {
		t.Error("tfoot without rows")
	}
}

func TestQueuesPollIDs(t *testing.T) {
	m := tableModel()
	m.Throughput = Throughput{Show: true, Ranges: []RangeButton{{ID: "range-7", Days: 7, Value: "7d"}}, Queues: []string{"default"}, FormURL: "/c/main",
		Chart: components.Chart{ID: ChartID, ViewW: "1000", ViewH: "240", Peak: -1}}
	doc := renderDoc(t, m)
	n := 0
	doc.Find("[hx-swap-oob]").Find("a[href], button, input:not([type=hidden]), select, textarea, summary, [tabindex]").Each(func(_ int, s *goquery.Selection) {
		n++
		if id, _ := s.Attr("id"); id == "" {
			h, _ := goquery.OuterHtml(s)
			t.Errorf("focusable without id: %s", h)
		}
	})
	if n < 20 {
		t.Errorf("only %d focusables", n)
	}
	if doc.Find("#throughput.card #tp-queue").Length() != 1 {
		t.Error("chart card missing")
	}
}

func TestFailureToasts(t *testing.T) {
	var b strings.Builder
	err := FailureToasts([]ActionFailure{
		{Verb: VerbPause, Queue: "a", Kind: FailureNotFound},
		{Verb: VerbDelete, Queue: "b", Kind: FailureNotEmpty},
		{Verb: VerbResume, Queue: "c", Detail: "boom"},
		{Verb: VerbPause, Queue: "d", Detail: "x"},
		{Verb: VerbDelete, Queue: "e", Detail: "y"},
	}).Render(context.Background(), &b)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(b.String()))
	var got []string
	doc.Find(".toast-body").Each(func(_ int, s *goquery.Selection) { got = append(got, text(s)) })
	want := "Queue a not found|Queue b is not empty|Could not resume c boom|Could not pause d x|Could not delete e y"
	if strings.Join(got, "|") != want {
		t.Errorf("toasts = %q", got)
	}
}
