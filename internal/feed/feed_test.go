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

// A notes stream is just a feed folder: its entries are pages like any
// other, so it feeds without any special casing.
func TestNoteStreamFeed(t *testing.T) {
	st := testStore(t)
	_, _ = st.SavePage("/now/"+store.FeedMarker,
		store.DefaultFeedMarker("Now", "Jeff", 30), "", "t")
	_, _ = st.SavePage(st.NewStreamPath("/now/", time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)),
		[]byte("first update"), "", "t")
	_, _ = st.SavePage(st.NewStreamPath("/now/", time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)),
		[]byte("a longer second update"), "", "t")

	c := config.Default()
	c.Hostname = "ex.example"
	f, ok := Resolve(c, st, "/now/feed.xml")
	if !ok {
		t.Fatal("notes folder does not publish")
	}
	b := &Builder{Store: st, Hostname: "ex.example", Loc: time.UTC}
	out := b.Build(f, "https://ex.example")
	if n := strings.Count(out, "<entry>"); n != 2 {
		t.Errorf("entries = %d, want 2:\n%s", n, out)
	}
	for _, want := range []string{"first update", "a longer second update", "<title>Now</title>"} {
		if !strings.Contains(out, want) {
			t.Errorf("notes feed missing %q", want)
		}
	}
}

func TestEffectiveFeedsDefaults(t *testing.T) {
	c := config.Default()
	c.Hostname = "ex.example"
	// nothing is published until something asks for it
	if fs := c.EffectiveFeeds(); len(fs) != 0 {
		t.Fatalf("expected no feeds by default, got %+v", fs)
	}

	c.Feeds = config.Feeds{
		Author: "Jeff",
		Limit:  10,
		List: []config.Feed{
			{Path: "posts/feed.xml", Source: "/posts/"}, // leading slash added
			{Path: "/notes/feed.xml", Source: "/notes/"},
			{Source: "/orphan/"}, // no path: dropped
		},
	}
	fs := c.EffectiveFeeds()
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
	if fs[1].Page != "/notes/" {
		t.Errorf("page should default to source: %q", fs[1].Page)
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

func TestFeedFoldersAreOptIn(t *testing.T) {
	st := testStore(t)
	_, _ = st.SavePage("/posts/2026-07-19-a.gmi", []byte("# A"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-20-b.gmi", []byte("# B"), "", "t")
	_, _ = st.SavePage("/posts/index.gmi", []byte("# Gemlog"), "", "t")
	_, _ = st.SavePage("/projects/2026-07-01-p.gmi", []byte("# P"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About"), "", "t") // undated: not a log

	// dated filenames alone must NOT create a feed — publishing is opt-in
	if len(FeedFolders(st)) != 0 {
		t.Errorf("dated pages should not auto-publish: %v", FeedFolders(st))
	}
	if st.IsFeedFolder("/posts/") {
		t.Error("unmarked folder reported as publishing")
	}
	// marking it is what turns the feed on
	_, _ = st.SavePage("/posts/"+store.FeedMarker, []byte("title: Posts\n"), "", "t")
	if !st.IsFeedFolder("/posts") || !st.IsFeedFolder("/posts/") {
		t.Error("marked folder should publish, either path form")
	}
	if _, ok := FeedFolders(st)["/posts/"]; !ok {
		t.Errorf("feed folders = %v", FeedFolders(st))
	}
}

func TestAutoFeedResolution(t *testing.T) {
	st := testStore(t)
	_, _ = st.SavePage("/posts/2026-07-20-b.gmi", []byte("# B\n\nbody"), "", "t")
	_, _ = st.SavePage("/posts/index.gmi", []byte("# Gemlog"), "", "t")
	_, _ = st.SavePage("/projects/2026-07-01-p.gmi", []byte("# P"), "", "t")

	c := config.Default()
	c.Hostname = "ex.example"

	// nothing is published until a folder is marked
	if _, ok := Resolve(c, st, "/posts/feed.xml"); ok {
		t.Error("feed served for an unmarked folder")
	}
	_, _ = st.SavePage("/posts/"+store.FeedMarker, []byte(""), "", "t")
	f, ok := Resolve(c, st, "/posts/feed.xml")
	if !ok || f.Source != "/posts/" || f.Title != "Gemlog" {
		t.Fatalf("marked posts feed = %+v ok=%v", f, ok)
	}
	// a marked folder elsewhere is independent
	if _, ok := Resolve(c, st, "/projects/feed.xml"); ok {
		t.Error("unmarked projects folder should not publish")
	}
	// explicit config still wins for the same path
	c.Feeds.List = []config.Feed{{Path: "/posts/feed.xml", Source: "/posts/", Title: "Custom"}}
	if f, _ := Resolve(c, st, "/posts/feed.xml"); f.Title != "Custom" {
		t.Errorf("explicit config should win: %+v", f)
	}
}

func TestDateSources(t *testing.T) {
	st := testStore(t)
	// 1. filename date — the conventional post
	_, _ = st.SavePage("/posts/2026-07-19-named.gmi", []byte("# Named\n\nbody"), "", "t")
	// 2. front-matter date, clean URL — also a post
	_, _ = st.SavePage("/posts/clean.gmi",
		[]byte("---\ntitle: Clean\ndate: 2026-07-20\n---\n# Clean\n\nbody"), "", "t")
	// 3. the filename wins over front matter (most visible signal)
	_, _ = st.SavePage("/posts/2026-07-21-named-wins.gmi",
		[]byte("---\ndate: 2020-01-01\n---\n# Named Wins\n\nbody"), "", "t")
	// 4. no date anywhere — a permanent page, never a post
	_, _ = st.SavePage("/posts/about.gmi", []byte("# About\n\nbody"), "", "t")

	b := &Builder{Store: st, Hostname: "ex.example", Loc: time.UTC}
	out := b.Build(config.Feed{Path: "/posts/feed.xml", Source: "/posts/", Limit: 30}, "https://ex.example")

	for _, want := range []string{"Named", "Clean", "Named Wins"} {
		if !strings.Contains(out, want) {
			t.Errorf("feed missing %q", want)
		}
	}
	if strings.Contains(out, ">About<") {
		t.Error("undated page treated as a post")
	}
	// the filename date is the one used, so this post sorts newest
	if !strings.Contains(out, "<published>2026-07-21T00:00:00Z</published>") {
		t.Errorf("filename date not used:\n%s", out)
	}
	if strings.Contains(out, "<published>2020-01-01") {
		t.Error("front matter overrode the filename")
	}
	if strings.Index(out, "Named Wins") > strings.Index(out, "Clean") {
		t.Error("ordering wrong")
	}

}

func TestFeedFolderCleanNames(t *testing.T) {
	st := testStore(t)
	// turning the feed on makes every page a post, dated from the DB
	_, _ = st.SavePage("/journal/"+store.FeedMarker,
		[]byte("title: Field Notes\nsubtitle: what I got up to\nauthor: Jeff\nlimit: 5\n"), "", "t")
	_, _ = st.SavePage("/journal/hello-world.gmi", []byte("# Hello World\n\nbody"), "", "t")
	_, _ = st.SavePage("/journal/second-post.gmi", []byte("# Second Post\n\nmore"), "", "t")
	_, _ = st.SavePage("/journal/index.gmi", []byte("# Field Notes"), "", "t")

	c := config.Default()
	c.Hostname = "ex.example"
	f, ok := Resolve(c, st, "/journal/feed.xml")
	if !ok {
		t.Fatal("marked folder has no feed")
	}
	// the marker supplies title, subtitle, author and limit
	if f.Title != "Field Notes" || f.Subtitle != "what I got up to" {
		t.Errorf("marker metadata not used: %+v", f)
	}
	if f.Author != "Jeff" || f.Limit != 5 {
		t.Errorf("marker author/limit not used: %+v", f)
	}

	b := &Builder{Store: st, Hostname: "ex.example", Loc: time.UTC}
	out := b.Build(f, "https://ex.example")
	for _, want := range []string{"Hello World", "Second Post"} {
		if !strings.Contains(out, want) {
			t.Errorf("undated post %q missing from marked folder feed:\n%s", want, out)
		}
	}
	// clean URLs, no date prefixes
	if !strings.Contains(out, "https://ex.example/journal/hello-world") {
		t.Error("expected a clean post URL")
	}
	// the folder's own index page is not one of its posts
	if strings.Count(out, "<entry>") != 2 {
		t.Errorf("entries = %d, want 2 (index.gmi should be excluded)", strings.Count(out, "<entry>"))
	}
	// today's date, from the database
	today := time.Now().UTC().Format("2006-01-02")
	if !strings.Contains(out, "<published>"+today) {
		t.Errorf("posts not dated from the database:\n%s", out)
	}
	if !strings.Contains(out, "<author><name>Jeff</name></author>") {
		t.Error("per-folder author not applied")
	}
}
