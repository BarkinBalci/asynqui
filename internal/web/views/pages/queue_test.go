package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

func renderC(t *testing.T, c templ.Component) *goquery.Document {
	t.Helper()
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func queueModel() Queue {
	m := Queue{Name: "emails", Tab: StateRetry, Total: 41, AllowAll: true, Next: "/c/main/queues/emails?state=retry",
		Live: Live{PollURL: "/c/main/queues/emails?state=retry"}, FormURL: "/x", FindURL: "/f"}
	for _, s := range TaskStates() {
		m.Tabs = append(m.Tabs, Tab{State: s, Count: 1, URL: "/t", On: s == StateRetry})
	}
	m.Rows = []TaskRow{{ID: "c3e81b90-04aa-4f1e-9d27-6b52e0c8a113", URL: "/task", LinkID: "tlink-1",
		Select: components.RowSelect{ID: "tsel-1", Form: TasksFormID, Name: "id", Value: "c3e81b90-04aa-4f1e-9d27-6b52e0c8a113"},
		Type:   "email:receipt", Payload: []byte("{\"a\":\n1}"), LastErr: "smtp:\n450"}}
	return m
}

func TestQueueTable(t *testing.T) {
	doc := renderC(t, QueuePoll(queueModel()))
	row := doc.Find("#tasks-rows tr")
	link := row.Find("a.row-link")
	if text(link) != "c3e81b90-04aa…" || link.AttrOr("title", "") != "c3e81b90-04aa-4f1e-9d27-6b52e0c8a113" {
		t.Errorf("short ID = %q", text(link))
	}
	if got := row.Find(".payload-cell").Text(); got != `{"a": 1}` {
		t.Errorf("payload preview = %q", got)
	}
	if got := row.Find(".err-cell").Text(); got != "smtp: 450" {
		t.Errorf("error preview = %q", got)
	}
	banner := doc.Find("#tasks-banner")
	if banner.AttrOr("data-total", "") != "41" || !strings.Contains(text(banner), "Select all 41 retry tasks") {
		t.Errorf("banner = %q", text(banner))
	}
	// All rows on one page: no banner.
	m := queueModel()
	m.Total = 1
	if renderC(t, QueuePoll(m)).Find("#tasks-banner[data-select-banner]").Length() != 0 {
		t.Error("banner for a single page")
	}
}

func TestTaskFailureToasts(t *testing.T) {
	doc := renderC(t, TaskFailureToasts([]TaskFailure{
		{Verb: TaskRun, ID: "abcdefghijklmnop", NotFound: true},
		{Verb: TaskArchive, ID: "p1", Detail: "boom"},
		{Verb: TaskDelete, Detail: "LOADING"},
		{Verb: TaskDelete, NotFound: true},
	}))
	var got []string
	doc.Find(".toast-body").Each(func(_ int, s *goquery.Selection) { got = append(got, text(s)) })
	want := []string{"Task abcdefghijkl… not found", "Could not archive p1 boom", "Could not delete all tasks LOADING", "Queue not found"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("toasts = %q", got)
	}
}

func TestNoticeText(t *testing.T) {
	tests := []struct {
		n    TaskNotice
		want string
	}{
		{TaskNotice{TaskArchive, 12}, "Archived 12 tasks"},
		{TaskNotice{TaskDelete, 1}, "Deleted 1 task"},
		{TaskNotice{TaskRun, 1200}, "Moved 1,200 tasks to pending"},
		{TaskNotice{TaskCancel, 2}, "Cancel requested for 2 tasks"},
	}
	for _, tt := range tests {
		if got := text(renderC(t, noticeText(tt.n)).Find("body")); got != tt.want {
			t.Errorf("notice = %q, want %q", got, tt.want)
		}
	}
}

func TestCodeBlockEscapes(t *testing.T) {
	doc := renderC(t, components.CodeBlock("payload", `<img src=x onerror=alert(1)>`, "Payload", "Copy payload"))
	if doc.Find("pre img").Length() != 0 || doc.Find("pre").Text() != `<img src=x onerror=alert(1)>` {
		t.Error("code block is not text")
	}
	if b := doc.Find("button#payload-copy.copy[hidden]"); b.AttrOr("data-copy-from", "") != "#payload" || b.AttrOr("aria-label", "") != "Copy payload" {
		t.Error("copy button")
	}
}

func TestActiveRunningForAndSizes(t *testing.T) {
	m := queueModel()
	m.Tab = StateActive
	m.Rows[0].Started = time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	m.Rows[0].RunningFor = 400 * time.Millisecond
	m.Sizes = SizePicker{FormURL: "/q", Size: 100, Choices: []int{20, 50, 100}}
	doc := renderC(t, QueuePoll(m))
	if got := text(doc.Find("#tasks-rows td").Last()); got != "<1s" {
		t.Errorf("running for = %q", got)
	}
	if v := doc.Find("#tasks-size option[selected]").AttrOr("value", ""); v != "100" {
		t.Errorf("size value = %q", v)
	}
}
