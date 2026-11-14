package web

import "testing"

// fakeUpdates is a fixed update check result.
type fakeUpdates struct {
	version string
	ok      bool
}

func (f fakeUpdates) Latest() (string, bool) { return f.version, f.ok }

func TestFooterUpdate(t *testing.T) {
	tests := []struct {
		name    string
		updates UpdateSource
		dot     bool
		href    string
	}{
		{"no check", nil, false, releasesURL + "/tag/v0.1.0"},
		{"up to date", fakeUpdates{}, false, releasesURL + "/tag/v0.1.0"},
		{"newer release", fakeUpdates{version: "v0.2.0", ok: true}, true, "https://github.com/BarkinBalci/asynqui/releases/tag/v0.2.0"},
		// update only reports semantic versions; the footer escapes anyway.
		{"tag is escaped", fakeUpdates{version: "../x?y#z", ok: true}, true, releasesURL + "/tag/..%2Fx%3Fy%23z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{updates: tt.updates})
			for _, path := range []string{"/", "/c/main/queues"} {
				doc := parse(t, do(t, h, request{path: path}))
				v := doc.Find(".side-foot .version")
				if got := text(v); got != "v0.1.0" {
					t.Errorf("%s: version = %q", path, got)
				}
				if href, _ := v.Attr("href"); href != tt.href {
					t.Errorf("%s: href = %q, want %q", path, href, tt.href)
				}
				if got := v.Find(".dot-update").Length() == 1; got != tt.dot {
					t.Errorf("%s: update dot = %v, want %v", path, got, tt.dot)
				}
			}
		})
	}
}
