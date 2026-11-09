package web

import (
	"context"
	"net/http"
	"sync"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// handleOverview serves GET /: one card per visible connection. With
// HX-Request it renders only the polled cards region.
func handleOverview(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := pages.Overview{
			Cards:   d.cards(r.Context(), d.visible(a.id)),
			PollURL: d.url(),
		}
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.OverviewFragment(m, d.now()))
			return
		}
		m.Layout = d.layout(r, a, components.SectionOverview, nil)
		d.render(w, r, http.StatusOK, pages.OverviewPage(m))
	})
}

// handleRecheck serves POST /c/{conn}/recheck ("Retry now"): it runs a
// health check now and re-renders the card (htmx), or redirects to the
// Overview without JS.
func handleRecheck(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		c := a.conn
		c.Health = d.conns.Recheck(r.Context(), c.Name)
		if !isFragment(r) {
			http.Redirect(w, r, d.url(), http.StatusSeeOther)
			return
		}
		cards := d.cards(r.Context(), []visibleConn{{Connection: c, role: a.role}})
		d.render(w, r, http.StatusOK, components.ConnCardView(cards[0]))
	})
}

// cards builds the Overview cards, fetching the summaries of reachable
// connections in parallel so one slow connection does not hold up the
// others longer than its own timeouts.
func (d *deps) cards(ctx context.Context, conns []visibleConn) []components.ConnCard {
	out := make([]components.ConnCard, len(conns))
	var wg sync.WaitGroup
	for i, c := range conns {
		out[i] = components.ConnCard{
			Name:       c.Name,
			Addr:       c.Addr,
			Role:       roleView(c.role),
			Health:     healthView(c.Health.State),
			OpenURL:    d.url("c", c.Name),
			RecheckURL: d.url("c", c.Name, "recheck"),
		}
		switch c.Health.State {
		case HealthDown:
			out[i].Error = errText(c.Health.Err)
		case HealthUp:
			wg.Go(func() {
				s, err := d.conns.Summary(ctx, c.Name)
				card := &out[i]
				if err != nil {
					// Reachable at the last PING but failing now: show
					// it as failed with this error until the next check.
					card.Health = components.HealthDown
					card.Error = errText(err)
					return
				}
				card.Queues = int64(s.Queues)
				card.Pending = int64(s.Pending)
				card.Active = int64(s.Active)
				card.Retry = int64(s.Retry)
				card.Processed = int64(s.ProcessedToday)
				card.Failed = int64(s.FailedToday)
				card.OldestPending = s.OldestPending
			})
		}
	}
	wg.Wait()
	return out
}

// errText is the error shown on a failed card; the Redis error text is
// data, not copy.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
