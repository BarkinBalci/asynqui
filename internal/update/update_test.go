package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// server answers every request with status and body.
func server(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newChecker(t *testing.T, version string, srv *httptest.Server) *Checker {
	t.Helper()
	c, err := New(Options{Version: version, Client: srv.Client(), URL: srv.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		version string
		status  int
		body    string
		want    string // latest tag; "" for none
	}{
		{"newer", "0.1.0", 200, `{"tag_name":"v0.2.0","html_url":"x"}`, "v0.2.0"},
		{"newer patch, v-prefixed running version", "v0.1.0", 200, `{"tag_name":"v0.1.1"}`, "v0.1.1"},
		{"newer minor, numerically", "1.9.0", 200, `{"tag_name":"v1.10.0"}`, "v1.10.0"},
		{"same", "0.2.0", 200, `{"tag_name":"v0.2.0"}`, ""},
		{"older", "0.3.0", 200, `{"tag_name":"v0.2.9"}`, ""},
		{"prerelease flag ignored", "0.1.0", 200, `{"tag_name":"v0.2.0","prerelease":true}`, ""},
		{"prerelease tag ignored", "0.1.0", 200, `{"tag_name":"v0.2.0-rc.1"}`, ""},
		{"draft ignored", "0.1.0", 200, `{"tag_name":"v0.2.0","draft":true}`, ""},
		{"tag not semver", "0.1.0", 200, `{"tag_name":"latest"}`, ""},
		{"no tag", "0.1.0", 200, `{}`, ""},
		{"malformed JSON", "0.1.0", 200, `{"tag_name":`, ""},
		{"not found", "0.1.0", 404, `{"message":"Not Found"}`, ""},
		{"rate limited", "0.1.0", 403, `{"message":"API rate limit exceeded"}`, ""},
		// Hostile tags never reach Latest: only a plain semantic version does.
		{"tag with a path", "0.1.0", 200, `{"tag_name":"v9.9.9/../../evil"}`, ""},
		{"tag with a scheme", "0.1.0", 200, `{"tag_name":"javascript:alert(1)//9.9.9"}`, ""},
		{"tag with markup in build metadata", "0.1.0", 200, `{"tag_name":"v9.9.9+\"><script>"}`, ""},
		{"tag with an escaped slash", "0.1.0", 200, `{"tag_name":"v9.9.9%2F.."}`, ""},
		{"tag with a newline", "0.1.0", 200, `{"tag_name":"v9.9.9\n"}`, ""},
		{"tag with spaces", "0.1.0", 200, `{"tag_name":" v9.9.9 "}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newChecker(t, tt.version, server(t, tt.status, tt.body))
			c.check(t.Context())
			if v, ok := c.Latest(); v != tt.want || ok != (tt.want != "") {
				t.Fatalf("Latest() = %q, %v; want %q", v, ok, tt.want)
			}
		})
	}
}

func TestCheckRequest(t *testing.T) {
	type seen struct {
		method, path, query string
		header              http.Header
	}
	reqs := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs <- seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()}
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.0"}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{Version: "0.1.0", Client: srv.Client(), URL: srv.URL + "/repos/BarkinBalci/asynqui/releases/latest"})
	if err != nil {
		t.Fatal(err)
	}
	c.check(t.Context())
	var got seen
	select {
	case got = <-reqs:
	default:
		t.Fatal("no request")
	}
	if got.method != http.MethodGet || got.path != "/repos/BarkinBalci/asynqui/releases/latest" || got.query != "" {
		t.Errorf("request = %s %s?%s", got.method, got.path, got.query)
	}
	if ua := got.header.Get("User-Agent"); ua != "asynqui/0.1.0" {
		t.Errorf("User-Agent = %q", ua)
	}
	for h := range got.header {
		switch h {
		case "User-Agent", "Accept", "X-Github-Api-Version", "Accept-Encoding":
		default:
			t.Errorf("unexpected header %s: %q", h, got.header.Get(h))
		}
	}
}

func TestCheckFailureKeepsResult(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
	}))
	t.Cleanup(srv.Close)
	c := newChecker(t, "0.1.0", srv)
	c.check(t.Context())
	fail.Store(true)
	c.check(t.Context())
	if v, ok := c.Latest(); !ok || v != "v0.2.0" {
		t.Errorf("Latest() = %q, %v after a failed check", v, ok)
	}
}

// TestCheckClears checks that a newer release seen earlier is forgotten
// once the latest release is no longer newer (it was withdrawn, or the
// instance runs it now).
func TestCheckClears(t *testing.T) {
	var tag atomic.Value
	tag.Store("v0.2.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"` + tag.Load().(string) + `"}`))
	}))
	t.Cleanup(srv.Close)
	c := newChecker(t, "0.1.0", srv)
	c.check(t.Context())
	if _, ok := c.Latest(); !ok {
		t.Fatal("no update after the first check")
	}
	tag.Store("v0.1.0")
	c.check(t.Context())
	if v, ok := c.Latest(); ok || v != "" {
		t.Errorf("Latest() = %q, %v after the release went away", v, ok)
	}
}

// TestRunCancelMidFetch cancels Run while a request hangs: Run returns at
// once, well before the check's own timeout, and nothing is recorded.
func TestRunCancelMidFetch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	c, err := New(Options{
		Version: "0.1.0",
		Client:  srv.Client(),
		URL:     srv.URL,
		Timeout: time.Minute,
		After: func(time.Duration) <-chan time.Time {
			t.Error("Run waited for the next check after being canceled")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, ok := c.Latest(); ok {
		t.Error("update recorded from a canceled check")
	}
}

func TestCheckTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	c, err := New(Options{Version: "0.1.0", Client: srv.Client(), URL: srv.URL, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c.check(t.Context())
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("check took %s", d)
	}
	if _, ok := c.Latest(); ok {
		t.Error("update after a timeout")
	}
}

func TestDefaults(t *testing.T) {
	c, err := New(Options{Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if c.url != DefaultURL || c.timeout != 5*time.Second || c.client == nil || c.after == nil || c.logger == nil {
		t.Errorf("defaults not applied: url %q timeout %s", c.url, c.timeout)
	}
}

func TestNewNotRelease(t *testing.T) {
	for _, v := range []string{
		"dev", "", "v1.2", "1.2.3.4", "abc1234", "v01.2.3",
		// A prerelease part: release candidates, git describe, dirty and
		// snapshot builds.
		"0.2.0-rc.1", "v0.1.0-3-gabc1234", "v0.1.0-3-gabc1234-dirty", "v0.1.0-dirty", "0.0.0-SNAPSHOT-0cf57cb",
		// Pseudo-versions and build metadata: builds in a checkout.
		"v0.1.1-0.20261114015959-c1a81a5e0d4f+dirty", "v0.1.0+dirty", "1.0.0+abc",
	} {
		if c, err := New(Options{Version: v}); !errors.Is(err, ErrNotRelease) || c != nil {
			t.Errorf("New(%q) = %v, %v; want ErrNotRelease", v, c, err)
		}
	}
}

func TestRun(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.0"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
	}))
	t.Cleanup(srv.Close)

	tick := make(chan time.Time)
	waits := make(chan time.Duration, 10)
	c, err := New(Options{
		Version: "0.1.0",
		Client:  srv.Client(),
		URL:     srv.URL,
		After: func(d time.Duration) <-chan time.Time {
			waits <- d
			return tick
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()

	// The first check runs at once, then Run waits 24h.
	if d := <-waits; d != 24*time.Hour {
		t.Fatalf("wait = %s", d)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d before the first tick", hits.Load())
	}
	if _, ok := c.Latest(); ok {
		t.Fatal("update after the first check")
	}
	tick <- time.Now()
	<-waits
	if hits.Load() != 2 {
		t.Fatalf("hits = %d after one tick", hits.Load())
	}
	if v, ok := c.Latest(); !ok || v != "v0.2.0" {
		t.Fatalf("Latest() = %q, %v after the second check", v, ok)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
