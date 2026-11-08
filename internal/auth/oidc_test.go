package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/go-cmp/cmp"
)

const (
	testClientID     = "asynqui"
	testClientSecret = "s3cret"
)

// fakeIDP is a minimal OpenID provider: discovery, JWKS, token and userinfo
// endpoints. It checks the PKCE verifier and signs ID tokens with an RSA
// key. Each test gets its own instance.
type fakeIDP struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey

	// claims edits the ID token claims; userinfo is served as is (nil
	// means no userinfo endpoint in discovery).
	claims   func(map[string]any)
	userinfo map[string]any
	// signRaw, if set, replaces RS256 signing (alg-confusion tests);
	// algs, if set, replaces the advertised signing algorithms.
	signRaw func(map[string]any) string
	algs    []string

	mu    sync.Mutex
	codes map[string]authRequest
}

type authRequest struct {
	nonce     string
	challenge string
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{t: t, key: key, codes: map[string]authRequest{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /jwks", f.jwks)
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /userinfo", f.userinfoHandler)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIDP) discovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":                                f.srv.URL,
		"authorization_endpoint":                f.srv.URL + "/authorize",
		"token_endpoint":                        f.srv.URL + "/token",
		"jwks_uri":                              f.srv.URL + "/jwks",
		"id_token_signing_alg_values_supported": f.algList(),
		"code_challenge_methods_supported":      []string{"S256"},
	}
	if f.userinfo != nil {
		doc["userinfo_endpoint"] = f.srv.URL + "/userinfo"
	}
	_ = json.NewEncoder(w).Encode(doc)
}

func (f *fakeIDP) jwks(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
	}})
}

// authorize plays the IdP's login page: it records the nonce and PKCE
// challenge from the authorization URL and returns a code.
func (f *fakeIDP) authorize(authURL string) (code, state string) {
	f.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		f.t.Fatal(err)
	}
	q := u.Query()
	if !strings.HasPrefix(authURL, f.srv.URL+"/authorize?") {
		f.t.Fatalf("redirected to %q, want the IdP", authURL)
	}
	for k, want := range map[string]string{
		"client_id": testClientID, "response_type": "code", "code_challenge_method": "S256",
		"redirect_uri": "https://asynqui.example.com/auth/callback", "scope": "openid profile email",
	} {
		if got := q.Get(k); got != want {
			f.t.Errorf("authorization %s = %q, want %q", k, got, want)
		}
	}
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		f.t.Fatalf("authorization URL lacks state, nonce or challenge: %s", authURL)
	}
	code = rand.Text()
	f.mu.Lock()
	f.codes[code] = authRequest{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	f.mu.Unlock()
	return code, q.Get("state")
}

func (f *fakeIDP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != testClientID || secret != testClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	ar, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != ar.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss":            f.srv.URL,
		"sub":            "user-1",
		"aud":            testClientID,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
		"nonce":          ar.nonce,
		"email":          "jdoe@example.com",
		"email_verified": true,
		"groups":         []string{"platform", "unrelated"},
	}
	if f.claims != nil {
		f.claims(claims)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at-" + rand.Text(),
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     f.signOrRaw(claims),
	})
}

func (f *fakeIDP) userinfoHandler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at-") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f.userinfo)
}

func (f *fakeIDP) sign(claims map[string]any) string {
	f.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		f.t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		f.t.Fatal(err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	s, err := jws.CompactSerialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fakeIDP) options(base string) Options {
	return Options{
		Provider:      KindOIDC,
		Session:       testSession(),
		SessionGroups: []string{"platform", "ops"},
		BasePath:      base,
		OIDC: OIDCOptions{
			Issuer:       f.srv.URL,
			ClientID:     testClientID,
			ClientSecret: testClientSecret,
			RedirectURL:  "https://asynqui.example.com/auth/callback",
			HTTPClient:   f.srv.Client(),
		},
	}
}

// flow drives POST /login and GET /auth/callback. tamper may change the
// callback query and cookies before the callback is sent.
type flowResult struct {
	start    resp
	callback resp
}

func (f *fakeIDP) flow(t *testing.T, h http.Handler, next string, tamper func(q url.Values, flow **http.Cookie)) flowResult {
	t.Helper()
	start := do(t, h, req{method: http.MethodPost, target: "/login", form: url.Values{"next": {next}}})
	if start.status != http.StatusSeeOther {
		t.Fatalf("POST /login = %d %s", start.status, start.body)
	}
	flow := start.cookies[flowCookie]
	if flow == nil {
		t.Fatal("no flow cookie")
	}
	code, state := f.authorize(start.header.Get("Location"))
	q := url.Values{"code": {code}, "state": {state}}
	if tamper != nil {
		tamper(q, &flow)
	}
	var cookies []*http.Cookie
	if flow != nil {
		cookies = append(cookies, flow)
	}
	cb := do(t, h, req{target: "/auth/callback?" + q.Encode(), cookies: cookies})
	return flowResult{start: start, callback: cb}
}

func TestOIDCSuccess(t *testing.T) {
	idp := newFakeIDP(t)
	a := mustNew(t, idp.options(""))
	h := app(a)
	res := idp.flow(t, h, "/c/main/queues/default?state=retry", nil)

	if fc := res.start.cookies[flowCookie]; fc.Path != "/auth/callback" || !fc.HttpOnly || !fc.Secure ||
		fc.SameSite != http.SameSiteLaxMode || fc.MaxAge != 600 {
		t.Errorf("flow cookie attributes: %+v", fc)
	}
	cb := res.callback
	if cb.status != http.StatusSeeOther || cb.header.Get("Location") != "/c/main/queues/default?state=retry" {
		t.Fatalf("callback = %d %q %s", cb.status, cb.header.Get("Location"), cb.body)
	}
	if fc := cb.cookies[flowCookie]; fc == nil || fc.MaxAge >= 0 {
		t.Error("flow cookie not cleared on callback")
	}
	sc := cb.cookies[sessionCookie]
	if sc == nil {
		t.Fatal("no session cookie")
	}
	if strings.Contains(sc.Value, "at-") || len(sc.Value) > 1000 {
		t.Error("session cookie looks like it carries tokens")
	}
	id, ok := whoami(t, h, sc)
	want := Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"platform"}}
	if diff := cmp.Diff(want, id); !ok || diff != "" {
		t.Errorf("identity ok=%v (-want +got):\n%s", ok, diff)
	}
	if !a.SignOutVisible() || a.Kind() != KindOIDC {
		t.Error("oidc should offer Sign out")
	}

	// Logout is local: it clears the session and goes to the login page.
	out := do(t, h, req{method: http.MethodPost, target: "/logout", cookies: []*http.Cookie{sc}})
	if out.status != http.StatusSeeOther || out.header.Get("Location") != "/login" ||
		out.cookies[sessionCookie] == nil || out.cookies[sessionCookie].MaxAge >= 0 {
		t.Errorf("logout = %d %q %+v", out.status, out.header.Get("Location"), out.cookies[sessionCookie])
	}
}

func TestOIDCBasePath(t *testing.T) {
	idp := newFakeIDP(t)
	h := app(mustNew(t, idp.options("/asynq")))
	res := idp.flow(t, h, "https://evil.example/", nil)
	if p := res.start.cookies[flowCookie].Path; p != "/asynq/auth/callback" {
		t.Errorf("flow cookie path = %q", p)
	}
	if loc := res.callback.header.Get("Location"); loc != "/asynq/" {
		t.Errorf("redirect = %q, want /asynq/", loc)
	}
	if p := res.callback.cookies[sessionCookie].Path; p != "/asynq" {
		t.Errorf("session cookie path = %q", p)
	}
}

func TestOIDCFailures(t *testing.T) {
	tests := []struct {
		name       string
		claims     func(map[string]any)
		tamper     func(q url.Values, flow **http.Cookie)
		wantStatus int
	}{
		{"bad state", nil, func(q url.Values, _ **http.Cookie) { q.Set("state", "forged") }, http.StatusBadRequest},
		{"missing state", nil, func(q url.Values, _ **http.Cookie) { q.Del("state") }, http.StatusBadRequest},
		{"missing flow cookie", nil, func(_ url.Values, f **http.Cookie) { *f = nil }, http.StatusBadRequest},
		{"forged flow cookie", nil, func(_ url.Values, f **http.Cookie) { *f = &http.Cookie{Name: flowCookie, Value: "x"} }, http.StatusBadRequest},
		{"missing code", nil, func(q url.Values, _ **http.Cookie) { q.Del("code") }, http.StatusBadRequest},
		{"idp error", nil, func(q url.Values, _ **http.Cookie) { q.Del("code"); q.Set("error", "access_denied") }, http.StatusUnauthorized},
		{"unknown code", nil, func(q url.Values, _ **http.Cookie) { q.Set("code", "nope") }, http.StatusBadGateway},
		{"bad nonce", func(c map[string]any) { c["nonce"] = "other" }, nil, http.StatusUnauthorized},
		{"no nonce", func(c map[string]any) { delete(c, "nonce") }, nil, http.StatusUnauthorized},
		{"wrong audience", func(c map[string]any) { c["aud"] = "someone-else" }, nil, http.StatusUnauthorized},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://evil.example" }, nil, http.StatusUnauthorized},
		{"expired token", func(c map[string]any) {
			c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
			c["exp"] = time.Now().Add(-time.Hour).Unix()
		}, nil, http.StatusUnauthorized},
		{"email not verified", func(c map[string]any) { c["email_verified"] = false }, nil, http.StatusUnauthorized},
		{"email_verified missing", func(c map[string]any) { delete(c, "email_verified") }, nil, http.StatusUnauthorized},
		{"no username claim", func(c map[string]any) { delete(c, "email") }, nil, http.StatusUnauthorized},
		{"malformed groups", func(c map[string]any) { c["groups"] = []any{"a", 1} }, nil, http.StatusUnauthorized},
		{"groups wrong type", func(c map[string]any) { c["groups"] = map[string]any{"a": 1} }, nil, http.StatusUnauthorized},
		{"no sub", func(c map[string]any) { delete(c, "sub") }, nil, http.StatusUnauthorized},
		{"empty sub", func(c map[string]any) { c["sub"] = "" }, nil, http.StatusUnauthorized},
		{"azp is another client", func(c map[string]any) {
			c["aud"] = []string{"other-client", testClientID}
			c["azp"] = "other-client"
		}, nil, http.StatusUnauthorized},
		{"several audiences without azp", func(c map[string]any) { c["aud"] = []string{testClientID, "other-client"} }, nil, http.StatusUnauthorized},
		{"azp present but wrong with one audience", func(c map[string]any) { c["azp"] = "other-client" }, nil, http.StatusUnauthorized},
		{"issued in the future", func(c map[string]any) { c["iat"] = time.Now().Add(48 * time.Hour).Unix() }, nil, http.StatusUnauthorized},
		{"email_verified as the string false", func(c map[string]any) { c["email_verified"] = "false" }, nil, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idp := newFakeIDP(t)
			idp.claims = tt.claims
			a := mustNew(t, idp.options(""))
			var reasons []Failure
			mux := http.NewServeMux()
			mux.Handle("POST /login", a.LoginHandler(nil))
			mux.Handle("GET /auth/callback", a.CallbackHandler(func(w http.ResponseWriter, _ *http.Request, status int, reason Failure) {
				reasons = append(reasons, reason)
				w.WriteHeader(status)
			}))
			res := idp.flow(t, a.Middleware(mux), "/", tt.tamper)
			if res.callback.status != tt.wantStatus {
				t.Errorf("callback status = %d, want %d", res.callback.status, tt.wantStatus)
			}
			if _, ok := res.callback.cookies[sessionCookie]; ok {
				t.Error("session cookie set on failure")
			}
			if diff := cmp.Diff([]Failure{FailSSO}, reasons); diff != "" {
				t.Errorf("reasons (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOIDCReplay(t *testing.T) {
	idp := newFakeIDP(t)
	h := app(mustNew(t, idp.options("")))
	var flow *http.Cookie
	var q url.Values
	res := idp.flow(t, h, "/", func(qq url.Values, f **http.Cookie) { q, flow = qq, *f })
	if res.callback.status != http.StatusSeeOther {
		t.Fatalf("first callback = %d", res.callback.status)
	}
	// The code is spent; the IdP refuses it and no session is issued.
	again := do(t, h, req{target: "/auth/callback?" + q.Encode(), cookies: []*http.Cookie{flow}})
	if again.status == http.StatusSeeOther || again.cookies[sessionCookie] != nil {
		t.Errorf("replayed callback = %d", again.status)
	}
}

func TestOIDCClaims(t *testing.T) {
	tests := []struct {
		name     string
		opts     func(*Options)
		claims   func(map[string]any)
		userinfo map[string]any
		want     Identity
		wantLog  string // must appear in the log when set
	}{
		{"email_verified as the string true (Cognito)", nil, func(c map[string]any) { c["email_verified"] = "true" }, nil,
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"platform"}}, ""},
		{"azp equal to client with several audiences", nil, func(c map[string]any) {
			c["aud"] = []string{testClientID, "other-client"}
			c["azp"] = testClientID
		}, nil, Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"platform"}}, ""},
		// Look-alike names are kept verbatim; authz's ASCII-only folding
		// keeps them from matching bindings for the real address.
		{"kelvin-sign email kept verbatim", nil, func(c map[string]any) { c["email"] = "\u212Aevin@example.com" }, nil,
			Identity{Subject: "user-1", Name: "\u212Aevin@example.com", Email: "\u212Aevin@example.com", Groups: []string{"platform"}}, ""},
		{"userinfo without sub ignored", nil, func(c map[string]any) { delete(c, "groups") },
			map[string]any{"groups": []string{"ops"}},
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com"}, "userinfo subject missing"},
		{"single string group", nil, func(c map[string]any) { c["groups"] = "ops" }, nil,
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"ops"}}, ""},
		{"no groups", nil, func(c map[string]any) { delete(c, "groups") }, nil,
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com"}, ""},
		{"groups from userinfo", nil, func(c map[string]any) { delete(c, "groups") },
			map[string]any{"sub": "user-1", "groups": []string{"ops", "x"}},
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"ops"}}, ""},
		{"email_verified from userinfo", nil, func(c map[string]any) { delete(c, "email_verified") },
			map[string]any{"sub": "user-1", "email_verified": true},
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"platform"}}, ""},
		{"userinfo for another subject ignored", nil, func(c map[string]any) { delete(c, "groups") },
			map[string]any{"sub": "user-2", "groups": []string{"ops"}},
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com"}, ""},
		{"id token wins over userinfo", nil, nil,
			map[string]any{"sub": "user-1", "groups": []string{"ops"}},
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"platform"}}, ""},
		{"overage", nil, func(c map[string]any) {
			// Microsoft Entra's shape for users in too many groups.
			delete(c, "groups")
			c["_claim_names"] = map[string]any{"groups": "src1"}
			c["_claim_sources"] = map[string]any{"src1": map[string]any{"endpoint": "https://graph.example/getMemberObjects"}}
		}, map[string]any{"sub": "user-1", "groups": []string{"ops"}},
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com"}, "groups overage"},
		{"overage hasgroups", nil, func(c map[string]any) {
			delete(c, "groups")
			c["hasgroups"] = true
		}, map[string]any{"sub": "user-1", "groups": []string{"ops"}},
			Identity{Subject: "user-1", Name: "jdoe@example.com", Email: "jdoe@example.com"}, "groups overage"},
		{"custom claims", func(o *Options) {
			o.OIDC.UsernameClaim = "preferred_username"
			o.OIDC.GroupsClaim = "roles"
		}, func(c map[string]any) {
			c["preferred_username"] = "jdoe"
			c["email_verified"] = false // not required when the username is not the email
			c["roles"] = []string{"ops"}
		}, nil,
			Identity{Subject: "user-1", Name: "jdoe", Email: "jdoe@example.com", Groups: []string{"ops"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idp := newFakeIDP(t)
			idp.claims = tt.claims
			idp.userinfo = tt.userinfo
			var logs bytes.Buffer
			o := idp.options("")
			o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			if tt.opts != nil {
				tt.opts(&o)
			}
			h := app(mustNew(t, o))
			res := idp.flow(t, h, "/", nil)
			if res.callback.status != http.StatusSeeOther {
				t.Fatalf("callback = %d %s\n%s", res.callback.status, res.callback.body, logs.String())
			}
			id, ok := whoami(t, h, res.callback.cookies[sessionCookie])
			if diff := cmp.Diff(tt.want, id); !ok || diff != "" {
				t.Errorf("identity ok=%v (-want +got):\n%s", ok, diff)
			}
			if tt.wantLog != "" && !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("log lacks %q:\n%s", tt.wantLog, logs.String())
			}
		})
	}
}

func TestOIDCSessionTooLarge(t *testing.T) {
	idp := newFakeIDP(t)
	groups := make([]string, 300)
	for i := range groups {
		groups[i] = fmt.Sprintf("team-with-a-long-name-%03d", i)
	}
	idp.claims = func(c map[string]any) { c["groups"] = groups }
	o := idp.options("")
	o.SessionGroups = groups
	a := mustNew(t, o)
	var reasons []Failure
	mux := http.NewServeMux()
	mux.Handle("POST /login", a.LoginHandler(nil))
	mux.Handle("GET /auth/callback", a.CallbackHandler(func(w http.ResponseWriter, _ *http.Request, status int, reason Failure) {
		reasons = append(reasons, reason)
		w.WriteHeader(status)
	}))
	res := idp.flow(t, a.Middleware(mux), "/", nil)
	if res.callback.status != http.StatusInternalServerError || res.callback.cookies[sessionCookie] != nil {
		t.Errorf("callback = %d, session=%v", res.callback.status, res.callback.cookies[sessionCookie] != nil)
	}
	if diff := cmp.Diff([]Failure{FailSessionTooLarge}, reasons); diff != "" {
		t.Errorf("reasons (-want +got):\n%s", diff)
	}
}

func TestOIDCDiscovery(t *testing.T) {
	idp := newFakeIDP(t)
	tests := []struct {
		name    string
		edit    func(*Options)
		wantErr string
	}{
		{"unreachable issuer", func(o *Options) { o.OIDC.Issuer = "http://127.0.0.1:1" }, "discovery"},
		{"issuer mismatch", func(o *Options) { o.OIDC.Issuer = idp.srv.URL + "/" + "realm" }, "discovery"},
		{"openid scope added", func(o *Options) { o.OIDC.Scopes = []string{"email"} }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := idp.options("")
			tt.edit(&o)
			a, err := New(t.Context(), o)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff([]string{"openid", "email"}, a.oidc.oauth.Scopes); diff != "" {
					t.Errorf("scopes (-want +got):\n%s", diff)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
