//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// axeFile is axe-core, vendored (testdata/axe-core-4.14.0, MPL-2.0) so
// the tests never fetch a script at run time. It is evaluated through the
// DevTools protocol, which the page's CSP does not apply to.
const axeFile = "testdata/axe-core-4.14.0/axe.min.js"

// a11yPages are the pages the accessibility and CSP checks visit.
func a11yPages(h *harness) []struct{ name, path string } {
	return []struct{ name, path string }{
		{"Overview", "/"},
		{"Connection", connPath},
		{"Queue", queuePath(qSched, "scheduled")},
		{"Queue (active)", queuePath(qWork, "active")},
		{"Task", h.seed.taskURL},
		{"Task (archived)", h.seed.archivedURL},
		{"Servers", connPath + "/servers"},
		{"Schedulers", connPath + "/schedulers"},
		{"Info", connPath + "/info"},
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

// Every page, in the light and the dark theme, has no serious or critical
// axe-core violation (WCAG 2.2 A/AA rules and best practices). Lesser
// ones are logged.
func testAccessibility(t *testing.T, h *harness) {
	src, err := os.ReadFile(axeFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"light", "dark"} {
		p := h.newPage(t, h.open, pageOpts{cookies: map[string]string{"asynqui_theme": theme}})
		for _, pg := range a11yPages(h) {
			p.open(pg.path)
			time.Sleep(200 * time.Millisecond) // app.js renders times, reveals copy buttons
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
				if v.Impact == "serious" || v.Impact == "critical" {
					t.Error(msg)
					for _, n := range v.Nodes {
						t.Logf("    %s", n.HTML)
					}
				} else {
					t.Log(msg)
				}
			}
		}
	}
}

// No page logs a CSP violation (or any console error), including after
// app.js has polled, opened a dialog and swapped content. The page
// helper fails the test on every console error at cleanup.
func testNoCSPViolations(t *testing.T, h *harness) {
	p := h.newPage(t, h.open, pageOpts{cookies: map[string]string{"asynqui_refresh": "2s"}})
	paths := []string{
		"/c/" + connName + "/queues/" + qFind + "/find?id=nope", // the no-JS Find task page
		"/c/" + connName + "/servers/" + firstServerID(t, h),
		"/c/" + connName + "/schedulers/" + firstEntryID(t, h),
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
	p.open(queuePath(qFind, "pending"))
	p.ok(p.Keyboard().Press("/"), "open Find task")
	p.ok(p.Keyboard().Press("Escape"), "close it")
	p.ok(p.Locator(`button[name=theme][value=dark]`).Click(), "dark theme")
	p.open(connPath)
	p.ok(p.Locator("#"+hexID("qsel-", qEmpty)).Check(), "select")
	p.ok(p.Locator("#act-delete").Click(), "Delete")
	p.ok(p.expect.Locator(p.Locator("#queues-delete")).ToBeVisible(), "dialog")
	p.ok(p.Locator("#queues-delete-cancel").Click(), "cancel")

	// The login page, on the basic instance.
	lp := h.newPage(t, h.basic, pageOpts{})
	lp.open("/login")
	lp.ok(lp.expect.Locator(lp.Locator("#login-username")).ToBeFocused(), "username autofocus")
}

func firstServerID(t *testing.T, h *harness) string {
	t.Helper()
	srvs, err := h.insp.Servers()
	if err != nil || len(srvs) == 0 {
		t.Fatalf("servers: %v (%d)", err, len(srvs))
	}
	return srvs[0].ID
}

func firstEntryID(t *testing.T, h *harness) string {
	t.Helper()
	es, err := h.insp.SchedulerEntries()
	if err != nil || len(es) == 0 {
		t.Fatalf("scheduler entries: %v (%d)", err, len(es))
	}
	return es[0].ID
}
