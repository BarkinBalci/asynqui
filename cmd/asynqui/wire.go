package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web"
)

// newPolicy builds the authorization policy from config. Load has already
// resolved the provider-dependent default and validated every binding.
func newPolicy(a config.Authz) (*authz.Policy, error) {
	def, err := authz.ParseRole(string(a.Default))
	if err != nil {
		return nil, fmt.Errorf("authz.default: %w", err)
	}
	bindings := make([]authz.Binding, 0, len(a.Bindings))
	for i, b := range a.Bindings {
		role, err := authz.ParseRole(string(b.Role))
		if err != nil {
			return nil, fmt.Errorf("authz.bindings[%d].role: %w", i, err)
		}
		bindings = append(bindings, authz.Binding{
			Users:       b.Users,
			Groups:      b.Groups,
			Role:        role,
			Connections: b.Connections,
		})
	}
	return authz.New(def, bindings)
}

// authOptions maps config onto auth.Options. sessionGroups are the groups
// the policy references (Policy.Groups), the only ones worth keeping in an
// OIDC session.
func authOptions(cfg *config.Config, sessionGroups []string, logger *slog.Logger) (auth.Options, error) {
	a := cfg.Auth
	o := auth.Options{
		Provider:      auth.Kind(a.Provider),
		SessionGroups: sessionGroups,
		BasePath:      cfg.Server.BasePath,
		Logger:        logger,
		Proxy: auth.ProxyOptions{
			UserHeader:     a.Proxy.UserHeader,
			GroupsHeader:   a.Proxy.GroupsHeader,
			TrustedProxies: a.Proxy.TrustedProxies,
			LogoutURL:      a.Proxy.LogoutURL,
		},
		OIDC: auth.OIDCOptions{
			Issuer:        a.OIDC.Issuer,
			ClientID:      a.OIDC.ClientID,
			ClientSecret:  a.OIDC.ClientSecret.Reveal(),
			RedirectURL:   a.OIDC.RedirectURL,
			Scopes:        a.OIDC.Scopes,
			UsernameClaim: a.OIDC.UsernameClaim,
			GroupsClaim:   a.OIDC.GroupsClaim,
		},
	}
	for _, u := range a.Basic.Users {
		o.Basic.Users = append(o.Basic.Users, auth.BasicUser{
			Username:     u.Username,
			PasswordHash: u.PasswordHash.Reveal(),
			Groups:       u.Groups,
		})
	}
	if a.Provider == config.ProviderBasic || a.Provider == config.ProviderOIDC {
		keys, err := a.Session.KeyPairs()
		if err != nil {
			return auth.Options{}, fmt.Errorf("auth.session: %w", err)
		}
		if len(keys) < 2 {
			return auth.Options{}, errors.New("auth.session: no key pair")
		}
		o.Session = auth.SessionOptions{
			KeyPair:  auth.KeyPair{HashKey: keys[0], BlockKey: keys[1]},
			TTL:      a.Session.TTL,
			Insecure: !a.Session.CookieSecure,
		}
		for i := 2; i+1 < len(keys); i += 2 {
			o.Session.PreviousKeys = append(o.Session.PreviousKeys, auth.KeyPair{HashKey: keys[i], BlockKey: keys[i+1]})
		}
	}
	return o, nil
}

// registrySource adapts the connection registry to the web layer's
// ConnectionSource, which web declares as its consumer contract.
type registrySource struct {
	reg *conn.Registry
}

func (s registrySource) Connections() []web.Connection {
	all := s.reg.All()
	out := make([]web.Connection, len(all))
	for i, c := range all {
		out[i] = web.Connection{Name: c.Name(), Addr: c.Addr(), Health: webHealth(c.Health())}
	}
	return out
}

func (s registrySource) Summary(ctx context.Context, name string) (web.Summary, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return web.Summary{}, conn.ErrNotFound
	}
	sum, err := c.Summary(ctx)
	if err != nil {
		return web.Summary{}, err
	}
	return web.Summary{
		Queues:         sum.Queues,
		Pending:        sum.Pending,
		Active:         sum.Active,
		Retry:          sum.Retry,
		ProcessedToday: sum.ProcessedToday,
		FailedToday:    sum.FailedToday,
		OldestPending:  sum.OldestPending,
	}, nil
}

func (s registrySource) Queues(ctx context.Context, name string) ([]web.Queue, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return nil, conn.ErrNotFound
	}
	qs, err := c.Queues(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.Queue, len(qs))
	for i, q := range qs {
		out[i] = web.Queue{
			Name:           q.Name,
			Paused:         q.Paused,
			Size:           q.Size,
			Pending:        q.Pending,
			Active:         q.Active,
			Scheduled:      q.Scheduled,
			Retry:          q.Retry,
			Archived:       q.Archived,
			ProcessedToday: q.ProcessedToday,
			FailedToday:    q.FailedToday,
			Latency:        q.Latency,
			MemoryUsage:    q.MemoryUsage,
		}
	}
	return out, nil
}

func (s registrySource) History(ctx context.Context, name, queue string, days int) ([]web.DailyStat, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return nil, conn.ErrNotFound
	}
	stats, err := c.History(ctx, queue, days)
	if err != nil {
		return nil, webQueueErr(err)
	}
	out := make([]web.DailyStat, len(stats))
	for i, st := range stats {
		out[i] = web.DailyStat{Date: st.Date, Processed: st.Processed, Failed: st.Failed}
	}
	return out, nil
}

func (s registrySource) PauseQueue(ctx context.Context, name, queue string) error {
	return s.act(ctx, name, queue, (*conn.Conn).PauseQueue)
}

func (s registrySource) ResumeQueue(ctx context.Context, name, queue string) error {
	return s.act(ctx, name, queue, (*conn.Conn).UnpauseQueue)
}

func (s registrySource) DeleteQueue(ctx context.Context, name, queue string) error {
	return s.act(ctx, name, queue, (*conn.Conn).DeleteQueue)
}

func (s registrySource) act(ctx context.Context, name, queue string, fn func(*conn.Conn, context.Context, string) error) error {
	c, ok := s.reg.Get(name)
	if !ok {
		return conn.ErrNotFound
	}
	return webQueueErr(fn(c, ctx, queue))
}

// webQueueErr adds the web error a handler tests for to conn's typed
// queue errors.
func webQueueErr(err error) error {
	switch {
	case errors.Is(err, conn.ErrQueueNotFound):
		return fmt.Errorf("%w: %w", web.ErrQueueNotFound, err)
	case errors.Is(err, conn.ErrQueueNotEmpty):
		return fmt.Errorf("%w: %w", web.ErrQueueNotEmpty, err)
	}
	return err
}

// Recheck runs a check now. When it does not finish (the request ended, or
// the registry is closing) nothing was stored, and the health is the cached
// one.
func (s registrySource) Recheck(ctx context.Context, name string) web.Health {
	c, ok := s.reg.Get(name)
	if !ok {
		return web.Health{}
	}
	h, err := c.Recheck(ctx)
	if err != nil {
		return webHealth(c.Health())
	}
	return webHealth(h)
}

func webHealth(h conn.Health) web.Health {
	out := web.Health{Err: h.Err, CheckedAt: h.CheckedAt}
	switch h.State {
	case conn.StateUp:
		out.State = web.HealthUp
	case conn.StateDown:
		out.State = web.HealthDown
	default:
		out.State = web.HealthUnknown
	}
	return out
}
