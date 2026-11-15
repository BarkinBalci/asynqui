//go:build e2e

package e2e

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	playwright "github.com/mxschmitt/playwright-go"
)

// The README's flow: from the Overview into payments and its webhooks
// queue, then Run now on three retry tasks, with the default 5s refresh
// polling along. The notice, the tab counts, the table and Redis all
// show the three tasks moved to pending.
func testRunRetries(t *testing.T, h *harness) {
	insp := h.fx.insp[connPayments]
	before, err := insp.GetQueueInfo(qWebhooks)
	if err != nil {
		t.Fatal(err)
	}
	if before.Retry != webhookRetries {
		t.Fatalf("%s has %d retry tasks, want the fixture's %d", qWebhooks, before.Retry, webhookRetries)
	}
	p := h.newPage(t, h.open, pageOpts{cookies: map[string]string{"asynqui_refresh": "5s"}})
	p.open("/")
	p.ok(p.Locator("#open-"+connPayments).Click(), "open payments")
	p.ok(p.expect.Page(p.Page).ToHaveURL(regexp.MustCompile(regexp.QuoteMeta(connPath(connPayments))+"$")), "on the Connection page")
	p.ok(p.Locator("#"+hexID("qlink-", qWebhooks)).Click(), "open webhooks")
	p.ok(p.expect.Page(p.Page).ToHaveURL(containing(connPath(connPayments)+"/queues/"+qWebhooks)), "on the Queue page")
	p.ok(p.Locator("#tab-retry").Click(), "Retry tab")
	p.ok(p.expect.Page(p.Page).ToHaveURL(containing("state=retry")), "URL has state=retry")
	p.ok(p.expect.Locator(p.Locator("#tab-retry")).ToHaveClass(containing("on")), "Retry tab on")

	retries, pending := p.Locator("#tab-count-retry"), p.Locator("#tab-count-pending")
	p.ok(p.expect.Locator(retries).ToHaveText(strconv.Itoa(webhookRetries)), "retry count")
	p.ok(p.expect.Locator(pending).ToHaveText(strconv.Itoa(before.Pending)), "pending count")
	boxes := p.Locator("#tasks-rows input.cb")
	p.ok(p.expect.Locator(boxes).ToHaveCount(webhookRetries), "a row per retry task")
	var ids []string
	for i := range 3 {
		box := boxes.Nth(i)
		p.ok(box.Check(), "select row %d", i+1)
		id, err := box.GetAttribute("value")
		p.ok(err, "row %d id", i+1)
		ids = append(ids, id)
	}
	p.ok(p.expect.Locator(p.Locator(`[data-toolbar=tasks-form] [data-sel-count]`)).ToHaveText("3"), "3 selected")
	p.ok(p.Locator("#act-run").Click(), "Run now")

	p.ok(p.expect.Locator(p.Locator(".notice")).ToHaveText("Moved 3 tasks to pending"), "notice")
	p.ok(p.expect.Locator(retries).ToHaveText(strconv.Itoa(webhookRetries-3)), "retry count after")
	p.ok(p.expect.Locator(pending).ToHaveText(strconv.Itoa(before.Pending+3)), "pending count after")
	p.ok(p.expect.Locator(boxes).ToHaveCount(webhookRetries-3), "rows after")
	for _, id := range ids {
		info, err := insp.GetTaskInfo(qWebhooks, id)
		if err != nil {
			t.Errorf("task %s: %v", id, err)
		} else if info.State != asynq.TaskStatePending {
			t.Errorf("task %s is %s, want pending", id, info.State)
		}
	}
}

// bodyBG is the body's computed background color.
func (p *page) bodyBG() string {
	return p.evalString(`() => getComputedStyle(document.body).backgroundColor`)
}

// themeBGs reads the light and dark backgrounds by pressing the theme
// buttons, which app.js applies in place.
func (p *page) themeBGs() (light, dark string) {
	p.t.Helper()
	p.ok(p.Locator(`button[name=theme][value=light]`).Click(), "light theme")
	p.waitTheme("light")
	light = p.settledBG()
	p.ok(p.Locator(`button[name=theme][value=dark]`).Click(), "dark theme")
	p.waitTheme("dark")
	dark = p.settledBG()
	if light == dark {
		p.t.Fatalf("light and dark backgrounds are both %s", light)
	}
	return light, dark
}

func (p *page) waitTheme(v string) {
	p.t.Helper()
	p.ok(p.expect.Locator(p.Locator("html")).ToHaveAttribute("data-theme", v), "data-theme %s", v)
}

// settledBG waits out any color transition.
func (p *page) settledBG() string {
	p.t.Helper()
	last := p.bodyBG()
	for range 40 {
		time.Sleep(50 * time.Millisecond)
		now := p.bodyBG()
		if now == last {
			return now
		}
		last = now
	}
	return last
}

// The theme is rendered by the server from the cookie, so a reload paints
// the chosen theme from the first byte, before app.js runs.
func testThemeNoFlash(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	// Record data-theme as the parser creates <html>, before any script.
	p.ok(p.ctx.AddInitScript(playwright.Script{Content: new(`
		new MutationObserver(function (_, o) {
			if (document.documentElement) {
				window.__firstTheme = document.documentElement.getAttribute("data-theme");
				o.disconnect();
			}
		}).observe(document, {childList: true});`)}), "init script")
	p.open("/")
	_, dark := p.themeBGs()

	resp, err := p.Reload()
	p.ok(err, "reload")
	body, err := resp.Text()
	p.ok(err, "body")
	if !strings.Contains(body, `data-theme="dark"`) {
		t.Errorf("reloaded HTML has no data-theme=\"dark\"")
	}
	if got := p.evalString(`() => window.__firstTheme`); got != "dark" {
		t.Errorf("first data-theme = %q, want dark", got)
	}
	if got := p.settledBG(); got != dark {
		t.Errorf("background after reload = %s, want dark %s", got, dark)
	}
	p.ok(p.expect.Locator(p.Locator(`button[name=theme][value=dark]`)).ToHaveAttribute("aria-pressed", "true"), "dark pressed")
}

// System follows prefers-color-scheme, live.
func testThemeSystem(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open("/")
	light, dark := p.themeBGs()
	p.ok(p.Locator(`button[name=theme][value=system]`).Click(), "system theme")
	p.waitTheme("system")
	p.ok(p.EmulateMedia(playwright.PageEmulateMediaOptions{ColorScheme: playwright.ColorSchemeDark}), "emulate dark")
	if got := p.settledBG(); got != dark {
		t.Errorf("system + prefers dark: background %s, want %s", got, dark)
	}
	p.ok(p.EmulateMedia(playwright.PageEmulateMediaOptions{ColorScheme: playwright.ColorSchemeLight}), "emulate light")
	if got := p.settledBG(); got != light {
		t.Errorf("system + prefers light: background %s, want %s", got, light)
	}
	// And it sticks across a reload.
	_, err := p.Reload()
	p.ok(err, "reload")
	p.waitTheme("system")
}

// Off sends no polls at all; 2s polls about every 2s.
func testRefreshOffAnd2s(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{cookies: map[string]string{"asynqui_refresh": "off"}})
	p.open(connPath(connPayments))
	p.ok(p.expect.Locator(p.Locator(`button[name=refresh][value=off]`)).ToHaveAttribute("aria-pressed", "true"), "Off pressed")
	time.Sleep(6 * time.Second)
	if n := p.polls.Load(); n != 0 {
		t.Errorf("refresh Off: %d poll requests in 6s, want 0", n)
	}
	p.ok(p.Locator(`button[name=refresh][value="2s"]`).Click(), "2s")
	start := time.Now()
	eventually(t, "two polls after choosing 2s", func() bool { return p.polls.Load() >= 2 })
	if d := time.Since(start); d < 3*time.Second || d > 5500*time.Millisecond {
		t.Errorf("two polls took %s, want about 4s", d)
	}
	// The choice is kept in the cookie.
	_, err := p.Reload()
	p.ok(err, "reload")
	p.ok(p.expect.Locator(p.Locator("html")).ToHaveAttribute("data-refresh", "2s"), "data-refresh after reload")
}

// setHidden fakes the tab's visibility and fires visibilitychange.
func (p *page) setHidden(hidden bool) {
	p.eval(`(hidden) => {
		Object.defineProperty(document, "hidden", {configurable: true, get: () => hidden});
		Object.defineProperty(document, "visibilityState", {configurable: true, get: () => hidden ? "hidden" : "visible"});
		document.dispatchEvent(new Event("visibilitychange"));
	}`, hidden)
}

// Polling stops while the tab is hidden and refreshes once on return.
func testPollingPausesWhileHidden(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{cookies: map[string]string{"asynqui_refresh": "2s"}})
	p.open(connPath(connPayments))
	eventually(t, "a poll with 2s", func() bool { return p.polls.Load() >= 1 })
	p.setHidden(true)
	time.Sleep(300 * time.Millisecond) // a poll already in flight
	before := p.polls.Load()
	time.Sleep(5 * time.Second)
	if n := p.polls.Load() - before; n != 0 {
		t.Errorf("hidden tab: %d polls in 5s, want 0", n)
	}
	before = p.polls.Load()
	p.setHidden(false)
	deadline := time.Now().Add(1 * time.Second)
	for p.polls.Load() == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if p.polls.Load() == before {
		t.Errorf("no refresh within 1s of the tab becoming visible")
	}
}

// delayPolls holds each answer to pollID for d, so a test can act while
// a poll is in flight. It returns how many were held.
func (p *page) delayPolls(pollID string, d time.Duration) *atomic.Int64 {
	p.t.Helper()
	var n atomic.Int64
	p.ok(p.Route("**/*", func(r playwright.Route) {
		if r.Request().Headers()["hx-trigger"] == pollID {
			n.Add(1)
			time.Sleep(d)
		}
		_ = r.Continue()
	}), "route")
	return &n
}

// rowNames lists the queue names in the Connection page's table order.
func (p *page) rowNames() []string {
	p.t.Helper()
	v := p.eval(`() => Array.from(document.querySelectorAll("#queues-rows tr[data-filter-text]")).map(tr => tr.getAttribute("data-filter-text"))`)
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// A sort clicked while a poll is in flight wins: the poll is aborted (or
// its stale answer dropped), so the table, the poll URL and the address
// keep the new sort.
func testSortDuringPoll(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open(connPath(connPayments))
	before := p.rowNames()
	// htmx logs the poll it aborts for the sort.
	p.allow(`^console error: htmx:(sendAbort|afterRequest), `)
	held := p.delayPolls("queues-poll", 1500*time.Millisecond)
	p.eval(`() => { htmx.trigger(document.getElementById("queues-poll"), "asynqui-refresh"); }`)
	eventually(t, "the poll to be in flight", func() bool { return held.Load() == 1 })
	p.ok(p.Locator("#sort-name").Click(), "sort by name")
	p.ok(p.expect.Page(p.Page).ToHaveURL(containing("sort=name")), "URL has sort=name")
	time.Sleep(2500 * time.Millisecond) // past the held poll's answer

	if !strings.Contains(p.URL(), "sort=name") {
		t.Errorf("URL lost the sort: %s", p.URL())
	}
	sortTh := p.Locator(`th:has(#sort-name)`)
	p.ok(p.expect.Locator(sortTh).ToHaveAttribute("aria-sort", "ascending"), "name column sorted")
	if got, _ := p.Locator("#queues-poll").GetAttribute("hx-get"); !strings.Contains(got, "sort=name") {
		t.Errorf("poll URL %q lost the sort", got)
	}
	got := p.rowNames()
	if !slices.IsSorted(got) {
		t.Errorf("rows not sorted by name: %v", got)
	}
	if slices.Equal(got, before) {
		t.Fatalf("test needs a default order that differs from by name: %v", got)
	}
	// The page still polls with the new sort.
	p.pollOnce("queues-poll")
	if got := p.rowNames(); !slices.IsSorted(got) {
		t.Errorf("after the next poll, rows not sorted by name: %v", got)
	}
}

// At 1440px with the sidebar open, every column of the queues table fits
// its card, so nothing is clipped behind the horizontal scroll.
func testQueuesTableFits(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.ok(p.SetViewportSize(1440, 900), "viewport")
	p.open(connPath(connPayments))
	p.ok(p.Locator("#queues").WaitFor(), "queues table")
	over, _ := p.eval(`async () => {
		await document.fonts.ready;
		const s = document.getElementById("queues").closest(".tbl-scroll");
		return s.scrollWidth - s.clientWidth;
	}`).(int)
	if over > 0 {
		t.Errorf("queues table is %dpx wider than its card", over)
	}
}

// A poll swaps only the chart body, never the queue picker: an open
// select is the same element, focused, with its value, afterwards.
func testPickerSurvivesPoll(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open(connPath(connPayments))
	p.ok(p.Locator("#tp-queue").Focus(), "focus picker")
	p.eval(`() => { document.getElementById("tp-queue").__mark = 1; document.getElementById("tp-chart").__mark = 1; }`)
	p.pollOnce("queues-poll")
	if !p.evalBool(`() => document.getElementById("tp-queue").__mark === 1`) {
		t.Errorf("the poll replaced the queue picker")
	}
	if p.evalBool(`() => document.getElementById("tp-chart").__mark === 1`) {
		t.Errorf("the poll did not replace the chart body (test is not exercising a swap)")
	}
	if id := p.activeID(); id != "tp-queue" {
		t.Errorf("focus moved to %q", id)
	}
}

// Checked rows stay checked across polls (hx-preserve) and the toolbar
// keeps counting them.
func testSelectionSurvivesPoll(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open(connPath(connPayments))
	count := p.Locator(`[data-toolbar=queues-form] [data-sel-count]`)
	a, b, c := "#"+hexID("qsel-", qCritical), "#"+hexID("qsel-", qInvoices), "#"+hexID("qsel-", qWebhooks)
	p.ok(p.Locator(a).Check(), "check a")
	p.ok(p.Locator(b).Check(), "check b")
	p.ok(p.expect.Locator(count).ToHaveText("2"), "2 selected")
	p.eval(`() => { document.querySelector("#queues-rows tr").__mark = 1; }`)
	p.pollOnce("queues-poll")
	if p.evalBool(`() => document.querySelector("#queues-rows tr").__mark === 1`) {
		t.Errorf("the poll did not replace the table (test is not exercising a swap)")
	}
	for _, sel := range []string{a, b} {
		p.ok(p.expect.Locator(p.Locator(sel)).ToBeChecked(), "%s checked after poll", sel)
	}
	p.ok(p.expect.Locator(count).ToHaveText("2"), "still 2 selected")
	p.ok(p.Locator(c).Check(), "check c")
	p.ok(p.expect.Locator(count).ToHaveText("3"), "3 selected")
	p.pollOnce("queues-poll")
	p.ok(p.expect.Locator(count).ToHaveText("3"), "3 selected after poll")
	p.ok(p.expect.Locator(p.Locator(`#act-pause`)).ToBeEnabled(), "Pause enabled")
}

// Delete is enabled only while every selected queue is empty.
func testDeleteNeedsEmptyQueue(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open(connPath(connPayments))
	del := p.Locator("#act-delete")
	p.ok(p.expect.Locator(del).ToBeDisabled(), "Delete disabled with no selection")
	p.ok(p.Locator("#"+hexID("qsel-", qCritical)).Check(), "check non-empty")
	p.ok(p.expect.Locator(del).ToBeDisabled(), "Delete disabled for a non-empty queue")
	p.ok(p.expect.Locator(del).ToHaveAttribute("title", "Queue is not empty"), "blocked title")
	p.ok(p.Locator("#"+hexID("qsel-", qPayouts)).Check(), "check empty")
	p.ok(p.expect.Locator(del).ToBeDisabled(), "Delete disabled for a mixed selection")
	p.ok(p.Locator("#"+hexID("qsel-", qCritical)).Uncheck(), "uncheck non-empty")
	p.ok(p.expect.Locator(del).ToBeEnabled(), "Delete enabled for an empty queue")
	if title, _ := del.GetAttribute("title"); title != "" {
		t.Errorf("enabled Delete still has title %q", title)
	}
}

// Select the page, then "Select all N", then Delete all: the dialog
// counts N and confirming empties the state.
func testSelectAllNDeleteAll(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open(queuePath(connReports, qLow, "pending"))
	banner := p.Locator("#tasks-banner")
	p.ok(p.expect.Locator(banner).ToBeHidden(), "banner hidden")
	p.ok(p.Locator("#tsel-all").Check(), "select page")
	p.ok(p.expect.Locator(banner).ToBeVisible(), "banner shown")
	count := p.Locator(`[data-toolbar=tasks-form] [data-sel-count]`)
	p.ok(p.expect.Locator(count).ToHaveText("20"), "20 selected")
	everything := p.Locator("#sel-everything")
	n := strconv.Itoa(lowBacklog)
	p.ok(p.expect.Locator(everything).ToHaveText("Select all "+n+" pending tasks"), "select all N label")
	p.ok(everything.Click(), "select all N")
	p.ok(p.expect.Locator(count).ToHaveText(n), "%s selected", n)
	p.ok(p.expect.Locator(p.Locator(`[data-toolbar=tasks-form] [data-sel-all]`)).ToBeVisible(), `"All" shown`)
	p.ok(p.expect.Locator(p.Locator("#sel-clear")).ToBeFocused(), "focus on Clear selection")

	p.ok(p.Locator("#act-delete").Click(), "Delete")
	dlg := p.Locator("#tasks-all-delete")
	p.ok(p.expect.Locator(dlg).ToBeVisible(), "all dialog open")
	p.ok(p.expect.Locator(p.Locator("#tasks-all-delete-title")).ToHaveText("Delete all "+n+" pending tasks"), "dialog title")
	if n, _ := p.Locator("#tasks-delete").IsVisible(); n {
		t.Errorf("the per-row delete dialog opened too")
	}
	p.ok(p.Locator("#tasks-all-delete-confirm").Click(), "confirm")
	p.ok(p.expect.Locator(dlg).ToBeHidden(), "dialog closed")
	p.ok(p.expect.Locator(p.Locator("#tab-count-pending")).ToHaveText("0"), "pending count 0")
	p.ok(p.expect.Locator(p.Locator("#tasks-rows")).ToHaveText("No pending tasks"), "empty table")
	p.ok(p.expect.Locator(p.Locator(".notice")).ToHaveText("Deleted "+n+" tasks"), "notice")
	p.ok(p.expect.Locator(count).ToBeHidden(), "selection cleared")
	info, err := h.fx.insp[connReports].GetQueueInfo(qLow)
	p.ok(err, "queue info")
	if info.Pending != 0 || info.Active != 1 {
		t.Errorf("Redis has %d pending and %d active tasks in %s, want 0 and the 1 running", info.Pending, info.Active, qLow)
	}
}

// Active offers no "select all N", even with more tasks than one page.
func testNoSelectAllOnActive(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open(queuePath(connNotifications, qPush, "active"))
	n := strconv.Itoa(pushActive)
	p.ok(p.expect.Locator(p.Locator("#tab-count-active")).ToHaveText(n), "%s active", n)
	p.ok(p.Locator("#tsel-all").Check(), "select page")
	count := p.Locator(`[data-toolbar=tasks-form] [data-sel-count]`)
	p.ok(p.expect.Locator(count).ToHaveText("20"), "20 selected")
	p.ok(p.expect.Locator(p.Locator("#tasks-banner")).ToBeHidden(), "no banner")
	if n, _ := p.Locator("#sel-everything, [data-select-everything]").Count(); n != 0 {
		t.Errorf("Active has a select all N button")
	}
	if n, _ := p.Locator("[data-confirm-all]").Count(); n != 0 {
		t.Errorf("Active has an \"all\" action")
	}
	p.ok(p.expect.Locator(p.Locator("#act-cancel")).ToBeEnabled(), "Cancel enabled")
}

// Archiving asks first and warns that asynq trims the archive.
func testArchiveWarning(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	insp := h.fx.insp[connPayments]
	before, err := insp.GetQueueInfo(qInvoices)
	p.ok(err, "queue info")
	p.open(queuePath(connPayments, qInvoices, "pending"))
	p.ok(p.Locator("#tasks-rows input[type=checkbox]").First().Check(), "check a task")
	p.ok(p.Locator("#act-archive").Click(), "Archive")
	dlg := p.Locator("#tasks-archive")
	p.ok(p.expect.Locator(dlg).ToBeVisible(), "archive dialog open")
	p.ok(p.expect.Locator(dlg.Locator(".dlg-warn")).ToBeVisible(), "warning shown")
	p.ok(p.expect.Locator(dlg.Locator(".dlg-warn")).ToContainText("archived tasks"), "warning text")
	p.ok(p.expect.Locator(dlg.Locator("[data-sel-names] li")).ToHaveCount(1), "one task listed")
	p.ok(p.expect.Locator(p.Locator("#tasks-archive-confirm")).ToHaveClass(containing("danger")), "danger confirm")
	p.ok(p.expect.Locator(p.Locator("#tasks-archive-cancel")).ToBeFocused(), "Cancel focused")
	p.ok(p.Keyboard().Press("Escape"), "Escape")
	p.ok(p.expect.Locator(dlg).ToBeHidden(), "dialog closed")
	after, err := insp.GetQueueInfo(qInvoices)
	p.ok(err, "queue info")
	if after.Archived != before.Archived || after.Pending != before.Pending {
		t.Errorf("closing the dialog acted: %d pending, %d archived, was %d and %d",
			after.Pending, after.Archived, before.Pending, before.Archived)
	}
}

// "/" opens Find task; a miss says so inline; Enter on a hit navigates;
// Escape closes it and gives focus back to the button.
func testFindTask(t *testing.T, h *harness) {
	task := h.fx.refund
	p := h.newPage(t, h.open, pageOpts{})
	p.open(queuePath(task.conn, task.queue, "scheduled"))
	dlg, input := p.Locator("#find-task"), p.Locator("#find-id")
	p.ok(p.Keyboard().Press("/"), "press /")
	p.ok(p.expect.Locator(dlg).ToBeVisible(), "dialog open")
	p.ok(p.expect.Locator(input).ToBeFocused(), "input focused")

	p.ok(p.Keyboard().Type("no-such-task"), "type a miss")
	p.ok(p.Keyboard().Press("Enter"), "Enter")
	p.ok(p.expect.Locator(p.Locator("#find-miss")).ToHaveText("Task not found"), "miss inline")
	p.ok(p.expect.Locator(dlg).ToBeVisible(), "dialog still open")
	p.ok(p.expect.Locator(input).ToHaveAttribute("aria-invalid", "true"), "input invalid")
	p.ok(p.expect.Locator(input).ToBeFocused(), "input focused after a miss")

	p.ok(p.Keyboard().Press("Escape"), "Escape")
	p.ok(p.expect.Locator(dlg).ToBeHidden(), "dialog closed")
	p.ok(p.expect.Locator(p.Locator("#find-open")).ToBeFocused(), "focus back on Find task")

	// Reopened, it is empty again.
	p.ok(p.Locator("#find-open").Click(), "Find task button")
	p.ok(p.expect.Locator(input).ToBeFocused(), "input focused")
	p.ok(p.expect.Locator(input).ToHaveValue(""), "input empty")
	if n, _ := p.Locator("#find-miss").Count(); n != 0 {
		t.Errorf("reopened dialog still shows the miss")
	}
	p.ok(p.Keyboard().Type(task.id), "type the ID")
	p.ok(p.Keyboard().Press("Enter"), "Enter")
	p.ok(p.expect.Page(p.Page).ToHaveURL(containing(task.path())), "on the Task page")
	p.ok(p.expect.Locator(p.Locator(".id-full")).ToHaveText(task.id), "task shown")
}

// readClipboard reads the clipboard (the context grants clipboard-read).
func (p *page) readClipboard() string {
	return p.evalString(`() => navigator.clipboard.readText()`)
}

// Copy buttons copy the full text, not what is shown.
func testCopy(t *testing.T, h *harness) {
	task := h.fx.refund
	var fields struct {
		RefundID string `json:"refund_id"`
	}
	if err := json.Unmarshal([]byte(task.payload), &fields); err != nil || fields.RefundID == "" {
		t.Fatalf("fixture payload %s: %v", task.payload, err)
	}
	p := h.newPage(t, h.open, pageOpts{permissions: []string{"clipboard-read", "clipboard-write"}})
	// In the table the ID is shortened.
	p.open(queuePath(task.conn, task.queue, "scheduled"))
	link := p.Locator("#" + hexID("tlink-", task.id))
	p.ok(p.expect.Locator(link).ToHaveText(task.id[:13]+"…"), "short ID in the table")

	p.open(task.path())
	btn := p.Locator("#task-copy-id")
	p.ok(p.expect.Locator(btn).ToBeVisible(), "Copy ID shown")
	p.ok(btn.Click(), "Copy ID")
	eventually(t, "the ID on the clipboard", func() bool { return p.readClipboard() == task.id })
	p.ok(p.expect.Locator(btn).ToHaveClass(containing("copied")), "copied feedback")

	payload, err := p.Locator("#payload").TextContent()
	p.ok(err, "payload text")
	if !strings.Contains(payload, fields.RefundID) {
		t.Fatalf("payload block %q", payload)
	}
	p.ok(p.Locator("#payload-copy").Click(), "Copy payload")
	eventually(t, "the payload on the clipboard", func() bool { return p.readClipboard() == payload })

	p.open(connPath(connPayments) + "/info")
	raw, err := p.Locator("#raw-info").TextContent()
	p.ok(err, "raw INFO")
	p.ok(p.Locator("#raw-info-copy").Click(), "Copy INFO")
	eventually(t, "the raw INFO on the clipboard", func() bool { return p.readClipboard() == raw })
	if len(raw) < 1000 {
		t.Errorf("raw INFO is only %d bytes", len(raw))
	}
}

// A poll gives focus back to the checkbox or link that had it.
func testFocusSurvivesPoll(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{})
	p.open(connPath(connPayments))
	for _, id := range []string{hexID("qsel-", qInvoices), hexID("qlink-", qInvoices), "qsel-all", "sort-size"} {
		p.ok(p.Locator("#"+id).Focus(), "focus %s", id)
		p.pollOnce("queues-poll")
		if got := p.activeID(); got != id {
			t.Errorf("Connection page: focus on %s moved to %q after a poll", id, got)
		}
	}
	// Keyboard selection keeps working after the poll.
	box := "#" + hexID("qsel-", qInvoices)
	p.ok(p.Locator(box).Focus(), "focus")
	p.ok(p.Keyboard().Press("Space"), "Space")
	p.ok(p.expect.Locator(p.Locator(box)).ToBeChecked(), "checked by keyboard")
	p.pollOnce("queues-poll")
	p.ok(p.expect.Locator(p.Locator(box)).ToBeFocused(), "still focused")
	p.ok(p.expect.Locator(p.Locator(box)).ToBeChecked(), "still checked")

	p.open(queuePath(connPayments, qInvoices, "scheduled"))
	first := p.Locator("#tasks-rows input[type=checkbox]").First()
	id, err := first.GetAttribute("id")
	p.ok(err, "row id")
	for _, id := range []string{id, "tab-pending", "tsel-all"} {
		p.ok(p.Locator("#"+id).Focus(), "focus %s", id)
		p.pollOnce("queue-poll")
		if got := p.activeID(); got != id {
			t.Errorf("Queue page: focus on %s moved to %q after a poll", id, got)
		}
	}
}

// The account menu closes on Escape and gives focus back to its button.
func testAccountMenuEscape(t *testing.T, h *harness) {
	p := h.newPage(t, h.basic, pageOpts{})
	p.login("/", adminUser, adminPassword)
	p.ok(p.expect.Page(p.Page).ToHaveURL(h.basic.url+"/"), "signed in")
	menu, summary := p.Locator("details.account"), p.Locator("details.account > summary")
	p.ok(summary.Click(), "open menu")
	p.ok(p.expect.Locator(menu).ToHaveAttribute("open", ""), "menu open")
	signOut := p.Locator(".account-menu .menu-item")
	p.ok(signOut.Focus(), "focus Sign out")
	p.ok(p.Keyboard().Press("Escape"), "Escape")
	p.ok(p.expect.Locator(menu).Not().ToHaveAttribute("open", ""), "menu closed")
	p.ok(p.expect.Locator(summary).ToBeFocused(), "focus back on the menu button")
	// An outside click closes it too.
	p.ok(summary.Click(), "reopen")
	p.ok(p.expect.Locator(menu).ToHaveAttribute("open", ""), "menu open")
	p.ok(p.Locator("h1").First().Click(), "click outside")
	p.ok(p.expect.Locator(menu).Not().ToHaveAttribute("open", ""), "menu closed by outside click")
}

// <time> elements show relative text with the absolute local time in
// title; data-format=absolute ones show the local time itself.
func testTimes(t *testing.T, h *harness) {
	const tz = "Asia/Kolkata" // +05:30: local times differ from UTC in minutes too
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	p := h.newPage(t, h.open, pageOpts{timezone: tz})
	p.open(queuePath(connPayments, qInvoices, "scheduled"))
	rel := p.Locator("#tasks-rows time:not([data-format])").First()
	relText := regexp.MustCompile(`^in (\d+)m (\d\d)s$`)
	p.ok(p.expect.Locator(rel).ToHaveText(relText), "relative text")
	dt, err := rel.GetAttribute("datetime")
	p.ok(err, "datetime")
	when, err := time.Parse(time.RFC3339, dt)
	p.ok(err, "parse datetime %q", dt)
	text, err := rel.TextContent()
	p.ok(err, "relative text")
	if m := relText.FindStringSubmatch(text); m != nil {
		mins, _ := strconv.Atoi(m[1])
		secs, _ := strconv.Atoi(m[2])
		shown := time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second
		if off := time.Until(when) - shown; off < -2*time.Second || off > 2*time.Second {
			t.Errorf("relative text %q is %s off the datetime %s", text, off, dt)
		}
	}
	title, err := rel.GetAttribute("title")
	p.ok(err, "title")
	wantAbs := when.In(loc).Format("15:04:05")
	if !strings.Contains(title, wantAbs) || !strings.Contains(title, when.In(loc).Format("Jan")) {
		t.Errorf("title %q, want the local time %s", title, wantAbs)
	}
	abs := p.Locator("#tasks-rows time[data-format=absolute]").First()
	p.ok(p.expect.Locator(abs).ToContainText(wantAbs), "absolute local time")
	if v, _ := abs.GetAttribute("title"); v != "" {
		t.Errorf("absolute time has a title %q", v)
	}
	// The Updated time counts up.
	upd := p.Locator("#updated-at")
	p.ok(p.expect.Locator(upd).ToHaveText(regexp.MustCompile(`^\d+s ago$`)), "Updated relative")
	p.ok(p.expect.Locator(upd).ToHaveText(regexp.MustCompile(`^[2-9]s ago$`), playwright.LocatorAssertionsToHaveTextOptions{Timeout: playwright.Float(5000)}), "Updated ticks")
}

// A bad password shows the error on the fields; a good one goes to next;
// a viewer's action buttons are disabled.
func testLogin(t *testing.T, h *harness) {
	p := h.newPage(t, h.basic, pageOpts{})
	target := queuePath(connPayments, qInvoices, "pending")
	// A failed sign-in answers 401.
	p.allow(`status of 401 \(Unauthorized\)`)
	p.login(target, adminUser, "wrong-password")
	alert := p.Locator("#login-error")
	p.ok(p.expect.Locator(alert).ToHaveText("Invalid username or password."), "error shown")
	p.ok(p.expect.Locator(alert).ToHaveAttribute("role", "alert"), "role alert")
	for _, f := range []string{"#login-username", "#login-password"} {
		p.ok(p.expect.Locator(p.Locator(f)).ToHaveAttribute("aria-invalid", "true"), "%s invalid", f)
		p.ok(p.expect.Locator(p.Locator(f)).ToHaveAttribute("aria-describedby", "login-error"), "%s described", f)
	}
	p.ok(p.Locator("#login-username").Fill(adminUser), "username")
	p.ok(p.Locator("#login-password").Fill(adminPassword), "password")
	p.ok(p.Locator("#login-password").Press("Enter"), "submit")
	p.ok(p.expect.Page(p.Page).ToHaveURL(h.basic.url+target), "redirected to next")
	p.ok(p.expect.Locator(p.Locator("#act-delete")).ToHaveAttribute("data-enable", "any"), "admin can delete")
}

func testViewerActionsDisabled(t *testing.T, h *harness) {
	p := h.newPage(t, h.basic, pageOpts{})
	p.login(connPath(connPayments), viewerUser, viewerPass)
	p.ok(p.expect.Page(p.Page).ToHaveURL(h.basic.url+connPath(connPayments)), "signed in")
	p.ok(p.Locator("#"+hexID("qsel-", qCritical)).Check(), "select a queue")
	want := map[string]string{
		"#act-pause":  "Requires operator",
		"#act-resume": "Requires operator",
		"#act-delete": "Requires admin",
	}
	for sel, title := range want {
		b := p.Locator(sel)
		p.ok(p.expect.Locator(b).ToBeDisabled(), "%s disabled", sel)
		p.ok(p.expect.Locator(b).ToHaveAttribute("title", containing(title)), "%s title", sel)
	}
	p.open(queuePath(connPayments, qInvoices, "pending"))
	p.ok(p.Locator("#tasks-rows input[type=checkbox]").First().Check(), "select a task")
	for _, sel := range []string{"#act-archive", "#act-delete", "#queue-toggle-btn"} {
		p.ok(p.expect.Locator(p.Locator(sel)).ToBeDisabled(), "%s disabled", sel)
	}
	p.ok(p.expect.Locator(p.Locator(".account-role")).ToHaveText("viewer"), "role shown")
}
