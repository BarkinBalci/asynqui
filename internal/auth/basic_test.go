package auth

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/crypto/bcrypt"

	"github.com/BarkinBalci/asynqui/internal/config"
)

func TestBasicVerify(t *testing.T) {
	pw72 := strings.Repeat("a", maxPasswordBytes)
	b, err := newBasic(config.Basic{Users: []config.BasicUser{
		{Username: "jdoe", PasswordHash: mustHash(t, "secret", bcrypt.MinCost+1), Groups: []string{"payments"}},
		{Username: "ops", PasswordHash: mustHash(t, "hunter2", bcrypt.MinCost+1)},
		{Username: "long", PasswordHash: mustHash(t, pw72, bcrypt.MinCost+1)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Unknown users are compared against a dummy hash at the configured
	// cost, so they cost the same bcrypt work as known users.
	if cost, err := bcrypt.Cost(b.dummy); err != nil || cost != bcrypt.MinCost+1 {
		t.Errorf("dummy hash cost = %d, %v; want %d", cost, err, bcrypt.MinCost+1)
	}
	tests := []struct {
		name     string
		user     string
		password string
		want     Identity
		wantOK   bool
	}{
		{"valid", "jdoe", "secret", Identity{Subject: "jdoe", Name: "jdoe", Groups: []string{"payments"}}, true},
		{"valid no groups", "ops", "hunter2", Identity{Subject: "ops", Name: "ops"}, true},
		{"wrong password", "jdoe", "Secret", Identity{}, false},
		{"empty password", "jdoe", "", Identity{}, false},
		{"unknown user", "mallory", "secret", Identity{}, false},
		{"unknown user empty", "", "", Identity{}, false},
		{"username is case-sensitive", "JDoe", "secret", Identity{}, false},
		{"other user's password", "ops", "secret", Identity{}, false},
		{"exactly 72 bytes", "long", pw72, Identity{Subject: "long", Name: "long"}, true},
		// bcrypt ignores bytes past 72; accepting them would let any suffix
		// log in.
		{"72 bytes plus suffix", "long", pw72 + "-anything", Identity{}, false},
		{"73 bytes unknown user", "nobody", pw72 + "x", Identity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, ok := b.verify(tt.user, tt.password)
			if ok != tt.wantOK {
				t.Fatalf("verify ok = %v, want %v", ok, tt.wantOK)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("identity (-want +got):\n%s", diff)
			}
		})
	}
}

func basicAuth(t *testing.T, users []config.BasicUser, now func() time.Time, base string) *Authenticator {
	t.Helper()
	return mustNew(t, testOpts{
		Provider: config.ProviderBasic,
		Session:  testSession(),
		Basic:    config.Basic{Users: users},
		BasePath: base,
		Now:      now,
	})
}

func login(form url.Values) req {
	return req{method: http.MethodPost, target: "/login", form: form, remote: "192.0.2.1:1234"}
}

func TestBasicLogin(t *testing.T) {
	users := []config.BasicUser{{Username: "jdoe", PasswordHash: mustHash(t, "secret", bcrypt.MinCost), Groups: []string{"payments"}}}
	tests := []struct {
		name         string
		base         string
		form         url.Values
		wantStatus   int
		wantLocation string
		wantSession  bool
	}{
		{"success to root", "", url.Values{"username": {"jdoe"}, "password": {"secret"}}, 303, "/", true},
		{"success to next", "", url.Values{"username": {"jdoe"}, "password": {"secret"}, "next": {"/c/main?state=retry"}}, 303, "/c/main?state=retry", true},
		{"offsite next ignored", "", url.Values{"username": {"jdoe"}, "password": {"secret"}, "next": {"//evil.example/x"}}, 303, "/", true},
		{"base path root", "/asynq", url.Values{"username": {"jdoe"}, "password": {"secret"}}, 303, "/asynq/", true},
		{"next outside base path", "/asynq", url.Values{"username": {"jdoe"}, "password": {"secret"}, "next": {"/other"}}, 303, "/asynq/", true},
		{"wrong password", "", url.Values{"username": {"jdoe"}, "password": {"nope"}}, 401, "", false},
		{"unknown user", "", url.Values{"username": {"eve"}, "password": {"secret"}}, 401, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := app(basicAuth(t, users, nil, tt.base))
			res := do(t, h, login(tt.form))
			if res.status != tt.wantStatus || res.header.Get("Location") != tt.wantLocation {
				t.Fatalf("POST /login = %d %q, want %d %q", res.status, res.header.Get("Location"), tt.wantStatus, tt.wantLocation)
			}
			c, ok := res.cookies[sessionCookie]
			if ok != tt.wantSession {
				t.Fatalf("session cookie set = %v, want %v", ok, tt.wantSession)
			}
			if !ok {
				return
			}
			id, ok := whoami(t, h, c)
			want := Identity{Subject: "jdoe", Name: "jdoe", Groups: []string{"payments"}}
			if diff := cmp.Diff(want, id); !ok || diff != "" {
				t.Errorf("identity ok=%v (-want +got):\n%s", ok, diff)
			}
		})
	}
}

func TestBasicLoginFailFunc(t *testing.T) {
	a := basicAuth(t, []config.BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}, nil, "")
	var got []Failure
	h := a.LoginHandler(func(w http.ResponseWriter, _ *http.Request, status int, reason Failure) {
		got = append(got, reason)
		w.WriteHeader(status)
	})
	do(t, h, login(url.Values{"username": {"u"}, "password": {strings.Repeat("x", maxFormBytes)}}))
	for range loginAttempts + 1 {
		do(t, h, login(url.Values{"username": {"u"}, "password": {"x"}}))
	}
	want := []Failure{FailBadRequest, FailInvalidCredentials, FailInvalidCredentials, FailInvalidCredentials,
		FailInvalidCredentials, FailInvalidCredentials, FailTooManyAttempts}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("reasons (-want +got):\n%s", diff)
	}
}

func TestBasicRateLimit(t *testing.T) {
	hash := mustHash(t, "p", bcrypt.MinCost)
	users := []config.BasicUser{{Username: "u", PasswordHash: hash}, {Username: "v", PasswordHash: hash}}
	type try struct{ remote, user, password string }
	wrong := func(remote, user string) []try {
		out := make([]try, loginAttempts)
		for i := range out {
			out[i] = try{remote, user, "wrong"}
		}
		return out
	}
	tests := []struct {
		name    string
		before  []try         // all must get 401
		advance time.Duration // after before
		then    try
		want    int
	}{
		{"sixth attempt from one IP", wrong("192.0.2.1:1", "u"), 0, try{"192.0.2.1:2", "u", "p"}, http.StatusTooManyRequests},
		{"other IP, other user", wrong("192.0.2.1:1", "u"), 0, try{"192.0.2.2:1", "v", "p"}, http.StatusSeeOther},
		{"other IP, same user", wrong("192.0.2.1:1", "u"), 0, try{"192.0.2.2:1", "u", "p"}, http.StatusTooManyRequests},
		{"same user spread over many /64s", func() []try {
			var out []try
			for i := range loginAttempts {
				out = append(out, try{fmt.Sprintf("[2001:db8:1:%x::1]:1", i), "U", "wrong"})
			}
			return out
		}(), 0, try{"[2001:db8:1:ff::1]:1", "u", "p"}, http.StatusTooManyRequests},
		{"same /64 shares a limit", wrong("[2001:db8::1]:1", "u"), 0, try{"[2001:db8::2]:1", "v", "p"}, http.StatusTooManyRequests},
		{"shared proxy IP locks out everyone behind it", wrong("10.0.0.1:1", "u"), 0, try{"10.0.0.1:2", "v", "p"}, http.StatusTooManyRequests},
		{"after the window", wrong("192.0.2.1:1", "u"), loginWindow + time.Second, try{"192.0.2.1:3", "u", "p"}, http.StatusSeeOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newClock()
			h := app(basicAuth(t, users, clk.now, ""))
			attempt := func(a try) resp {
				return do(t, h, req{method: http.MethodPost, target: "/login", remote: a.remote,
					form: url.Values{"username": {a.user}, "password": {a.password}}})
			}
			for i, a := range tt.before {
				if res := attempt(a); res.status != http.StatusUnauthorized {
					t.Fatalf("attempt %d = %d, want 401", i+1, res.status)
				}
			}
			clk.add(tt.advance)
			res := attempt(tt.then)
			if res.status != tt.want {
				t.Fatalf("attempt = %d, want %d", res.status, tt.want)
			}
			if tt.want == http.StatusTooManyRequests && res.header.Get("Retry-After") == "" {
				t.Error("429 without Retry-After")
			}
		})
	}
}

func TestLimiterBounded(t *testing.T) {
	clk := newClock()
	l := newLimiter(loginAttempts, loginWindow, 100, clk.now)
	for i := range 2000 {
		l.allow(clientKey(fmt.Sprintf("[2001:db8:1:%x::1]:1", i)))
	}
	if n := l.len(); n != 100 {
		t.Errorf("limiter holds %d keys, want the cap 100", n)
	}
	// The most recent keys survive eviction; the oldest are gone.
	for range loginAttempts - 1 {
		l.allow(clientKey("[2001:db8:1:7cf::1]:1")) // key 1999
	}
	if ok, _ := l.allow(clientKey("[2001:db8:1:7cf::1]:1")); ok {
		t.Error("recent key lost its history")
	}
	// Stale entries are swept once a window has passed.
	clk.add(loginWindow + time.Second)
	l.allow("fresh")
	if n := l.len(); n != 1 {
		t.Errorf("after a window the limiter holds %d keys, want 1", n)
	}
}

func TestBasicLoginLogging(t *testing.T) {
	users := []config.BasicUser{{Username: "jdoe", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
	tests := []struct {
		name     string
		username string
		wantUser bool
	}{
		{"known user is logged", "jdoe", true},
		{"unknown user is not", "Hunter2-my-password!", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			a := mustNew(t, testOpts{Provider: config.ProviderBasic, Session: testSession(), Basic: config.Basic{Users: users},
				Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
			do(t, app(a), login(url.Values{"username": {tt.username}, "password": {"wrong"}}))
			if !strings.Contains(logs.String(), "login failed") {
				t.Fatalf("no failure logged: %s", logs.String())
			}
			if got := strings.Contains(logs.String(), tt.username); got != tt.wantUser {
				t.Errorf("username in log = %v, want %v: %s", got, tt.wantUser, logs.String())
			}
		})
	}
}

func TestBasicSessionRecheckedAgainstConfig(t *testing.T) {
	hash := mustHash(t, "p", bcrypt.MinCost)
	before := basicAuth(t, []config.BasicUser{{Username: "u", PasswordHash: hash, Groups: []string{"old"}}}, nil, "")
	c := do(t, app(before), login(url.Values{"username": {"u"}, "password": {"p"}})).cookies[sessionCookie]

	tests := []struct {
		name   string
		users  []config.BasicUser
		want   Identity
		wantOK bool
	}{
		{"unchanged", []config.BasicUser{{Username: "u", PasswordHash: hash, Groups: []string{"old"}}}, Identity{Subject: "u", Name: "u", Groups: []string{"old"}}, true},
		{"groups come from config", []config.BasicUser{{Username: "u", PasswordHash: hash, Groups: []string{"new"}}}, Identity{Subject: "u", Name: "u", Groups: []string{"new"}}, true},
		{"user removed", []config.BasicUser{{Username: "other", PasswordHash: hash}}, Identity{}, false},
		{"password changed", []config.BasicUser{{Username: "u", PasswordHash: mustHash(t, "p2", bcrypt.MinCost)}}, Identity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := app(basicAuth(t, tt.users, nil, ""))
			id, ok := whoami(t, h, c)
			if ok != tt.wantOK {
				t.Fatalf("authenticated = %v, want %v", ok, tt.wantOK)
			}
			if diff := cmp.Diff(tt.want, id); diff != "" {
				t.Errorf("identity (-want +got):\n%s", diff)
			}
			if !ok {
				res := do(t, h, req{target: "/whoami", cookies: []*http.Cookie{c}})
				if cl := res.cookies[sessionCookie]; cl == nil || cl.MaxAge >= 0 {
					t.Error("rejected session cookie was not cleared")
				}
			}
		})
	}
}

func TestLogout(t *testing.T) {
	users := []config.BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
	h := app(basicAuth(t, users, nil, "/asynq"))
	res := do(t, h, req{method: http.MethodPost, target: "/logout"})
	c := res.cookies[sessionCookie]
	if res.status != http.StatusSeeOther || res.header.Get("Location") != "/asynq/login" {
		t.Errorf("logout = %d %q", res.status, res.header.Get("Location"))
	}
	if c == nil || c.MaxAge >= 0 || c.Path != "/asynq" {
		t.Errorf("session not cleared: %+v", c)
	}
}
