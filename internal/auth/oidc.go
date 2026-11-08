package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	defaultUsernameClaim = "email"
	defaultGroupsClaim   = "groups"
	oidcHTTPTimeout      = 10 * time.Second
	// maxIssuedAtSkew tolerates IdP clocks running ahead of ours.
	maxIssuedAtSkew = 5 * time.Minute
)

// OIDCOptions configures the OpenID Connect provider.
type OIDCOptions struct {
	Issuer       string
	ClientID     string
	ClientSecret string // optional for public clients; PKCE is always used
	// RedirectURL is the absolute URL of GET <base>/auth/callback. Required.
	RedirectURL string
	// Scopes requested. "openid" is always included. Empty means
	// openid, profile, email.
	Scopes []string
	// UsernameClaim names the claim used as Identity.Name. Empty means
	// "email", in which case email_verified must be true (the boolean, or
	// the string "true" as Amazon Cognito sends it).
	UsernameClaim string
	// GroupsClaim names the claim holding the user's groups, a string array
	// or a single string. It is read from the ID token, falling back to the
	// userinfo endpoint. Empty means "groups".
	GroupsClaim string
	// HTTPClient talks to the IdP. Nil means a client with a 10s timeout.
	HTTPClient *http.Client
}

type oidcProvider struct {
	provider      *oidc.Provider
	oauth         oauth2.Config
	verifier      *oidc.IDTokenVerifier
	client        *http.Client
	usernameClaim string
	groupsClaim   string
	logger        *slog.Logger
	now           func() time.Time
}

// newOIDC runs discovery against the issuer, so it fails at startup when the
// IdP is unreachable or misconfigured.
func newOIDC(ctx context.Context, o OIDCOptions, logger *slog.Logger, now func() time.Time) (*oidcProvider, error) {
	switch {
	case o.Issuer == "":
		return nil, errors.New("oidc: issuer is required")
	case o.ClientID == "":
		return nil, errors.New("oidc: client id is required")
	case o.RedirectURL == "":
		return nil, errors.New("oidc: redirect url is required")
	}
	if u, err := url.Parse(o.RedirectURL); err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("oidc: redirect url %q must be absolute", o.RedirectURL)
	}
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: oidcHTTPTimeout}
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), o.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery for %s: %w", o.Issuer, err)
	}
	scopes := slices.Clone(o.Scopes)
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	if !slices.Contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	p := &oidcProvider{
		provider: provider,
		oauth: oauth2.Config{
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  o.RedirectURL,
			Scopes:       scopes,
		},
		verifier:      provider.Verifier(&oidc.Config{ClientID: o.ClientID, Now: now}),
		client:        client,
		usernameClaim: o.UsernameClaim,
		groupsClaim:   o.GroupsClaim,
		logger:        logger,
		now:           now,
	}
	if p.usernameClaim == "" {
		p.usernameClaim = defaultUsernameClaim
	}
	if p.groupsClaim == "" {
		p.groupsClaim = defaultGroupsClaim
	}
	return p, nil
}

// start stores fresh state, nonce and PKCE verifier in the flow cookie and
// redirects to the IdP.
func (p *oidcProvider) start(w http.ResponseWriter, r *http.Request, jar *cookieJar, next string) error {
	f := flowData{
		State:    rand.Text(),
		Nonce:    rand.Text(),
		Verifier: oauth2.GenerateVerifier(),
		Next:     next,
	}
	if err := jar.setFlow(w, f); err != nil {
		return err
	}
	u := p.oauth.AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier))
	http.Redirect(w, r, u, http.StatusSeeOther)
	return nil
}

// callbackError carries the HTTP status for a failed callback.
type callbackError struct {
	status int
	err    error
}

func (e *callbackError) Error() string { return e.err.Error() }
func (e *callbackError) Unwrap() error { return e.err }

func failCallback(status int, err error) error {
	return &callbackError{status: status, err: err}
}

// callback validates the flow cookie and state, exchanges the code with the
// PKCE verifier, verifies the ID token (signature, issuer, audience, expiry)
// and its nonce, and returns the identity and post-login path.
func (p *oidcProvider) callback(w http.ResponseWriter, r *http.Request, jar *cookieJar) (Identity, string, error) {
	f, err := jar.readFlow(r)
	if err != nil {
		return Identity{}, "", failCallback(http.StatusBadRequest, err)
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(f.State)) != 1 {
		// Leave the cookie: a forged cross-site callback must not cancel
		// the user's own login in progress.
		return Identity{}, "", failCallback(http.StatusBadRequest, errors.New("state mismatch"))
	}
	jar.clearFlow(w) // the state is single-use from here on
	if e := q.Get("error"); e != "" {
		return Identity{}, "", failCallback(http.StatusUnauthorized, fmt.Errorf("idp returned error %q", e))
	}
	code := q.Get("code")
	if code == "" {
		return Identity{}, "", failCallback(http.StatusBadRequest, errors.New("missing code"))
	}

	ctx := oidc.ClientContext(r.Context(), p.client)
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return Identity{}, "", failCallback(http.StatusBadGateway, fmt.Errorf("code exchange: %w", err))
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, "", failCallback(http.StatusBadGateway, errors.New("token response has no id_token"))
	}
	idTok, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, "", failCallback(http.StatusUnauthorized, fmt.Errorf("verify id token: %w", err))
	}
	if subtle.ConstantTimeCompare([]byte(idTok.Nonce), []byte(f.Nonce)) != 1 {
		return Identity{}, "", failCallback(http.StatusUnauthorized, errors.New("nonce mismatch"))
	}
	id, err := p.identity(ctx, idTok, tok)
	if err != nil {
		return Identity{}, "", failCallback(http.StatusUnauthorized, err)
	}
	return id, f.Next, nil
}

// checkToken adds the checks go-oidc leaves to the caller: a subject, the
// authorized party when the token names one or several audiences, and an
// issue time not in the future.
func (p *oidcProvider) checkToken(t *oidc.IDToken, claims map[string]any) error {
	if t.Subject == "" {
		return errors.New("id token has no subject")
	}
	azp, hasAzp := claims["azp"]
	if hasAzp || len(t.Audience) > 1 {
		if s, _ := azp.(string); s != p.oauth.ClientID {
			return fmt.Errorf("id token azp %q is not this client", azp)
		}
	}
	if t.IssuedAt.After(p.now().Add(maxIssuedAtSkew)) {
		return fmt.Errorf("id token issued in the future (%s)", t.IssuedAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// claimSet reads claims from the ID token, falling back to the userinfo
// endpoint (fetched at most once, and only if its subject matches) for
// claims the ID token lacks.
type claimSet struct {
	p        *oidcProvider
	tok      *oauth2.Token
	subject  string
	id       map[string]any
	userinfo map[string]any // nil until fetched
	fetched  bool
}

// get returns the claim, or nil when neither source has it.
func (c *claimSet) get(ctx context.Context, name string) any {
	if v, ok := c.id[name]; ok {
		return v
	}
	if !c.fetched {
		c.fetched = true
		c.userinfo = c.p.fetchUserinfo(ctx, c.tok, c.subject)
	}
	return c.userinfo[name]
}

func (p *oidcProvider) fetchUserinfo(ctx context.Context, tok *oauth2.Token, subject string) map[string]any {
	if p.provider.UserInfoEndpoint() == "" {
		return nil
	}
	ui, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		p.logger.WarnContext(ctx, "oidc: userinfo request failed", slog.Any("error", err))
		return nil
	}
	if ui.Subject == "" || ui.Subject != subject {
		p.logger.WarnContext(ctx, "oidc: userinfo subject missing or not the id token's; ignored")
		return nil
	}
	var m map[string]any
	if err := ui.Claims(&m); err != nil {
		p.logger.WarnContext(ctx, "oidc: decode userinfo", slog.Any("error", err))
		return nil
	}
	return m
}

func (p *oidcProvider) identity(ctx context.Context, t *oidc.IDToken, tok *oauth2.Token) (Identity, error) {
	c := &claimSet{p: p, tok: tok, subject: t.Subject}
	if err := t.Claims(&c.id); err != nil {
		return Identity{}, fmt.Errorf("decode claims: %w", err)
	}
	if err := p.checkToken(t, c.id); err != nil {
		return Identity{}, err
	}
	name, _ := c.get(ctx, p.usernameClaim).(string)
	if name == "" {
		return Identity{}, fmt.Errorf("no %q claim", p.usernameClaim)
	}
	// An unverified email must not act as a username: anyone could claim
	// someone else's address and inherit their bindings.
	if p.usernameClaim == "email" && !isTrue(c.get(ctx, "email_verified")) {
		return Identity{}, errors.New("email is not verified")
	}
	email, _ := c.get(ctx, "email").(string)

	var groups []string
	if overage(c.id, p.groupsClaim) {
		p.logger.WarnContext(ctx, "oidc: groups overage; groups treated as empty",
			slog.String("user", name), slog.String("claim", p.groupsClaim))
	} else {
		var err error
		if groups, err = stringList(c.get(ctx, p.groupsClaim)); err != nil {
			return Identity{}, fmt.Errorf("claim %q: %w", p.groupsClaim, err)
		}
	}
	return Identity{Subject: t.Subject, Name: name, Email: email, Groups: groups}, nil
}

// isTrue accepts the boolean true and the string "true", which some IdPs
// (Amazon Cognito) send for email_verified.
func isTrue(v any) bool {
	return v == true || v == "true"
}

// overage reports whether the IdP left the groups out because there are too
// many (Microsoft Entra: "_claim_names" points elsewhere, or "hasgroups").
func overage(claims map[string]any, groupsClaim string) bool {
	if names, ok := claims["_claim_names"].(map[string]any); ok {
		if _, ok := names[groupsClaim]; ok {
			return true
		}
	}
	return claims["hasgroups"] == true
}

func stringList(v any) ([]string, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("want strings, got %T", e)
			}
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("want a string or string array, got %T", v)
}
