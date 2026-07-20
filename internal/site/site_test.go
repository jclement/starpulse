package site

import (
	"strings"
	"testing"
	"time"

	"github.com/jclement/starpulse/internal/store"
)

func testSite(t *testing.T) (*Site, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

func save(t *testing.T, st *store.Store, p, content string) {
	t.Helper()
	if _, err := st.SavePage(p, []byte(content), "", "test"); err != nil {
		t.Fatalf("save %s: %v", p, err)
	}
}

func TestCleanURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/", "/", true},
		{"", "/", true},
		{"/about", "/about", true},
		{"/posts/", "/posts/", true},
		{"/posts/../x", "", false},
		{"/.header", "", false},
		{"/posts/.theme", "", false},
		{"/a//b", "/a/b", true},
	}
	for _, c := range cases {
		got, ok := CleanURL(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("CleanURL(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestResolveBasics(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/index.gmi", "# Home\n\nwelcome")
	save(t, st, "/about.gmi", "# About")
	save(t, st, "/notes.txt", "plain text file")
	save(t, st, "/posts/2026-01-02-two.gmi", "# Post two")

	// root index
	r := sy.Resolve("/", "")
	if r.Type != PageResult || r.Page.Title != "Home" || r.Page.SourcePath != "/index.gmi" {
		t.Fatalf("root: %+v", r)
	}
	// extensionless
	if r := sy.Resolve("/about", ""); r.Type != PageResult || r.Page.Title != "About" {
		t.Fatalf("/about: %+v", r)
	}
	// explicit .gmi also serves
	if r := sy.Resolve("/about.gmi", ""); r.Type != PageResult {
		t.Fatalf("/about.gmi: %+v", r)
	}
	// static file
	r = sy.Resolve("/notes.txt", "")
	if r.Type != FileResult || string(r.File.Content) != "plain text file" {
		t.Fatalf("/notes.txt: %+v", r)
	}
	// dir without slash redirects
	if r := sy.Resolve("/posts", ""); r.Type != RedirectResult || r.Location != "/posts/" {
		t.Fatalf("/posts: %+v", r)
	}
	// dir with no index → synthetic listing containing the post
	r = sy.Resolve("/posts/", "")
	if r.Type != PageResult || !strings.Contains(r.Page.Gemtext, "=> /posts/2026-01-02-two 2026-01-02 Post two") {
		t.Fatalf("/posts/ listing: %+v\n%s", r.Type, r.Page.Gemtext)
	}
	// hidden and missing
	if r := sy.Resolve("/.header", ""); r.Type != NotFound {
		t.Fatalf("hidden served: %+v", r)
	}
	if r := sy.Resolve("/nope", ""); r.Type != NotFound {
		t.Fatalf("missing served: %+v", r)
	}
}

func TestHeaderFooterThemeInheritance(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/.header", "=> / HOME-LINK")
	save(t, st, "/.footer", "root footer")
	save(t, st, "/.theme", "body { color: red }")
	save(t, st, "/posts/.theme", "body { color: blue }")
	save(t, st, "/index.gmi", "# Home")
	save(t, st, "/posts/one.gmi", "# One")

	r := sy.Resolve("/", "")
	g := r.Page.Gemtext
	if !strings.Contains(g, "HOME-LINK") || !strings.Contains(g, "root footer") {
		t.Fatalf("header/footer missing:\n%s", g)
	}
	if !strings.HasPrefix(g, "=> / HOME-LINK") {
		t.Fatalf("header not first:\n%s", g)
	}
	if r.Page.Theme != "body { color: red }" {
		t.Errorf("root theme = %q", r.Page.Theme)
	}

	// subfolder inherits header, overrides theme
	r = sy.Resolve("/posts/one", "")
	if !strings.Contains(r.Page.Gemtext, "HOME-LINK") {
		t.Errorf("inherited header missing")
	}
	if r.Page.Theme != "body { color: blue }" {
		t.Errorf("posts theme = %q", r.Page.Theme)
	}

	// front matter can disable header/footer
	save(t, st, "/bare.gmi", "---\ntitle: Bare\nheader: none\nfooter: none\n---\n# Bare page")
	r = sy.Resolve("/bare", "")
	if strings.Contains(r.Page.Gemtext, "HOME-LINK") || strings.Contains(r.Page.Gemtext, "root footer") {
		t.Errorf("front matter disable ignored:\n%s", r.Page.Gemtext)
	}
	if r.Page.Title != "Bare" {
		t.Errorf("front matter title = %q", r.Page.Title)
	}
}

func TestDirectives(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/posts/2026-01-01-a.gmi", "# Alpha")
	save(t, st, "/posts/2026-02-01-b.gmi", "# Beta")
	save(t, st, "/posts/undated.gmi", "# Undated")
	save(t, st, "/snippet.gmi", "included text here")
	save(t, st, "/.taglines.txt", "only line")

	save(t, st, "/index.gmi", `# Home

{{list /posts 2}}

{{include /snippet}}

{{random /.taglines.txt}}
`)
	g := sy.Resolve("/", "").Page.Gemtext
	if !strings.Contains(g, "=> /posts/2026-02-01-b 2026-02-01 Beta") {
		t.Errorf("list missing beta:\n%s", g)
	}
	if strings.Contains(g, "Undated") {
		t.Errorf("list limit ignored:\n%s", g)
	}
	if !strings.Contains(g, "included text here") {
		t.Errorf("include failed:\n%s", g)
	}
	if !strings.Contains(g, "only line") {
		t.Errorf("random failed:\n%s", g)
	}
	// dated sort: beta (feb) before alpha (jan)
	if strings.Index(g, "Beta") > strings.Index(g, "Alpha") && strings.Contains(g, "Alpha") {
		t.Errorf("date sort wrong:\n%s", g)
	}
}

func TestIncludeCycleSafe(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/a.gmi", "# A\n{{include /b}}")
	save(t, st, "/b.gmi", "{{include /a}}")
	g := sy.Resolve("/a", "").Page.Gemtext
	if len(g) > 10000 {
		t.Fatalf("runaway include: %d bytes", len(g))
	}
}

func TestCountDirectiveAndStats(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/.footer", "seen {{count}} times")
	save(t, st, "/index.gmi", "# Home")

	g := sy.Resolve("/", "gemini").Page.Gemtext
	if !strings.Contains(g, "seen 1 times") {
		t.Errorf("count after first view:\n%s", g)
	}
	g = sy.Resolve("/", "http").Page.Gemtext
	if !strings.Contains(g, "seen 2 times") {
		t.Errorf("count after second view:\n%s", g)
	}
	// per-proto stats recorded
	hits, _ := st.Stats()
	if len(hits) != 2 {
		t.Errorf("stats = %+v", hits)
	}
	// proto "" must not bump
	_ = sy.Resolve("/", "")
	if n := st.Count("/"); n != 2 {
		t.Errorf("count after preview = %d, want 2", n)
	}
}

func TestNowDirectiveAndAuthoredPage(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/now/"+store.FeedMarker, string(store.DefaultFeedMarker("Now", "", 30, true)))
	save(t, st, "/now/2026-07-19-0900.gmi", "hello from now")
	save(t, st, "/now/2026-07-20-0900.gmi", "second update")

	save(t, st, "/index.gmi", "# Home\n\n{{now 1}}")
	g := sy.Resolve("/", "").Page.Gemtext
	if !strings.Contains(g, "second update") || strings.Contains(g, "hello from now") {
		t.Errorf("now limit wrong:\n%s", g)
	}

	save(t, st, "/now/index.gmi", "# My Now\n\n{{now 0}}")
	r := sy.Resolve("/now/", "")
	if r.Type != PageResult || r.Page.Title != "My Now" {
		t.Fatalf("authored /now: %+v", r)
	}
	if !strings.Contains(r.Page.Gemtext, "hello from now") || !strings.Contains(r.Page.Gemtext, "second update") {
		t.Errorf("authored /now missing notes:\n%s", r.Page.Gemtext)
	}
}

func TestRevAndUpdatedDirectives(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/.footer", "last updated {{updated}} · r{{rev}}")
	save(t, st, "/page.gmi", "# One")

	g := sy.Resolve("/page", "").Page.Gemtext
	today := timeNowDate()
	if !strings.Contains(g, "last updated "+today+" · r1") {
		t.Errorf("first revision footer wrong:\n%s", g)
	}
	// two edits → r3
	save(t, st, "/page.gmi", "# Two")
	save(t, st, "/page.gmi", "# Three")
	g = sy.Resolve("/page", "").Page.Gemtext
	if !strings.Contains(g, "· r3") {
		t.Errorf("rev after edits wrong:\n%s", g)
	}
	// synthetic pages (directory listings) don't explode
	save(t, st, "/dir/leaf.gmi", "# Leaf")
	g = sy.Resolve("/dir/", "").Page.Gemtext
	if !strings.Contains(g, "· r1") || !strings.Contains(g, "recently") {
		t.Errorf("synthetic rev/updated wrong:\n%s", g)
	}
}

func timeNowDate() string {
	return time.Now().Format("2006-01-02")
}

// {{latest}} generalises to any folder, and picks out one part of the entry.
func TestLatestDirective(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/a.gmi", "# A\n\nlatest: {{latest_now}} on {{latest_now_date}}!")
	g := sy.Resolve("/a", "").Page.Gemtext
	if !strings.Contains(g, "latest:  on !") {
		t.Errorf("empty stream should render empty, not error:\n%s", g)
	}

	save(t, st, "/now/"+store.FeedMarker, string(store.DefaultFeedMarker("Now", "", 30, true)))
	save(t, st, "/now/2026-07-19-0900.gmi", "older note")
	save(t, st, "/now/2026-07-20-0900.gmi", "# Newest\n\nthe newest note")

	g = sy.Resolve("/a", "").Page.Gemtext
	if !strings.Contains(g, "latest: the newest note on 2026-07-20!") {
		t.Errorf("latest_now wrong:\n%s", g)
	}
	if strings.Contains(g, "older note") {
		t.Errorf("latest leaked an older entry:\n%s", g)
	}

	// the general form works on any folder, and can pick a part
	save(t, st, "/posts/"+store.FeedMarker, "title: Posts\n")
	save(t, st, "/posts/2026-07-21-hello.gmi", "# Hello There\n\nthe body")
	save(t, st, "/b.gmi", "T={{latest /posts title}} D={{latest /posts date}}\n{{latest /posts link}}")
	g = sy.Resolve("/b", "").Page.Gemtext
	for _, want := range []string{"T=Hello There", "D=2026-07-21", "=> /posts/2026-07-21-hello Hello There"} {
		if !strings.Contains(g, want) {
			t.Errorf("latest part missing %q:\n%s", want, g)
		}
	}

	// {{stream}} renders bodies newest-first for any folder
	save(t, st, "/c.gmi", "{{stream /now 1}}")
	g = sy.Resolve("/c", "").Page.Gemtext
	if !strings.Contains(g, "the newest note") || strings.Contains(g, "older note") {
		t.Errorf("stream wrong:\n%s", g)
	}
}

func TestListEntryTitlesAndDirs(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/stuff/index.gmi", "# All the stuff")
	save(t, st, "/stuff/thing.gmi", "# A thing")
	save(t, st, "/top.gmi", "# Top")
	save(t, st, "/index.gmi", "# Home\n\n{{list}}")

	g := sy.Resolve("/", "").Page.Gemtext
	if !strings.Contains(g, "=> /stuff/ All the stuff") {
		t.Errorf("dir title from index missing:\n%s", g)
	}
	if !strings.Contains(g, "=> /top Top") {
		t.Errorf("page entry missing:\n%s", g)
	}
	if strings.Contains(g, "index") {
		t.Errorf("index leaked into listing:\n%s", g)
	}
}
