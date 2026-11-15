package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// The flash cookie carries the failures of an action posted without JS across
// its redirect, and is read once. It is unsigned: it holds display data only,
// bounded and validated, so a forged one can only show a toast.
const (
	flashCookie   = "asynqui_flash"
	flashMaxAge   = 60
	flashMaxItems = 20
	flashMaxBytes = 3000 // encoded, well under the 4 KB cookie limit
	flashMaxText  = 300  // per error text
	// flashMaxCount bounds a notice's count.
	flashMaxCount = 1 << 40
)

// flashItem is one failure, or the result notice of a task action, with
// short keys to keep the cookie small. A queue action failure has Q; a
// task item has A set, and is the notice when N is set, else a failure
// on task T (or on every task of a state, T empty; of aggregation group
// Q when Q is set).
type flashItem struct {
	V int    `json:"v"`
	K int    `json:"k"`
	Q string `json:"q,omitempty"`
	D string `json:"d,omitempty"`
	A bool   `json:"a,omitempty"`
	T string `json:"t,omitempty"`
	N *int64 `json:"n,omitempty"`
}

// setFlash stores f for the connection's next page view. With nothing
// in f it does nothing. Items that do not fit the size bound are dropped,
// failures first from the end; the notice is kept.
func (d *deps) setFlash(w http.ResponseWriter, conn string, f pages.Flash) {
	var items []flashItem
	if f.Notice != nil {
		n := f.Notice.Count
		items = append(items, flashItem{V: int(f.Notice.Verb), A: true, N: &n})
	}
	for _, x := range f.Failures {
		items = append(items, flashItem{V: int(x.Verb), K: int(x.Kind), Q: x.Queue, D: clip(x.Detail)})
	}
	for _, x := range f.TaskFailures {
		k := 0
		if x.NotFound {
			k = 1
		}
		items = append(items, flashItem{V: int(x.Verb), K: k, A: true, T: x.ID, Q: x.Group, D: clip(x.Detail)})
	}
	items = items[:min(len(items), flashMaxItems)]
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

// clip cuts detail to at most flashMaxText bytes, at a rune boundary.
func clip(detail string) string {
	if len(detail) <= flashMaxText {
		return detail
	}
	cut := flashMaxText
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut]
}

// takeFlash returns what is stored for the connection, if anything, and
// clears the cookie. A malformed cookie is cleared and ignored.
func (d *deps) takeFlash(w http.ResponseWriter, r *http.Request, conn string) (pages.Flash, bool) {
	var out pages.Flash
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return out, false
	}
	http.SetCookie(w, d.flash(conn, "", -1))
	if len(c.Value) > flashMaxBytes {
		return out, false
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return out, false
	}
	var items []flashItem
	if err := json.Unmarshal(b, &items); err != nil || len(items) == 0 || len(items) > flashMaxItems {
		return out, false
	}
	for _, it := range items {
		if it.A {
			verb := pages.TaskVerb(it.V)
			if verb < pages.TaskRun || verb > pages.TaskCancel || it.K < 0 || it.K > 1 {
				return pages.Flash{}, false
			}
			if it.N != nil {
				out.Notice = &pages.TaskNotice{Verb: verb, Count: min(max(*it.N, 0), flashMaxCount)}
				continue
			}
			out.TaskFailures = append(out.TaskFailures, pages.TaskFailure{Verb: verb, ID: it.T, Group: it.Q, NotFound: it.K == 1, Detail: it.D})
			continue
		}
		verb, kind := pages.QueueVerb(it.V), pages.FailureKind(it.K)
		if it.Q == "" || verb < pages.VerbPause || verb > pages.VerbDelete || kind < pages.FailureOther || kind > pages.FailureNotEmpty {
			return pages.Flash{}, false
		}
		out.Failures = append(out.Failures, pages.ActionFailure{Verb: verb, Kind: kind, Queue: it.Q, Detail: it.D})
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
