//go:build e2e

package e2e

import (
	"bytes"
	"cmp"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// resultsDir holds a video per page and, for a failed test, its trace:
// test-results/<Test>/<Subtest>.webm and <Subtest>.trace.zip.
const resultsDir = "test-results"

// imagesDir holds the README's images.
const imagesDir = "../docs/images"

// gifOut is where the GIF of ASYNQUI_E2E_GIF's subtest is written.
const gifOut = imagesDir + "/demo.gif"

// slowMo is how long Playwright waits after each action, in
// milliseconds, so the videos can be followed. The cursor's glide must
// fit in it.
const slowMo = 400.0

// presentation is how a run looks, from the environment. It never
// changes what a test does or asserts.
type presentation struct {
	headed bool   // ASYNQUI_E2E_HEADED=1
	gif    string // ASYNQUI_E2E_GIF: the subtest whose frames become gifOut
	// screenshots is ASYNQUI_E2E_SCREENSHOTS=1: pages get the larger
	// viewport at shotScale and no drawn cursor, and the tests save the
	// README's screenshots (see screenshot).
	screenshots bool
}

func presentationFromEnv() presentation {
	return presentation{
		headed:      os.Getenv("ASYNQUI_E2E_HEADED") == "1",
		gif:         os.Getenv("ASYNQUI_E2E_GIF"),
		screenshots: os.Getenv("ASYNQUI_E2E_SCREENSHOTS") == "1",
	}
}

func (l presentation) launchOptions() playwright.BrowserTypeLaunchOptions {
	return playwright.BrowserTypeLaunchOptions{Headless: new(!l.headed), SlowMo: new(slowMo)}
}

// viewport is the page, video and GIF size, and with screenshots on
// the screenshots' size in CSS pixels.
func (l presentation) viewport() *playwright.Size {
	if l.screenshots {
		return &playwright.Size{Width: 1440, Height: 900}
	}
	return &playwright.Size{Width: 1200, Height: 750}
}

// records reports whether t is the subtest ASYNQUI_E2E_GIF names, by its
// full name or its last part.
func (l presentation) records(t *testing.T) bool {
	if l.gif == "" {
		return false
	}
	name := t.Name()
	return l.gif == name || l.gif == name[strings.LastIndex(name, "/")+1:]
}

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

// cursorScript draws an arrow that follows the mouse, since screenshots
// and videos show none. It glides to each new position, shows presses
// and keeps its place across page loads.
const cursorScript = `(() => {
	const key = "asynqui-e2e-cursor";
	let pos = {x: Math.round(innerWidth * 0.62), y: Math.round(innerHeight * 0.72)};
	try { pos = JSON.parse(sessionStorage.getItem(key)) || pos; } catch (e) {}
	const el = document.createElement("div");
	el.setAttribute("aria-hidden", "true");
	el.innerHTML = '<svg width="22" height="22" viewBox="0 0 22 22"><path d="M3 2 L3 18 L7.2 14.2 L10 20.5 L13 19.2 L10.3 13 L16 13 Z" fill="#111827" stroke="#FFFFFF" stroke-width="1.5" stroke-linejoin="round"/></svg>';
	Object.assign(el.style, {position: "fixed", left: "0", top: "0", zIndex: "2147483647", pointerEvents: "none", transformOrigin: "3px 2px"});
	let down = false, downAt = 0;
	const place = () => { el.style.transform = "translate(" + (pos.x - 3) + "px," + (pos.y - 2) + "px)" + (down ? " scale(0.8)" : ""); };
	place();
	const attach = () => {
		document.documentElement.appendChild(el);
		requestAnimationFrame(() => { el.style.transition = "transform 300ms cubic-bezier(.3,.1,.2,1)"; });
	};
	if (document.readyState === "loading") { document.addEventListener("DOMContentLoaded", attach); } else { attach(); }
	window.addEventListener("mousemove", (e) => {
		pos = {x: e.clientX, y: e.clientY};
		place();
		try { sessionStorage.setItem(key, JSON.stringify(pos)); } catch (err) {}
	}, true);
	window.addEventListener("mousedown", () => { down = true; downAt = Date.now(); place(); }, true);
	window.addEventListener("mouseup", () => {
		setTimeout(() => { down = false; place(); }, Math.max(0, 180 - (Date.now() - downAt)));
	}, true);
})();`

// ---- GIF ----

// gifFrame is the GIF's frame interval.
const gifFrame = 110 * time.Millisecond

// gifReady is true once the page shows data: the frames before it are
// dropped.
const gifReady = `() => location.protocol.startsWith("http") && document.readyState === "complete" &&
	document.fonts.status === "loaded" && !document.querySelector(".checking, .conn-checking, .dot-unknown")`

type frame struct {
	img image.Image
	at  time.Time
}

type recording struct {
	pg     playwright.Page
	done   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	frames []frame
}

// recordGIF takes a screenshot of pg every gifFrame, from the first one
// that shows data until t's cleanup, and writes them to gifOut if t
// passed. Register it after the page's other cleanups that need the
// page, since it runs before them.
func recordGIF(t *testing.T, pg playwright.Page) {
	t.Helper()
	r := &recording{pg: pg, done: make(chan struct{})}
	r.wg.Go(func() {
		ready := false
		for {
			at := time.Now()
			if !ready {
				v, err := pg.Evaluate(gifReady)
				ready = err == nil && v == true
			}
			if ready {
				// Caret "initial" leaves the page alone: hiding the caret
				// sets a style attribute on inputs, which htmx copies when
				// it settles a swap and the CSP then blocks.
				b, err := pg.Screenshot(playwright.PageScreenshotOptions{
					Type:  playwright.ScreenshotTypePng,
					Caret: playwright.ScreenshotCaretInitial,
				})
				if err == nil {
					if img, err := png.Decode(bytes.NewReader(b)); err == nil {
						r.mu.Lock()
						r.frames = append(r.frames, frame{img: img, at: at})
						r.mu.Unlock()
					}
				}
			}
			select {
			case <-r.done:
				return
			case <-time.After(time.Until(at.Add(gifFrame))):
			}
		}
	})
	t.Cleanup(func() {
		close(r.done)
		r.wg.Wait()
		if t.Failed() {
			t.Logf("%s not written: the test failed", gifOut)
			return
		}
		if len(r.frames) < 2 {
			t.Errorf("GIF: recorded %d frames", len(r.frames))
			return
		}
		if err := writeGIF(gifOut, r.frames); err != nil {
			t.Errorf("write %s: %v", gifOut, err)
			return
		}
		if st, err := os.Stat(gifOut); err == nil {
			t.Logf("wrote %s: %d frames, %d KB", gifOut, len(r.frames), st.Size()>>10)
		}
	})
}

// GIF holds: the first frame shows at least gifFirst, the last gifLast.
const (
	gifFirst = 100 // 1/100 s
	gifLast  = 250
)

// transparent is the palette index of "unchanged since the last frame".
const transparent = 255

// writeGIF encodes frames with one palette of the most frequent colors,
// nearest-color mapping without dithering, and each frame cut to the
// box that changed, with unchanged pixels inside it transparent.
func writeGIF(out string, frames []frame) error {
	pal := buildPalette(frames)
	b := frames[0].img.Bounds()
	nearest := newMatcher(pal[:transparent])
	prev := image.NewPaletted(b, pal)
	g := &gif.GIF{Config: image.Config{ColorModel: pal, Width: b.Dx(), Height: b.Dy()}}
	cur := image.NewPaletted(b, pal)
	for i, f := range frames {
		nearest.mapImage(cur, f.img)
		delay := gifLast
		if i+1 < len(frames) {
			delay = int(frames[i+1].at.Sub(f.at) / (10 * time.Millisecond))
		}
		if i == 0 {
			g.Image = append(g.Image, clonePaletted(cur))
			g.Delay = append(g.Delay, max(delay, gifFirst))
			g.Disposal = append(g.Disposal, gif.DisposalNone)
			copy(prev.Pix, cur.Pix)
			continue
		}
		box, changed := diffBox(prev, cur)
		if !changed {
			g.Delay[len(g.Delay)-1] += delay
			continue
		}
		sub := image.NewPaletted(box, pal)
		for y := box.Min.Y; y < box.Max.Y; y++ {
			for x := box.Min.X; x < box.Max.X; x++ {
				o := cur.PixOffset(x, y)
				if cur.Pix[o] == prev.Pix[o] {
					sub.Pix[sub.PixOffset(x, y)] = transparent
				} else {
					sub.Pix[sub.PixOffset(x, y)] = cur.Pix[o]
				}
			}
		}
		g.Image = append(g.Image, sub)
		g.Delay = append(g.Delay, delay)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
		copy(prev.Pix, cur.Pix)
	}
	g.Delay[len(g.Delay)-1] = max(g.Delay[len(g.Delay)-1], gifLast)
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	fh, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := gif.EncodeAll(fh, g); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

func clonePaletted(p *image.Paletted) *image.Paletted {
	c := image.NewPaletted(p.Rect, p.Palette)
	copy(c.Pix, p.Pix)
	return c
}

// diffBox is the bounding box of the pixels that differ.
func diffBox(a, b *image.Paletted) (image.Rectangle, bool) {
	r := b.Rect
	minX, minY, maxX, maxY := r.Max.X, r.Max.Y, r.Min.X-1, r.Min.Y-1
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := b.PixOffset(r.Min.X, y)
		for x := 0; x < r.Dx(); x++ {
			if a.Pix[row+x] != b.Pix[row+x] {
				minX, maxX = min(minX, r.Min.X+x), max(maxX, r.Min.X+x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
		}
	}
	if maxX < minX {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

// frequent is how many palette entries go to the most frequent colors.
const frequent = 200

// buildPalette counts colors over all frames in coarse buckets, averaged
// within each. It takes the most common buckets, fills the rest with
// common buckets far from those, and adds the transparent entry.
func buildPalette(frames []frame) color.Palette {
	type bucket struct {
		n       int
		r, g, b int
	}
	counts := map[uint32]*bucket{}
	for _, f := range frames {
		rgba := toRGBA(f.img)
		for i := 0; i < len(rgba.Pix); i += 4 {
			r, g, b := int(rgba.Pix[i]), int(rgba.Pix[i+1]), int(rgba.Pix[i+2])
			k := uint32(r>>2)<<12 | uint32(g>>2)<<6 | uint32(b>>2)
			bk := counts[k]
			if bk == nil {
				bk = &bucket{}
				counts[k] = bk
			}
			bk.n++
			bk.r += r
			bk.g += g
			bk.b += b
		}
	}
	all := make([]*bucket, 0, len(counts))
	for _, bk := range counts {
		all = append(all, bk)
	}
	slices.SortFunc(all, func(x, y *bucket) int {
		return cmp.Or(cmp.Compare(y.n, x.n), cmp.Compare(x.r, y.r), cmp.Compare(x.g, y.g), cmp.Compare(x.b, y.b))
	})
	avg := func(bk *bucket) color.RGBA {
		return color.RGBA{uint8(bk.r / bk.n), uint8(bk.g / bk.n), uint8(bk.b / bk.n), 255}
	}
	pal := make(color.Palette, 0, 256)
	for _, bk := range all[:min(len(all), frequent)] {
		pal = append(pal, avg(bk))
	}
	// The rest go to small areas of a color far from every entry so far,
	// such as status dots.
	rest := all[min(len(all), frequent):]
	for len(pal) < transparent && len(rest) > 0 {
		best, bestScore := 0, -1.0
		for i, bk := range rest {
			c := avg(bk)
			d := math.MaxInt
			for _, q := range pal {
				d = min(d, distance(c, q.(color.RGBA)))
			}
			if score := float64(bk.n) * math.Sqrt(float64(d)); score > bestScore {
				best, bestScore = i, score
			}
		}
		pal = append(pal, avg(rest[best]))
		rest = slices.Delete(rest, best, best+1)
	}
	for len(pal) < transparent {
		pal = append(pal, color.RGBA{0, 0, 0, 255})
	}
	return append(pal, color.RGBA{})
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	r := image.NewRGBA(img.Bounds())
	draw.Draw(r, r.Rect, img, img.Bounds().Min, draw.Src)
	return r
}

// matcher maps colors to the nearest palette entry, with a cache.
type matcher struct {
	pal   []color.RGBA
	cache map[uint32]uint8
}

func newMatcher(p color.Palette) *matcher {
	m := &matcher{cache: map[uint32]uint8{}}
	for _, c := range p {
		m.pal = append(m.pal, c.(color.RGBA))
	}
	return m
}

func (m *matcher) index(r, g, b uint8) uint8 {
	k := uint32(r)<<16 | uint32(g)<<8 | uint32(b)
	if i, ok := m.cache[k]; ok {
		return i
	}
	best, bestD := 0, math.MaxInt
	for i, c := range m.pal {
		if d := distance(color.RGBA{r, g, b, 255}, c); d < bestD {
			best, bestD = i, d
		}
	}
	m.cache[k] = uint8(best)
	return uint8(best)
}

func (m *matcher) mapImage(dst *image.Paletted, src image.Image) {
	rgba := toRGBA(src)
	for i, j := 0, 0; i < len(rgba.Pix); i, j = i+4, j+1 {
		dst.Pix[j] = m.index(rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2])
	}
}

// distance is a squared color distance, weighted toward green as the eye
// is.
func distance(a, b color.RGBA) int {
	dr, dg, db := int(a.R)-int(b.R), int(a.G)-int(b.G), int(a.B)-int(b.B)
	return 3*dr*dr + 4*dg*dg + 2*db*db
}

// ---- Screenshots ----

// shotScale is the screenshots' device scale factor.
const shotScale = 2.0

// shotReady is gifReady with no htmx request in flight.
const shotReady = `() => (` + gifReady + `)() && !document.querySelector(".htmx-request")`

// screenshot saves p as imagesDir/<name>-<theme>.png, theme being the
// page's data-theme, once it has settled. It does nothing without
// ASYNQUI_E2E_SCREENSHOTS.
func (h *harness) screenshot(p *page, name string) {
	p.t.Helper()
	if !h.look.screenshots {
		return
	}
	p.settle()
	theme, err := p.Locator("html").GetAttribute("data-theme")
	p.ok(err, "data-theme")
	b, err := p.Screenshot(playwright.PageScreenshotOptions{
		Type:       playwright.ScreenshotTypePng,
		Caret:      playwright.ScreenshotCaretInitial,
		Animations: playwright.ScreenshotAnimationsDisabled,
	})
	p.ok(err, "screenshot")
	img, err := png.Decode(bytes.NewReader(b))
	p.ok(err, "decode screenshot")
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	p.ok(enc.Encode(&out, img), "encode screenshot")
	file := filepath.Join(imagesDir, name+"-"+theme+".png")
	p.ok(os.WriteFile(file, out.Bytes(), 0o600), "write %s", file)
	p.t.Logf("wrote %s: %d KB", file, out.Len()>>10)
}

// settle moves the pointer off the content and waits until the page
// shows its data with no request in flight. A page still checking is
// refreshed every 2s.
func (p *page) settle() {
	p.t.Helper()
	vp := p.ViewportSize()
	p.ok(p.Mouse().Move(float64(vp.Width-1), float64(vp.Height-1)), "move the pointer away")
	deadline := time.Now().Add(15 * time.Second)
	refresh := time.Now().Add(2 * time.Second)
	for !p.evalBool(shotReady) {
		now := time.Now()
		if now.After(deadline) {
			p.t.Fatalf("%s did not settle", p.URL())
		}
		if now.After(refresh) {
			p.eval(`() => document.querySelectorAll("[data-poll]").forEach((el) => htmx.trigger(el, "asynqui-refresh"))`)
			refresh = now.Add(2 * time.Second)
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.eval(`() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))`)
}
