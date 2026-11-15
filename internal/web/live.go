package web

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// The connection pages share one request and polling scheme, pages.Live: a
// hidden poll element, out-of-band answers, and a banner when a poll finds no
// data while the page shows some.

// pollInit is the poll URL's ?poll= value for a page without data
// regions: the answer then renders every region.
const pollInit = "init"

// answer is what a Live page request is answered with.
type answer struct {
	// poll is a poll: the page's controls are left alone.
	poll bool
	// init is a poll from a page without data regions.
	init bool
}

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
	// unreachable marks a failure to reach the server: the connection is
	// known down, or the read failed with a network error or timeout. An
	// error reply (NOPERM, LOADING) is shown inline instead.
	unreachable bool
	// down marks a connection reachable at the last PING whose read
	// could not reach it now: the sidebar shows it down, as the Overview
	// does.
	down bool
	// cause is the read's error, if any.
	cause error
}

// pageContext bounds a Live page's reads by the page budget. Pages with
// two reads share it.
func (d *deps) pageContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.pageBudget)
}

// readLive runs fetch under ctx (see pageContext), unless the connection is
// known to be down. A read that misses the budget is "Checking".
func readLive[T any](ctx context.Context, a access, fetch func(ctx context.Context) (T, error)) (T, liveRead) {
	var zero T
	if a.health.State == conn.StateDown {
		return zero, liveRead{state: pages.DataFailed, err: errText(a.health.Err), unreachable: true}
	}
	v, err := fetch(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		return zero, liveRead{state: pages.DataChecking, cause: err}
	case err != nil:
		down := conn.Unreachable(err)
		return zero, liveRead{state: pages.DataFailed, err: errText(err), unreachable: down, down: down, cause: err}
	}
	return v, liveRead{state: pages.DataReady}
}

// readyIfMissing reports whether the read found that its item does not
// exist (its error is one of targets), and then rewrites rd as a ready
// read: that is an answer, not a failure, and the page shows "not found".
func readyIfMissing(rd *liveRead, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(rd.cause, t) {
			*rd = liveRead{state: pages.DataReady}
			return true
		}
	}
	return false
}

// live builds a Live page's frame. regions reports whether the page has
// its data regions (it read its data, and a detail page found its item);
// without them the poll asks for every region (?poll=init). q is the
// page's state in its URL query (without defaults); path are the page's
// path segments.
func (d *deps) live(ans answer, pageID, pollID string, rd liveRead, regions bool, q url.Values, path ...string) pages.Live {
	l := pages.Live{
		PageID: pageID,
		PollID: pollID,
		State:  rd.state,
		Error:  rd.err,
		Down:   rd.unreachable,
		Poll:   ans.poll,
		Init:   ans.init,
		Keep:   ans.poll && !ans.init && rd.state != pages.DataReady,
	}
	pq := maps.Clone(q)
	if pq == nil {
		pq = url.Values{}
	}
	if !regions && !l.Keep {
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
		health = map[string]components.Health{a.conn.Name(): components.HealthDown}
	}
	all := append([]components.Crumb{{Text: a.conn.Name(), URL: d.url("c", a.conn.Name())}}, crumbs...)
	return d.layoutWith(r, a, section, all, health)
}

// sectionCrumb is the breadcrumb of a connection's section page.
func sectionCrumb(d *deps, s components.Section, segments ...string) components.Crumb {
	return components.Crumb{Section: s, URL: d.url(segments...)}
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
