package authz

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseRole(t *testing.T) {
	tests := []struct {
		in      string
		want    Role
		wantErr bool
	}{
		{"none", RoleNone, false},
		{"viewer", RoleViewer, false},
		{"operator", RoleOperator, false},
		{"admin", RoleAdmin, false},
		{"Admin", RoleNone, true},
		{"", RoleNone, true},
		{"owner", RoleNone, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseRole(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("ParseRole(%q) = %v, %v; want %v, err=%v", tt.in, got, err, tt.want, tt.wantErr)
			}
			if err == nil && got.String() != tt.in {
				t.Errorf("String() = %q, want %q", got.String(), tt.in)
			}
		})
	}
	if got := Role(42).String(); got != "Role(42)" {
		t.Errorf("Role(42).String() = %q", got)
	}
}

// TestMatrix checks every role against every action, exactly as in the
// requirements' authorization table.
func TestMatrix(t *testing.T) {
	type row struct{ none, viewer, operator, admin bool }
	matrix := map[Action]row{
		ActionView:              {false, true, true, true},
		ActionPauseQueue:        {false, false, true, true},
		ActionResumeQueue:       {false, false, true, true},
		ActionRunTask:           {false, false, true, true},
		ActionArchiveTask:       {false, false, true, true},
		ActionCancelTask:        {false, false, true, true},
		ActionRunSchedulerEntry: {false, false, true, true},
		ActionDeleteTask:        {false, false, false, true},
		ActionDeleteQueue:       {false, false, false, true},
		Action(0):               {false, false, false, false},
		ActionDeleteQueue + 1:   {false, false, false, false},
		Action(999):             {false, false, false, false},
	}
	for a, want := range matrix {
		got := row{
			RoleNone.Allows(a),
			RoleViewer.Allows(a),
			RoleOperator.Allows(a),
			RoleAdmin.Allows(a),
		}
		if diff := cmp.Diff(want, got, cmp.AllowUnexported(row{})); diff != "" {
			t.Errorf("action %d (-want +got):\n%s", a, diff)
		}
	}
	for a := ActionView; a <= ActionDeleteQueue; a++ {
		if _, ok := matrix[a]; !ok {
			t.Errorf("action %d missing from matrix", a)
		}
	}
}

func TestMinRole(t *testing.T) {
	tests := []struct {
		a      Action
		want   Role
		wantOK bool
	}{
		{ActionView, RoleViewer, true},
		{ActionPauseQueue, RoleOperator, true},
		{ActionResumeQueue, RoleOperator, true},
		{ActionRunTask, RoleOperator, true},
		{ActionArchiveTask, RoleOperator, true},
		{ActionCancelTask, RoleOperator, true},
		{ActionRunSchedulerEntry, RoleOperator, true},
		{ActionDeleteTask, RoleAdmin, true},
		{ActionDeleteQueue, RoleAdmin, true},
		{Action(0), RoleNone, false},
	}
	for _, tt := range tests {
		got, ok := MinRole(tt.a)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("MinRole(%d) = %v, %v; want %v, %v", tt.a, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestNewValidation(t *testing.T) {
	ok := Binding{Users: []string{"u"}, Role: RoleViewer, Connections: []string{"*"}}
	tests := []struct {
		name    string
		def     Role
		b       []Binding
		wantErr string // substring; empty means success
	}{
		{"no bindings", RoleViewer, nil, ""},
		{"default none", RoleNone, []Binding{ok}, ""},
		{"groups only", RoleViewer, []Binding{{Groups: []string{"g"}, Role: RoleAdmin, Connections: []string{"a", "b*"}}}, ""},
		{"invalid default", Role(9), nil, "invalid default role"},
		{"negative default", Role(-1), nil, "invalid default role"},
		{"role none", RoleViewer, []Binding{{Users: []string{"u"}, Role: RoleNone, Connections: []string{"*"}}}, "role must be"},
		{"unknown role", RoleViewer, []Binding{{Users: []string{"u"}, Role: Role(7), Connections: []string{"*"}}}, "role must be"},
		{"no subjects", RoleViewer, []Binding{{Role: RoleViewer, Connections: []string{"*"}}}, "needs users or groups"},
		{"empty user", RoleViewer, []Binding{{Users: []string{""}, Role: RoleViewer, Connections: []string{"*"}}}, "empty user or group"},
		{"empty group", RoleViewer, []Binding{{Groups: []string{""}, Role: RoleViewer, Connections: []string{"*"}}}, "empty user or group"},
		{"no connections", RoleViewer, []Binding{{Users: []string{"u"}, Role: RoleViewer}}, "connections must not be empty"},
		{"bad glob", RoleViewer, []Binding{{Users: []string{"u"}, Role: RoleViewer, Connections: []string{"main", "[a-"}}}, `invalid connection glob "[a-"`},
		{"bad glob trailing escape", RoleViewer, []Binding{{Users: []string{"u"}, Role: RoleViewer, Connections: []string{`x\`}}}, "invalid connection glob"},
		{"index in message", RoleViewer, []Binding{ok, {Users: []string{"u"}, Role: RoleViewer}}, "binding 1:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := New(tt.def, tt.b)
			if tt.wantErr == "" {
				if err != nil || p == nil {
					t.Fatalf("New() = %v, %v; want policy", p, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewReportsEveryError(t *testing.T) {
	_, err := New(RoleViewer, []Binding{
		{Role: RoleNone},
		{Users: []string{"u"}, Role: RoleViewer, Connections: []string{"["}},
	})
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"binding 0: role must be", "needs users or groups", "connections must not be empty", "binding 1: invalid connection glob"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func mustPolicy(t *testing.T, def Role, b ...Binding) *Policy {
	t.Helper()
	p, err := New(def, b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRoleFor(t *testing.T) {
	// The bindings from requirements.md §4 plus a few edge cases.
	bindings := []Binding{
		{Groups: []string{"platform"}, Role: RoleAdmin, Connections: []string{"*"}},
		{Users: []string{"jdoe@example.com"}, Role: RoleOperator, Connections: []string{"billing", "main*"}},
		{Groups: []string{"payments"}, Role: RoleViewer, Connections: []string{"pay-?"}},
		{Users: []string{"ops"}, Groups: []string{"oncall"}, Role: RoleOperator, Connections: []string{"prod-[a-c]"}},
	}
	viewerDefault := mustPolicy(t, RoleViewer, bindings...)
	noneDefault := mustPolicy(t, RoleNone, bindings...)
	adminDefault := mustPolicy(t, RoleAdmin, bindings...)

	jdoe := Identity{Name: "jdoe@example.com"}
	platform := Identity{Name: "alice", Groups: []string{"dev", "platform"}}
	payments := Identity{Name: "bob", Groups: []string{"payments"}}
	ops := Identity{Name: "ops"}
	oncall := Identity{Name: "carol", Groups: []string{"oncall"}}
	stranger := Identity{Name: "eve", Groups: []string{"guests"}}
	anon := Identity{}

	tests := []struct {
		name   string
		p      *Policy
		user   Identity
		conn   string
		want   Role
		wantOK bool
	}{
		// default viewer
		{"viewer default stranger", viewerDefault, stranger, "main", RoleViewer, true},
		{"viewer default anonymous", viewerDefault, anon, "main", RoleViewer, true},
		{"user binding exact", viewerDefault, jdoe, "billing", RoleOperator, true},
		{"user binding glob", viewerDefault, jdoe, "main-jobs", RoleOperator, true},
		{"user binding glob empty suffix", viewerDefault, jdoe, "main", RoleOperator, true},
		{"user binding no match falls to default", viewerDefault, jdoe, "local", RoleViewer, true},
		{"group binding star", viewerDefault, platform, "anything", RoleAdmin, true},
		{"lower binding never lowers default", mustPolicy(t, RoleOperator, bindings...), payments, "pay-1", RoleOperator, true},
		{"question mark matches one char", viewerDefault, payments, "pay-1", RoleViewer, true},
		{"user or group: user", viewerDefault, ops, "prod-b", RoleOperator, true},
		{"user or group: group", viewerDefault, oncall, "prod-c", RoleOperator, true},
		{"char class miss", viewerDefault, oncall, "prod-d", RoleViewer, true},

		// default none hides
		{"none default stranger hidden", noneDefault, stranger, "main", RoleNone, false},
		{"none default anonymous hidden", noneDefault, anon, "main", RoleNone, false},
		{"none default binding reveals", noneDefault, jdoe, "billing", RoleOperator, true},
		{"none default other conn hidden", noneDefault, jdoe, "local", RoleNone, false},
		{"none default question mark needs one char", noneDefault, payments, "pay-", RoleNone, false},
		{"none default question mark too long", noneDefault, payments, "pay-12", RoleNone, false},
		{"none default glob does not span prefix", noneDefault, jdoe, "xmain", RoleNone, false},
		{"none default user case-insensitive", noneDefault, Identity{Name: "JDoe@Example.COM"}, "billing", RoleOperator, true},
		{"binding user case-insensitive", mustPolicy(t, RoleNone,
			Binding{Users: []string{"Ops@Example.com"}, Role: RoleAdmin, Connections: []string{"x"}},
		), Identity{Name: "ops@example.com"}, "x", RoleAdmin, true},
		{"none default case sensitive group", noneDefault, Identity{Name: "x", Groups: []string{"Platform"}}, "main", RoleNone, false},
		{"none default name is not a group", noneDefault, Identity{Name: "platform"}, "main", RoleNone, false},
		{"none default group is not a name", noneDefault, Identity{Groups: []string{"jdoe@example.com"}}, "billing", RoleNone, false},

		// highest wins
		{"highest wins across bindings", noneDefault, Identity{Name: "jdoe@example.com", Groups: []string{"platform"}}, "billing", RoleAdmin, true},
		{"highest wins regardless of order", mustPolicy(t, RoleNone,
			Binding{Groups: []string{"a"}, Role: RoleAdmin, Connections: []string{"x"}},
			Binding{Groups: []string{"a"}, Role: RoleViewer, Connections: []string{"x"}},
		), Identity{Groups: []string{"a"}}, "x", RoleAdmin, true},
		{"default admin beats bindings", adminDefault, payments, "pay-1", RoleAdmin, true},
		{"default admin for anonymous", adminDefault, anon, "main", RoleAdmin, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.p.RoleFor(tt.user, tt.conn)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("RoleFor(%+v, %q) = %v, %v; want %v, %v", tt.user, tt.conn, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestCan(t *testing.T) {
	p := mustPolicy(t, RoleNone,
		Binding{Users: []string{"v"}, Role: RoleViewer, Connections: []string{"c"}},
		Binding{Users: []string{"o"}, Role: RoleOperator, Connections: []string{"c"}},
		Binding{Users: []string{"a"}, Role: RoleAdmin, Connections: []string{"c"}},
	)
	actions := []Action{
		ActionView, ActionPauseQueue, ActionResumeQueue, ActionRunTask, ActionArchiveTask,
		ActionCancelTask, ActionRunSchedulerEntry, ActionDeleteTask, ActionDeleteQueue,
	}
	tests := []struct {
		user string
		conn string
		want []bool // per action, in the order above
	}{
		{"nobody", "c", []bool{false, false, false, false, false, false, false, false, false}},
		{"v", "c", []bool{true, false, false, false, false, false, false, false, false}},
		{"o", "c", []bool{true, true, true, true, true, true, true, false, false}},
		{"a", "c", []bool{true, true, true, true, true, true, true, true, true}},
		{"a", "other", []bool{false, false, false, false, false, false, false, false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.user+"@"+tt.conn, func(t *testing.T) {
			got := make([]bool, len(actions))
			for i, a := range actions {
				got[i] = p.Can(Identity{Name: tt.user}, tt.conn, a)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Can (-want +got):\n%s", diff)
			}
			if p.Can(Identity{Name: tt.user}, tt.conn, Action(0)) {
				t.Error("unknown action allowed")
			}
		})
	}
}

func TestVisibleAndMaxRole(t *testing.T) {
	conns := []string{"main", "main-jobs", "billing", "local", "pay-1"}
	p := mustPolicy(t, RoleNone,
		Binding{Users: []string{"jdoe"}, Role: RoleOperator, Connections: []string{"main*"}},
		Binding{Users: []string{"jdoe"}, Role: RoleViewer, Connections: []string{"local"}},
		Binding{Groups: []string{"platform"}, Role: RoleAdmin, Connections: []string{"*"}},
	)
	tests := []struct {
		name        string
		p           *Policy
		user        Identity
		conns       []string
		wantVisible []string
		wantMax     Role
	}{
		{"subset in config order", p, Identity{Name: "jdoe"}, conns, []string{"main", "main-jobs", "local"}, RoleOperator},
		{"everything", p, Identity{Groups: []string{"platform"}}, conns, conns, RoleAdmin},
		{"nothing", p, Identity{Name: "eve"}, conns, []string{}, RoleNone},
		{"empty input", p, Identity{Name: "jdoe"}, nil, []string{}, RoleNone},
		{"viewer default shows all", mustPolicy(t, RoleViewer), Identity{}, conns, conns, RoleViewer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.wantVisible, tt.p.Visible(tt.user, tt.conns)); diff != "" {
				t.Errorf("Visible (-want +got):\n%s", diff)
			}
			if got := tt.p.MaxRole(tt.user, tt.conns); got != tt.wantMax {
				t.Errorf("MaxRole = %v, want %v", got, tt.wantMax)
			}
		})
	}
}

func TestNewCopiesInput(t *testing.T) {
	b := []Binding{{Users: []string{"u"}, Role: RoleAdmin, Connections: []string{"a"}}}
	p := mustPolicy(t, RoleNone, b...)
	b[0].Users[0] = "mallory"
	b[0].Connections[0] = "*"
	if r, _ := p.RoleFor(Identity{Name: "u"}, "a"); r != RoleAdmin {
		t.Errorf("RoleFor after caller mutation = %v, want admin", r)
	}
	if _, ok := p.RoleFor(Identity{Name: "u"}, "b"); ok {
		t.Error("caller mutation of Connections leaked into policy")
	}
}

func TestFoldUsername(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"jdoe", "jdoe"},
		{"JDoe@Example.COM", "jdoe@example.com"},
		{"\u212Aevin", "\u212Aevin"},       // Kelvin sign is not K
		{"adm\u0130n", "adm\u0130n"},       // U+0130 is not I
		{"\u212Bngström", "\u212Bngström"}, // Angstrom sign is not Å
		{"ÄBC", "Äbc"},                     // only ASCII folds
	}
	for _, tt := range tests {
		if got := FoldUsername(tt.in); got != tt.want {
			t.Errorf("FoldUsername(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestLookalikeUsernames: Unicode case folding would map these onto the
// bound ASCII names; ASCII-only folding must not.
func TestLookalikeUsernames(t *testing.T) {
	p := mustPolicy(t, RoleNone,
		Binding{Users: []string{"kevin@example.com"}, Role: RoleAdmin, Connections: []string{"*"}},
		Binding{Users: []string{"admin"}, Role: RoleAdmin, Connections: []string{"*"}},
		Binding{Users: []string{"ångström"}, Role: RoleAdmin, Connections: []string{"*"}},
	)
	tests := []struct {
		name string
		user string
		want Role
	}{
		{"ascii upper case", "KEVIN@EXAMPLE.COM", RoleAdmin},
		{"kelvin sign", "\u212Aevin@example.com", RoleNone},
		{"capital I with dot", "adm\u0130n", RoleNone},
		{"dotless i", "adm\u0131n", RoleNone},
		{"angstrom sign", "\u212Bngström", RoleNone},
		{"zero-width space", "admin\u200B", RoleNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := p.RoleFor(Identity{Name: tt.user}, "main"); got != tt.want {
				t.Errorf("RoleFor(%q) = %v, want %v", tt.user, got, tt.want)
			}
		})
	}
}

func TestEdgeIdentitiesAndConnections(t *testing.T) {
	p := mustPolicy(t, RoleNone,
		Binding{Users: []string{"x"}, Groups: []string{"g"}, Role: RoleAdmin, Connections: []string{"main*", "[ab]illing"}},
	)
	ids := []struct {
		name string
		id   Identity
	}{
		{"zero", Identity{}},
		{"empty group", Identity{Groups: []string{""}}},
		{"group case differs", Identity{Groups: []string{"G"}}},
		{"group with space", Identity{Groups: []string{" g"}}},
	}
	for _, tt := range ids {
		if r, ok := p.RoleFor(tt.id, "main"); ok {
			t.Errorf("%s: RoleFor = %v, want hidden", tt.name, r)
		}
	}
	conns := []struct {
		conn string
		want Role
	}{
		{"main", RoleAdmin},
		{"main-prod", RoleAdmin},
		{"mainx", RoleAdmin},
		{"billing", RoleAdmin},
		{"ailling", RoleAdmin},
		{"cilling", RoleNone},
		{"Main", RoleNone},
		{"", RoleNone},
		{"../main", RoleNone},
	}
	for _, tt := range conns {
		if got, _ := p.RoleFor(Identity{Name: "x"}, tt.conn); got != tt.want {
			t.Errorf("RoleFor(x, %q) = %v, want %v", tt.conn, got, tt.want)
		}
	}
}

func TestGlobValidation(t *testing.T) {
	tests := []struct {
		glob    string
		wantErr bool
	}{
		{"*", false},
		{"[^a]", false},
		{`\*`, false},
		{"[", true},
		{"x[", true},
		{"*[", true},
		{`a\`, true},
		{"[a-]", true},
		{"[]a]", true},
	}
	for _, tt := range tests {
		_, err := New(RoleNone, []Binding{{Users: []string{"x"}, Role: RoleAdmin, Connections: []string{tt.glob}}})
		if (err != nil) != tt.wantErr {
			t.Errorf("glob %q: err = %v, wantErr %v", tt.glob, err, tt.wantErr)
		}
	}
}

func TestGroups(t *testing.T) {
	tests := []struct {
		name string
		b    []Binding
		want []string
	}{
		{"none", nil, nil},
		{"users only", []Binding{{Users: []string{"u"}, Role: RoleViewer, Connections: []string{"*"}}}, nil},
		{"sorted and deduplicated", []Binding{
			{Groups: []string{"ops", "Platform"}, Role: RoleViewer, Connections: []string{"*"}},
			{Users: []string{"u"}, Groups: []string{"ops", "dev"}, Role: RoleAdmin, Connections: []string{"a"}},
		}, []string{"Platform", "dev", "ops"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, mustPolicy(t, RoleNone, tt.b...).Groups()); diff != "" {
				t.Errorf("Groups (-want +got):\n%s", diff)
			}
		})
	}
}
