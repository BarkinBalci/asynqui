package auth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/crypto/bcrypt"
)

func TestBasicVerify(t *testing.T) {
	b, err := newBasic(BasicOptions{Users: []BasicUser{
		{Username: "jdoe", PasswordHash: mustHash(t, "secret", bcrypt.MinCost), Groups: []string{"payments"}},
		{Username: "ops", PasswordHash: mustHash(t, "hunter2", bcrypt.MinCost+1)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Unknown users are compared against a dummy hash at the highest
	// configured cost, so they cost the same bcrypt work as known users.
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

func TestBasicValidation(t *testing.T) {
	h := mustHash(t, "x", bcrypt.MinCost)
	tests := []struct {
		name    string
		users   []BasicUser
		wantErr string
	}{
		{"no users", nil, "no users configured"},
		{"empty username", []BasicUser{{PasswordHash: h}}, "user 0: empty username"},
		{"duplicate", []BasicUser{{Username: "a", PasswordHash: h}, {Username: "a", PasswordHash: h}}, `duplicate username "a"`},
		{"plaintext password", []BasicUser{{Username: "a", PasswordHash: "secret"}}, `user "a": invalid bcrypt password hash`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newBasic(BasicOptions{Users: tt.users})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Error("error leaks the configured hash")
			}
		})
	}
}

func basicAuth(t *testing.T, users []BasicUser, now func() time.Time, base string) *Authenticator {
	t.Helper()
	return mustNew(t, Options{
		Provider: KindBasic,
		Session:  testSession(),
		Basic:    BasicOptions{Users: users},
		BasePath: base,
		Now:      now,
	})
}

func login(form url.Values) req {
	return req{method: http.MethodPost, target: "/login", form: form, remote: "192.0.2.1:1234"}
}

func TestBasicLogin(t *testing.T) {
	users := []BasicUser{{Username: "jdoe", PasswordHash: mustHash(t, "secret", bcrypt.MinCost), Groups: []string{"payments"}}}
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
	a := basicAuth(t, []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}, nil, "")
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
	clk := newClock()
	h := app(basicAuth(t, []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}, clk.now, ""))
	attempt := func(remote, password string) resp {
		return do(t, h, req{method: http.MethodPost, target: "/login", remote: remote,
			form: url.Values{"username": {"u"}, "password": {password}}})
	}
	for i := range loginAttempts {
		if res := attempt("192.0.2.1:1", "wrong"); res.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, res.status)
		}
	}
	res := attempt("192.0.2.1:2", "p")
	if res.status != http.StatusTooManyRequests || res.header.Get("Retry-After") == "" {
		t.Fatalf("6th attempt = %d Retry-After=%q, want 429 with Retry-After", res.status, res.header.Get("Retry-After"))
	}
	if res := attempt("192.0.2.2:1", "p"); res.status != http.StatusSeeOther {
		t.Errorf("other IP = %d, want 303", res.status)
	}
	// IPv6 clients share a limit per /64.
	for range loginAttempts {
		attempt("[2001:db8::1]:1", "wrong")
	}
	if res := attempt("[2001:db8::2]:1", "p"); res.status != http.StatusTooManyRequests {
		t.Errorf("same /64 = %d, want 429", res.status)
	}
	clk.add(loginWindow + time.Second)
	if res := attempt("192.0.2.1:3", "p"); res.status != http.StatusSeeOther {
		t.Errorf("after window = %d, want 303", res.status)
	}
}

func TestBasicSessionRecheckedAgainstConfig(t *testing.T) {
	hash := mustHash(t, "p", bcrypt.MinCost)
	before := basicAuth(t, []BasicUser{{Username: "u", PasswordHash: hash, Groups: []string{"old"}}}, nil, "")
	c := do(t, app(before), login(url.Values{"username": {"u"}, "password": {"p"}})).cookies[sessionCookie]

	tests := []struct {
		name   string
		users  []BasicUser
		want   Identity
		wantOK bool
	}{
		{"unchanged", []BasicUser{{Username: "u", PasswordHash: hash, Groups: []string{"old"}}}, Identity{Subject: "u", Name: "u", Groups: []string{"old"}}, true},
		{"groups come from config", []BasicUser{{Username: "u", PasswordHash: hash, Groups: []string{"new"}}}, Identity{Subject: "u", Name: "u", Groups: []string{"new"}}, true},
		{"user removed", []BasicUser{{Username: "other", PasswordHash: hash}}, Identity{}, false},
		{"password changed", []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p2", bcrypt.MinCost)}}, Identity{}, false},
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
	users := []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
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
