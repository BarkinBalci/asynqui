package auth

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func proxyAuth(t *testing.T, o ProxyOptions) *Authenticator {
	t.Helper()
	return mustNew(t, Options{Provider: KindProxy, Proxy: o})
}

func TestProxyIdentify(t *testing.T) {
	trusted := ProxyOptions{
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
		opts   ProxyOptions
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
			hdr("X-Forwarded-User", " jdoe ", "X-Forwarded-Groups", " platform, ,ops,,", "X-Forwarded-Groups", "dev"),
			Identity{Subject: "jdoe", Name: "jdoe", Groups: []string{"platform", "ops", "dev"}}, true},
		{"groups header disabled", ProxyOptions{TrustedProxies: []string{"10.0.0.0/8"}}, "10.0.0.1:1",
			hdr("X-Forwarded-User", "jdoe", "X-Forwarded-Groups", "platform"), Identity{Subject: "jdoe", Name: "jdoe"}, true},
		{"custom user header", ProxyOptions{UserHeader: "Remote-User", TrustedProxies: []string{"10.0.0.0/8"}}, "10.0.0.1:1",
			hdr("Remote-User", "jdoe", "X-Forwarded-User", "admin"), Identity{Subject: "jdoe", Name: "jdoe"}, true},

		{"untrusted remote with headers", trusted, "203.0.113.9:1", hdr("X-Forwarded-User", "admin", "X-Forwarded-Groups", "platform"), Identity{}, false},
		{"untrusted remote spoofing X-Forwarded-For", trusted, "203.0.113.9:1", hdr("X-Forwarded-User", "admin", "X-Forwarded-For", "10.0.0.1"), Identity{}, false},
		{"just outside bare IP", trusted, "192.0.2.8:1", hdr("X-Forwarded-User", "jdoe"), Identity{}, false},
		{"trusted missing user header", trusted, "10.0.0.1:1", hdr("X-Forwarded-Groups", "platform"), Identity{}, false},
		{"trusted empty user header", trusted, "10.0.0.1:1", hdr("X-Forwarded-User", "  "), Identity{}, false},
		{"no trusted proxies configured", ProxyOptions{}, "10.0.0.1:1", hdr("X-Forwarded-User", "jdoe"), Identity{}, false},
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
		_, err := New(t.Context(), Options{Provider: KindProxy, Proxy: ProxyOptions{TrustedProxies: []string{cidr}}})
		if err == nil || !strings.Contains(err.Error(), cidr) {
			t.Errorf("TrustedProxies %q: error = %v", cidr, err)
		}
	}
}

func TestProxyWarnings(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	a := mustNew(t, Options{Provider: KindProxy, Logger: logger})
	if !strings.Contains(buf.String(), "no trusted proxies") {
		t.Errorf("no startup warning: %q", buf.String())
	}
	buf.Reset()
	do(t, app(a), req{target: "/whoami", remote: "203.0.113.1:1", header: http.Header{"X-Forwarded-User": {"admin"}}})
	if !strings.Contains(buf.String(), "untrusted peer") {
		t.Errorf("no spoofing warning: %q", buf.String())
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
			a := proxyAuth(t, ProxyOptions{TrustedProxies: []string{"10.0.0.0/8"}, LogoutURL: tt.logoutURL})
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
