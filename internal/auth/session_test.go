package auth

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func mustJar(t *testing.T, o SessionOptions, base string, groups []string, now func() time.Time) *cookieJar {
	t.Helper()
	j, err := newCookieJar(o, base, groups, now)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// issue writes a session with j and returns the cookie.
func issue(t *testing.T, j *cookieJar, id Identity, version string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	if err := j.setSession(w, id, version); err != nil {
		t.Fatal(err)
	}
	cs := w.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cs))
	}
	return cs[0]
}

func read(j *cookieJar, c *http.Cookie) (sessionData, bool, bool) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return j.readSession(r)
}

func TestSessionRoundTrip(t *testing.T) {
	clk := newClock()
	j := mustJar(t, testSession(), "", []string{"platform", "ops"}, clk.now)
	id := Identity{Subject: "sub-1", Name: "jdoe@example.com", Email: "jdoe@example.com", Groups: []string{"platform", "unrelated", "ops"}}
	c := issue(t, j, id, "v1")

	got, ok, present := read(j, c)
	if !ok || !present {
		t.Fatalf("readSession ok=%v present=%v", ok, present)
	}
	want := sessionData{
		Subject: "sub-1", Name: "jdoe@example.com", Email: "jdoe@example.com",
		Groups:  []string{"platform", "ops"}, // only groups referenced by bindings
		Version: "v1",
		Expires: clk.now().Add(12 * time.Hour).Unix(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("session (-want +got):\n%s", diff)
	}
	if strings.Contains(c.Value, "jdoe") {
		t.Error("cookie value is not encrypted")
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		insecure   bool
		ttl        time.Duration
		wantPath   string
		wantSecure bool
		wantMaxAge int
	}{
		{"defaults", "", false, 0, "/", true, 12 * 3600},
		{"base path", "/asynq", false, time.Hour, "/asynq", true, 3600},
		{"insecure for local http", "", true, 0, "/", false, 12 * 3600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := testSession()
			o.Insecure = tt.insecure
			o.TTL = tt.ttl
			c := issue(t, mustJar(t, o, tt.base, nil, time.Now), Identity{Name: "u"}, "")
			got := fmt.Sprintf("%s path=%s secure=%v httponly=%v samesite=%v maxage=%d",
				c.Name, c.Path, c.Secure, c.HttpOnly, c.SameSite, c.MaxAge)
			want := fmt.Sprintf("asynqui_session path=%s secure=%v httponly=true samesite=%v maxage=%d",
				tt.wantPath, tt.wantSecure, http.SameSiteLaxMode, tt.wantMaxAge)
			if got != want {
				t.Errorf("cookie\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestSessionRejects(t *testing.T) {
	t0 := time.Now()
	at := func(d time.Duration) func() time.Time { return func() time.Time { return t0.Add(d) } }
	j := mustJar(t, testSession(), "", nil, at(0))
	good := issue(t, j, Identity{Name: "u"}, "")

	tamper := func(f func(string) string) *http.Cookie {
		c := *good
		c.Value = f(c.Value)
		return &c
	}
	flipped := tamper(func(v string) string {
		b := []byte(v)
		i := len(b) / 2
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	})
	// A flow cookie value replayed under the session name must not decode.
	w := httptest.NewRecorder()
	if err := j.setFlow(w, flowData{State: "s"}); err != nil {
		t.Fatal(err)
	}
	flow := w.Result().Cookies()[0]
	swapped := &http.Cookie{Name: sessionCookie, Value: flow.Value}

	tests := []struct {
		name        string
		keys        KeyPair
		cookie      *http.Cookie
		advance     time.Duration // reader's clock relative to issue time
		wantOK      bool
		wantPresent bool
	}{
		{"valid", testKeys(1), good, 0, true, true},
		{"valid just before expiry", testKeys(1), good, 12*time.Hour - time.Second, true, true},
		{"no cookie", testKeys(1), nil, 0, false, false},
		{"flipped byte", testKeys(1), flipped, 0, false, true},
		{"truncated", testKeys(1), tamper(func(v string) string { return v[:len(v)-4] }), 0, false, true},
		{"garbage", testKeys(1), tamper(func(string) string { return "not-a-cookie" }), 0, false, true},
		{"other keys", testKeys(7), good, 0, false, true},
		{"flow cookie as session", testKeys(1), swapped, 0, false, true},
		{"exactly at expiry", testKeys(1), good, 12 * time.Hour, false, true},
		{"expired", testKeys(1), good, 12*time.Hour + time.Second, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := mustJar(t, SessionOptions{KeyPair: tt.keys}, "", nil, at(tt.advance))
			_, ok, present := read(reader, tt.cookie)
			if ok != tt.wantOK || present != tt.wantPresent {
				t.Errorf("readSession ok=%v present=%v; want %v, %v", ok, present, tt.wantOK, tt.wantPresent)
			}
		})
	}
}

func TestSessionKeyRotation(t *testing.T) {
	oldJar := mustJar(t, SessionOptions{KeyPair: testKeys(1)}, "", nil, time.Now)
	c := issue(t, oldJar, Identity{Name: "u"}, "")

	rotated := mustJar(t, SessionOptions{KeyPair: testKeys(5), PreviousKeys: []KeyPair{testKeys(1)}}, "", nil, time.Now)
	if _, ok, _ := read(rotated, c); !ok {
		t.Error("previous key no longer decodes")
	}
	if _, ok, _ := read(oldJar, issue(t, rotated, Identity{Name: "u"}, "")); ok {
		t.Error("new cookies are not encoded with the current key")
	}
	dropped := mustJar(t, SessionOptions{KeyPair: testKeys(5)}, "", nil, time.Now)
	if _, ok, _ := read(dropped, c); ok {
		t.Error("cookie decoded after its key was removed")
	}
}

func TestSessionTooLarge(t *testing.T) {
	var groups []string
	for i := range 300 {
		groups = append(groups, fmt.Sprintf("group-with-a-long-name-%03d", i))
	}
	j := mustJar(t, testSession(), "", groups, time.Now)
	w := httptest.NewRecorder()
	err := j.setSession(w, Identity{Name: "u", Groups: groups}, "")
	if err == nil || !strings.Contains(err.Error(), errSessionTooLarge.Error()) {
		t.Fatalf("setSession error = %v, want errSessionTooLarge", err)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("oversized cookie was written")
	}
	// The same groups are fine when no binding references them.
	issue(t, mustJar(t, testSession(), "", nil, time.Now), Identity{Name: "u", Groups: groups}, "")
}

func TestSessionKeyValidation(t *testing.T) {
	k32, k64, k16 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{1}, 64), bytes.Repeat([]byte{1}, 16)
	tests := []struct {
		name    string
		o       SessionOptions
		wantErr string
	}{
		{"32/32", SessionOptions{KeyPair: KeyPair{k32, k32}}, ""},
		{"64/32", SessionOptions{KeyPair: KeyPair{k64, k32}}, ""},
		{"missing", SessionOptions{}, "hash key must be 32 or 64 bytes, got 0"},
		{"short hash", SessionOptions{KeyPair: KeyPair{k16, k32}}, "hash key must be 32 or 64 bytes"},
		{"16-byte block", SessionOptions{KeyPair: KeyPair{k32, k16}}, "block key must be 32 bytes, got 16"},
		{"64-byte block", SessionOptions{KeyPair: KeyPair{k32, k64}}, "block key must be 32 bytes"},
		{"bad previous", SessionOptions{KeyPair: KeyPair{k32, k32}, PreviousKeys: []KeyPair{{k32, k16}}}, "previous key 0: block key"},
		{"negative ttl", SessionOptions{KeyPair: KeyPair{k32, k32}, TTL: -time.Second}, "ttl must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newCookieJar(tt.o, "", nil, time.Now)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestFlowCookie(t *testing.T) {
	clk := newClock()
	j := mustJar(t, testSession(), "/asynq", nil, clk.now)
	w := httptest.NewRecorder()
	if err := j.setFlow(w, flowData{State: "st", Nonce: "n", Verifier: "v", Next: "/asynq/c/main"}); err != nil {
		t.Fatal(err)
	}
	c := w.Result().Cookies()[0]
	if c.Name != flowCookie || c.Path != "/asynq/auth/callback" || c.MaxAge != 600 ||
		!c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("flow cookie attributes: %+v", c)
	}

	take := func() (flowData, *http.Cookie, error) {
		r := httptest.NewRequest(http.MethodGet, "/asynq/auth/callback", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		f, err := j.takeFlow(w, r)
		return f, w.Result().Cookies()[0], err
	}
	f, cleared, err := take()
	if err != nil || f.State != "st" || f.Nonce != "n" || f.Verifier != "v" || f.Next != "/asynq/c/main" {
		t.Errorf("takeFlow = %+v, %v", f, err)
	}
	if cleared.MaxAge >= 0 || cleared.Path != "/asynq/auth/callback" {
		t.Errorf("flow cookie not cleared: %+v", cleared)
	}

	clk.add(10*time.Minute + time.Second)
	if _, _, err := take(); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired flow error = %v", err)
	}
}
