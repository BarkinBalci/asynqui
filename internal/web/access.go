package web

import (
	"context"
	"net/http"
	"slices"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// access is what a route wrapper resolved for the request: who is asking,
// the connections they can see and, for /c/{conn} routes, the connection
// and their role on it. Health is read once, here, so one answer never
// mixes two health checks.
type access struct {
	id      auth.Identity
	visible []visibleConn // nil when no wrapper ran
	conn    Conn          // nil without {conn}
	health  conn.Health   // conn's
	role    authz.Role    // RoleNone without {conn}
}

type accessKey struct{}

func accessFrom(ctx context.Context) access {
	a, _ := ctx.Value(accessKey{}).(access)
	return a
}

func subject(id auth.Identity) authz.Identity {
	return authz.Identity{Name: id.Name, Groups: id.Groups}
}

// view, op and admin guard a route with the role it needs on {conn} (see
// docs/architecture.md, "Routes"). Routes without {conn} (the Overview) only
// need an identity.
func view(d *deps, h http.Handler) http.Handler  { return d.require(authz.RoleViewer, h) }
func op(d *deps, h http.Handler) http.Handler    { return d.require(authz.RoleOperator, h) }
func admin(d *deps, h http.Handler) http.Handler { return d.require(authz.RoleAdmin, h) }

func (d *deps) require(need authz.Role, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFrom(r.Context())
		if !ok {
			d.auth.Challenge(w, r)
			return
		}
		a := access{id: id, visible: d.visible(id)}
		if name := r.PathValue("conn"); name != "" {
			i := slices.IndexFunc(a.visible, func(c visibleConn) bool { return c.Name() == name })
			if i < 0 { // unknown, or hidden from the user
				d.renderError(w, r, http.StatusNotFound, pages.ErrorNotFound)
				return
			}
			c := a.visible[i]
			a.conn, a.health, a.role = c.Conn, c.health, c.role
			if a.role < need {
				d.renderError(w, r, http.StatusForbidden, pages.ErrorForbidden)
				return
			}
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, a)))
	})
}

// visibleConn is a connection the user can see, with their role on it and
// its health when the request read it.
type visibleConn struct {
	Conn
	role   authz.Role
	health conn.Health
}

// visible returns the connections the user can see, in config order.
func (d *deps) visible(id auth.Identity) []visibleConn {
	all := d.conns.Connections()
	out := make([]visibleConn, 0, len(all))
	for _, c := range all {
		if role, ok := d.policy.RoleFor(subject(id), c.Name()); ok {
			out = append(out, visibleConn{Conn: c, role: role, health: c.Health()})
		}
	}
	return out
}

// visibleOf is the request's visible connections, read now when no route
// wrapper read them (the catch-all's error page).
func (d *deps) visibleOf(a access) []visibleConn {
	if a.visible == nil {
		return d.visible(a.id)
	}
	return a.visible
}
