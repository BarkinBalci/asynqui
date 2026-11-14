package web

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
)

// fakeInfra holds the Servers, Schedulers and Info reads of fakeConns, by
// connection name. errs fail every read of a connection; block makes them
// wait until ctx ends.
type fakeInfra struct {
	servers   map[string][]conn.Server
	entries   map[string][]conn.SchedulerEntry
	events    map[string][]conn.EnqueueEvent // by entry ID, newest first
	eventErr  error
	info      map[string]fakeInfo
	errs      map[string]error
	block     map[string]bool
	eventsArg []string // "entry/queue/page/size" per SchedulerEnqueueEvents
}

func (f *fakeConns) infraRead(ctx context.Context, name string) error {
	f.mu.Lock()
	block, err := f.infra.block[name], f.infra.errs[name]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (c fakeConn) Servers(ctx context.Context) ([]conn.Server, error) {
	f, name := c.f, c.name
	if err := f.infraRead(ctx, name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.infra.servers[name], nil
}

func (c fakeConn) SchedulerEntries(ctx context.Context) ([]conn.SchedulerEntry, error) {
	f, name := c.f, c.name
	if err := f.infraRead(ctx, name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.infra.entries[name], nil
}

func (c fakeConn) SchedulerEnqueueEvents(_ context.Context, entryID, queue string, page, size int) (conn.EnqueuePage, error) {
	f := c.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infra.eventsArg = append(f.infra.eventsArg, fmt.Sprintf("%s/%s/%d/%d", entryID, queue, page, size))
	if f.infra.eventErr != nil {
		return conn.EnqueuePage{}, f.infra.eventErr
	}
	evs := f.infra.events[entryID]
	start := (page - 1) * size
	if start >= len(evs) {
		return conn.EnqueuePage{}, nil
	}
	end := min(start+size, len(evs))
	return conn.EnqueuePage{Events: evs[start:end], HasNext: end < len(evs)}, nil
}

// fakeInfo is a connection's INFO with its TLS setting and ACL user.
type fakeInfo struct {
	conn.Info
	TLS      conn.TLSInfo
	Username string
}

func (c fakeConn) Info(ctx context.Context) (*conn.Info, error) {
	if err := c.f.infraRead(ctx, c.name); err != nil {
		return nil, err
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	i := c.f.infra.info[c.name].Info
	return &i, nil
}

func (c fakeConn) TLS() conn.TLSInfo {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.f.infra.info[c.name].TLS
}

func (c fakeConn) Username() string {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.f.infra.info[c.name].Username
}

const (
	srvA    = "5e2b9f41-0c7d-4a18-93e6-b1f07c2d8a55"
	srvB    = "a1b2c3d4-0000-4000-8000-000000000002"
	srvC    = "host/x?y" // asynq server IDs are UUIDs; this one checks escaping
	entryA  = "3c1e8d27-5b90-4f6a-b2c1-9e04d7a3a07f"
	entryB  = "8a42f0c3-7d16-4e11-9a0b-1f2e3d4c5b6a"
	taskOld = "9f2c4e1a-1111-4aaa-8bbb-000000000001"
	taskNew = "b7d01e93-2222-4aaa-8bbb-000000000002"
)

// infraConns are defaultConns with servers, scheduler entries and INFO
// for main.
func infraConns() *fakeConns {
	c := defaultConns()
	now := fixedNow()
	c.infra = fakeInfra{
		servers: map[string][]conn.Server{"main": {
			{ID: srvA, Host: "server-7f9c", PID: 41, Concurrency: 20, Status: "active", Started: now.Add(-15 * time.Hour),
				Queues: []conn.QueueWeight{{Name: "critical", Weight: 6}, {Name: "default", Weight: 3}, {Name: "emails", Weight: 2}},
				Workers: []conn.Worker{
					{TaskID: taskOld, TaskType: "email:welcome", Queue: "emails", Started: now.Add(-14 * time.Minute), Deadline: now.Add(time.Minute)},
					{TaskID: taskNew, TaskType: "user:sync", Queue: "default", Started: now.Add(-12 * time.Second)},
				}},
			{ID: srvB, Host: "server-2b1e", PID: 40213, Concurrency: 2, Status: "stopped", Started: now.Add(-time.Hour),
				Queues: []conn.QueueWeight{{Name: "low", Weight: 1}}, Workers: []conn.Worker{{TaskID: "w1", Queue: "low"}}},
			{ID: srvC, Host: "server-c40a", PID: 38, Concurrency: 10, Status: "closed", Started: now.Add(-2 * time.Hour)},
		}},
		entries: map[string][]conn.SchedulerEntry{"main": {
			{ID: entryA, Spec: "0 6 * * *", TaskType: "report:daily", Payload: []byte(`{"tz":"Europe/Istanbul"}`),
				Options: []conn.EntryOption{{Name: "queue", Value: "reports"}, {Name: "max_retry", Value: "3"}}, Queue: "reports", Next: now.Add(6 * time.Hour), Prev: now.Add(-18 * time.Hour)},
			// No Queue option: the queue is unknown.
			{ID: entryB, Spec: "*/5 * * * *", TaskType: "cache:warm", Payload: []byte("\xff raw <b>" + strings.Repeat("x", 100)),
				Options: []conn.EntryOption{{Name: "timeout", Value: "2m0s"}}, Next: now.Add(3 * time.Minute)},
		}},
		events: map[string][]conn.EnqueueEvent{
			entryA: {
				{TaskID: taskNew, EnqueuedAt: now.Add(-18 * time.Hour), TaskExists: true},
				{TaskID: taskOld, EnqueuedAt: now.Add(-42 * time.Hour)},
			},
			entryB: {{TaskID: taskNew, EnqueuedAt: now.Add(-time.Minute)}},
		},
		info: map[string]fakeInfo{"main": {
			Info: conn.Info{
				Version: "7.2.4", RedisVersion: "7.2.4", Mode: "standalone", Uptime: 41*24*time.Hour + 3*time.Hour,
				ConnectedClients: 184, UsedMemory: 1299202048, UsedMemoryPeak: 1761607680, MaxMemory: 4 << 30,
				MaxMemoryPolicy: "noeviction", Keyspace: conn.Keyspace{Keys: 48213, Expires: 3902},
				Raw: "# Server\r\nredis_version:7.2.4\r\n",
			},
			TLS:      conn.TLSInfo{Enabled: true, Verified: true, Version: tls.VersionTLS13},
			Username: "asynq-ui",
		}},
	}
	return c
}

func crumbsOf(doc *goquery.Document) string {
	var crumbs []string
	doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { crumbs = append(crumbs, text(s)) })
	return strings.Join(crumbs, " / ")
}

func titleOf(doc *goquery.Document) string {
	return strings.Join(strings.Fields(doc.Find("title").Text()), " ")
}

func TestServersPage(t *testing.T) {
	h, _ := newTestServer(t, testOpts{conns: infraConns()})
	rec := do(t, h, request{path: "/c/main/servers"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	doc := parse(t, rec)
	if got := crumbsOf(doc); got != "Overview / main / Servers" {
		t.Errorf("crumbs = %q", got)
	}
	if got := titleOf(doc); got != "Servers – asynqui" {
		t.Errorf("title = %q", got)
	}
	if got := text(doc.Find(".subnav a.on")); got != "Servers" {
		t.Errorf("active sub-nav = %q", got)
	}
	var heads []string
	doc.Find("#servers thead th").Each(func(_ int, s *goquery.Selection) {
		h := text(s)
		if s.Find("a.sort").Length() == 1 {
			h += "*"
		}
		heads = append(heads, h)
	})
	if diff := cmp.Diff([]string{"Host*", "PID*", "State*", "Busy*", "Queues", "Started*"}, heads); diff != "" {
		t.Errorf("headers (-want +got, * sorts):\n%s", diff)
	}
	if doc.Find("#servers input[type=checkbox]").Length() != 0 {
		t.Error("read-only table has checkboxes")
	}
	// Default: Busy desc, by share of busy workers.
	var rows [][]string
	doc.Find("#servers-rows tr").Each(func(_ int, tr *goquery.Selection) {
		var cells []string
		tr.Find("td").Each(func(_ int, td *goquery.Selection) { cells = append(cells, text(td)) })
		rows = append(rows, cells)
	})
	want := [][]string{
		{"server-2b1e", "40213", "Stopped", "1 / 2", "low 1", "Nov 7, 23:41:52"},
		{"server-7f9c", "41", "Active", "2 / 20", "critical 6 default 3 emails 2", "Nov 7, 09:41:52"},
		{"server-c40a", "38", "closed", "0 / 10", "", "Nov 7, 22:41:52"},
	}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	link := doc.Find("#servers-rows a.row-link").First()
	if href, _ := link.Attr("href"); href != "/c/main/servers/"+srvB {
		t.Errorf("host link = %q", href)
	}
	if title, _ := link.Attr("title"); title != srvB {
		t.Errorf("host title = %q", title)
	}
	if href, _ := doc.Find("#servers-rows a.row-link").Last().Attr("href"); href != "/c/main/servers/host%2Fx%3Fy" {
		t.Errorf("escaped link = %q", href)
	}
	// The bar is an SVG with its fill as an attribute.
	bars := doc.Find("#servers-rows svg.busy")
	if bars.Length() != 3 {
		t.Fatalf("%d bars", bars.Length())
	}
	var widths []string
	bars.Each(func(_ int, s *goquery.Selection) {
		widths = append(widths, s.Find("rect.busy-fill").AttrOr("width", "none"))
	})
	if diff := cmp.Diff([]string{"50.0", "10.0", "none"}, widths); diff != "" {
		t.Errorf("bar widths (-want +got):\n%s", diff)
	}
	if v, _ := bars.First().Attr("aria-hidden"); v != "true" {
		t.Error("bar not hidden from assistive technology")
	}
	if got := doc.Find("#servers-rows time").First().AttrOr("datetime", ""); got != "2026-11-07T23:41:52Z" {
		t.Errorf("started datetime = %q", got)
	}
	if v, _ := doc.Find("#servers-filter").Attr("data-filter"); v != "#servers-rows" {
		t.Errorf("filter target = %q", v)
	}
	if doc.Find("#servers-table #servers-filter").Length() != 0 {
		t.Error("the filter is inside the polled region")
	}

	t.Run("empty", func(t *testing.T) {
		c := infraConns()
		c.infra.servers["main"] = nil
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/servers"}))
		if got := text(doc.Find("#servers-rows td.empty")); got != "No servers running" {
			t.Errorf("empty = %q", got)
		}
	})
}

func TestServersSort(t *testing.T) {
	tests := []struct {
		query string
		hosts string
		// heads are the sort links of Host, PID and Busy.
		heads string
	}{
		{"", "server-2b1e server-7f9c server-c40a",
			"/c/main/servers?dir=asc&sort=host /c/main/servers?dir=desc&sort=pid /c/main/servers?dir=asc&sort=busy"},
		{"?sort=host", "server-2b1e server-7f9c server-c40a",
			"/c/main/servers?dir=desc&sort=host /c/main/servers?dir=desc&sort=pid /c/main/servers"},
		{"?sort=pid", "server-2b1e server-7f9c server-c40a",
			"/c/main/servers?dir=asc&sort=host /c/main/servers?dir=asc&sort=pid /c/main/servers"},
		{"?sort=pid&dir=asc", "server-c40a server-7f9c server-2b1e",
			"/c/main/servers?dir=asc&sort=host /c/main/servers?dir=desc&sort=pid /c/main/servers"},
		{"?sort=started&dir=desc", "server-2b1e server-c40a server-7f9c",
			"/c/main/servers?dir=asc&sort=host /c/main/servers?dir=desc&sort=pid /c/main/servers"},
		{"?sort=state", "server-7f9c server-c40a server-2b1e",
			"/c/main/servers?dir=asc&sort=host /c/main/servers?dir=desc&sort=pid /c/main/servers"},
		{"?sort=queues", "server-2b1e server-7f9c server-c40a",
			"/c/main/servers?dir=asc&sort=host /c/main/servers?dir=desc&sort=pid /c/main/servers?dir=asc&sort=busy"},
	}
	h, _ := newTestServer(t, testOpts{conns: infraConns()})
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			doc := parse(t, do(t, h, request{path: "/c/main/servers" + tt.query}))
			var hosts []string
			doc.Find("#servers-rows a.row-link").Each(func(_ int, s *goquery.Selection) { hosts = append(hosts, text(s)) })
			if got := strings.Join(hosts, " "); got != tt.hosts {
				t.Errorf("order = %q, want %q", got, tt.hosts)
			}
			var heads []string
			for _, id := range []string{"sort-host", "sort-pid", "sort-busy"} {
				heads = append(heads, doc.Find("#"+id).AttrOr("href", ""))
			}
			if got := strings.Join(heads, " "); got != tt.heads {
				t.Errorf("sort links = %q\nwant %q", got, tt.heads)
			}
		})
	}
}

func TestServerPage(t *testing.T) {
	h, _ := newTestServer(t, testOpts{conns: infraConns(), basePath: "/asynq"})
	doc := parse(t, do(t, h, request{path: "/asynq/c/main/servers/" + srvA}))
	if got := crumbsOf(doc); got != "Overview / main / Servers / server-7f9c" {
		t.Errorf("crumbs = %q", got)
	}
	last := doc.Find(".crumbs li:last-child span")
	if !last.HasClass("crumb-mono") || last.AttrOr("title", "") != srvA {
		t.Error("last crumb not mono with the full ID")
	}
	if got := titleOf(doc); got != "server-7f9c – asynqui" {
		t.Errorf("title = %q", got)
	}
	if got := text(doc.Find(".subnav a.on")); got != "Servers" {
		t.Errorf("active sub-nav = %q", got)
	}
	if href, _ := doc.Find("#back-servers").Attr("href"); href != "/asynq/c/main/servers" {
		t.Errorf("back link = %q", href)
	}
	if got := text(doc.Find("#server-body h1")); got != "server-7f9c" {
		t.Errorf("h1 = %q", got)
	}
	if got := text(doc.Find("#server-body .detail-title .badge")); got != "Active" {
		t.Errorf("badge = %q", got)
	}
	if v, _ := doc.Find("#copy-id").Attr("data-copy"); v != srvA {
		t.Errorf("copy = %q", v)
	}
	if got := text(doc.Find("#workers-title")); got != "Active workers (2)" {
		t.Errorf("workers title = %q", got)
	}
	var rows [][]string
	doc.Find("#workers tbody tr").Each(func(_ int, tr *goquery.Selection) {
		var cells []string
		tr.Find("td").Each(func(_ int, td *goquery.Selection) { cells = append(cells, text(td)) })
		rows = append(rows, cells)
	})
	// Default: Running for desc.
	want := [][]string{
		{"9f2c4e1a-1111…", "email:welcome", "emails", "Nov 8, 00:27:52", "14m 00s"},
		{"b7d01e93-2222…", "user:sync", "default", "Nov 8, 00:41:40", "12s"},
	}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("workers (-want +got):\n%s", diff)
	}
	var heads []string
	doc.Find("#workers thead th").Each(func(_ int, s *goquery.Selection) { heads = append(heads, text(s)) })
	if diff := cmp.Diff([]string{"Task ID", "Type", "Queue", "Started", "Running for"}, heads); diff != "" {
		t.Errorf("worker columns (-want +got):\n%s", diff)
	}
	if !doc.Find("#workers tbody td").Eq(1).HasClass("c-type") {
		t.Error("type cell not at weight 500")
	}
	task := doc.Find("#workers tbody a.mono").First()
	if href, _ := task.Attr("href"); href != "/asynq/c/main/queues/emails/tasks/"+taskOld {
		t.Errorf("task link = %q", href)
	}
	if !doc.Find("#workers tbody tr").First().Find("td.c-long").Is("td") || doc.Find("#workers tbody tr").Last().Find("td.c-long").Length() != 0 {
		t.Error("running ≥10m not highlighted, or the short one is")
	}
	if v := doc.Find("#workers tbody time").First().AttrOr("data-format", ""); v != "absolute" {
		t.Errorf("started format = %q", v)
	}
	var details []string
	doc.Find(".dl dt").Each(func(_ int, dt *goquery.Selection) { details = append(details, text(dt)+"="+text(dt.Next())) })
	wantDetails := []string{"PID=41", "Started=Nov 7, 09:41:52", "Concurrency=20", "Busy=2 / 20", "Queues=critical 6 default 3 emails 2", "Strict priority=No"}
	if diff := cmp.Diff(wantDetails, details); diff != "" {
		t.Errorf("details (-want +got):\n%s", diff)
	}

	doc = parse(t, do(t, h, request{path: "/asynq/c/main/servers/" + srvA + "?sort=started"}))
	if got := text(doc.Find("#workers tbody a.mono").First()); got != "b7d01e93-2222…" {
		t.Errorf("sorted by started desc, first = %q", got)
	}
	if href, _ := doc.Find("#sort-started").Attr("href"); href != "/asynq/c/main/servers/"+srvA+"?dir=asc&sort=started" {
		t.Errorf("toggle = %q", href)
	}

	t.Run("not found", func(t *testing.T) {
		rec := do(t, h, request{path: "/asynq/c/main/servers/gone-server-id-123"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d", rec.Code)
		}
		doc := parse(t, rec)
		if got := text(doc.Find("main h1")); got != "Server not found" {
			t.Errorf("h1 = %q", got)
		}
		if got := crumbsOf(doc); got != "Overview / main / Servers / gone-server-…" {
			t.Errorf("crumbs = %q", got)
		}
		// The page has no regions to update: it polls for all of them.
		if got, _ := doc.Find("#server-poll").Attr("hx-get"); got != "/asynq/c/main/servers/gone-server-id-123?poll=init" {
			t.Errorf("poll URL = %q", got)
		}
		// A poll that finds it gone gets the whole body, with 200 so htmx
		// swaps it.
		rec = do(t, h, request{path: "/asynq/c/main/servers/gone-server-id-123", htmx: true, header: map[string]string{"HX-Trigger": "server-poll"}})
		if rec.Code != http.StatusOK || oobIDs(t, parse(t, rec)) != "server-poll live-state server-body updated-at sidebar-conns" ||
			!strings.Contains(rec.Body.String(), "Server not found") {
			t.Errorf("poll = %d %q", rec.Code, oobIDs(t, parse(t, rec)))
		}
	})
}

// TestDetailHeadingsOutsidePolls: the heading, the ID and its Copy ID
// button (and the entry's payload) are rendered once; polls swap only the
// state badge, workers, history and details, so the copy button never
// flickers. An init poll (the page had no data) gets the whole body.
func TestDetailHeadingsOutsidePolls(t *testing.T) {
	h, _ := newTestServer(t, testOpts{conns: infraConns()})
	for _, p := range []struct{ path, poll, regions string }{
		{"/c/main/servers/" + srvA, "server-poll", "server-state server-workers server-details"},
		{"/c/main/schedulers/" + entryA, "entry-poll", "entry-history entry-details"},
	} {
		doc := parse(t, do(t, h, request{path: p.path}))
		for _, id := range strings.Fields(p.regions) {
			if doc.Find("#"+id).Find("#copy-id, h1, #payload").Length() != 0 {
				t.Errorf("%s: #%s holds the heading, the copy button or the payload", p.path, id)
			}
		}
		for _, rq := range []request{
			{path: p.path, htmx: true, header: map[string]string{"HX-Trigger": p.poll}},
			{path: p.path + "?sort=started", htmx: true},
		} {
			ans := parse(t, do(t, h, rq))
			if ans.Find("#copy-id, h1, #payload").Length() != 0 {
				t.Errorf("%s: an answer re-renders the heading", rq.path)
			}
			if got := oobIDs(t, ans); got != p.poll+" live-state "+p.regions+" updated-at sidebar-conns" {
				t.Errorf("%s: oob ids = %q", rq.path, got)
			}
		}
		init := parse(t, do(t, h, request{path: p.path + "?poll=init", htmx: true, header: map[string]string{"HX-Trigger": p.poll}}))
		if init.Find("#copy-id").Length() != 1 {
			t.Errorf("%s: an init poll lacks the heading", p.path)
		}
	}
	// The badge is its own region.
	doc := parse(t, do(t, h, request{path: "/c/main/servers/" + srvA, htmx: true, header: map[string]string{"HX-Trigger": "server-poll"}}))
	if got := text(doc.Find("#server-state .badge")); got != "Active" {
		t.Errorf("state badge = %q", got)
	}
}

func TestSchedulersPage(t *testing.T) {
	h, _ := newTestServer(t, testOpts{conns: infraConns()})
	doc := parse(t, do(t, h, request{path: "/c/main/schedulers"}))
	if got := crumbsOf(doc); got != "Overview / main / Schedulers" {
		t.Errorf("crumbs = %q", got)
	}
	if got := titleOf(doc); got != "Schedulers – asynqui" {
		t.Errorf("title = %q", got)
	}
	var heads []string
	doc.Find("#schedulers thead th").Each(func(_ int, s *goquery.Selection) {
		h := text(s)
		if s.Find("a.sort").Length() == 1 {
			h += "*"
		}
		heads = append(heads, h)
	})
	if diff := cmp.Diff([]string{"Entry ID*", "Spec*", "Task type*", "Payload", "Options", "Next run*", "Last run*"}, heads); diff != "" {
		t.Errorf("headers (-want +got):\n%s", diff)
	}
	if doc.Find("#schedulers input[type=checkbox]").Length() != 0 {
		t.Error("read-only table has checkboxes")
	}
	var rows [][]string
	doc.Find("#schedulers-rows tr").Each(func(_ int, tr *goquery.Selection) {
		var cells []string
		tr.Find("td").Each(func(_ int, td *goquery.Selection) { cells = append(cells, text(td)) })
		rows = append(rows, cells)
	})
	want := [][]string{ // Next run asc
		// The payload is cut to 60 runes on the server.
		{"8a42f0c3-7d16…", "*/5 * * * *", "cache:warm", "� raw <b>" + strings.Repeat("x", 51) + "…", "timeout=2m0s", "Nov 8, 00:44:52", "—"},
		{"3c1e8d27-5b90…", "0 6 * * *", "report:daily", `{"tz":"Europe/Istanbul"}`, "queue=reports max_retry=3", "Nov 8, 06:41:52", "Nov 7, 06:41:52"},
	}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	cell := doc.Find("#schedulers-rows td.c-payload").First()
	if v, _ := cell.Attr("title"); v != "� raw <b>"+strings.Repeat("x", 100) || !cell.HasClass("mono") {
		t.Errorf("payload title = %q", v)
	}
	if doc.Find("#schedulers-rows td.c-type").Length() != 2 {
		t.Error("task type not at weight 500")
	}
	link := doc.Find("#schedulers-rows a.row-link").Last()
	if href, _ := link.Attr("href"); href != "/c/main/schedulers/"+entryA {
		t.Errorf("entry link = %q", href)
	}
	if v, _ := link.Attr("title"); v != entryA {
		t.Errorf("entry title = %q", v)
	}

	doc = parse(t, do(t, h, request{path: "/c/main/schedulers?sort=type&dir=desc"}))
	if got := text(doc.Find("#schedulers-rows td.c-type").First()); got != "report:daily" {
		t.Errorf("type desc first = %q", got)
	}
	if href, _ := doc.Find("#sort-next").Attr("href"); href != "/c/main/schedulers" {
		t.Errorf("default sort link = %q", href)
	}

	t.Run("empty", func(t *testing.T) {
		c := infraConns()
		c.infra.entries["main"] = nil
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/schedulers"}))
		if got := text(doc.Find("#schedulers-rows td.empty")); got != "No scheduler entries" {
			t.Errorf("empty = %q", got)
		}
	})
}

func TestSchedulerEntryPage(t *testing.T) {
	conns := infraConns()
	h, _ := newTestServer(t, testOpts{conns: conns})
	doc := parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA}))
	if got := crumbsOf(doc); got != "Overview / main / Schedulers / 3c1e8d27-5b90…" {
		t.Errorf("crumbs = %q", got)
	}
	if v := doc.Find(".crumbs li:last-child span").AttrOr("title", ""); v != entryA {
		t.Errorf("crumb title = %q", v)
	}
	if got := titleOf(doc); got != "3c1e8d27-5b90… – asynqui" {
		t.Errorf("title = %q", got)
	}
	if got := text(doc.Find(".subnav a.on")); got != "Schedulers" {
		t.Errorf("active sub-nav = %q", got)
	}
	if got := text(doc.Find("#entry-body h1")); got != "report:daily" {
		t.Errorf("h1 = %q", got)
	}
	if v, _ := doc.Find("#copy-id").Attr("data-copy"); v != entryA {
		t.Errorf("copy = %q", v)
	}
	if got := doc.Find("#payload").Text(); got != "{\n  \"tz\": \"Europe/Istanbul\"\n}" {
		t.Errorf("payload = %q", got)
	}
	if v, _ := doc.Find("#payload-copy").Attr("data-copy-from"); v != "#payload" {
		t.Errorf("payload copy = %q", v)
	}
	var details []string
	doc.Find(".dl dt").Each(func(_ int, dt *goquery.Selection) { details = append(details, text(dt)+"="+text(dt.Next())) })
	// Absolute, then relative to the server's clock (app.js renders the
	// same in the viewer's time zone).
	wantDetails := []string{"Spec=0 6 * * *", "Queue=reports", "Options=max_retry=3",
		"Next run=Nov 8, 06:41:52 (in 6h 00m)", "Last run=Nov 7, 06:41:52 (18h ago)"}
	if diff := cmp.Diff(wantDetails, details); diff != "" {
		t.Errorf("details (-want +got):\n%s", diff)
	}
	if v := doc.Find("#entry-details .run-rel time").First().AttrOr("datetime", ""); v != "2026-11-08T06:41:52Z" {
		t.Errorf("relative time datetime = %q", v)
	}
	if href, _ := doc.Find("#entry-queue").Attr("href"); href != "/c/main/queues/reports" {
		t.Errorf("queue link = %q", href)
	}
	var events []string
	doc.Find("#history tbody tr").Each(func(_ int, tr *goquery.Selection) {
		id := tr.Find("td").Last()
		e := text(tr.Find("td").First()) + " " + text(id)
		if href, ok := id.Find("a").Attr("href"); ok {
			e += " -> " + href
		}
		events = append(events, e)
	})
	wantEvents := []string{
		"Nov 7, 06:41:52 b7d01e93-2222… -> /c/main/queues/reports/tasks/" + taskNew,
		"Nov 6, 06:41:52 9f2c4e1a-1111…",
	}
	if diff := cmp.Diff(wantEvents, events); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
	if doc.Find(".pager").Length() != 0 {
		t.Error("pager on a single page")
	}
	if got := conns.infra.eventsArg; len(got) != 1 || got[0] != entryA+"/reports/1/20" {
		t.Errorf("events read = %q", got)
	}

	t.Run("pages", func(t *testing.T) {
		c := infraConns()
		var evs []conn.EnqueueEvent
		for i := range 45 {
			evs = append(evs, conn.EnqueueEvent{TaskID: fmt.Sprintf("t%02d", i), EnqueuedAt: fixedNow().Add(-time.Duration(i) * time.Minute)})
		}
		c.infra.events[entryA] = evs
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA + "?page=2"}))
		if got := text(doc.Find(".pager .muted")); got != "21–40" {
			t.Errorf("range = %q", got)
		}
		if href, _ := doc.Find("#hist-prev").Attr("href"); href != "/c/main/schedulers/"+entryA {
			t.Errorf("prev = %q", href)
		}
		if href, _ := doc.Find("#hist-next").Attr("href"); href != "/c/main/schedulers/"+entryA+"?page=3" {
			t.Errorf("next = %q", href)
		}
		if v, _ := doc.Find("#hist-next").Attr("hx-push-url"); v != "true" {
			t.Error("page links do not push the URL")
		}
		if got, _ := doc.Find("#entry-poll").Attr("hx-get"); got != "/c/main/schedulers/"+entryA+"?page=2" {
			t.Errorf("poll URL = %q", got)
		}
		doc = parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA + "?page=3"}))
		if got := text(doc.Find(".pager .muted")); got != "41–45" || doc.Find("#hist-next").Length() != 0 {
			t.Errorf("last page range = %q, next %d", got, doc.Find("#hist-next").Length())
		}
		for _, bad := range []string{"0", "-3", "x"} {
			doc = parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA + "?page=" + bad}))
			if got := text(doc.Find(".pager .muted")); got != "1–20" {
				t.Errorf("page=%s range = %q", bad, got)
			}
		}
	})

	t.Run("history fails", func(t *testing.T) {
		c := infraConns()
		c.infra.eventErr = errors.New("history boom")
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA}))
		if got := text(doc.Find("#entry-body .conn-error-text")); got != "history boom" {
			t.Errorf("error = %q", got)
		}
		if text(doc.Find("#entry-body h1")) != "report:daily" {
			t.Error("entry not shown")
		}
	})

	t.Run("page cap", func(t *testing.T) {
		c := infraConns()
		var evs []conn.EnqueueEvent
		for i := range 1100 { // the fake holds more than asynq keeps
			evs = append(evs, conn.EnqueueEvent{TaskID: fmt.Sprintf("t%04d", i)})
		}
		c.infra.events[entryA] = evs
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA + "?page=50"}))
		if got := text(doc.Find(".pager .muted")); got != "981–1,000" || doc.Find("#hist-next").Length() != 0 {
			t.Errorf("page 50: range %q, next %d", got, doc.Find("#hist-next").Length())
		}
		doc = parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA + "?page=51&payload=raw"}))
		if got := text(doc.Find("#history td.empty")); got != "No enqueue events" {
			t.Errorf("page 51 = %q", got)
		}
		if href, _ := doc.Find("#hist-prev").Attr("href"); href != "/c/main/schedulers/"+entryA+"?page=50&payload=raw" {
			t.Errorf("back to the last page = %q", href)
		}
		if got := c.infra.eventsArg; len(got) != 1 || got[0] != entryA+"/reports/50/20" {
			t.Errorf("events read = %q (page 51 must not be read)", got)
		}
	})

	t.Run("payload toggle", func(t *testing.T) {
		doc := parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA + "?page=2"}))
		if href, _ := doc.Find("#payload-raw").Attr("href"); href != "/c/main/schedulers/"+entryA+"?page=2&payload=raw" {
			t.Errorf("raw link = %q", href)
		}
		if !doc.Find("#payload-json").HasClass("on") || doc.Find("#payload-json").AttrOr("aria-current", "") != "true" {
			t.Error("JSON not current")
		}
		if doc.Find("#payload-raw[hx-get], #payload-json[hx-get]").Length() != 0 {
			t.Error("the toggle needs JS")
		}
		doc = parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA + "?payload=raw"}))
		if got := doc.Find("#payload").Text(); got != `{"tz":"Europe/Istanbul"}` {
			t.Errorf("raw payload = %q", got)
		}
		if !doc.Find("#payload-raw").HasClass("on") || doc.Find("#payload-json").HasClass("on") {
			t.Error("Raw not current")
		}
		if href, _ := doc.Find("#payload-json").Attr("href"); href != "/c/main/schedulers/"+entryA {
			t.Errorf("JSON link = %q", href)
		}
		if got, _ := doc.Find("#entry-poll").Attr("hx-get"); got != "/c/main/schedulers/"+entryA+"?payload=raw" {
			t.Errorf("poll URL = %q", got)
		}
	})

	t.Run("not JSON, unknown queue", func(t *testing.T) {
		c := infraConns()
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryB}))
		if got := doc.Find("#payload").Text(); got != "� raw <b>"+strings.Repeat("x", 100) {
			t.Errorf("payload = %q", got)
		}
		if doc.Find("#payload-raw, #payload-json").Length() != 0 {
			t.Error("toggle without JSON")
		}
		if got := text(doc.Find(".dl dt:contains('Queue')").Next()); got != "—" || doc.Find("#entry-queue").Length() != 0 {
			t.Errorf("unknown queue = %q", got)
		}
		if got := text(doc.Find(".dl dt:contains('Options')").Next()); got != "timeout=2m0s" {
			t.Errorf("options = %q", got)
		}
		if doc.Find("#history tbody a").Length() != 0 || text(doc.Find("#history tbody td").Last()) != "b7d01e93-2222…" {
			t.Error("a task ID is linked to a guessed queue")
		}
		if got := c.infra.eventsArg; len(got) != 1 || got[0] != entryB+"//1/20" {
			t.Errorf("events read = %q, want no queue", got)
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		c := infraConns()
		c.infra.entries["main"][0].Payload = nil
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/schedulers/" + entryA}))
		if got := text(doc.Find("#entry-body .empty")); got != "No payload" || doc.Find("#payload").Length() != 0 {
			t.Errorf("empty payload = %q", got)
		}
	})

	t.Run("stale ID", func(t *testing.T) {
		rec := do(t, h, request{path: "/c/main/schedulers/0000aaaa-5b90-4f6a-b2c1-9e04d7a3a07f"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d", rec.Code)
		}
		doc := parse(t, rec)
		if got := text(doc.Find("main h1")); got != "Entry not found" {
			t.Errorf("h1 = %q", got)
		}
		if href, _ := doc.Find("#back-schedulers").Attr("href"); href != "/c/main/schedulers" {
			t.Errorf("back = %q", href)
		}
		if got := crumbsOf(doc); got != "Overview / main / Schedulers / 0000aaaa-5b90…" {
			t.Errorf("crumbs = %q", got)
		}
	})
}

func TestInfoPage(t *testing.T) {
	cards := func(doc *goquery.Document) []string {
		var out []string
		doc.Find("#info-cards .info-card").Each(func(_ int, s *goquery.Selection) { out = append(out, text(s)) })
		return out
	}
	t.Run("redis", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{conns: infraConns()})
		doc := parse(t, do(t, h, request{path: "/c/main/info"}))
		want := []string{
			"Version Redis 7.2.4",
			"Mode standalone",
			"Uptime 41 days",
			"Connected clients 184",
			"Used memory / peak 1.2 GB / 1.6 GB maxmemory 4.0 GB",
			"maxmemory-policy noeviction",
			"Keyspace db0 48,213 keys 3,902 with expiry",
			"TLS On TLS 1.3, server cert verified",
			"ACL user asynq-ui",
		}
		if diff := cmp.Diff(want, cards(doc)); diff != "" {
			t.Errorf("cards (-want +got):\n%s", diff)
		}
		if got := text(doc.Find("main h1")); got != "Redis info" {
			t.Errorf("h1 = %q", got)
		}
		if got := text(doc.Find("#info-page .head-title .mono")); got != "redis-main.internal:6379/0" {
			t.Errorf("address = %q", got)
		}
		if got := crumbsOf(doc); got != "Overview / main / Redis info" {
			t.Errorf("crumbs = %q", got)
		}
		if got := text(doc.Find(".subnav a.on")); got != "Redis info" {
			t.Errorf("sub-nav = %q", got)
		}
		raw := doc.Find("#raw-info")
		// The HTML parser turns CRLF into LF.
		if raw.Text() != "# Server\nredis_version:7.2.4\n" || raw.AttrOr("tabindex", "") != "0" {
			t.Errorf("raw = %q", raw.Text())
		}
		if v, _ := doc.Find("#raw-info-copy").Attr("data-copy-from"); v != "#raw-info" {
			t.Errorf("copy = %q", v)
		}
		if doc.Find(".raw-card #raw-info").Length() != 1 || doc.Find(".raw-card #payload").Length() != 0 {
			t.Error("only the raw INFO block is height-capped")
		}
	})
	t.Run("NOPERM", func(t *testing.T) {
		c := infraConns()
		c.infra.errs = map[string]error{"main": errors.New("info: NOPERM User asynq-ui has no permissions to run the 'info' command")}
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/info"}))
		if got := text(doc.Find("#info-cards .conn-error-title")); got != "Could not read INFO" {
			t.Errorf("title = %q", got)
		}
		if doc.Find("#nav-main .dot-up").Length() != 1 {
			t.Error("the sidebar does not show the connection up")
		}
	})
	t.Run("TLS without a handshake yet", func(t *testing.T) {
		c := infraConns()
		i := c.infra.info["main"]
		i.TLS.Version, i.TLS.Verified = 0, false
		c.infra.info["main"] = i
		h, _ := newTestServer(t, testOpts{conns: c})
		if got := cards(parse(t, do(t, h, request{path: "/c/main/info"}))); !strings.Contains(strings.Join(got, "|"), "TLS On Server cert not verified") {
			t.Errorf("cards = %q", got)
		}
	})
	t.Run("valkey", func(t *testing.T) {
		c := infraConns()
		i := c.infra.info["main"]
		i.Backend, i.Version, i.MaxMemory, i.TLS.Enabled, i.Username, i.Keyspace.Keys = conn.BackendValkey, "8.0.2", 0, false, "", 1
		c.infra.info["main"] = i
		h, _ := newTestServer(t, testOpts{conns: c})
		doc := parse(t, do(t, h, request{path: "/c/main/info"}))
		got := cards(doc)
		for _, want := range []string{
			"Version Valkey 8.0.2 Redis-compatible 7.2.4",
			"Used memory / peak 1.2 GB / 1.6 GB No maxmemory",
			"Keyspace db0 1 key 3,902 with expiry",
			"TLS Off",
			"ACL user default",
		} {
			if !strings.Contains(strings.Join(got, "|"), want) {
				t.Errorf("cards %q lack %q", got, want)
			}
		}
		// The read's backend labels the page, the crumb, the title and the
		// sidebar, even before the registry knew it.
		if text(doc.Find("main h1")) != "Valkey info" || crumbsOf(doc) != "Overview / main / Valkey info" ||
			titleOf(doc) != "Valkey info – asynqui" || text(doc.Find("#subnav-info")) != "Valkey info" {
			t.Errorf("labels: h1 %q crumbs %q title %q sub-nav %q", text(doc.Find("main h1")), crumbsOf(doc), titleOf(doc), text(doc.Find("#subnav-info")))
		}
	})
	t.Run("valkey label everywhere", func(t *testing.T) {
		c := infraConns()
		c.conns[0].Valkey = true
		h, _ := newTestServer(t, testOpts{conns: c})
		for _, p := range []string{"/c/main", "/c/main/servers", "/c/main/schedulers/" + entryA} {
			doc := parse(t, do(t, h, request{path: p}))
			if got := text(doc.Find("#subnav-info")); got != "Valkey info" {
				t.Errorf("%s: sub-nav = %q", p, got)
			}
		}
	})
}

// TestInfraUnavailable: a connection known down, a failing read and a slow
// one, on every page.
func TestInfraUnavailable(t *testing.T) {
	paths := []struct{ path, region string }{
		{"/c/%s/servers", "#servers-table"},
		{"/c/%s/servers/" + srvA, "#server-body"},
		{"/c/%s/schedulers", "#schedulers-table"},
		{"/c/%s/schedulers/" + entryA, "#entry-body"},
		{"/c/%s/info", "#info-cards"},
	}
	for _, p := range paths {
		t.Run(p.path, func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{conns: infraConns()})
			rec := do(t, h, request{path: fmt.Sprintf(p.path, "analytics")})
			doc := parse(t, rec)
			if rec.Code != http.StatusOK || text(doc.Find(p.region+" .conn-error-text")) != "dial tcp 10.0.4.12:6380: i/o timeout" {
				t.Errorf("down: %d %q", rec.Code, text(doc.Find(p.region)))
			}
			if doc.Find("main h1").Length() != 1 {
				t.Error("no heading")
			}
			if got, _ := doc.Find("[data-poll]").Attr("hx-get"); !strings.Contains(got, "poll=init") {
				t.Errorf("poll URL without data = %q", got)
			}

			if got := text(doc.Find(p.region + " .conn-error-title")); got != "Connection failed" {
				t.Errorf("down title = %q", got)
			}

			// A network error or timeout: down here and in the sidebar.
			c := infraConns()
			c.infra.errs = map[string]error{"main": unreachable("connection refused")}
			h, _ = newTestServer(t, testOpts{conns: c})
			doc = parse(t, do(t, h, request{path: fmt.Sprintf(p.path, "main")}))
			if got := text(doc.Find(p.region + " .conn-error-title")); got != "Connection failed" {
				t.Errorf("unreachable title = %q", got)
			}
			if doc.Find("#nav-main .dot-down").Length() != 1 {
				t.Error("sidebar not down")
			}

			// An error reply: the server is up, the read failed.
			c = infraConns()
			c.infra.errs = map[string]error{"main": errors.New("NOPERM User asynq-ui has no permissions to run the 'info' command")}
			h, _ = newTestServer(t, testOpts{conns: c})
			doc = parse(t, do(t, h, request{path: fmt.Sprintf(p.path, "main")}))
			if got := text(doc.Find(p.region + " .conn-error-text")); !strings.HasPrefix(got, "NOPERM") {
				t.Errorf("failing read: %q", got)
			}
			if got := text(doc.Find(p.region + " .conn-error-title")); got == "Connection failed" || got == "" {
				t.Errorf("error reply title = %q", got)
			}
			if doc.Find("#nav-main .dot-down").Length() != 0 {
				t.Error("an error reply shows the connection down")
			}

			c = infraConns()
			c.infra.block = map[string]bool{"main": true}
			d, h, _ := newTestDeps(t, testOpts{conns: c})
			d.pageBudget = 20 * time.Millisecond
			doc = parse(t, do(t, h, request{path: fmt.Sprintf(p.path, "main")}))
			if got := text(doc.Find(p.region + " .checking")); got != "Checking" {
				t.Errorf("slow read: %q", got)
			}
		})
	}
}

// TestInfraPollAnswers: what each answer swaps, by id.
func TestInfraPollAnswers(t *testing.T) {
	poll := func(path, id string) request {
		return request{path: path, htmx: true, header: map[string]string{"HX-Trigger": id}}
	}
	failing := func() *fakeConns {
		c := infraConns()
		c.infra.errs = map[string]error{"main": errors.New("LOADING")}
		return c
	}
	tests := []struct {
		name  string
		conns func() *fakeConns
		rq    request
		ids   string
	}{
		{"servers sort", infraConns, request{path: "/c/main/servers?sort=host", htmx: true},
			"servers-poll live-state servers-table updated-at sidebar-conns"},
		{"servers poll", infraConns, poll("/c/main/servers", "servers-poll"),
			"servers-poll live-state servers-table updated-at sidebar-conns"},
		{"servers poll without data keeps the table", failing, poll("/c/main/servers", "servers-poll"),
			"servers-poll live-state updated-at sidebar-conns"},
		{"servers init poll", failing, poll("/c/main/servers?poll=init", "servers-poll"),
			"servers-poll live-state servers-table updated-at sidebar-conns"},
		{"server poll", infraConns, poll("/c/main/servers/"+srvA, "server-poll"),
			"server-poll live-state server-state server-workers server-details updated-at sidebar-conns"},
		{"server init poll", infraConns, poll("/c/main/servers/"+srvA+"?poll=init", "server-poll"),
			"server-poll live-state server-body updated-at sidebar-conns"},
		{"schedulers poll", infraConns, poll("/c/main/schedulers", "schedulers-poll"),
			"schedulers-poll live-state schedulers-table updated-at sidebar-conns"},
		{"entry page link", infraConns, request{path: "/c/main/schedulers/" + entryA + "?page=2", htmx: true},
			"entry-poll live-state entry-history entry-details updated-at sidebar-conns"},
		{"entry poll without data", failing, poll("/c/main/schedulers/"+entryA, "entry-poll"),
			"entry-poll live-state updated-at sidebar-conns"},
		{"info poll: cards only", infraConns, poll("/c/main/info", "info-poll"),
			"info-poll live-state info-cards updated-at sidebar-conns"},
		{"info init poll: raw too", infraConns, poll("/c/main/info?poll=init", "info-poll"),
			"info-poll live-state info-cards info-raw updated-at sidebar-conns"},
		{"info poll without data", failing, poll("/c/main/info", "info-poll"),
			"info-poll live-state updated-at sidebar-conns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{conns: tt.conns()})
			rec := do(t, h, tt.rq)
			if strings.Contains(rec.Body.String(), "<html") {
				t.Fatal("a full page")
			}
			doc := parse(t, rec)
			if got := oobIDs(t, doc); got != tt.ids {
				t.Errorf("oob ids = %q, want %q", got, tt.ids)
			}
		})
	}

	// A poll that keeps the data shows why in the banner.
	h, _ := newTestServer(t, testOpts{conns: failing()})
	doc := parse(t, do(t, h, poll("/c/main/schedulers?sort=type", "schedulers-poll")))
	if got := text(doc.Find("#live-state .conn-error-text")); got != "LOADING" {
		t.Errorf("banner = %q", got)
	}
	if got, _ := doc.Find("#schedulers-poll").Attr("hx-get"); got != "/c/main/schedulers?dir=asc&sort=type" {
		t.Errorf("poll URL = %q", got)
	}
}

// TestInfraSync: one hx-sync scope per page; the poll aborts on user
// requests, and every other request queues.
func TestInfraSync(t *testing.T) {
	h, _ := newTestServer(t, testOpts{conns: infraConns()})
	for _, p := range []struct{ path, page, poll string }{
		{"/c/main/servers", "servers-page", "servers-poll"},
		{"/c/main/servers/" + srvA, "server-page", "server-poll"},
		{"/c/main/schedulers", "schedulers-page", "schedulers-poll"},
		{"/c/main/schedulers/" + entryA + "?page=1", "entry-page", "entry-poll"},
		{"/c/main/info", "info-page", "info-poll"},
	} {
		doc := parse(t, do(t, h, request{path: p.path}))
		page := doc.Find("main #" + p.page)
		if v, _ := page.Attr("hx-sync"); v != "this:queue last" {
			t.Errorf("%s: page hx-sync = %q", p.path, v)
		}
		if v, _ := page.Find("#" + p.poll).Attr("hx-sync"); v != "#"+p.page+":abort" {
			t.Errorf("%s: poll hx-sync = %q", p.path, v)
		}
		if n := doc.Find("[hx-post]").Length(); n != 0 {
			t.Errorf("%s: %d posting elements on a read-only page", p.path, n)
		}
		doc.Find("[hx-get]").Each(func(_ int, s *goquery.Selection) {
			if s.Closest("#"+p.page).Length() == 0 {
				t.Errorf("%s: #%s outside the page scope", p.path, s.AttrOr("id", ""))
			}
			if s.Is("a") && s.AttrOr("hx-swap", "") != "none" {
				t.Errorf("%s: #%s swaps in place", p.path, s.AttrOr("id", ""))
			}
		})
	}
}

// TestInfraFocusablesHaveIDs: htmx refocuses swapped elements by id.
func TestInfraFocusablesHaveIDs(t *testing.T) {
	c := infraConns()
	var evs []conn.EnqueueEvent
	for i := range 30 {
		evs = append(evs, conn.EnqueueEvent{TaskID: fmt.Sprintf("t%02d", i), TaskExists: i%2 == 0})
	}
	c.infra.events[entryA] = evs
	h, _ := newTestServer(t, testOpts{conns: c})
	for _, p := range []string{"/c/main/servers", "/c/main/servers/" + srvA, "/c/main/schedulers", "/c/main/schedulers/" + entryA, "/c/main/info"} {
		doc := parse(t, do(t, h, request{path: p}))
		seen := map[string]bool{}
		doc.Find("[id]").Each(func(_ int, s *goquery.Selection) {
			id, _ := s.Attr("id")
			if seen[id] {
				t.Errorf("%s: duplicate id %q", p, id)
			}
			seen[id] = true
		})
		n := 0
		poll := parse(t, do(t, h, request{path: p, htmx: true}))
		poll.Find("[hx-swap-oob]").Each(func(_ int, region *goquery.Selection) {
			id, _ := region.Attr("id")
			doc.Find(`[id="` + id + `"]`).Find("a[href], button, input:not([type=hidden]), select, textarea, summary, [tabindex]").Each(func(_ int, s *goquery.Selection) {
				n++
				if v, ok := s.Attr("id"); !ok || v == "" {
					h, _ := goquery.OuterHtml(s)
					t.Errorf("%s: focusable in #%s without id: %s", p, id, h)
				}
			})
		})
		if n < 3 {
			t.Errorf("%s: only %d focusables found", p, n)
		}
	}
}

// TestInfraAccess: viewer is enough; hidden and unknown connections are
// 404; the pages are GET only, and a cross-origin post is refused.
func TestInfraAccess(t *testing.T) {
	authz := config.Authz{
		Default:  config.RoleNone,
		Bindings: []config.Binding{{Users: []string{"bob"}, Role: config.RoleViewer, Connections: []string{"main"}}},
	}
	h, _ := newTestServer(t, testOpts{provider: config.ProviderProxy, authz: authz, conns: infraConns()})
	for _, p := range []string{"/c/%s/servers", "/c/%s/servers/" + srvA, "/c/%s/schedulers", "/c/%s/schedulers/" + entryA, "/c/%s/info"} {
		if rec := do(t, h, request{path: fmt.Sprintf(p, "main"), user: "bob"}); rec.Code != http.StatusOK {
			t.Errorf("viewer %s = %d", p, rec.Code)
		}
		for _, name := range []string{"local", "nope"} {
			rec := do(t, h, request{path: fmt.Sprintf(p, name), user: "bob"})
			if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "server-7f9c") {
				t.Errorf("%s on %s = %d", p, name, rec.Code)
			}
		}
		if rec := do(t, h, request{path: fmt.Sprintf(p, "main")}); rec.Code != http.StatusUnauthorized {
			t.Errorf("no identity %s = %d", p, rec.Code)
		}
		rec := do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(p, "main"), user: "bob", header: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if rec.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST %s = %d", p, rec.Code)
		}
		rec = do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(p, "main"), user: "bob"})
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d", p, rec.Code)
		}
	}
}
