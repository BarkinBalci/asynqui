package static

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
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

func TestTreeHash(t *testing.T) {
	fsys := fstest.MapFS{
		"app.css":         {Data: []byte(`@font-face{src:url(fonts/a.woff2) format("woff2")}`)},
		"fonts/a.woff2":   {Data: []byte("font-v1")},
		"vendor/x.min.js": {Data: []byte("js")},
	}
	a, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	css := a.Path("app.css")
	if !regexp.MustCompile(`^[0-9a-f]{12}/app\.css$`).MatchString(css) {
		t.Fatalf("css path %q", css)
	}
	if font := a.Path("fonts/a.woff2"); path.Dir(path.Dir(font)) != path.Dir(css) {
		t.Errorf("font %q is not under the hash of %q", font, css)
	}
	// The stylesheet's relative font URL resolves as written.
	if got := get(t, a, a.Path("app.css")); !strings.Contains(got, "url(fonts/a.woff2)") {
		t.Errorf("css rewritten: %s", got)
	}
	if get(t, a, path.Dir(css)+"/fonts/a.woff2") != "font-v1" {
		t.Error("font not served next to the stylesheet")
	}

	// A changed font moves every path.
	fsys["fonts/a.woff2"] = &fstest.MapFile{Data: []byte("font-v2")}
	b, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path("app.css") == b.Path("app.css") {
		t.Error("css path did not change with the font")
	}
}

func TestServeHeaders(t *testing.T) {
	a, err := New(fstest.MapFS{"x.js": {Data: []byte("1")}, "d/y.txt": {Data: []byte("2")}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+a.Path("x.js"), nil))
	if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("ETag") == "" {
		t.Errorf("headers = %v", rec.Header())
	}
	hash := path.Dir(a.Path("x.js"))
	for _, p := range []string{"/x.js", "/nope", "/", "/" + hash, "/" + hash + "/", "/" + hash + "/d", "/" + hash + "/d/",
		"/" + hash + "/nope.js", "/000000000000/x.js", "/" + hash + "/../x.js"} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s = %d %q, want 404", p, rec.Code, rec.Header().Get("Cache-Control"))
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
