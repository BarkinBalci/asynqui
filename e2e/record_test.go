//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	playwright "github.com/mxschmitt/playwright-go"
)

// resultsDir holds a video per page and, for a failed test, its trace:
// test-results/<Test>/<Subtest>.webm and <Subtest>.trace.zip.
const resultsDir = "test-results"

// viewport is the page and video size, Playwright's default.
func viewport() *playwright.Size { return &playwright.Size{Width: 1280, Height: 720} }

// resultName is the file name, without extension, for the next page of
// t: the subtest's name, with -2, -3, … for further pages.
func (h *harness) resultName(t *testing.T) string {
	name := t.Name()
	if i := strings.Index(name, "/"); i >= 0 {
		name = strings.ReplaceAll(name[i+1:], "/", "_")
	}
	base := name
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(h.results, name+".webm")); os.IsNotExist(err) {
			return name
		}
		name = base + "-" + strconv.Itoa(n)
	}
}

// record starts tracing on bctx and registers the cleanup that keeps
// the page's video, and the trace if t failed. It runs after every
// other cleanup of the page, so t.Failed() includes them.
func (h *harness) record(t *testing.T, bctx playwright.BrowserContext, pg playwright.Page) {
	t.Helper()
	name := h.resultName(t)
	video := filepath.Join(h.results, name+".webm")
	// Reserve the name until the video is saved.
	if err := os.WriteFile(video, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bctx.Tracing().Start(playwright.TracingStartOptions{
		Title:       new(t.Name()),
		Screenshots: new(true),
		Snapshots:   new(true),
		Sources:     new(true),
	}); err != nil {
		t.Fatalf("start tracing: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			trace := filepath.Join(h.results, name+".trace.zip")
			if err := bctx.Tracing().Stop(trace); err != nil {
				t.Logf("save trace: %v", err)
			} else {
				t.Logf("trace: %s", trace)
			}
		} else if err := bctx.Tracing().Stop(); err != nil {
			t.Logf("stop tracing: %v", err)
		}
		if err := bctx.Close(); err != nil {
			t.Logf("close browser context: %v", err)
		}
		v := pg.Video()
		if v == nil {
			return
		}
		if err := v.SaveAs(video); err != nil {
			t.Logf("save video: %v", err)
		}
		_ = v.Delete()
	})
}
