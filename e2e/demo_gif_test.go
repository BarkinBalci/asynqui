//go:build demo

package e2e

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"math"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// GIF geometry and pace.
const (
	gifWidth  = 1200
	gifHeight = 750
	gifFrame  = 110 * time.Millisecond // about 9 fps
)

// cursorScript draws an arrow that follows the mouse, since headless
// screenshots show none. It keeps its place across page loads.
const cursorScript = `(() => {
	const key = "asynqui-demo-cursor";
	let pos = {x: -40, y: -40};
	try { pos = JSON.parse(sessionStorage.getItem(key)) || pos; } catch (e) {}
	const el = document.createElement("div");
	el.setAttribute("aria-hidden", "true");
	el.innerHTML = '<svg width="22" height="22" viewBox="0 0 22 22"><path d="M3 2 L3 18 L7.2 14.2 L10 20.5 L13 19.2 L10.3 13 L16 13 Z" fill="#111827" stroke="#FFFFFF" stroke-width="1.5" stroke-linejoin="round"/></svg>';
	Object.assign(el.style, {position: "fixed", left: "0", top: "0", zIndex: "2147483647", pointerEvents: "none", transformOrigin: "3px 2px"});
	let down = false;
	const place = () => { el.style.transform = "translate(" + (pos.x - 3) + "px," + (pos.y - 2) + "px)" + (down ? " scale(0.85)" : ""); };
	place();
	const attach = () => document.documentElement.appendChild(el);
	if (document.readyState === "loading") { document.addEventListener("DOMContentLoaded", attach); } else { attach(); }
	window.addEventListener("mousemove", (e) => {
		pos = {x: e.clientX, y: e.clientY};
		place();
		try { sessionStorage.setItem(key, JSON.stringify(pos)); } catch (err) {}
	}, true);
	window.addEventListener("mousedown", () => { down = true; place(); }, true);
	window.addEventListener("mouseup", () => { down = false; place(); }, true);
})();`

// recordDemo records Overview → payments → the webhooks queue → Retry
// tab → select three tasks → Run, and writes it as a GIF.
func recordDemo(t *testing.T, browser playwright.Browser, baseURL string, d demo, out string) {
	t.Helper()
	bctx, pg := newDemoPage(t, browser, baseURL, "light", gifWidth, gifHeight, 1)
	if err := bctx.AddInitScript(playwright.Script{Content: new(cursorScript)}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Goto("/"); err != nil {
		t.Fatal(err)
	}
	settle(t, pg)
	c := &cursor{t: t, pg: pg, x: gifWidth * 0.62, y: gifHeight * 0.72}
	if err := pg.Mouse().Move(c.x, c.y); err != nil {
		t.Fatal(err)
	}

	rec := startRecording(t, pg)
	hold := func(d time.Duration) { time.Sleep(d) }
	hold(800 * time.Millisecond)
	c.click("#open-" + demoPayments)
	waitData(t, pg, "#queues-rows")
	hold(900 * time.Millisecond)
	c.click("#qlink-" + hex.EncodeToString([]byte(d.queue)))
	waitData(t, pg, "#tasks-rows")
	hold(400 * time.Millisecond)
	c.click("#tab-retry")
	if err := pg.Locator("#tab-retry.on").WaitFor(); err != nil {
		t.Fatalf("retry tab: %v", err)
	}
	hold(500 * time.Millisecond)
	for i := range 3 {
		c.click("#tasks-rows tr:nth-child(" + strconv.Itoa(i+1) + ") input.cb")
		hold(150 * time.Millisecond)
	}
	hold(200 * time.Millisecond)
	c.click(`.tbar button:text-is("Run now")`)
	if err := pg.Locator(".notice").WaitFor(); err != nil {
		t.Fatalf("run notice: %v", err)
	}
	c.move(c.x+140, c.y+90)
	hold(1500 * time.Millisecond)
	frames := rec.stop()
	_ = bctx.Close()

	if err := writeGIF(out, frames); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s: %d frames, %d KB", out, len(frames), st.Size()>>10)
}

// waitData waits for sel to be on the page and for no region to be
// checking.
func waitData(t *testing.T, pg playwright.Page, sel string) {
	t.Helper()
	if err := pg.Locator(sel).WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateAttached}); err != nil {
		t.Fatalf("waiting for %s: %v", sel, err)
	}
	if _, err := pg.WaitForFunction(`() => document.fonts.status === "loaded" && !document.querySelector(".checking, .conn-checking")`, nil); err != nil {
		t.Fatalf("waiting for data: %v", err)
	}
}

// cursor glides the mouse at a human pace.
type cursor struct {
	t    *testing.T
	pg   playwright.Page
	x, y float64
}

func (c *cursor) move(x, y float64) {
	c.t.Helper()
	const steps = 12
	x0, y0 := c.x, c.y
	for i := 1; i <= steps; i++ {
		f := float64(i) / steps
		f = f * f * (3 - 2*f)
		if err := c.pg.Mouse().Move(x0+(x-x0)*f, y0+(y-y0)*f); err != nil {
			c.t.Fatal(err)
		}
		time.Sleep(22 * time.Millisecond)
	}
	c.x, c.y = x, y
}

func (c *cursor) click(sel string) {
	c.t.Helper()
	loc := c.pg.Locator(sel).First()
	if err := loc.ScrollIntoViewIfNeeded(); err != nil {
		c.t.Fatalf("scroll to %s: %v", sel, err)
	}
	box, err := loc.BoundingBox()
	if err != nil || box == nil {
		c.t.Fatalf("locate %s: %v", sel, err)
	}
	c.move(box.X+box.Width/2, box.Y+box.Height/2)
	time.Sleep(100 * time.Millisecond)
	if err := c.pg.Mouse().Down(); err != nil {
		c.t.Fatal(err)
	}
	time.Sleep(90 * time.Millisecond)
	if err := c.pg.Mouse().Up(); err != nil {
		c.t.Fatal(err)
	}
}

// ---- recording ----

type frame struct {
	img image.Image
	at  time.Time
}

type recording struct {
	t      *testing.T
	pg     playwright.Page
	done   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	frames []frame
}

// startRecording takes a screenshot every gifFrame until stop.
func startRecording(t *testing.T, pg playwright.Page) *recording {
	r := &recording{t: t, pg: pg, done: make(chan struct{})}
	r.wg.Go(func() {
		for {
			at := time.Now()
			b, err := pg.Screenshot(playwright.PageScreenshotOptions{Type: playwright.ScreenshotTypePng})
			if err == nil {
				if img, err := png.Decode(bytes.NewReader(b)); err == nil {
					r.mu.Lock()
					r.frames = append(r.frames, frame{img: img, at: at})
					r.mu.Unlock()
				}
			}
			select {
			case <-r.done:
				return
			case <-time.After(time.Until(at.Add(gifFrame))):
			}
		}
	})
	return r
}

func (r *recording) stop() []frame {
	close(r.done)
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.frames) < 2 {
		r.t.Fatalf("recorded %d frames", len(r.frames))
	}
	return r.frames
}

// ---- GIF encoding ----

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
		delay := 200
		if i+1 < len(frames) {
			delay = int(frames[i+1].at.Sub(f.at) / (10 * time.Millisecond))
		}
		if i == 0 {
			g.Image = append(g.Image, clonePaletted(cur))
			g.Delay = append(g.Delay, delay)
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
	g.Delay[len(g.Delay)-1] = max(g.Delay[len(g.Delay)-1], 200)
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

// buildPalette counts colors over all frames in buckets of 6 bits per
// channel, averaged within each. It takes the frequent most common
// buckets, fills up to 255 with common buckets far from those, and adds
// the transparent entry.
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
