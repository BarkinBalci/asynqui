package web

import (
	"context"
	"net/http"
	"sync"

	"github.com/BarkinBalci/asynqui/internal/conn"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// OverviewSource is what the Overview needs from a connection.
type OverviewSource interface {
	// Summary returns the Overview numbers. It may be served from a
	// short-lived cache.
	Summary(ctx context.Context) (conn.Summary, error)
	// Recheck runs a health check now and stores it.
	Recheck(ctx context.Context) (conn.Health, error)
}

// handleOverview serves GET /: one card per visible connection. With
// HX-Request (a poll) it answers with out-of-band swaps only.
func handleOverview(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := d.overview(r, accessFrom(r.Context()))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.OverviewPoll(m))
			return
		}
		d.render(w, r, http.StatusOK, pages.OverviewPage(m))
	})
}

// handleRecheck serves POST /c/{conn}/recheck ("Retry now"): it runs a
// health check now and re-renders the card, with the same out-of-band
// updates as a poll (htmx), or redirects to the Overview without JS.
func handleRecheck(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		_, _ = a.conn.Recheck(r.Context()) // the cards read the stored health
		if !isFragment(r) {
			navigate(w, r, d.url())
			return
		}
		// Re-read after the check so every card, the badge and the sidebar
		// agree. The page has no current connection: it is the Overview.
		m := d.overview(r, access{id: a.id})
		for _, c := range m.Cards {
			if c.Name == a.conn.Name() {
				d.render(w, r, http.StatusOK, pages.RecheckResult(m, c))
				return
			}
		}
		plainError(w, http.StatusNotFound) // vanished from config: cannot happen at runtime
	})
}

// overview builds the Overview model: the cards, and a layout whose sidebar
// shows the health the cards found.
func (d *deps) overview(r *http.Request, a access) pages.Overview {
	cards := d.cards(r.Context(), d.visibleOf(a))
	health := make(map[string]components.Health, len(cards))
	for _, c := range cards {
		health[c.Name] = c.Health
	}
	return pages.Overview{
		Layout:  d.layoutWith(r, a, components.SectionOverview, nil, health),
		Cards:   cards,
		PollURL: d.url(),
	}
}

// cards builds the Overview cards, fetching the summaries of reachable
// connections in parallel within the page budget. A card whose summary misses
// it shows "Checking".
func (d *deps) cards(ctx context.Context, conns []visibleConn) []components.ConnCard {
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()
	out := make([]components.ConnCard, len(conns))
	var wg sync.WaitGroup
	for i, c := range conns {
		name, health := c.Name(), c.health
		out[i] = components.ConnCard{
			Name:       name,
			Addr:       c.Addr(),
			Role:       roleView(c.role),
			Health:     healthView(health.State),
			OpenURL:    d.url("c", name),
			RecheckURL: d.url("c", name, "recheck"),
		}
		switch health.State {
		case conn.StateDown:
			out[i].Error = errText(health.Err)
		case conn.StateUp:
			wg.Go(func() {
				s, err := c.Summary(ctx)
				card := &out[i]
				switch {
				case err != nil && ctx.Err() != nil:
					// Out of budget (or the viewer left): not known yet.
					card.Health = components.HealthUnknown
				case err != nil:
					// Reachable at the last PING but failing now: show it
					// as failed with this error, here and in the sidebar.
					card.Health = components.HealthDown
					card.Error = errText(err)
				default:
					card.Queues = int64(s.Queues)
					card.Pending = int64(s.Pending)
					card.Active = int64(s.Active)
					card.Retry = int64(s.Retry)
					card.Processed = int64(s.ProcessedToday)
					card.Failed = int64(s.FailedToday)
					card.OldestPending = s.OldestPending
				}
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
