package static

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func appCSS(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(FS, "files/app.css")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// tokens returns the custom properties of the light (:root) and explicit
// dark blocks.
func tokens(t *testing.T, css string) (light, dark map[string]string) {
	t.Helper()
	block := func(start string) map[string]string {
		i := strings.Index(css, start)
		if i < 0 {
			t.Fatalf("no %q block", start)
		}
		rest := css[i+len(start):]
		rest = rest[:strings.Index(rest, "}")]
		out := map[string]string{}
		for _, m := range regexp.MustCompile(`(--[a-z0-9-]+):([^;]+);`).FindAllStringSubmatch(rest, -1) {
			out[m[1]] = strings.TrimSpace(m[2])
		}
		return out
	}
	light = block(":root{")
	dark = block(":root[data-theme=dark]{")
	for k, v := range light {
		if _, ok := dark[k]; !ok {
			dark[k] = v // shared (non-color) tokens
		}
	}
	return light, dark
}

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		t.Fatalf("not a #rrggbb color: %q", hex)
	}
	var c [3]float64
	for i := range c {
		v, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		x := float64(v) / 255
		if x <= 0.04045 {
			c[i] = x / 12.92
		} else {
			c[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

func contrast(t *testing.T, a, b string) float64 {
	la, lb := luminance(t, a), luminance(t, b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// TestStatusDotContrast: status dots are non-text indicators, so they need
// 3:1 against every background they sit on (WCAG 1.4.11).
func TestStatusDotContrast(t *testing.T) {
	light, dark := tokens(t, appCSS(t))
	for theme, tok := range map[string]map[string]string{"light": light, "dark": dark} {
		for _, dot := range []string{"--dot-up", "--dot-down", "--dot-unknown"} {
			for _, bg := range []string{"--sidebar-bg", "--sidebar-current", "--hover"} {
				if c := contrast(t, tok[dot], tok[bg]); c < 3 {
					t.Errorf("%s %s %s on %s %s = %.2f:1, want ≥ 3", theme, dot, tok[dot], bg, tok[bg], c)
				}
			}
		}
	}
}

func TestCSSFindings(t *testing.T) {
	css := appCSS(t)
	light, dark := tokens(t, css)
	tests := []struct {
		name string
		ok   bool
	}{
		{"hover inside the current connection has its own token",
			light["--sidebar-current-hover"] != light["--sidebar-current"] && dark["--sidebar-current-hover"] != dark["--sidebar-current"] &&
				strings.Contains(css, ".conn-current .nav a:hover{background:var(--sidebar-current-hover)}")},
		{"focus rings inset inside clipping containers",
			strings.Contains(css, ".seg button:focus-visible,.conn-list a:focus-visible,.nav a:focus-visible{outline-offset:-2px}")},
		{"breadcrumb list is flex",
			strings.Contains(css, ".crumbs ol{display:flex;")},
		{"breadcrumb separator has empty alt text",
			strings.Contains(css, `content:"/" / ""`)},
		{"dark alert text is #FCA5A5", dark["--alert-fg"] == "#FCA5A5" && strings.Contains(css, "color:var(--alert-fg)")},
		{"dark nav hover text is #FFFFFF", dark["--nav-hover-fg"] == "#FFFFFF" && strings.Contains(css, "color:var(--nav-hover-fg)")},
		{"button gap is 6px", strings.Contains(css, ".btn{display:inline-flex;align-items:center;justify-content:center;gap:6px;")},
		{"skip link is shown on focus", strings.Contains(css, ".skip-link:focus{top:8px}")},
		{"hidden beats display rules", strings.Contains(css, "[hidden]{display:none !important}")},
	}
	for _, tt := range tests {
		if !tt.ok {
			t.Error(tt.name)
		}
	}
}
