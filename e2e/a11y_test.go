//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// axeFile is axe-core, vendored in testdata so the tests never fetch a script
// at run time. It is evaluated through the DevTools protocol, which the page's
// CSP does not apply to.
const axeFile = "testdata/axe-core-4.14.0/axe.min.js"

// a11yPage is a page the accessibility and CSP checks visit.
type a11yPage struct {
	name, path string
	// prepare puts the open page in the state the accessibility check
	// sees.
	prepare func(*page)
	// shot is the README screenshot the accessibility check saves.
	shot string
}

func a11yPages(h *harness) []a11yPage {
	return []a11yPage{
		{name: "Overview", path: "/", shot: "overview"},
		{name: "Connection", path: connPath(connPayments)},
		{name: "Queue", path: queuePath(connPayments, qInvoices, "scheduled")},
		{name: "Queue (retry, 3 selected)", path: queuePath(connPayments, qWebhooks, "retry"), prepare: selectTasks(3), shot: "queue"},
		{name: "Queue (active)", path: queuePath(connNotifications, qPush, "active")},
		{name: "Queue (paused)", path: queuePath(connNotifications, qDigest, "pending")},
		{name: "Task", path: h.fx.refund.path()},
		{name: "Task (retry)", path: h.fx.retry.path(), shot: "task"},
		{name: "Task (archived)", path: h.fx.archived.path()},
		{name: "Servers", path: connPath(connPayments) + "/servers"},
		{name: "Schedulers", path: connPath(connPayments) + "/schedulers", shot: "schedulers"},
		{name: "Info", path: connPath(connPayments) + "/info"},
	}
}

// selectTasks checks the first n rows of a Queue page, which shows the
// selection toolbar.
func selectTasks(n int) func(*page) {
	return func(p *page) {
		p.t.Helper()
		boxes := p.Locator("#tasks-rows input.cb")
		for i := range n {
			p.ok(boxes.Nth(i).Check(), "select row %d", i+1)
		}
		p.ok(p.expect.Locator(p.Locator(`[data-toolbar=tasks-form] [data-sel-count]`)).ToHaveText(strconv.Itoa(n)), "%d selected", n)
	}
}

type axeResult struct {
	Violations []struct {
		ID     string `json:"id"`
		Impact string `json:"impact"`
		Help   string `json:"help"`
		Nodes  []struct {
			Target []any  `json:"target"`
			HTML   string `json:"html"`
		} `json:"nodes"`
	} `json:"violations"`
}

// Every page, in the light and the dark theme, has no axe-core violation
// of any impact (WCAG 2.2 A/AA rules and best practices).
func testAccessibility(t *testing.T, h *harness) {
	src, err := os.ReadFile(axeFile)
	if err != nil {
		t.Fatal(err)
	}
	p := h.newPage(t, h.open, pageOpts{})
	for _, theme := range []string{"light", "dark"} {
		p.ok(p.ctx.AddCookies([]playwright.OptionalCookie{{Name: "asynqui_theme", Value: theme, URL: new(h.open.url)}}), "theme cookie")
		for _, pg := range a11yPages(h) {
			p.open(pg.path)
			if pg.prepare != nil {
				pg.prepare(p)
			}
			time.Sleep(200 * time.Millisecond) // app.js renders times, reveals copy buttons
			if pg.shot != "" && theme == "light" {
				h.screenshot(p, pg.shot)
			}
			if !p.evalBool(`() => typeof window.axe === "object"`) {
				p.eval(string(src))
			}
			raw := p.eval(`async () => JSON.stringify(await axe.run(document, {
				runOnly: {type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"]},
				resultTypes: ["violations"]
			}))`)
			var res axeResult
			if err := json.Unmarshal([]byte(raw.(string)), &res); err != nil {
				t.Fatal(err)
			}
			for _, v := range res.Violations {
				var where []string
				for _, n := range v.Nodes {
					b, _ := json.Marshal(n.Target)
					where = append(where, string(b))
				}
				msg := pg.name + " (" + theme + "): " + v.Impact + " " + v.ID + ": " + v.Help + " at " + strings.Join(where, ", ")
				t.Error(msg)
				for _, n := range v.Nodes {
					t.Logf("    %s", n.HTML)
				}
			}
		}
	}
}

// No page logs a CSP violation (or any console error), including after app.js
// has polled, opened a dialog and swapped content.
func testNoCSPViolations(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{cookies: map[string]string{"asynqui_refresh": "2s"}})
	paths := []string{
		connPath(connPayments) + "/queues/" + qInvoices + "/find?id=nope", // the no-JS Find task page
		connPath(connPayments) + "/servers/" + firstServerID(t, h),
		connPath(connPayments) + "/schedulers/" + firstEntryID(t, h),
	}
	for _, pg := range a11yPages(h) {
		paths = append(paths, pg.path)
	}
	for _, path := range paths {
		p.open(path)
		before := p.polls.Load()
		if p.evalBool(`() => !!document.querySelector("[data-poll]")`) {
			eventually(t, "a poll on "+path, func() bool { return p.polls.Load() > before })
			time.Sleep(300 * time.Millisecond) // its swap
		}
	}
	// Dialogs, the theme and a toast on an action that fails.
	p.open(queuePath(connPayments, qInvoices, "pending"))
	p.ok(p.Keyboard().Press("/"), "open Find task")
	p.ok(p.Keyboard().Press("Escape"), "close it")
	p.ok(p.Locator(`button[name=theme][value=dark]`).Click(), "dark theme")
	p.open(connPath(connPayments))
	p.ok(p.Locator("#"+hexID("qsel-", qPayouts)).Check(), "select")
	p.ok(p.Locator("#act-delete").Click(), "Delete")
	p.ok(p.expect.Locator(p.Locator("#queues-delete")).ToBeVisible(), "dialog")
	p.ok(p.Locator("#queues-delete-cancel").Click(), "cancel")

	// The login page, on the basic instance.
	p.open(h.basic.url + "/login")
	p.ok(p.expect.Locator(p.Locator("#login-username")).ToBeFocused(), "username autofocus")
}

func firstServerID(t *testing.T, h *harness) string {
	t.Helper()
	srvs, err := h.fx.insp[connPayments].Servers()
	if err != nil || len(srvs) == 0 {
		t.Fatalf("servers: %v (%d)", err, len(srvs))
	}
	return srvs[0].ID
}

func firstEntryID(t *testing.T, h *harness) string {
	t.Helper()
	es, err := h.fx.insp[connPayments].SchedulerEntries()
	if err != nil || len(es) == 0 {
		t.Fatalf("scheduler entries: %v (%d)", err, len(es))
	}
	return es[0].ID
}
