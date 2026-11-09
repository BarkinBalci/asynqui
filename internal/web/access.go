package web

import (
	"context"
	"net/http"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// access is what a route wrapper resolved for the request: who is asking
// and, for /c/{conn} routes, the connection and the user's role on it.
type access struct {
	id   auth.Identity
	conn Connection // zero without {conn}
	role authz.Role // RoleNone without {conn}
}

type accessKey struct{}

func accessFrom(ctx context.Context) access {
	a, _ := ctx.Value(accessKey{}).(access)
	return a
}

func subject(id auth.Identity) authz.Identity {
	return authz.Identity{Name: id.Name, Groups: id.Groups}
}

// view, op and admin guard a route with the role it needs on {conn}:
//
//   - no identity: authn Challenge (303 to login, 401 + HX-Redirect for
//     htmx, or 401 for proxy and none),
//   - unknown or hidden connection: 404,
//   - insufficient role: 403.
//
// Routes without {conn} (the Overview) only need an identity.
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
		a := access{id: id}
		if name := r.PathValue("conn"); name != "" {
			c, found := lookup(d.conns, name)
			role, visible := d.policy.RoleFor(subject(id), name)
			if !found || !visible {
				d.renderError(w, r, http.StatusNotFound, pages.ErrorNotFound, a)
				return
			}
			a.conn, a.role = c, role
			if role < need {
				d.renderError(w, r, http.StatusForbidden, pages.ErrorForbidden, a)
				return
			}
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, a)))
	})
}

// visibleConn is a connection the user can see, with their role on it.
type visibleConn struct {
	Connection
	role authz.Role
}

// visible returns the connections the user can see, in config order.
func (d *deps) visible(id auth.Identity) []visibleConn {
	all := d.conns.Connections()
	out := make([]visibleConn, 0, len(all))
	for _, c := range all {
		if role, ok := d.policy.RoleFor(subject(id), c.Name); ok {
			out = append(out, visibleConn{Connection: c, role: role})
		}
	}
	return out
}
