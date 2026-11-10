package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web"
)

// closedAddr returns a local address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestRegistrySource drives the adapter against a registry whose Redis is
// down, which needs no Redis: unknown until checked, then down with the
// dial error; Summary fails.
func TestRegistrySource(t *testing.T) {
	addr := closedAddr(t)
	reg, err := conn.NewRegistry([]config.Connection{
		{Name: "a", Addr: addr, DB: 2, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	src := registrySource{reg: reg}

	got := src.Connections()
	if len(got) != 1 || got[0].Name != "a" || got[0].Addr != addr+"/2" || got[0].Health.State != web.HealthUnknown {
		t.Fatalf("Connections = %+v", got)
	}
	h := src.Recheck(t.Context(), "a")
	if h.State != web.HealthDown || h.Err == nil || h.CheckedAt.IsZero() {
		t.Fatalf("Recheck = %+v", h)
	}
	if c := src.Connections()[0]; c.Health.State != web.HealthDown {
		t.Errorf("health not stored: %+v", c.Health)
	}
	if _, err := src.Summary(t.Context(), "a"); err == nil {
		t.Error("Summary of a down connection succeeded")
	}
	if _, err := src.Summary(t.Context(), "nope"); !errors.Is(err, conn.ErrNotFound) {
		t.Errorf("Summary(nope) = %v", err)
	}
	if h := src.Recheck(t.Context(), "nope"); h.State != web.HealthUnknown {
		t.Errorf("Recheck(nope) = %+v", h)
	}

	// A canceled check stores nothing; the cached health is returned.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if h := src.Recheck(ctx, "a"); h.State != web.HealthDown {
		t.Errorf("canceled Recheck = %+v, want the cached down", h)
	}
}

func TestWebHealth(t *testing.T) {
	at := time.Unix(100, 0)
	boom := errors.New("boom")
	tests := []struct {
		in   conn.Health
		want web.Health
	}{
		{conn.Health{}, web.Health{State: web.HealthUnknown}},
		{conn.Health{State: conn.StateUp, CheckedAt: at, Latency: time.Millisecond}, web.Health{State: web.HealthUp, CheckedAt: at}},
		{conn.Health{State: conn.StateDown, Err: boom, CheckedAt: at}, web.Health{State: web.HealthDown, Err: boom, CheckedAt: at}},
	}
	for _, tt := range tests {
		if diff := cmp.Diff(tt.want, webHealth(tt.in), cmp.Comparer(func(a, b error) bool { return errors.Is(a, b) && errors.Is(b, a) })); diff != "" {
			t.Errorf("webHealth(%+v) (-want +got):\n%s", tt.in, diff)
		}
	}
}

func TestNewPolicy(t *testing.T) {
	p, err := newPolicy(config.Authz{
		Default:  config.RoleNone,
		Bindings: []config.Binding{{Groups: []string{"ops"}, Role: config.RoleAdmin, Connections: []string{"*"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := p.RoleFor(authz.Identity{Groups: []string{"ops"}}, "x"); r != authz.RoleAdmin {
		t.Errorf("role = %v", r)
	}
	if diff := cmp.Diff([]string{"ops"}, p.Groups()); diff != "" {
		t.Errorf("groups: %s", diff)
	}
	if _, err := newPolicy(config.Authz{Default: "owner"}); err == nil {
		t.Error("bad default accepted")
	}
}

func TestAuthOptions(t *testing.T) {
	cfg := &config.Config{
		Server: config.Server{BasePath: "/asynq"},
		Auth: config.Auth{
			Provider: config.ProviderBasic,
			Session: config.Session{
				HashKey:  config.Secret("11111111111111111111111111111111111111111111111111111111111111" + "11"),
				BlockKey: config.Secret("22222222222222222222222222222222222222222222222222222222222222" + "22"),
				PreviousKeys: []config.KeyPair{{
					HashKey:  config.Secret("33333333333333333333333333333333333333333333333333333333333333" + "33"),
					BlockKey: config.Secret("44444444444444444444444444444444444444444444444444444444444444" + "44"),
				}},
				TTL: time.Hour,
			},
			Basic: config.Basic{Users: []config.BasicUser{{Username: "u", PasswordHash: "$2a$04$x", Groups: []string{"g"}}}},
		},
	}
	o, err := authOptions(cfg, []string{"g"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Provider != auth.KindBasic || o.BasePath != "/asynq" || o.Session.TTL != time.Hour || !o.Session.Insecure {
		t.Errorf("options = %+v", o)
	}
	if len(o.Session.HashKey) != 32 || o.Session.HashKey[0] != 0x11 || o.Session.BlockKey[0] != 0x22 {
		t.Error("current key pair wrong")
	}
	if len(o.Session.PreviousKeys) != 1 || o.Session.PreviousKeys[0].HashKey[0] != 0x33 || o.Session.PreviousKeys[0].BlockKey[0] != 0x44 {
		t.Errorf("previous keys = %+v", o.Session.PreviousKeys)
	}
	if len(o.Basic.Users) != 1 || o.Basic.Users[0].PasswordHash != "$2a$04$x" || o.Basic.Users[0].Groups[0] != "g" {
		t.Errorf("basic users = %+v", o.Basic.Users)
	}
}
