package web

import "github.com/BarkinBalci/asynqui/internal/web/views/components"

// sectionCrumb is the breadcrumb of a connection's section page.
func sectionCrumb(d *deps, s components.Section, segments ...string) components.Crumb {
	return components.Crumb{Section: s, URL: d.url(segments...)}
}
