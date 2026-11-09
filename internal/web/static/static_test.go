package static

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbedded(t *testing.T) {
	a, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"app.css", "app.js", "logo.svg", "vendor/htmx.min.js", "vendor/htmx-LICENSE.txt",
		"fonts/inter-latin-400-normal.woff2", "fonts/Inter-OFL.txt", "fonts/JetBrainsMono-OFL.txt",
	} {
		p := a.Path(name)
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+p, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s (%s) = %d", name, p, rec.Code)
		}
	}
}

func TestHashedNamesAndCSSRewrite(t *testing.T) {
	fsys := fstest.MapFS{
		"app.css":         {Data: []byte(`@font-face{src:url(fonts/a.woff2) format("woff2")} .x{background:url('data:image/png;base64,AA')} .y{background:url("/abs.png")}`)},
		"fonts/a.woff2":   {Data: []byte("font-v1")},
		"vendor/x.min.js": {Data: []byte("js")},
	}
	a, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^fonts/a\.[0-9a-f]{12}\.woff2$`)
	font := a.Path("fonts/a.woff2")
	if !re.MatchString(font) {
		t.Fatalf("font path %q", font)
	}
	if got := a.Path("vendor/x.min.js"); !regexp.MustCompile(`^vendor/x\.min\.[0-9a-f]{12}\.js$`).MatchString(got) {
		t.Errorf("js path %q", got)
	}
	css := get(t, a, a.Path("app.css"))
	if !strings.Contains(css, "url("+font+")") {
		t.Errorf("css not rewritten: %s", css)
	}
	if !strings.Contains(css, `url('data:image/png;base64,AA')`) || !strings.Contains(css, `url("/abs.png")`) {
		t.Errorf("absolute urls changed: %s", css)
	}

	// A changed font changes the CSS name too.
	fsys["fonts/a.woff2"] = &fstest.MapFile{Data: []byte("font-v2")}
	b, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path("app.css") == b.Path("app.css") {
		t.Error("css hash did not change with the font")
	}
}

func TestCSSMissingReference(t *testing.T) {
	_, err := New(fstest.MapFS{"app.css": {Data: []byte(`.x{background:url(missing.png)}`)}})
	if err == nil || !strings.Contains(err.Error(), "missing.png") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeHeaders(t *testing.T) {
	a, err := New(fstest.MapFS{"x.js": {Data: []byte("1")}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+a.Path("x.js"), nil))
	if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("ETag") == "" {
		t.Errorf("headers = %v", rec.Header())
	}
	for _, p := range []string{"/x.js", "/nope", "/"} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
}

// TestDarkTokensMatch keeps the two dark-token blocks of app.css (explicit
// dark, and system under prefers-color-scheme: dark) identical, and checks
// both define every color token of the light block.
func TestDarkTokensMatch(t *testing.T) {
	b, err := fs.ReadFile(FS, "files/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	block := func(start string) string {
		i := strings.Index(css, start)
		if i < 0 {
			t.Fatalf("no %q block", start)
		}
		rest := css[i+len(start):]
		return rest[:strings.Index(rest, "}")]
	}
	normalize := func(s string) string { return strings.Join(strings.Fields(s), "") }
	dark := normalize(block(":root[data-theme=dark]{"))
	system := normalize(block("@media (prefers-color-scheme:dark){\n\t:root[data-theme=system]{"))
	if dark != system {
		t.Errorf("dark blocks differ:\n%s\n%s", dark, system)
	}
	vars := regexp.MustCompile(`(--[a-z0-9-]+):`)
	defined := map[string]bool{}
	for _, m := range vars.FindAllStringSubmatch(dark, -1) {
		defined[m[1]] = true
	}
	for _, m := range vars.FindAllStringSubmatch(block(":root{"), -1) {
		name := m[1]
		if strings.HasPrefix(name, "--font") || strings.HasPrefix(name, "--radius") || name == "--control-h" {
			continue // not colors
		}
		if !defined[name] {
			t.Errorf("dark theme lacks %s", name)
		}
	}
}

func get(t *testing.T, a *Assets, p string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+p, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", p, rec.Code)
	}
	return rec.Body.String()
}
