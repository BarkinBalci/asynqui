package web

import (
	"net/http"

	"github.com/BarkinBalci/asynqui/internal/web/format"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// Connection pages beyond the Overview arrive in later milestones. Until
// then their routes exist (so links, breadcrumbs and authorization work) and
// render a placeholder inside the layout.

// stubPage describes a placeholder page: its sidebar section and the
// breadcrumbs after "Overview / {conn}".
type stubPage struct {
	section components.Section
	// crumbs builds the trailing breadcrumbs from the request's path values.
	crumbs func(d *deps, r *http.Request) []components.Crumb
}

func handleStub(d *deps, p stubPage) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		crumbs := []components.Crumb{{Text: a.conn.Name, URL: d.url("c", a.conn.Name)}}
		if p.crumbs != nil {
			crumbs = append(crumbs, p.crumbs(d, r)...)
		}
		d.render(w, r, http.StatusOK, pages.NotImplementedPage(pages.NotImplemented{
			Layout: d.layout(r, a, p.section, crumbs),
		}))
	})
}

// handleActionStub answers the not yet implemented action routes. They
// already sit behind their op/admin wrapper, so authorization is enforced.
func handleActionStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainError(w, http.StatusNotImplemented)
	})
}

func sectionCrumb(d *deps, s components.Section, segments ...string) components.Crumb {
	return components.Crumb{Section: s, URL: d.url(segments...)}
}

func queueCrumbs(d *deps, r *http.Request) []components.Crumb {
	c, q := r.PathValue("conn"), r.PathValue("queue")
	return []components.Crumb{{Text: q, URL: d.url("c", c, "queues", q)}}
}

func taskCrumbs(d *deps, r *http.Request) []components.Crumb {
	id := r.PathValue("id")
	return append(queueCrumbs(d, r), components.Crumb{Text: format.ShortID(id)})
}

func serversCrumbs(d *deps, r *http.Request) []components.Crumb {
	return []components.Crumb{sectionCrumb(d, components.SectionServers, "c", r.PathValue("conn"), "servers")}
}

func serverCrumbs(d *deps, r *http.Request) []components.Crumb {
	return append(serversCrumbs(d, r), components.Crumb{Text: format.ShortID(r.PathValue("id"))})
}

func schedulersCrumbs(d *deps, r *http.Request) []components.Crumb {
	return []components.Crumb{sectionCrumb(d, components.SectionSchedulers, "c", r.PathValue("conn"), "schedulers")}
}

func schedulerCrumbs(d *deps, r *http.Request) []components.Crumb {
	return append(schedulersCrumbs(d, r), components.Crumb{Text: format.ShortID(r.PathValue("id"))})
}

func infoCrumbs(d *deps, r *http.Request) []components.Crumb {
	return []components.Crumb{sectionCrumb(d, components.SectionInfo, "c", r.PathValue("conn"), "info")}
}
