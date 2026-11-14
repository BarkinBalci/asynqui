package web

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/format"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// ServerSource is what the Servers pages need from a connection.
type ServerSource interface {
	// Servers lists the connection's asynq servers. It may be served from
	// a short-lived cache.
	Servers(ctx context.Context) ([]conn.Server, error)
}

// longRunning is the running time the workers table highlights.
const longRunning = 10 * time.Minute

// serversSort is the servers table's sort: every column but Queues, by
// Busy descending by default; names and states sort ascending first,
// numbers and times descending.
func serversSort() sortSpec {
	return sortSpec{
		cols:    []string{"host", "pid", "state", "busy", "started"},
		def:     int(pages.SrvColBusy),
		defDesc: true,
		firstDesc: func(c int) bool {
			return c != int(pages.SrvColHost) && c != int(pages.SrvColState)
		},
	}
}

// workersSort is the active workers table's sort: Started or Running for,
// Running for descending (longest first) by default.
func workersSort() sortSpec {
	return sortSpec{
		cols:      []string{"started", "running"},
		def:       int(pages.WrkColRunning),
		defDesc:   true,
		firstDesc: func(int) bool { return true },
	}
}

// handleServers serves GET /c/{conn}/servers. With HX-Request (a poll or
// a sort) it answers with out-of-band swaps (pages.ServersPoll).
func handleServers(d *deps) http.Handler {
	spec := serversSort()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.serversModel(r, a, spec, liveAnswer(r, pages.ServersPollID))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.ServersPoll(m))
			return
		}
		d.render(w, r, http.StatusOK, pages.ServersPage(m))
	})
}

func (d *deps) serversModel(r *http.Request, a access, spec sortSpec, ans answer) pages.Servers {
	name := a.conn.Name()
	st := spec.parse(r.URL.Query())
	q := url.Values{}
	spec.set(q, st)
	ctx, cancel := d.pageContext(r.Context())
	defer cancel()
	servers, rd := readLive(ctx, a, a.conn.Servers)
	m := pages.Servers{
		Layout: d.liveLayout(r, a, components.SectionServers, rd, sectionCrumb(d, components.SectionServers, "c", name, "servers")),
		Live:   d.live(ans, pages.ServersPageID, pages.ServersPollID, rd, rd.state == pages.DataReady, q, "c", name, "servers"),
	}
	sorted := slices.Clone(servers)
	slices.SortStableFunc(sorted, func(x, y conn.Server) int { return compareServers(x, y, pages.ServerColumn(st.col), st.desc) })
	for _, s := range sorted {
		m.Rows = append(m.Rows, d.serverRow(name, s))
	}
	heads := spec.heads(st, url.Values{}, func(c int) bool { return c == int(pages.SrvColPID) },
		func(q url.Values) string { return d.urlQuery(q, "c", name, "servers") })
	copy(m.Heads[:], heads)
	return m
}

func (d *deps) serverRow(name string, s conn.Server) pages.ServerRow {
	r := pages.ServerRow{
		ID:          s.ID,
		Host:        s.Host,
		URL:         d.url("c", name, "servers", s.ID),
		LinkID:      components.HexID("slink-", s.ID),
		PID:         int64(s.PID),
		Busy:        int64(len(s.Workers)),
		Concurrency: int64(s.Concurrency),
		Started:     s.Started,
	}
	switch s.Status {
	case "active":
		r.Status = pages.StatusActive
	case "stopped":
		r.Status = pages.StatusStopped
	default:
		r.Status, r.StatusText = pages.StatusOther, s.Status
	}
	for _, q := range s.Queues {
		r.Queues = append(r.Queues, pages.QueueToken{Name: q.Name, Weight: int64(q.Weight)})
	}
	return r
}

// compareServers orders two servers by column c, ties by host, then ID.
// Busy compares the share of busy workers, then their count.
func compareServers(x, y conn.Server, c pages.ServerColumn, desc bool) int {
	var r int
	switch c {
	case pages.SrvColHost:
		r = strings.Compare(x.Host, y.Host)
	case pages.SrvColPID:
		r = cmp.Compare(x.PID, y.PID)
	case pages.SrvColState:
		r = strings.Compare(x.Status, y.Status)
	case pages.SrvColBusy:
		r = cmp.Or(cmp.Compare(busyShare(x), busyShare(y)), cmp.Compare(len(x.Workers), len(y.Workers)))
	case pages.SrvColStarted:
		r = x.Started.Compare(y.Started)
	}
	if desc {
		r = -r
	}
	return cmp.Or(r, strings.Compare(x.Host, y.Host), strings.Compare(x.ID, y.ID))
}

func busyShare(s conn.Server) float64 {
	if s.Concurrency <= 0 {
		return 0
	}
	return float64(len(s.Workers)) / float64(s.Concurrency)
}

// handleServer serves GET /c/{conn}/servers/{id}, the Server detail page.
// A server that is gone shows "Server not found" (404 for the page itself;
// a poll gets the notice out of band).
func handleServer(d *deps) http.Handler {
	spec := workersSort()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.serverModel(r, a, spec, liveAnswer(r, pages.ServerPollID))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.ServerPoll(m))
			return
		}
		status := http.StatusOK
		if m.Live.State == pages.DataReady && !m.Found {
			status = http.StatusNotFound
		}
		d.render(w, r, status, pages.ServerPage(m))
	})
}

func (d *deps) serverModel(r *http.Request, a access, spec sortSpec, ans answer) pages.Server {
	name, id := a.conn.Name(), r.PathValue("id")
	st := spec.parse(r.URL.Query())
	q := url.Values{}
	spec.set(q, st)
	ctx, cancel := d.pageContext(r.Context())
	defer cancel()
	servers, rd := readLive(ctx, a, a.conn.Servers)
	i := slices.IndexFunc(servers, func(s conn.Server) bool { return s.ID == id })

	crumb := components.Crumb{Text: format.ShortID(id), Mono: true, Title: id}
	if i >= 0 && servers[i].Host != "" {
		crumb.Text = servers[i].Host
	}
	ready := rd.state == pages.DataReady
	m := pages.Server{
		Layout: d.liveLayout(r, a, components.SectionServers, rd,
			sectionCrumb(d, components.SectionServers, "c", name, "servers"), crumb),
		Live:       d.live(ans, pages.ServerPageID, pages.ServerPollID, rd, ready && i >= 0, q, "c", name, "servers", id),
		ServersURL: d.url("c", name, "servers"),
		Found:      i >= 0,
		// The page has no heading or regions to update yet, or the
		// server just went away.
		Whole: ans.init || !ready || i < 0,
	}
	if i < 0 {
		return m
	}
	s := servers[i]
	m.Row, m.Strict = d.serverRow(name, s), s.StrictPriority
	now := d.now()
	workers := slices.Clone(s.Workers)
	slices.SortStableFunc(workers, func(x, y conn.Worker) int {
		// Running for is the reverse of Started.
		r := x.Started.Compare(y.Started)
		if pages.WorkerColumn(st.col) == pages.WrkColRunning {
			r = -r
		}
		if st.desc {
			r = -r
		}
		return cmp.Or(r, strings.Compare(x.TaskID, y.TaskID))
	})
	for _, wk := range workers {
		running := max(now.Sub(wk.Started), 0)
		m.Workers = append(m.Workers, pages.WorkerRow{
			TaskID:      wk.TaskID,
			ShortID:     format.ShortID(wk.TaskID),
			TaskURL:     d.url("c", name, "queues", wk.Queue, "tasks", wk.TaskID),
			LinkID:      components.HexID("wtask-", wk.TaskID),
			Type:        wk.TaskType,
			Queue:       wk.Queue,
			QueueURL:    d.url("c", name, "queues", wk.Queue),
			QueueLinkID: components.HexID("wqueue-", wk.TaskID),
			Started:     wk.Started,
			Running:     running,
			Long:        running >= longRunning,
		})
	}
	heads := spec.heads(st, url.Values{}, func(int) bool { return false },
		func(q url.Values) string { return d.urlQuery(q, "c", name, "servers", id) })
	copy(m.Heads[:], heads)
	return m
}
