package views

import (
	"context"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

func TestDocumentThemeAttribute(t *testing.T) {
	for _, th := range components.Themes() {
		t.Run(th.Value(), func(t *testing.T) {
			var b strings.Builder
			d := Doc{Theme: th, Refresh: components.Refresh2s, CookiePath: "/asynq", Assets: Assets{CSS: "/a.css", JS: "/a.js", HTMX: "/h.js", Icon: "/i.svg"}}
			if err := Document(d, templ.Raw("T")).Render(context.Background(), &b); err != nil {
				t.Fatal(err)
			}
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(b.String()))
			if err != nil {
				t.Fatal(err)
			}
			html := doc.Find("html")
			for attr, want := range map[string]string{"data-theme": th.Value(), "data-refresh": "2s", "data-base": "/asynq", "lang": "en"} {
				if got, _ := html.Attr(attr); got != want {
					t.Errorf("%s = %q, want %q", attr, got, want)
				}
			}
			// htmx loads before app.js, both deferred.
			var srcs []string
			doc.Find("script").Each(func(_ int, s *goquery.Selection) {
				src, _ := s.Attr("src")
				if _, ok := s.Attr("defer"); !ok {
					t.Errorf("%s not deferred", src)
				}
				srcs = append(srcs, src)
			})
			if strings.Join(srcs, " ") != "/h.js /a.js" {
				t.Errorf("scripts = %v", srcs)
			}
			if got := strings.Join(strings.Fields(doc.Find("title").Text()), " "); got != "T – asynqui" {
				t.Errorf("title = %q", got)
			}
			if href, _ := doc.Find("body > a.skip-link").First().Attr("href"); href != "#content" {
				t.Errorf("skip link = %q", href)
			}
		})
	}
}
