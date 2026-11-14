package main

import (
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

// registrySource lists the registry's connections for web, which uses
// each *conn.Conn through its own interfaces.
type registrySource struct {
	reg *conn.Registry
}

func (s registrySource) Connections() []web.Conn {
	all := s.reg.All()
	out := make([]web.Conn, len(all))
	for i, c := range all {
		out[i] = c
	}
	return out
}
