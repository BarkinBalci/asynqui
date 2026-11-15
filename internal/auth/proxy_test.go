package auth

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/config"
)

func proxyAuth(t *testing.T, o config.Proxy) *Authenticator {
	t.Helper()
	return mustNew(t, testOpts{Provider: config.ProviderProxy, Proxy: o})
}

func TestProxyIdentify(t *testing.T) {
	trusted := config.Proxy{
		GroupsHeader:   "X-Forwarded-Groups",
		TrustedProxies: []string{"10.0.0.0/8", "192.0.2.7", "2001:db8::/32", "::ffff:172.16.0.0/108"},
	}
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Add(kv[i], kv[i+1])
		}
		return h
	}
	tests := []struct {
		name   string
		opts   config.Proxy
		remote string
		header http.Header
		want   Identity
		wantOK bool
	}{
		{"trusted CIDR", trusted, "10.1.2.3:5555", hdr("X-Forwarded-User", "jdoe"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"trusted bare IP", trusted, "192.0.2.7:1", hdr("X-Forwarded-User", "jdoe"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"trusted IPv6", trusted, "[2001:db8::5]:1", hdr("X-Forwarded-User", "jdoe"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"trusted IPv6 with zone", trusted, "[2001:db8::5%eth0]:1", hdr("X-Forwarded-User", "jdoe"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"v4-mapped remote", trusted, "[::ffff:10.0.0.1]:1", hdr("X-Forwarded-User", "jdoe"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"v4-mapped CIDR", trusted, "172.16.9.9:1", hdr("X-Forwarded-User", "jdoe"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"groups parsed", trusted, "10.0.0.1:1",
			hdr("X-Forwarded-User", " jdoe ", "X-Forwarded-Groups", " platform, ,ops,,dev"),
			Identity{Subject: "jdoe", Name: "jdoe", Groups: []string{"platform", "ops", "dev"}}, true},
		// net/http canonicalises header names.
		{"lower-case header name", trusted, "10.0.0.1:1", hdr("x-forwarded-user", "jdoe"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"underscore is a different header", trusted, "10.0.0.1:1", hdr("X_Forwarded_User", "jdoe"), Identity{}, false},

		// A proxy that appends rather than replaces would pass the
		// client's copy along with its own: fail closed.
		{"two user lines", trusted, "10.0.0.1:1", hdr("X-Forwarded-User", "admin", "X-Forwarded-User", "jdoe"), Identity{}, false},
		{"two groups lines", trusted, "10.0.0.1:1",
			hdr("X-Forwarded-User", "jdoe", "X-Forwarded-Groups", "platform", "X-Forwarded-Groups", "dev"), Identity{}, false},
		{"two groups lines ignored when groups are off", config.Proxy{TrustedProxies: []string{"10.0.0.0/8"}}, "10.0.0.1:1",
			hdr("X-Forwarded-User", "jdoe", "X-Forwarded-Groups", "a", "X-Forwarded-Groups", "b"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"groups header disabled", config.Proxy{TrustedProxies: []string{"10.0.0.0/8"}}, "10.0.0.1:1",
			hdr("X-Forwarded-User", "jdoe", "X-Forwarded-Groups", "platform"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"custom user header", config.Proxy{UserHeader: "Remote-User", TrustedProxies: []string{"10.0.0.0/8"}}, "10.0.0.1:1",
			hdr("Remote-User", "jdoe", "X-Forwarded-User", "admin"), Identity{Subject: "jdoe", Name: "jdoe"}, true},

		{"untrusted remote with headers", trusted, "203.0.113.9:1", hdr("X-Forwarded-User", "admin", "X-Forwarded-Groups", "platform"), Identity{}, false},
		{"untrusted remote spoofing X-Forwarded-For", trusted, "203.0.113.9:1", hdr("X-Forwarded-User", "admin", "X-Forwarded-For", "10.0.0.1"), Identity{}, false},
		{"just outside bare IP", trusted, "192.0.2.8:1", hdr("X-Forwarded-User", "jdoe"), Identity{}, false},
		{"trusted missing user header", trusted, "10.0.0.1:1", hdr("X-Forwarded-Groups", "platform"), Identity{}, false},
		{"trusted empty user header", trusted, "10.0.0.1:1", hdr("X-Forwarded-User", "  "), Identity{}, false},
		{"unparseable remote", trusted, "garbage", hdr("X-Forwarded-User", "jdoe"), Identity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := app(proxyAuth(t, tt.opts))
			res := do(t, h, req{target: "/whoami", remote: tt.remote, header: tt.header})
			if !tt.wantOK {
				if res.status != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401", res.status)
				}
				if len(res.cookies) != 0 {
					t.Error("proxy set a cookie")
				}
				return
			}
			if res.status != http.StatusOK {
				t.Fatalf("not authenticated (status %d)", res.status)
			}
			if diff := cmp.Diff(tt.want, decodeIdentity(t, res.body)); diff != "" {
				t.Errorf("identity (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProxyNewErrors(t *testing.T) {
	for _, cidr := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0.1/"} {
		_, err := newTest(t.Context(), testOpts{Provider: config.ProviderProxy, Proxy: config.Proxy{TrustedProxies: []string{cidr}}})
		if err == nil || !strings.Contains(err.Error(), cidr) {
			t.Errorf("TrustedProxies %q: error = %v", cidr, err)
		}
	}
}

func TestProxyTrustedPrefixes(t *testing.T) {
	tests := []struct {
		cidr          string
		wantWarn      bool
		trustsV4      bool // 203.0.113.1
		trustsV6      bool // 2001:db8::1
		trustsPrivate bool // 10.9.9.9
	}{
		{"0.0.0.0/0", true, true, false, true},
		{"::/0", true, false, true, false},
		{"::ffff:0:0/96", true, true, false, true}, // every IPv4 address, mapped
		{"::ffff:10.0.0.0/104", false, false, false, true},
		{"::ffff:10.0.0.0/90", false, false, false, false}, // shorter than the mapping: matches no unmapped IPv4
		{"10.0.0.1/8", false, false, false, true},          // host bits masked
		{"10.0.0.0/8", false, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			var logs bytes.Buffer
			p, err := newProxy(t.Context(), config.Proxy{TrustedProxies: []string{tt.cidr}},
				slog.New(slog.NewTextHandler(&logs, nil)), time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), "covers every address"); got != tt.wantWarn {
				t.Errorf("warned = %v, want %v: %s", got, tt.wantWarn, logs.String())
			}
			got := [3]bool{p.trustedPeer("203.0.113.1:1"), p.trustedPeer("[2001:db8::1]:1"), p.trustedPeer("10.9.9.9:1")}
			if want := [3]bool{tt.trustsV4, tt.trustsV6, tt.trustsPrivate}; got != want {
				t.Errorf("trusts (v4, v6, private) = %v, want %v", got, want)
			}
		})
	}
}

func TestProxyWarnings(t *testing.T) {
	clk := newClock()
	var buf bytes.Buffer
	a := mustNew(t, testOpts{Provider: config.ProviderProxy, Proxy: config.Proxy{TrustedProxies: []string{"10.0.0.0/8"}, GroupsHeader: "X-Forwarded-Groups"},
		Logger: slog.New(slog.NewTextHandler(&buf, nil)), Now: clk.now})
	h := app(a)
	spoof := func(hdr http.Header) {
		do(t, h, req{target: "/whoami", remote: "203.0.113.1:1", header: hdr})
	}
	count := func() int { return strings.Count(buf.String(), "untrusted peer") }

	spoof(http.Header{"X-Forwarded-User": {"admin"}})
	if count() != 1 {
		t.Fatalf("no spoofing warning: %q", buf.String())
	}
	// An attacker can trigger this at will, so it is throttled.
	spoof(http.Header{"X-Forwarded-User": {"admin"}})
	spoof(http.Header{"X-Forwarded-Groups": {"platform"}})
	if count() != 1 {
		t.Errorf("warning not throttled: %d", count())
	}
	clk.add(time.Minute)
	spoof(http.Header{"X-Forwarded-Groups": {"platform"}})
	if count() != 2 {
		t.Errorf("groups-only spoof after a minute not logged: %d", count())
	}

	buf.Reset()
	do(t, h, req{target: "/whoami", remote: "10.0.0.1:1", header: http.Header{"X-Forwarded-User": {"a", "b"}}})
	if !strings.Contains(buf.String(), "repeated") {
		t.Errorf("repeated header not logged: %q", buf.String())
	}
}

func TestProxyLogout(t *testing.T) {
	tests := []struct {
		name         string
		logoutURL    string
		wantVisible  bool
		wantStatus   int
		wantLocation string
	}{
		{"without logout url", "", false, http.StatusNotFound, ""},
		{"with logout url", "https://sso.example.com/logout", true, http.StatusSeeOther, "https://sso.example.com/logout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := proxyAuth(t, config.Proxy{TrustedProxies: []string{"10.0.0.0/8"}, LogoutURL: tt.logoutURL})
			if a.SignOutVisible() != tt.wantVisible {
				t.Errorf("SignOutVisible = %v", a.SignOutVisible())
			}
			res := do(t, app(a), req{method: http.MethodPost, target: "/logout"})
			if res.status != tt.wantStatus || res.header.Get("Location") != tt.wantLocation {
				t.Errorf("logout = %d %q", res.status, res.header.Get("Location"))
			}
		})
	}
}
