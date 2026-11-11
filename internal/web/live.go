package web

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"

	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// The read-only connection pages (Servers, Server detail, Schedulers,
// Scheduler entry, Redis info) share one request and polling scheme,
// pages.Live: a hidden poll element, out-of-band answers, and a banner
// when a poll finds no data while the page shows some.

// liveAnswer tells a poll of the page whose poll element is pollID
// (htmx names it in HX-Trigger) from a user request.
func liveAnswer(r *http.Request, pollID string) answer {
	if !isFragment(r) || r.Header.Get("HX-Trigger") != pollID {
		return answer{}
	}
	return answer{poll: true, init: r.URL.Query().Get("poll") == pollInit}
}

// liveRead is the outcome of a Live page's read.
type liveRead struct {
	state pages.DataState
	err   string
	// down marks a connection reachable at the last PING whose read
	// failed now: the sidebar shows it down, as the Overview does.
	down bool
}

// readLive runs fetch within the page budget, unless the connection is
// known to be down. A read that misses the budget is "Checking"; it goes
// on in conn's cache, so the next poll usually has it.
func readLive[T any](d *deps, r *http.Request, a access, fetch func(ctx context.Context) (T, error)) (T, liveRead) {
	var zero T
	if a.conn.Health.State == HealthDown {
		return zero, liveRead{state: pages.DataFailed, err: errText(a.conn.Health.Err)}
	}
	ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
	defer cancel()
	v, err := fetch(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		return zero, liveRead{state: pages.DataChecking}
	case err != nil:
		return zero, liveRead{state: pages.DataFailed, err: errText(err), down: true}
	}
	return v, liveRead{state: pages.DataReady}
}

// live builds a Live page's frame. q is the page's state in its URL query
// (without defaults); path are the page's path segments.
func (d *deps) live(ans answer, pageID, pollID string, rd liveRead, q url.Values, path ...string) pages.Live {
	l := pages.Live{
		PageID: pageID,
		PollID: pollID,
		State:  rd.state,
		Error:  rd.err,
		Poll:   ans.poll,
		Init:   ans.init,
		Keep:   ans.poll && !ans.init && rd.state != pages.DataReady,
	}
	pq := maps.Clone(q)
	if pq == nil {
		pq = url.Values{}
	}
	if rd.state != pages.DataReady && !l.Keep {
		pq.Set("poll", pollInit) // the page will have no data regions
	}
	l.PollURL = d.urlQuery(pq, path...)
	return l
}

// liveLayout is the shell of a Live page; a connection that failed now
// shows down in the sidebar too.
func (d *deps) liveLayout(r *http.Request, a access, section components.Section, rd liveRead, crumbs ...components.Crumb) views.Layout {
	var health map[string]components.Health
	if rd.down {
		health = map[string]components.Health{a.conn.Name: components.HealthDown}
	}
	all := append([]components.Crumb{{Text: a.conn.Name, URL: d.url("c", a.conn.Name)}}, crumbs...)
	return d.layoutWith(r, a, section, all, d.visible(a.id), health)
}

// sortSpec describes a table's ?sort=&dir= state: its sortable columns'
// parameter values in header order, the default sort, and the direction
// a column takes when first picked.
type sortSpec struct {
	cols      []string
	def       int
	defDesc   bool
	firstDesc func(col int) bool
}

// sortState is a table's sort: a column index into sortSpec.cols.
type sortState struct {
	col  int
	desc bool
}

// parse reads the sort from a query; unknown values fall back to the
// default.
func (s sortSpec) parse(q url.Values) sortState {
	st := sortState{col: s.def, desc: s.defDesc}
	if i := slices.Index(s.cols, q.Get("sort")); i >= 0 {
		st = sortState{col: i, desc: s.firstDesc(i)}
	}
	switch q.Get("dir") {
	case "asc":
		st.desc = false
	case "desc":
		st.desc = true
	}
	return st
}

// set writes st into q, leaving the default out.
func (s sortSpec) set(q url.Values, st sortState) {
	if st.col == s.def && st.desc == s.defDesc {
		q.Del("sort")
		q.Del("dir")
		return
	}
	q.Set("sort", s.cols[st.col])
	q.Set("dir", dirParam(st.desc))
}

// heads builds the sort header of each column. link is the page URL with
// a query; q is the rest of the page's state, kept by every link.
func (s sortSpec) heads(st sortState, q url.Values, num func(col int) bool, link func(url.Values) string) []components.SortHead {
	out := make([]components.SortHead, len(s.cols))
	for i, c := range s.cols {
		next := sortState{col: i, desc: s.firstDesc(i)}
		h := components.SortHead{ID: "sort-" + c, Num: num(i)}
		if i == st.col {
			h.Dir = sortDir(st.desc)
			next.desc = !st.desc
		}
		nq := maps.Clone(q)
		if nq == nil {
			nq = url.Values{}
		}
		s.set(nq, next)
		h.URL = link(nq)
		out[i] = h
	}
	return out
}
