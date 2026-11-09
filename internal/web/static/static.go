// Package static embeds the CSS, JavaScript, fonts and images served under
// /static/, and serves them under content-hashed names so they can be
// cached forever.
package static

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// FS holds the embedded static assets. Without "all:", files whose names
// start with "." or "_" are never embedded.
//
//go:embed files
var FS embed.FS

// hashLen is the number of hex digits of the SHA-256 content hash put into
// each served file name.
const hashLen = 12

// Assets serves a file tree under content-hashed names: "app.css" is served
// as "app.<hash>.css". Hashed names never change meaning, so responses are
// cacheable forever. Build it with New; it is immutable and safe for
// concurrent use.
type Assets struct {
	byName   map[string]string // logical name → hashed name
	byHashed map[string]asset  // hashed name → content
}

type asset struct {
	body        []byte
	contentType string
	hash        string
}

// New reads every file of fsys. Stylesheets are rewritten so that their
// url(...) references to other files point at the hashed names, and are
// hashed after the rewrite, so a changed font also changes the CSS name.
func New(fsys fs.FS) (*Assets, error) {
	a := &Assets{byName: map[string]string{}, byHashed: map[string]asset{}}
	// cssURL matches url(...) references in stylesheets.
	cssURL := regexp.MustCompile(`url\(\s*['"]?([^'")\s]+)['"]?\s*\)`)
	var css []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path.Ext(name) == ".css" {
			css = append(css, name) // after everything they may reference
			return nil
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		a.add(name, body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("static: %w", err)
	}
	for _, name := range css {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("static: %w", err)
		}
		body, err = a.rewriteCSS(cssURL, name, body)
		if err != nil {
			return nil, err
		}
		a.add(name, body)
	}
	return a, nil
}

// Embedded returns the Assets of the embedded files directory.
func Embedded() (*Assets, error) {
	sub, err := fs.Sub(FS, "files")
	if err != nil {
		return nil, fmt.Errorf("static: %w", err)
	}
	return New(sub)
}

func (a *Assets) add(name string, body []byte) {
	sum := sha256.Sum256(body)
	ext := path.Ext(name)
	hash := hex.EncodeToString(sum[:])[:hashLen]
	hashed := strings.TrimSuffix(name, ext) + "." + hash + ext
	ct := mime.TypeByExtension(ext)
	switch ext { // not in every system MIME table
	case ".woff2":
		ct = "font/woff2"
	case ".js":
		ct = "text/javascript; charset=utf-8"
	case ".css":
		ct = "text/css; charset=utf-8"
	case ".svg":
		ct = "image/svg+xml"
	case ".txt":
		ct = "text/plain; charset=utf-8"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	a.byName[name] = hashed
	a.byHashed[hashed] = asset{body: body, contentType: ct, hash: hash}
}

// rewriteCSS replaces each relative url(...) in a stylesheet with the
// hashed name of the file it references. A reference to a missing file is
// an error, so a typo fails at startup rather than as a 404.
func (a *Assets) rewriteCSS(cssURL *regexp.Regexp, name string, body []byte) ([]byte, error) {
	dir := path.Dir(name)
	var missing []string
	out := cssURL.ReplaceAllFunc(body, func(m []byte) []byte {
		ref := string(cssURL.FindSubmatch(m)[1])
		if strings.Contains(ref, ":") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "#") {
			return m // absolute, data: or fragment: leave alone
		}
		target := path.Join(dir, ref)
		hashed, ok := a.byName[target]
		if !ok {
			missing = append(missing, ref)
			return m
		}
		rel := strings.TrimPrefix(hashed, dir+"/")
		if dir == "." {
			rel = hashed
		}
		return bytes.Join([][]byte{[]byte("url("), []byte(rel), []byte(")")}, nil)
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("static: %s references missing files %q", name, missing)
	}
	return out, nil
}

// Path returns the hashed name of a file ("app.css" → "app.3f9a2c1b7d0e.css").
// It panics on an unknown name: asset names are fixed at build time, so a
// miss is a programming error caught by any test that renders a page.
func (a *Assets) Path(name string) string {
	hashed, ok := a.byName[name]
	if !ok {
		panic(fmt.Sprintf("static: unknown asset %q", name))
	}
	return hashed
}

// ServeHTTP serves a hashed name relative to the handler's mount point
// (strip the /static/ prefix before it). Unknown names, including unhashed
// ones, are 404.
func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, ok := a.byHashed[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", `"`+f.hash+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
}
