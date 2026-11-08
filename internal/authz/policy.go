package authz

import (
	"errors"
	"fmt"
	"path"
	"slices"
)

// Identity is what the policy needs to know about a user. Name is the
// username matched against Binding.Users after FoldUsername; Groups are
// matched exactly against Binding.Groups.
type Identity struct {
	Name   string
	Groups []string
}

// Binding grants Role on every connection matching one of the Connections
// globs (path.Match syntax) to the listed users and to members of the listed
// groups.
type Binding struct {
	Users       []string
	Groups      []string
	Role        Role
	Connections []string
}

// Policy resolves per-connection roles. It is immutable and safe for
// concurrent use.
type Policy struct {
	def      Role
	bindings []binding
}

type binding struct {
	users  map[string]struct{}
	groups map[string]struct{}
	role   Role
	conns  []string
}

// New builds a Policy. def is the role of every user on every connection
// when no binding grants more; RoleNone hides connections by default.
//
// New fails on a binding with an invalid or none role, no users and no
// groups, an empty connections list, or a malformed glob.
func New(def Role, bindings []Binding) (*Policy, error) {
	if !def.valid() {
		return nil, fmt.Errorf("authz: invalid default role %v", def)
	}
	p := &Policy{def: def, bindings: make([]binding, 0, len(bindings))}
	var errs []error
	for i, b := range bindings {
		if err := validate(b); err != nil {
			errs = append(errs, fmt.Errorf("authz: binding %d: %w", i, err))
			continue
		}
		p.bindings = append(p.bindings, binding{
			users:  set(b.Users, FoldUsername),
			groups: set(b.Groups, nil),
			role:   b.Role,
			conns:  slices.Clone(b.Connections),
		})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return p, nil
}

func validate(b Binding) error {
	var errs []error
	if !b.Role.valid() || b.Role == RoleNone {
		errs = append(errs, fmt.Errorf("role must be viewer, operator or admin, got %v", b.Role))
	}
	if len(b.Users) == 0 && len(b.Groups) == 0 {
		errs = append(errs, errors.New("needs users or groups"))
	}
	if slices.Contains(b.Users, "") || slices.Contains(b.Groups, "") {
		errs = append(errs, errors.New("empty user or group name"))
	}
	if len(b.Connections) == 0 {
		errs = append(errs, errors.New("connections must not be empty"))
	}
	for _, g := range b.Connections {
		if _, err := path.Match(g, ""); err != nil {
			errs = append(errs, fmt.Errorf("invalid connection glob %q: %w", g, err))
		}
	}
	return errors.Join(errs...)
}

func set(xs []string, norm func(string) string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		if norm != nil {
			x = norm(x)
		}
		m[x] = struct{}{}
	}
	return m
}

// Groups returns every group referenced by a binding, sorted. Sessions only
// need to remember these.
func (p *Policy) Groups() []string {
	var out []string
	for _, b := range p.bindings {
		for g := range b.groups {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// RoleFor returns the user's effective role on conn: the highest of the
// default role and every matching binding. ok is false when the role is
// RoleNone, meaning conn must be hidden from the user.
func (p *Policy) RoleFor(user Identity, conn string) (role Role, ok bool) {
	role = p.def
	for _, b := range p.bindings {
		if b.role <= role || !b.matchesUser(user) || !b.matchesConn(conn) {
			continue
		}
		role = b.role
	}
	return role, role != RoleNone
}

// Can reports whether user may perform action on conn. A hidden connection
// allows nothing.
func (p *Policy) Can(user Identity, conn string, action Action) bool {
	role, _ := p.RoleFor(user, conn)
	return role.Allows(action)
}

// Visible returns the connections of conns the user can see, in their
// original order.
func (p *Policy) Visible(user Identity, conns []string) []string {
	out := make([]string, 0, len(conns))
	for _, c := range conns {
		if _, ok := p.RoleFor(user, c); ok {
			out = append(out, c)
		}
	}
	return out
}

// MaxRole returns the user's highest role across conns, as shown in the
// account menu on the Overview. It is RoleNone when conns is empty or every
// connection is hidden.
func (p *Policy) MaxRole(user Identity, conns []string) Role {
	maxRole := RoleNone
	for _, c := range conns {
		if r, _ := p.RoleFor(user, c); r > maxRole {
			maxRole = r
		}
	}
	return maxRole
}

func (b binding) matchesUser(u Identity) bool {
	if u.Name != "" {
		if _, ok := b.users[FoldUsername(u.Name)]; ok {
			return true
		}
	}
	for _, g := range u.Groups {
		if _, ok := b.groups[g]; ok {
			return true
		}
	}
	return false
}

func (b binding) matchesConn(conn string) bool {
	for _, g := range b.conns {
		// Patterns were validated in New, so the error is always nil.
		if ok, _ := path.Match(g, conn); ok {
			return true
		}
	}
	return false
}
