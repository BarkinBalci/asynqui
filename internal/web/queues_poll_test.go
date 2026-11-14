package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// testFlashCookie carries one failed pause, as an action without JS
// leaves it.
func testFlashCookie() *http.Cookie {
	v := base64.RawURLEncoding.EncodeToString([]byte(`[{"v":0,"k":0,"q":"default","d":"boom"}]`))
	return &http.Cookie{Name: flashCookie, Value: v}
}

// pollRequest is a poll from the page's poll element: htmx names it in
// HX-Trigger.
func pollRequest(path string) request {
	return request{path: path, htmx: true, header: map[string]string{"HX-Trigger": "queues-poll"}}
}

// oobIDs lists the ids of the top-level out-of-band elements.
func oobIDs(t *testing.T, doc *goquery.Document) string {
	t.Helper()
	var ids []string
	doc.Find("body").Children().Each(func(_ int, s *goquery.Selection) {
		if oob, _ := s.Attr("hx-swap-oob"); oob != "true" {
			t.Errorf("top-level <%s> is not out of band", goquery.NodeName(s))
		}
		ids = append(ids, s.AttrOr("id", ""))
	})
	return strings.Join(ids, " ")
}

// TestQueuesPollAnswers: a poll leaves the chart's controls alone; when it
// finds no data while the page shows a table, it keeps the table (and the
// selection in it) and shows a banner; a page without data regions polls
// with poll=init and gets every region.
func TestQueuesPollAnswers(t *testing.T) {
	failing := func() *fakeConns {
		c := defaultConns()
		c.queueErrs = map[string]error{"main": errors.New("LOADING Redis is loading the dataset in memory")}
		return c
	}
	tests := []struct {
		name     string
		conns    func() *fakeConns
		rq       request
		ids      string
		banner   string
		pollURL  string
		hasTable bool
	}{
		{"user request: every region", defaultConns, request{path: "/c/main?range=7d", htmx: true},
			"queues-poll conn-health conn-state queues-table throughput updated-at sidebar-conns", "", "/c/main?range=7d", true},
		{"poll: chart body only", defaultConns, pollRequest("/c/main?range=7d"),
			"queues-poll conn-health conn-state queues-table tp-chart updated-at sidebar-conns", "", "/c/main?range=7d", true},
		{"poll finds no data: the table stays", failing, pollRequest("/c/main"),
			"queues-poll conn-health conn-state updated-at sidebar-conns", "LOADING Redis is loading the dataset in memory", "/c/main", false},
		{"init poll without data", failing, pollRequest("/c/main?poll=init"),
			"queues-poll conn-health conn-state queues-table throughput updated-at sidebar-conns", "", "/c/main?poll=init", false},
		{"init poll with data: back to normal", defaultConns, pollRequest("/c/main?poll=init"),
			"queues-poll conn-health conn-state queues-table throughput updated-at sidebar-conns", "", "/c/main", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{conns: tt.conns()})
			doc := parse(t, do(t, h, tt.rq))
			if got := oobIDs(t, doc); got != tt.ids {
				t.Errorf("oob ids = %q, want %q", got, tt.ids)
			}
			if got := text(doc.Find("#conn-state .conn-error-text")); got != tt.banner {
				t.Errorf("banner = %q, want %q", got, tt.banner)
			}
			if got, _ := doc.Find("#queues-poll").Attr("hx-get"); got != tt.pollURL {
				t.Errorf("poll URL = %q, want %q", got, tt.pollURL)
			}
			if (doc.Find("#queues").Length() == 1) != tt.hasTable {
				t.Errorf("table present = %v", !tt.hasTable)
			}
			if doc.Find("#tp-chart").Length() == 1 && strings.Contains(tt.ids, "tp-chart") && doc.Find("#tp-range, #tp-queue").Length() != 0 {
				t.Error("a poll replaces the chart controls")
			}
		})
	}

	// A full page without data polls with poll=init.
	h, _ := newTestServer(t, testOpts{})
	doc := parse(t, do(t, h, request{path: "/c/analytics?sort=name"}))
	if got, _ := doc.Find("#queues-poll").Attr("hx-get"); got != "/c/analytics?dir=asc&poll=init&sort=name" {
		t.Errorf("down page poll URL = %q", got)
	}
	if v, _ := doc.Find(`#queues-table input[name=next]`).Attr("value"); v != "/c/analytics?dir=asc&sort=name" {
		t.Errorf("next = %q", v)
	}
}

func TestQueuesHistoryStates(t *testing.T) {
	t.Run("budget miss: Checking in the chart body", func(t *testing.T) {
		conns := defaultConns()
		conns.historyBlock = map[string]bool{"main": true}
		d, h, _ := newTestDeps(t, testOpts{conns: conns})
		d.pageBudget = 30 * time.Millisecond
		start := time.Now()
		doc := parse(t, do(t, h, request{path: "/c/main"}))
		if took := time.Since(start); took > time.Second {
			t.Errorf("page took %v", took)
		}
		if got := text(doc.Find("#tp-chart .checking")); got != "Checking" {
			t.Errorf("chart body = %q", text(doc.Find("#tp-chart")))
		}
		if len(rowNames(doc)) != 4 || doc.Find("#tp-range").Length() != 1 {
			t.Error("table or controls missing")
		}
	})
	t.Run("other errors have their own copy", func(t *testing.T) {
		conns := defaultConns()
		conns.historyErrs = map[string]error{"main": errors.New("history boom")}
		h, _ := newTestServer(t, testOpts{conns: conns})
		doc := parse(t, do(t, h, request{path: "/c/main"}))
		if got := text(doc.Find("#tp-chart .conn-error-title")); got != "Could not load throughput" {
			t.Errorf("title = %q", got)
		}
		if got := text(doc.Find("#tp-chart .conn-error-text")); got != "history boom" {
			t.Errorf("error = %q", got)
		}
	})
	t.Run("a queue deleted meanwhile falls back to all", func(t *testing.T) {
		conns := defaultConns()
		conns.historyErrs = nil
		h, conns := newTestServer(t, testOpts{conns: conns})
		conns.history["main"] = testHistory(14)
		doc := parse(t, do(t, h, request{path: "/c/main?queue=gone"}))
		if diff := cmp.Diff([]string{"gone/14", "/14"}, conns.historyAsked); diff != "" {
			t.Errorf("asked (-want +got):\n%s", diff)
		}
		if v := doc.Find("#tp-queue option[selected]").AttrOr("value", "?"); v != "" {
			t.Errorf("picked = %q", v)
		}
	})
}

func TestQueuesA11y(t *testing.T) {
	h, _ := newTestServer(t, testOpts{authz: config.Authz{Default: config.RoleViewer}})
	doc := parse(t, do(t, h, request{path: "/c/main"}))
	for id, want := range map[string]string{
		"act-pause":  "Pause (requires operator)",
		"act-resume": "Resume (requires operator)",
		"act-delete": "Delete (requires admin)",
	} {
		if got, _ := doc.Find("#" + id).Attr("aria-label"); got != want {
			t.Errorf("%s aria-label = %q, want %q", id, got, want)
		}
	}
	if doc.Find(`.tbar [role=status] [data-sel-label]`).Length() != 1 {
		t.Error("N selected is not in a live region")
	}
	if cancel := doc.Find("dialog#queues-delete .dlgacts button[autofocus]"); text(cancel) != "Cancel" {
		t.Errorf("initial focus = %q", text(cancel))
	}
	if v := doc.Find("#throughput .select").AttrOr("id", ""); v != "tp-queue" {
		t.Errorf("picker = %q", v)
	}
	q := url.Values{}
	doc.Find("#tp-form input[type=hidden]").Each(func(_ int, s *goquery.Selection) { q.Add(s.AttrOr("name", ""), "x") })
	if len(q) != 0 {
		t.Errorf("default picker carries %v", q)
	}
}
