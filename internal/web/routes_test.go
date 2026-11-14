package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
)

// The checks every page or action shares, run over the route table:
// access by role, CSRF, focusable ids in polled regions, the answers'
// shape and the hx-sync scopes, and path escaping. Each page's own
// answers and controls are tested next to its handler.

// page is a page of the route table. path has %s for the connection.
type page struct {
	path string
	// pageID and pollID are the page's hx-sync scope and poll element;
	// empty on the Overview, whose grid polls.
	pageID, pollID string
	// posts is how many elements of the page post with htmx.
	posts int
	// focusables is the least number of focusable elements the answers'
	// regions hold, so a wrong selector cannot pass.
	focusables int
}

func routePages() []page {
	return []page{
		{"/", "", "", 0, 6},
		{"/c/%s", "queues-page", "queues-poll", 3, 30},
		{"/c/%s/queues/default", "queue-page", "queue-poll", 5, 20},
		{"/c/%s/queues/default?state=retry", "queue-page", "queue-poll", 7, 10},
		{"/c/%s/queues/default/tasks/" + uuidTask, "task-page", "task-poll", 3, 4},
		{"/c/%s/queues/default/tasks/a0", "task-page", "task-poll", 1, 4},
		{"/c/%s/queues/default/tasks/c0", "task-page", "task-poll", 1, 4},
		{"/c/%s/queues/default/tasks/x0", "task-page", "task-poll", 2, 4},
		{"/c/%s/servers", "servers-page", "servers-poll", 0, 3},
		{"/c/%s/servers/" + srvA, "server-page", "server-poll", 0, 3},
		{"/c/%s/schedulers", "schedulers-page", "schedulers-poll", 0, 3},
		{"/c/%s/schedulers/" + entryA, "entry-page", "entry-poll", 0, 3},
		{"/c/%s/info", "info-page", "info-poll", 0, 3},
	}
}

// action is a post of the route table: what it does when allowed (acted,
// as actedOn lists it) and the role it needs.
type action struct {
	path, form string
	need       config.Role
	acted      string
}

func routeActions() []action {
	return []action{
		{"/c/%s/recheck", "", config.RoleViewer, "recheck main"},
		{"/c/%s/queues/pause", "queue=default", config.RoleOperator, "pause main/default"},
		{"/c/%s/queues/resume", "queue=image-resize", config.RoleOperator, "resume main/image-resize"},
		{"/c/%s/queues/delete", "queue=image-resize&confirm=1", config.RoleAdmin, "delete main/image-resize"},
		{"/c/%s/queues/default/tasks/run", "id=s0&confirm=1", config.RoleOperator, "run default/s0"},
		{"/c/%s/queues/default/tasks/archive", "id=s0&confirm=1", config.RoleOperator, "archive default/s0"},
		{"/c/%s/queues/default/tasks/archive", "all=1&state=aggregating&group=g1&confirm=1", config.RoleOperator, "archive-all default/group:g1"},
		{"/c/%s/queues/default/tasks/cancel", "id=a0", config.RoleOperator, "cancel default/a0"},
		{"/c/%s/queues/default/tasks/delete", "id=p00&confirm=1", config.RoleAdmin, "delete default/p00"},
		{"/c/%s/queues/default/tasks/delete", "all=1&state=aggregating&group=g1&confirm=1", config.RoleAdmin, "delete-all default/group:g1"},
	}
}

// routeConns are the route table's data: infraConns plus a down
// connection named with a dot and enqueue history worth a pager.
func routeConns() *fakeConns {
	c := infraConns()
	c.conns = append(c.conns, connInfo{Name: "a.b", Addr: "x:1/0", Health: conn.Health{State: conn.StateDown}})
	var evs []conn.EnqueueEvent
	for i := range 30 {
		evs = append(evs, conn.EnqueueEvent{TaskID: fmt.Sprintf("t%02d", i), TaskExists: i%2 == 0})
	}
	c.infra.events[entryA] = evs
	return c
}

// roleOpts: vic is a viewer, olga an operator and ada an admin of main;
// local is hidden from all of them.
func roleOpts() testOpts {
	return testOpts{provider: config.ProviderProxy, conns: routeConns(), authz: config.Authz{
		Default: config.RoleNone,
		Bindings: []config.Binding{
			{Users: []string{"vic"}, Role: config.RoleViewer, Connections: []string{"main"}},
			{Users: []string{"olga"}, Role: config.RoleOperator, Connections: []string{"main"}},
			{Users: []string{"ada"}, Role: config.RoleAdmin, Connections: []string{"main"}},
		},
	}}
}

func roleUsers() map[string]config.Role {
	return map[string]config.Role{"vic": config.RoleViewer, "olga": config.RoleOperator, "ada": config.RoleAdmin}
}

func roleRank(r config.Role) int {
	return slices.Index([]config.Role{config.RoleNone, config.RoleViewer, config.RoleOperator, config.RoleAdmin}, r)
}

// actedOn lists what the fake did: rechecks, queue actions, task actions.
func actedOn(c *fakeConns) string {
	c.mu.Lock()
	var out []string
	for _, name := range c.rechecked {
		out = append(out, "recheck "+name)
	}
	c.mu.Unlock()
	return strings.Join(slices.Concat(out, c.actions(), c.taskActions()), ", ")
}

// TestRoutesAccess: viewer is enough for a page and the route's role for
// an action; a hidden or unknown connection is 404 and leaks nothing; no
// identity is 401; a cross-origin post is refused and acts on nothing.
func TestRoutesAccess(t *testing.T) {
	for _, p := range routePages() {
		if !strings.Contains(p.path, "%s") {
			continue
		}
		t.Run(p.path, func(t *testing.T) {
			h, c := newTestServer(t, roleOpts())
			if rec := do(t, h, request{path: fmt.Sprintf(p.path, "main"), user: "vic"}); rec.Code != http.StatusOK {
				t.Errorf("viewer = %d", rec.Code)
			}
			for _, name := range []string{"local", "nope"} {
				rec := do(t, h, request{path: fmt.Sprintf(p.path, name), user: "vic"})
				if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "localhost:6379/1") {
					t.Errorf("%s = %d", name, rec.Code)
				}
			}
			if rec := do(t, h, request{path: fmt.Sprintf(p.path, "main")}); rec.Code != http.StatusUnauthorized {
				t.Errorf("no identity = %d", rec.Code)
			}
			rec := do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(p.path, "main"), user: "ada"})
			if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
				t.Errorf("POST = %d", rec.Code)
			}
			rec = do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(p.path, "main"), user: "ada", header: map[string]string{"Sec-Fetch-Site": "cross-site"}})
			if rec.Code != http.StatusForbidden {
				t.Errorf("cross-origin POST = %d", rec.Code)
			}
			if got := actedOn(c); got != "" {
				t.Errorf("a page acted: %s", got)
			}
		})
	}
	for _, a := range routeActions() {
		t.Run(a.path+"?"+a.form, func(t *testing.T) {
			for user, role := range roleUsers() {
				h, c := newTestServer(t, roleOpts())
				rec := do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(a.path, "main"), user: user, form: a.form})
				allowed := roleRank(role) >= roleRank(a.need)
				switch {
				case allowed && (rec.Code != http.StatusSeeOther || actedOn(c) != a.acted):
					t.Errorf("%s = %d, acted %q; want 303 and %q", user, rec.Code, actedOn(c), a.acted)
				case !allowed && (rec.Code != http.StatusForbidden || actedOn(c) != "" || rec.Header().Get("Cache-Control") != "no-store"):
					t.Errorf("%s = %d, acted %q; want an uncached 403 and nothing done", user, rec.Code, actedOn(c))
				}
			}
			h, c := newTestServer(t, roleOpts())
			if rec := do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(a.path, "local"), user: "ada", form: a.form}); rec.Code != http.StatusNotFound {
				t.Errorf("hidden = %d, want 404", rec.Code)
			}
			if rec := do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(a.path, "main"), form: a.form}); rec.Code != http.StatusUnauthorized {
				t.Errorf("no identity = %d", rec.Code)
			}
			for _, hdr := range []map[string]string{{"Sec-Fetch-Site": "cross-site"}, {"Origin": "https://evil.example"}} {
				rec := do(t, h, request{method: http.MethodPost, path: fmt.Sprintf(a.path, "main"), user: "ada", form: a.form, header: hdr})
				if rec.Code != http.StatusForbidden {
					t.Errorf("cross-origin %v = %d", hdr, rec.Code)
				}
			}
			if got := actedOn(c); got != "" {
				t.Errorf("refused posts acted: %s", got)
			}
		})
	}
}

// TestRoutesAnswers: htmx refocuses swapped elements by id, so every
// focusable element in a region an answer (a poll, a user request) swaps
// has one, and ids are unique; answers are fragments of out-of-band swaps
// only.
func TestRoutesAnswers(t *testing.T) {
	opts := testOpts{conns: routeConns(), authz: config.Authz{Default: config.RoleAdmin}}
	opts.conns.queues["main"] = append(opts.conns.queues["main"], conn.Queue{Name: "a/b c"})
	h, _ := newTestServer(t, opts)
	focusable := "a[href], button, input:not([type=hidden]), select, textarea, summary, [tabindex]"
	for _, p := range routePages() {
		t.Run(p.path, func(t *testing.T) {
			path := strings.ReplaceAll(p.path, "%s", "main")
			doc := parse(t, do(t, h, request{path: path}))
			seen := map[string]bool{}
			doc.Find("[id]").Each(func(_ int, s *goquery.Selection) {
				id := s.AttrOr("id", "")
				if seen[id] {
					t.Errorf("duplicate id %q", id)
				}
				seen[id] = true
			})
			answers := []request{{path: path, htmx: true}}
			if p.pollID != "" {
				answers = append(answers, request{path: path, htmx: true, header: map[string]string{"HX-Trigger": p.pollID}})
			}
			n := 0
			for _, rq := range answers {
				rec := do(t, h, rq)
				if strings.Contains(rec.Body.String(), "<html") {
					t.Fatalf("%v: a full page", rq.header)
				}
				answer := parse(t, rec)
				oobIDs(t, answer)
				answer.Find("[hx-swap-oob]").Each(func(_ int, region *goquery.Selection) {
					id := region.AttrOr("id", "")
					// The page's copy of the region, and the answer's.
					for _, r := range []*goquery.Selection{doc.Find(`[id="` + id + `"]`), region} {
						r.Find(focusable).Each(func(_ int, s *goquery.Selection) {
							n++
							if s.AttrOr("id", "") == "" {
								h, _ := goquery.OuterHtml(s)
								t.Errorf("focusable in #%s without id: %s", id, h)
							}
						})
					}
				})
			}
			if n < p.focusables {
				t.Errorf("only %d focusables found", n)
			}
		})
	}
}

// TestRoutesSync: one hx-sync scope per page. The poll aborts on user
// requests and is dropped while one is in flight; posts queue in order;
// every other request queues, the last one winning, and swaps nothing in
// place.
func TestRoutesSync(t *testing.T) {
	h, _ := newTestServer(t, testOpts{conns: routeConns(), authz: config.Authz{Default: config.RoleAdmin}})
	for _, p := range routePages() {
		if p.pageID == "" {
			continue
		}
		t.Run(p.path, func(t *testing.T) {
			doc := parse(t, do(t, h, request{path: strings.ReplaceAll(p.path, "%s", "main")}))
			scope := doc.Find("main #" + p.pageID)
			if v := scope.AttrOr("hx-sync", ""); v != "this:queue last" {
				t.Fatalf("page hx-sync = %q", v)
			}
			if v := scope.Find("#"+p.pollID).AttrOr("hx-sync", ""); v != "#"+p.pageID+":abort" {
				t.Errorf("poll hx-sync = %q", v)
			}
			doc.Find("[hx-get], [hx-post]").Each(func(_ int, s *goquery.Selection) {
				if s.Closest("#"+p.pageID).Length() == 0 {
					t.Errorf("#%s outside the page scope", s.AttrOr("id", ""))
				}
				var sync string
				for n := s; n.Length() > 0 && sync == ""; n = n.Parent() {
					sync = n.AttrOr("hx-sync", "")
				}
				want := "this:queue last"
				switch {
				case s.Is("#" + p.pollID):
					want = "#" + p.pageID + ":abort"
				case s.Is("[hx-post]"):
					want = "#" + p.pageID + ":queue all"
				}
				if sync != want {
					t.Errorf("%s#%s syncs %q, want %q", goquery.NodeName(s), s.AttrOr("id", ""), sync, want)
				}
				if s.Is("a") && s.AttrOr("hx-swap", "") != "none" {
					t.Errorf("#%s swaps in place", s.AttrOr("id", ""))
				}
			})
			if n := doc.Find("[hx-post]").Length(); n != p.posts {
				t.Errorf("%d posting elements, want %d", n, p.posts)
			}
		})
	}
}

// TestRoutesPathEscaping: under a base path, with names that need
// escaping, every link, form target and htmx URL of a page stays under
// the base path and escapes each path segment (url.PathEscape); want are
// links each page must have.
func TestRoutesPathEscaping(t *testing.T) {
	opts := testOpts{conns: routeConns(), basePath: "/asynq", authz: config.Authz{Default: config.RoleAdmin}}
	opts.conns.queues["main"] = append(opts.conns.queues["main"], conn.Queue{Name: "a/b c"}, conn.Queue{Name: "x?y#z"})
	opts.conns.tasks.tasks = append(opts.conns.tasks.tasks, conn.Task{ID: "x/y?z", Queue: "a/b c", Type: "t", State: conn.TaskPending})
	h, _ := newTestServer(t, opts)
	const q = "/asynq/c/main/queues/a%2Fb%20c"
	pages := map[string][]string{
		"/asynq/c/main?sort=name":       {q, "/asynq/c/main/queues/x%3Fy%23z"},
		q:                               {q + "/tasks/x%2Fy%3Fz", q + "/find", q + "/tasks/archive", q + "?state=retry"},
		q + "/tasks/x%2Fy%3Fz":          {q, q + "/tasks/run"},
		"/asynq/c/main/servers":         {"/asynq/c/main/servers/host%2Fx%3Fy"},
		"/asynq/c/main/schedulers":      {"/asynq/c/main/schedulers/" + entryA},
		"/asynq/c/main/servers/" + srvA: {"/asynq/c/main/queues/emails"},
	}
	for path, want := range pages {
		t.Run(path, func(t *testing.T) {
			rec := do(t, h, request{path: path})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			var links []string
			parse(t, rec).Find("[href], [action], [formaction], [hx-get], [hx-post], [src]").Each(func(_ int, s *goquery.Selection) {
				for _, attr := range []string{"href", "action", "formaction", "hx-get", "hx-post", "src"} {
					v, ok := s.Attr(attr)
					if !ok || !strings.HasPrefix(v, "/") {
						continue // absolute (the footer's GitHub links) or none
					}
					u, err := url.Parse(v)
					if err != nil || !strings.HasPrefix(u.EscapedPath()+"/", "/asynq/") {
						t.Errorf("%s=%q is outside the base path", attr, v)
						continue
					}
					for _, seg := range strings.Split(u.EscapedPath(), "/") {
						if plain, err := url.PathUnescape(seg); err != nil || url.PathEscape(plain) != seg {
							t.Errorf("%s=%q: segment %q is not escaped as one", attr, v, seg)
						}
					}
					links = append(links, u.EscapedPath())
				}
			})
			for _, w := range want {
				if !slices.Contains(links, strings.SplitN(w, "?", 2)[0]) {
					t.Errorf("no link to %s", w)
				}
			}
		})
	}
}
