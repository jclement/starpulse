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

func TestLogFolderDiscovery(t *testing.T) {
	st := testStore(t)
	_, _ = st.SavePage("/posts/2026-07-19-a.gmi", []byte("# A"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-20-b.gmi", []byte("# B"), "", "t")
	_, _ = st.SavePage("/posts/index.gmi", []byte("# Gemlog"), "", "t")
	_, _ = st.SavePage("/projects/2026-07-01-p.gmi", []byte("# P"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About"), "", "t") // undated: not a log

	logs := LogFolders(st)
	if logs["/posts/"] != 2 || logs["/projects/"] != 1 {
		t.Errorf("log folders = %v", logs)
	}
	if _, ok := logs["/"]; ok {
		t.Error("root should not be a log folder (no dated pages)")
	}
	if !IsLogFolder(st, "/posts") || !IsLogFolder(st, "/posts/") {
		t.Error("IsLogFolder should accept either form")
	}
	if IsLogFolder(st, "/nope/") {
		t.Error("non-log folder reported as log")
	}
}

func TestAutoFeedResolution(t *testing.T) {
	st := testStore(t)
	_, _ = st.SavePage("/posts/2026-07-20-b.gmi", []byte("# B\n\nbody"), "", "t")
	_, _ = st.SavePage("/posts/index.gmi", []byte("# Gemlog"), "", "t")
	_, _ = st.SavePage("/projects/2026-07-01-p.gmi", []byte("# P"), "", "t")

	c := config.Default()
	c.Hostname = "ex.example"

	// auto feed for each log folder, titled from the folder index
	f, ok := Resolve(c, st, "/posts/feed.xml")
	if !ok || f.Source != "/posts/" || f.Title != "Gemlog" {
		t.Fatalf("auto posts feed = %+v ok=%v", f, ok)
	}
	f2, ok2 := Resolve(c, st, "/projects/feed.xml")
	if !ok2 || f2.Source != "/projects/" {
		t.Fatalf("auto projects feed = %+v ok=%v", f2, ok2)
	}
	// folder without dated pages has no feed
	if _, ok := Resolve(c, st, "/nope/feed.xml"); ok {
		t.Error("feed invented for a non-log folder")
	}
	// explicit config wins over auto
	c.Feeds.List = []config.Feed{{Path: "/posts/feed.xml", Source: "/posts/", Title: "Custom"}}
	if f, _ := Resolve(c, st, "/posts/feed.xml"); f.Title != "Custom" {
		t.Errorf("explicit config should win: %+v", f)
	}
	// auto can be switched off
	c.Feeds.Auto = false
	if _, ok := Resolve(c, st, "/projects/feed.xml"); ok {
		t.Error("auto feeds still served with auto disabled")
	}
}

func TestDateSources(t *testing.T) {
	st := testStore(t)
	// 1. filename date — the conventional post
	_, _ = st.SavePage("/posts/2026-07-19-named.gmi", []byte("# Named\n\nbody"), "", "t")
	// 2. front-matter date, clean URL — also a post
	_, _ = st.SavePage("/posts/clean.gmi",
		[]byte("---\ntitle: Clean\ndate: 2026-07-20\n---\n# Clean\n\nbody"), "", "t")
	// 3. front matter overrides the filename
	_, _ = st.SavePage("/posts/2020-01-01-wrong.gmi",
		[]byte("---\ndate: 2026-07-21\n---\n# Corrected\n\nbody"), "", "t")
	// 4. no date anywhere — a permanent page, never a post
	_, _ = st.SavePage("/posts/about.gmi", []byte("# About\n\nbody"), "", "t")

	b := &Builder{Store: st, Hostname: "ex.example", Loc: time.UTC}
	out := b.Build(config.Feed{Path: "/posts/feed.xml", Source: "/posts/", Limit: 30}, "https://ex.example")

	for _, want := range []string{"Named", "Clean", "Corrected"} {
		if !strings.Contains(out, want) {
			t.Errorf("feed missing %q", want)
		}
	}
	if strings.Contains(out, ">About<") {
		t.Error("undated page treated as a post")
	}
	// front matter wins: the corrected post sorts newest
	if !strings.Contains(out, "<published>2026-07-21T00:00:00Z</published>") {
		t.Errorf("front-matter date did not override the filename:\n%s", out)
	}
	if strings.Index(out, "Corrected") > strings.Index(out, "Clean") {
		t.Error("front-matter date not used for ordering")
	}
	// and the folder counts as a log folder on either signal
	if LogFolders(st)["/posts/"] != 3 {
		t.Errorf("log folder count = %d, want 3", LogFolders(st)["/posts/"])
	}
}
