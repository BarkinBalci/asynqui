package authz

import (
	"fmt"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// Role is a permission level on one connection. Roles are ordered, so a
// higher Role includes every capability of the lower ones.
type Role int

// Roles in ascending order. RoleNone means the connection is hidden.
const (
	RoleNone Role = iota
	RoleViewer
	RoleOperator
	RoleAdmin
)

// String returns the config spelling of r: "none", "viewer", "operator" or
// "admin".
func (r Role) String() string {
	switch r {
	case RoleNone:
		return "none"
	case RoleViewer:
		return "viewer"
	case RoleOperator:
		return "operator"
	case RoleAdmin:
		return "admin"
	}
	return fmt.Sprintf("Role(%d)", int(r))
}

// parseRole maps a config role onto Role.
func parseRole(r config.Role) (Role, error) {
	switch r {
	case config.RoleNone:
		return RoleNone, nil
	case config.RoleViewer:
		return RoleViewer, nil
	case config.RoleOperator:
		return RoleOperator, nil
	case config.RoleAdmin:
		return RoleAdmin, nil
	}
	return RoleNone, fmt.Errorf("unknown role %q (want viewer, operator, admin or none)", r)
}

// Action is something a user can do on a connection.
type Action int

// Actions checked by the server. Every capability in the requirements'
// authorization table has one.
const (
	ActionView Action = iota + 1 // every page and fragment
	ActionPauseQueue
	ActionResumeQueue
	ActionRunTask     // run tasks (single, bulk, all)
	ActionArchiveTask // archive tasks (single, bulk, all)
	ActionCancelTask  // cancel active tasks (single, bulk, all)
	ActionDeleteTask  // delete tasks (single, bulk, all)
	ActionDeleteQueue
)

// MinRole returns the lowest role allowed to perform a. An unknown action
// returns false and is never allowed.
func MinRole(a Action) (Role, bool) {
	switch a {
	case ActionView:
		return RoleViewer, true
	case ActionPauseQueue, ActionResumeQueue,
		ActionRunTask, ActionArchiveTask, ActionCancelTask:
		return RoleOperator, true
	case ActionDeleteTask, ActionDeleteQueue:
		return RoleAdmin, true
	}
	return RoleNone, false
}

// Allows reports whether r may perform a.
func (r Role) Allows(a Action) bool {
	need, ok := MinRole(a)
	return ok && r != RoleNone && r >= need
}
