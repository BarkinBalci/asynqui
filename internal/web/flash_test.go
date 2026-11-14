package web

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

func roundTrip(t *testing.T, d *deps, f pages.Flash, tamper func(string) string) (pages.Flash, bool) {
	t.Helper()
	w := httptest.NewRecorder()
	d.setFlashAll(w, "main", f)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if tamper != nil {
		c.Value = tamper(c.Value)
	}
	r := httptest.NewRequest(http.MethodGet, "/c/main", nil)
	r.AddCookie(c)
	return d.takeFlash(httptest.NewRecorder(), r, "main")
}

func TestFlashRoundTrip(t *testing.T) {
	d, _, _ := newTestDeps(t, testOpts{provider: config.ProviderBasic})
	f := pages.Flash{Notice: &pages.TaskNotice{Verb: pages.TaskArchive, Count: 3},
		TaskFailures: []pages.TaskFailure{{Verb: pages.TaskRun, ID: "p1", Detail: "boom"}}}
	got, ok := roundTrip(t, d, f, nil)
	if !ok || got.Notice == nil || got.Notice.Count != 3 || len(got.TaskFailures) != 1 || got.TaskFailures[0].Detail != "boom" {
		t.Errorf("round trip = %+v, %v", got, ok)
	}
	// Plain base64 JSON, signed or not by session keys.
	if _, ok := roundTrip(t, d, f, func(v string) string { return v + "!" }); ok {
		t.Error("malformed cookie accepted")
	}
}

func TestFlashBounds(t *testing.T) {
	d, _, _ := newTestDeps(t, testOpts{})
	enc := func(s string) func(string) string {
		return func(string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	}
	f := pages.Flash{Notice: &pages.TaskNotice{Verb: pages.TaskRun, Count: 1}}
	if got, ok := roundTrip(t, d, f, enc(`[{"v":0,"k":0,"a":true,"n":9223372036854775807}]`)); !ok || got.Notice.Count != flashMaxCount {
		t.Errorf("unbounded count = %+v", got.Notice)
	}
	for _, bad := range []string{`[{"v":9,"k":0,"a":true,"n":1}]`, `[{"v":0,"k":5,"a":true,"t":"x"}]`, `[{"v":0,"k":0,"q":""}]`} {
		if _, ok := roundTrip(t, d, f, enc(bad)); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestClipAtRune(t *testing.T) {
	s := strings.Repeat("a", flashMaxText-1) + "é and more"
	got := clip(s)
	if !utf8.ValidString(got) || len(got) > flashMaxText || !strings.HasSuffix(got, "a") {
		t.Errorf("clip = %q", got[len(got)-4:])
	}
}
