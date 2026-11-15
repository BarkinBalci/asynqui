// Package static embeds the CSS, JavaScript, fonts and images served under
// /static/, and serves them under a content hash of the whole tree so they
// can be cached forever.
package static

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// FS holds the embedded static assets. Without "all:", files whose names
// start with "." or "_" are never embedded.
//
//go:embed files
var FS embed.FS

// hashLen is the number of hex digits of the SHA-256 content hash in every
// asset path.
const hashLen = 12

// Assets serves a file tree under "<hash>/", hash being a content hash of
// every file, so responses are cacheable forever and a stylesheet's relative
// url(...) references resolve as they are. Build it with New; it is immutable
// and safe for concurrent use.
type Assets struct {
	names map[string]bool // every file, by name
	hash  string
	files http.Handler
}

// New hashes every file of fsys, names and contents.
func New(fsys fs.FS) (*Assets, error) {
	h := sha256.New()
	names := map[string]bool{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		names[name] = true
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		h.Write([]byte(name + "\x00" + strconv.Itoa(len(body)) + "\x00"))
		h.Write(body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("static: %w", err)
	}
	hash := hex.EncodeToString(h.Sum(nil))[:hashLen]
	return &Assets{names: names, hash: hash, files: http.StripPrefix("/"+hash, http.FileServerFS(fsys))}, nil
}

// Embedded returns the Assets of the embedded files directory.
func Embedded() (*Assets, error) {
	sub, err := fs.Sub(FS, "files")
	if err != nil {
		return nil, fmt.Errorf("static: %w", err)
	}
	return New(sub)
}

// Path returns a file's path under the content hash ("app.css" →
// "3f9a2c1b7d0e/app.css"). It panics on an unknown name, a programming error
// that any test rendering a page catches.
func (a *Assets) Path(name string) string {
	if !a.names[name] {
		panic(fmt.Sprintf("static: unknown asset %q", name))
	}
	return a.hash + "/" + name
}

// ServeHTTP serves "/<hash>/<name>", relative to the handler's mount point
// (strip the /static/ prefix before it). Another hash, a directory or an
// unknown name is 404, so there are no directory listings.
func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/"+a.hash+"/")
	if !ok || !a.names[name] {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", `"`+a.hash+`"`)
	// Not left to the system MIME table, which may differ.
	switch path.Ext(name) {
	case ".css":
		h.Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		h.Set("Content-Type", "text/javascript; charset=utf-8")
	case ".svg":
		h.Set("Content-Type", "image/svg+xml")
	case ".txt":
		h.Set("Content-Type", "text/plain; charset=utf-8")
	case ".woff2":
		h.Set("Content-Type", "font/woff2")
	}
	a.files.ServeHTTP(w, r)
}
