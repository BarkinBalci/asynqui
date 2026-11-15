//go:build e2e

package e2e

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// TestBrowser runs every browser test against one harness: one Redis
// with the fixture, two asynqui instances and one Chromium. Each subtest
// gets its own browser context (cookies, permissions) and changes only
// what no other subtest reads, so any of them can run alone:
// go test -tags e2e ./e2e -run 'TestBrowser/Theme'.
func TestBrowser(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name string
		run  func(*testing.T, *harness)
	}{
		{"RunRetries", testRunRetries},
		{"ThemeNoFlash", testThemeNoFlash},
		{"ThemeSystem", testThemeSystem},
		{"RefreshOffAnd2s", testRefreshOffAnd2s},
		{"PollingPausesWhileHidden", testPollingPausesWhileHidden},
		{"SortDuringPoll", testSortDuringPoll},
		{"QueuesTableFits", testQueuesTableFits},
		{"PickerSurvivesPoll", testPickerSurvivesPoll},
		{"SelectionSurvivesPoll", testSelectionSurvivesPoll},
		{"DeleteNeedsEmptyQueue", testDeleteNeedsEmptyQueue},
		{"SelectAllNDeleteAll", testSelectAllNDeleteAll},
		{"NoSelectAllOnActive", testNoSelectAllOnActive},
		{"ArchiveWarning", testArchiveWarning},
		{"FindTask", testFindTask},
		{"Copy", testCopy},
		{"FocusSurvivesPoll", testFocusSurvivesPoll},
		{"AccountMenuEscape", testAccountMenuEscape},
		{"Times", testTimes},
		{"Login", testLogin},
		{"ViewerActionsDisabled", testViewerActionsDisabled},
		{"Accessibility", testAccessibility},
		{"NoCSPViolations", testNoCSPViolations},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, h) })
	}
}

// page is a browser tab in its own context. Every console error, page
// error and CSP violation it sees fails the test at cleanup, so each test
// also checks that the pages it visits run clean under the strict CSP.
type page struct {
	playwright.Page
	t      *testing.T
	ctx    playwright.BrowserContext
	expect playwright.PlaywrightAssertions
	pace   bool

	mu       sync.Mutex
	problems []string
	allowed  []*regexp.Regexp
	// polls counts poll requests: htmx GETs from a *-poll element, or
	// from the Overview grid, which has no id.
	polls atomic.Int64
}

// pageOpts tweak a new page's browser context.
type pageOpts struct {
	cookies     map[string]string
	permissions []string
	timezone    string
}

// recordCSP turns every CSP violation into a console error, which the
// page's listener records; it runs before any page script, outside the
// page's CSP.
const recordCSP = `document.addEventListener("securitypolicyviolation", function (e) {
	console.error("CSP violation: " + e.violatedDirective + " blocked " + (e.blockedURI || "inline") +
		" at " + e.sourceFile + ":" + e.lineNumber);
});`

// newPage opens a tab in a new browser context. The context records a
// video and a trace (see record) and, in presentation mode, draws the
// cursor.
func (h *harness) newPage(t *testing.T, inst *instance, o pageOpts) *page {
	t.Helper()
	videos := t.TempDir()
	opts := playwright.BrowserNewContextOptions{
		BaseURL:     new(inst.url),
		Locale:      new("en-US"),
		Permissions: o.permissions,
		Viewport:    h.look.viewport(),
		RecordVideo: &playwright.RecordVideo{Dir: new(videos), Size: h.look.viewport()},
	}
	if o.timezone != "" {
		opts.TimezoneId = new(o.timezone)
	}
	bctx, err := h.browser.NewContext(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bctx.Close() })
	if h.look.pace {
		if err := bctx.AddInitScript(playwright.Script{Content: new(cursorScript)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(o.cookies) > 0 {
		var cs []playwright.OptionalCookie
		for k, v := range o.cookies {
			cs = append(cs, playwright.OptionalCookie{Name: k, Value: v, URL: new(inst.url)})
		}
		if err := bctx.AddCookies(cs); err != nil {
			t.Fatal(err)
		}
	}
	if err := bctx.AddInitScript(playwright.Script{Content: new(recordCSP)}); err != nil {
		t.Fatal(err)
	}
	pg, err := bctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	h.record(t, bctx, pg)
	if h.look.records(t) && !h.gifTaken {
		h.gifTaken = true
		recordGIF(t, pg)
	}
	pg.SetDefaultTimeout(10000)
	p := &page{Page: pg, t: t, ctx: bctx, expect: playwright.NewPlaywrightAssertions(5000), pace: h.look.pace}
	pg.OnConsole(func(m playwright.ConsoleMessage) {
		if m.Type() == "error" {
			p.problem("console error: " + m.Text())
		}
	})
	pg.OnPageError(func(err error) { p.problem("page error: " + err.Error()) })
	pg.OnRequest(func(r playwright.Request) {
		hd := r.Headers()
		if r.Method() == "GET" && hd["hx-request"] == "true" && (hd["hx-trigger"] == "" || strings.HasSuffix(hd["hx-trigger"], "-poll")) {
			p.polls.Add(1)
		}
	})
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, s := range p.problems {
			t.Error(s)
		}
	})
	return p
}

func (p *page) problem(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, re := range p.allowed {
		if re.MatchString(s) {
			return
		}
	}
	p.problems = append(p.problems, s)
}

// allow lets console errors matching re through: ones a test causes on
// purpose.
func (p *page) allow(re string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowed = append(p.allowed, regexp.MustCompile(re))
}

// open navigates and fails the test on an error or a non-2xx answer.
func (p *page) open(path string) playwright.Response {
	p.t.Helper()
	resp, err := p.Goto(path)
	if err != nil {
		p.t.Fatalf("open %s: %v", path, err)
	}
	if resp != nil && !resp.Ok() {
		p.t.Fatalf("open %s: status %d", path, resp.Status())
	}
	return resp
}

// ok fails the test on err; use it with the expect assertions.
func (p *page) ok(err error, what string, args ...any) {
	p.t.Helper()
	if err != nil {
		p.t.Fatalf("%s: %v", fmt.Sprintf(what, args...), err)
	}
}

func (p *page) eval(js string, arg ...any) any {
	p.t.Helper()
	v, err := p.Evaluate(js, arg...)
	if err != nil {
		p.t.Fatalf("evaluate %q: %v", js, err)
	}
	return v
}

func (p *page) evalString(js string) string {
	p.t.Helper()
	s, _ := p.eval(js).(string)
	return s
}

func (p *page) evalBool(js string) bool {
	p.t.Helper()
	b, _ := p.eval(js).(bool)
	return b
}

// pollOnce fires the page's poll (the event app.js's timer sends) and
// waits until its answer has been swapped in: the poll element itself is
// the first out-of-band swap of every answer, and the rest follow in the
// same task.
func (p *page) pollOnce(pollID string) {
	p.t.Helper()
	p.eval(`(id) => new Promise((resolve, reject) => {
		const timer = setTimeout(() => reject(new Error("no poll answer for #" + id)), 8000);
		document.addEventListener("htmx:oobAfterSwap", function on(e) {
			if (e.detail.target && e.detail.target.id === id) {
				document.removeEventListener("htmx:oobAfterSwap", on);
				clearTimeout(timer);
				setTimeout(resolve, 50);
			}
		});
		htmx.trigger(document.getElementById(id), "asynqui-refresh");
	})`, pollID)
}

// activeID is the id of the focused element.
func (p *page) activeID() string {
	return p.evalString(`() => document.activeElement ? document.activeElement.id : ""`)
}

// eventually retries cond until it holds or 5s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// containing matches a string that contains s.
func containing(s string) *regexp.Regexp { return regexp.MustCompile(regexp.QuoteMeta(s)) }

// hexID is components.HexID: the id of a row's checkbox or link.
func hexID(prefix, s string) string { return prefix + hex.EncodeToString([]byte(s)) }

// connPath is the Connection page of c.
func connPath(c string) string { return "/c/" + c }

// queuePath is the Queue page of q on c, on one tab.
func queuePath(c, q, state string) string {
	return connPath(c) + "/queues/" + q + "?state=" + state
}

// click clicks loc. In presentation mode the pointer first moves onto
// it, so the drawn cursor gets there before the click.
func (p *page) click(loc playwright.Locator, what string) {
	p.t.Helper()
	if p.pace {
		p.ok(loc.Hover(), "hover %s", what)
	}
	p.ok(loc.Click(), "%s", what)
}

// check checks the checkbox loc, moving the pointer first as click does.
func (p *page) check(loc playwright.Locator, what string) {
	p.t.Helper()
	if p.pace {
		p.ok(loc.Hover(), "hover %s", what)
	}
	p.ok(loc.Check(), "%s", what)
}

// login signs in through the basic login form, starting at path.
func (p *page) login(path, user, password string) {
	p.t.Helper()
	p.open(path)
	p.ok(p.expect.Page(p.Page).ToHaveURL(containing("/login")), "redirect to login")
	p.ok(p.Locator("#login-username").Fill(user), "fill username")
	p.ok(p.Locator("#login-password").Fill(password), "fill password")
	p.ok(p.Locator("#login-password").Press("Enter"), "submit")
}
