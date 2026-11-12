package components

import (
	"testing"
	"time"
)

func TestBusyBar(t *testing.T) {
	tests := []struct {
		busy, conc int64
		width      string // "" for no fill
	}{
		{12, 20, "60.0"},
		{1, 3, "33.3"},
		{20, 20, "100.0"},
		{25, 20, "100.0"}, // clamped
		{0, 20, ""},
		{3, 0, ""},
	}
	for _, tt := range tests {
		doc := render(t, BusyBar(tt.busy, tt.conc))
		svg := doc.Find("svg.busy")
		if svg.Length() != 1 || svg.AttrOr("aria-hidden", "") != "true" || svg.AttrOr("preserveAspectRatio", "") != "none" {
			t.Fatalf("%d/%d: svg %v", tt.busy, tt.conc, svg.Nodes)
		}
		fill := svg.Find("rect.busy-fill")
		if got := fill.AttrOr("width", ""); got != tt.width {
			t.Errorf("%d/%d: width = %q, want %q", tt.busy, tt.conc, got, tt.width)
		}
		if _, ok := svg.Attr("style"); ok {
			t.Error("inline style")
		}
	}
}

func TestCopyIDButton(t *testing.T) {
	b := render(t, CopyIDButton("copy-id", "c3e81b90-04aa-4f1e-9d27-6b52e0c8a113")).Find("button#copy-id")
	if b.AttrOr("data-copy", "") != "c3e81b90-04aa-4f1e-9d27-6b52e0c8a113" || text(b) != "Copy ID" || b.AttrOr("type", "") != "button" {
		t.Errorf("button = %v", b.Nodes)
	}
	if _, hidden := b.Attr("hidden"); !hidden {
		t.Error("shown before app.js knows the Clipboard API exists")
	}
}

func TestCodeBlock(t *testing.T) {
	doc := render(t, CodeBlock("raw-info", "# Server\nredis_version:7.2.4\n<x>", "Raw Redis INFO output", "Copy INFO"))
	pre := doc.Find(".code pre#raw-info")
	if pre.Text() != "# Server\nredis_version:7.2.4\n<x>" || pre.AttrOr("tabindex", "") != "0" || pre.AttrOr("aria-label", "") != "Raw Redis INFO output" {
		t.Errorf("pre = %q", pre.Text())
	}
	b := doc.Find(".code > button.copy#raw-info-copy")
	// The button copies the block's text, which is not repeated in an
	// attribute.
	if b.AttrOr("data-copy-from", "") != "#raw-info" || b.AttrOr("aria-label", "") != "Copy INFO" || b.AttrOr("data-copy", "-") != "-" {
		t.Errorf("copy button = %v", b.Nodes)
	}
	if b.Parent().Is("pre") {
		t.Error("the copy button scrolls with the code")
	}
}

func TestTimes(t *testing.T) {
	at := time.Date(2026, 11, 8, 0, 11, 52, 0, time.UTC)
	tm := render(t, TimeAbs(at)).Find("time")
	if tm.AttrOr("datetime", "") != "2026-11-08T00:11:52Z" || tm.AttrOr("data-format", "") != "absolute" || text(tm) != "Nov 8, 00:11:52" {
		t.Errorf("TimeAbs = %v", tm.Nodes)
	}
	if got := text(render(t, TimeOrDash(time.Time{})).Find("body")); got != "—" {
		t.Errorf("zero = %q", got)
	}
	if render(t, TimeOrDash(at)).Find("time").Length() != 1 {
		t.Error("TimeOrDash without a time")
	}
}

func TestCrumbMono(t *testing.T) {
	doc := render(t, TopBarView(TopBar{Crumbs: []Crumb{
		{Section: SectionOverview, URL: "/"},
		{Text: "main", URL: "/c/main", Mono: true},
		{Text: "3c1e8d27-5b90…", Mono: true, Title: "3c1e8d27-5b90-4f6a-b2c1-9e04d7a3a07f"},
	}}))
	if a := doc.Find(".crumbs a.crumb-mono"); a.Length() != 1 || text(a) != "main" {
		t.Error("linked mono crumb")
	}
	last := doc.Find(".crumbs [aria-current=page]")
	if !last.HasClass("crumb-mono") || last.AttrOr("title", "") != "3c1e8d27-5b90-4f6a-b2c1-9e04d7a3a07f" {
		t.Errorf("last crumb = %v", last.Nodes)
	}
	if doc.Find(".crumbs li:first-child a[class], .crumbs li:first-child a[title]").Length() != 0 {
		t.Error("plain crumb has a class or title")
	}
}
