package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// The flash cookie carries the failures of an action posted without JS
// across its redirect (post, redirect, get), so the page can show them as
// toasts and a reload never posts again. It lives for a minute, is scoped
// to the connection's page and is read once. It holds display data only
// (queue names and error text, which templ escapes), so it is not signed:
// a forged one can only show a toast.
const (
	flashCookie   = "asynqui_flash"
	flashMaxAge   = 60
	flashMaxItems = 20
	flashMaxBytes = 3000 // encoded, well under the 4 KB cookie limit
	flashMaxText  = 300  // per error text
)

// flashItem is one failure, with short keys to keep the cookie small.
type flashItem struct {
	V int    `json:"v"`
	K int    `json:"k"`
	Q string `json:"q"`
	D string `json:"d,omitempty"`
}

// setFlash stores failures for the connection's next page view. With no
// failures it does nothing. Items that do not fit the size bound are
// dropped.
func (d *deps) setFlash(w http.ResponseWriter, conn string, failures []pages.ActionFailure) {
	items := make([]flashItem, 0, min(len(failures), flashMaxItems))
	for _, f := range failures[:min(len(failures), flashMaxItems)] {
		detail := f.Detail
		if len(detail) > flashMaxText {
			detail = detail[:flashMaxText]
		}
		items = append(items, flashItem{V: int(f.Verb), K: int(f.Kind), Q: f.Queue, D: detail})
	}
	for len(items) > 0 {
		b, err := json.Marshal(items)
		if err != nil {
			return
		}
		if v := base64.RawURLEncoding.EncodeToString(b); len(v) <= flashMaxBytes {
			http.SetCookie(w, d.flash(conn, v, flashMaxAge))
			return
		}
		items = items[:len(items)-1]
	}
}

// takeFlash returns the failures stored for the connection, if any, and
// clears the cookie. A malformed cookie is cleared and ignored.
func (d *deps) takeFlash(w http.ResponseWriter, r *http.Request, conn string) ([]pages.ActionFailure, bool) {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil, false
	}
	http.SetCookie(w, d.flash(conn, "", -1))
	if len(c.Value) > flashMaxBytes {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, false
	}
	var items []flashItem
	if err := json.Unmarshal(b, &items); err != nil || len(items) == 0 || len(items) > flashMaxItems {
		return nil, false
	}
	out := make([]pages.ActionFailure, 0, len(items))
	for _, it := range items {
		verb, kind := pages.QueueVerb(it.V), pages.FailureKind(it.K)
		if it.Q == "" || verb < pages.VerbPause || verb > pages.VerbDelete || kind < pages.FailureOther || kind > pages.FailureNotEmpty {
			return nil, false
		}
		out = append(out, pages.ActionFailure{Verb: verb, Kind: kind, Queue: it.Q, Detail: it.D})
	}
	return out, true
}

func (d *deps) flash(conn, value string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // G124: Secure follows cookie_secure (default true).
		Name:     flashCookie,
		Value:    value,
		Path:     d.url("c", conn),
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   d.secureCookies,
		SameSite: http.SameSiteLaxMode,
	}
}
