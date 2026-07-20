package feed

import (
	"strings"
	"testing"
	"time"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestFolderFeed(t *testing.T) {
	st := testStore(t)
	_, _ = st.SavePage("/posts/2026-07-19-first.gmi", []byte("# First Post\n\nSome body text here."), "", "t")
	_, _ = st.SavePage("/posts/2026-07-20-second.gmi", []byte("# Second Post\n\nMore words."), "", "t")
	_, _ = st.SavePage("/posts/undated.gmi", []byte("# Undated"), "", "t")
	_, _ = st.SavePage("/notes/2026-07-18-other.gmi", []byte("# Elsewhere"), "", "t")

	b := &Builder{Store: st, Hostname: "ex.example", Author: "Jeff", Loc: time.UTC}
	out := b.Build(config.Feed{Path: "/posts/feed.xml", Source: "/posts/", Page: "/posts/",
		Title: "gemlog", Limit: 30}, "https://ex.example")

	for _, want := range []string{
		"<title>gemlog</title>",
		"<author><name>Jeff</name></author>",
		`<link rel="self" type="application/atom+xml" href="https://ex.example/posts/feed.xml"/>`,
		"<title>Second Post</title>",
		"<title>First Post</title>",
		"https://ex.example/posts/2026-07-20-second",
		"<summary type=\"text\">More words.</summary>",
		"<published>2026-07-20T00:00:00Z</published>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feed missing %q:\n%s", want, out)
		}
	}
	// scoping: other folders and undated pages stay out
	if strings.Contains(out, "Elsewhere") {
		t.Error("feed leaked a page from another folder")
	}
	if strings.Contains(out, "Undated") {
		t.Error("undated page appeared in the feed")
	}
	// newest first
	if strings.Index(out, "Second Post") > strings.Index(out, "First Post") {
		t.Error("entries are not newest-first")
	}
	// the heading is the title, so it should not be repeated in the summary
	if strings.Contains(out, "<summary type=\"text\">Second Post") {
		t.Error("summary repeats the title")
	}
}

func TestSiteWideFeedAndLimit(t *testing.T) {
	st := testStore(t)
	for _, p := range []string{
		"/posts/2026-07-01-a.gmi", "/notes/2026-07-02-b.gmi", "/2026-07-03-c.gmi",
	} {
		_, _ = st.SavePage(p, []byte("# Page\n\nbody"), "", "t")
	}
	b := &Builder{Store: st, Hostname: "ex.example", Loc: time.UTC}
	out := b.Build(config.Feed{Path: "/feed.xml", Source: "/", Limit: 2}, "https://ex.example")
	if n := strings.Count(out, "<entry>"); n != 2 {
		t.Errorf("limit not honoured: %d entries", n)
	}
	// author falls back to the hostname
	if !strings.Contains(out, "<author><name>ex.example</name></author>") {
		t.Error("author fallback missing")
	}
}

func TestNowFeed(t *testing.T) {
	st := testStore(t)
	_, _ = st.AddNow("first update")
	_, _ = st.AddNow("a longer second update\nwith a second line that should not be in the title")

	b := &Builder{Store: st, Hostname: "ex.example", Loc: time.UTC}
	out := b.Build(config.Feed{Path: "/now/feed.xml", Source: "now", Page: "/now",
		Title: "now", Limit: 30}, "https://ex.example")

	if !strings.Contains(out, "<title>a longer second update</title>") {
		t.Errorf("now title should be the first line:\n%s", out)
	}
	if !strings.Contains(out, "with a second line") {
		t.Error("now summary should carry the full text")
	}
	// stable tag: ids, and a link to the human page
	if !strings.Contains(out, "<id>tag:ex.example,") {
		t.Errorf("now entries need tag ids:\n%s", out)
	}
	if !strings.Contains(out, `href="https://ex.example/now"`) {
		t.Error("now entries should link to the configured page")
	}
	if n := strings.Count(out, "<entry>"); n != 2 {
		t.Errorf("entries = %d, want 2", n)
	}
}

func TestEffectiveFeedsDefaults(t *testing.T) {
	c := config.Default()
	c.Hostname = "ex.example"
	fs := c.EffectiveFeeds()
	if len(fs) != 1 || fs[0].Path != "/feed.xml" || fs[0].Source != "/" {
		t.Fatalf("default feed wrong: %+v", fs)
	}
	if fs[0].Limit != 30 {
		t.Errorf("default limit = %d", fs[0].Limit)
	}

	c.Feeds = config.Feeds{
		Author: "Jeff",
		Limit:  10,
		List: []config.Feed{
			{Path: "posts/feed.xml", Source: "/posts/"}, // leading slash added
			{Path: "/now/feed.xml", Source: "now"},
			{Source: "/orphan/"}, // no path: dropped
		},
	}
	fs = c.EffectiveFeeds()
	if len(fs) != 2 {
		t.Fatalf("expected 2 usable feeds, got %d: %+v", len(fs), fs)
	}
	if fs[0].Path != "/posts/feed.xml" {
		t.Errorf("path not normalised: %q", fs[0].Path)
	}
	if fs[0].Page != "/posts/" {
		t.Errorf("page should default to source: %q", fs[0].Page)
	}
	if fs[0].Limit != 10 {
		t.Errorf("global limit not inherited: %d", fs[0].Limit)
	}
	if !fs[1].IsNow() {
		t.Error("now feed not detected")
	}
	if fs[1].Page != "/" {
		t.Errorf("now page default = %q", fs[1].Page)
	}
}

func TestDatedNameAndPageURL(t *testing.T) {
	if DatedName("/posts/2026-07-20-hi.gmi") != "2026-07-20" {
		t.Error("dated name not parsed")
	}
	if DatedName("/posts/hello.gmi") != "" {
		t.Error("undated name misparsed")
	}
	if PageURL("/posts/index.gmi") != "/posts/" {
		t.Errorf("index URL = %q", PageURL("/posts/index.gmi"))
	}
	if PageURL("/about.gmi") != "/about" {
		t.Errorf("page URL = %q", PageURL("/about.gmi"))
	}
}
