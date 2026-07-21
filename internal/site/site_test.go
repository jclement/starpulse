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
		{"/posts/.css", "", false},
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
	save(t, st, "/.css", "body { color: red }")
	save(t, st, "/posts/.css", "body { color: blue }")
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
	save(t, st, "/now/"+store.FeedMarker, string(store.DefaultFeedMarker("Now", "", 30)))
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
	save(t, st, "/a.gmi", "# A\n\nlatest: {{latest /now/}} on {{latest /now/ date}}!")
	g := sy.Resolve("/a", "").Page.Gemtext
	if !strings.Contains(g, "latest:  on !") {
		t.Errorf("empty folder should render empty, not error:\n%s", g)
	}

	save(t, st, "/now/"+store.FeedMarker, string(store.DefaultFeedMarker("Now", "", 30)))
	save(t, st, "/now/2026-07-19-0900.gmi", "older note")
	save(t, st, "/now/2026-07-20-0900.gmi", "# Newest\n\nthe newest note")

	g = sy.Resolve("/a", "").Page.Gemtext
	if !strings.Contains(g, "latest: the newest note on 2026-07-20!") {
		t.Errorf("{{latest /now/}} wrong:\n%s", g)
	}

	// the folder is required, and the old shorthands say so rather than
	// silently rendering nothing
	save(t, st, "/c.gmi", "x {{latest}} y {{latest date}} z")
	if g := sy.Resolve("/c", "").Page.Gemtext; strings.Count(g, "name a folder") != 2 {
		t.Errorf("a folderless {{latest}} should say so:\n%s", g)
	}

	// a directive in the heading must reach the <title> expanded too, not
	// just the rendered body
	save(t, st, "/t.gmi", "# Latest ({{latest /now/ date}})\n\nbody")
	if got := sy.Resolve("/t", "").Page.Title; got != "Latest (2026-07-20)" {
		t.Errorf("title not expanded: %q", got)
	}

	// "." is the page's own folder, so a folder's index.gmi can show its
	// own newest entry without naming itself
	save(t, st, "/now/index.gmi", "# Now\n\n{{latest . body}} ({{latest . date}})")
	if g := sy.Resolve("/now/", "").Page.Gemtext; !strings.Contains(g, "the newest note (2026-07-20)") {
		t.Errorf("{{latest . }} did not resolve to the containing folder:\n%s", g)
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

// A folder's .footer replaces the inherited one rather than adding to it, so
// a page inside a folder had no way to ask for the site-wide footer without
// copying it. Front matter can now name the file to use.
func TestFrontMatterCanNameADifferentWrapper(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/.header", "SITE HEADER")
	save(t, st, "/.footer", "SITE FOOTER")
	save(t, st, "/posts/.header", "POSTS HEADER")
	save(t, st, "/posts/.footer", "=> /posts/ back to the gemlog")

	// an ordinary entry inherits its folder's pair
	save(t, st, "/posts/entry.gmi", "# Entry")
	g := sy.Resolve("/posts/entry", "").Page.Gemtext
	if !strings.Contains(g, "POSTS HEADER") || !strings.Contains(g, "back to the gemlog") {
		t.Errorf("entry did not inherit the folder wrappers:\n%s", g)
	}

	// the index asks for the site-wide footer instead of its folder's
	save(t, st, "/posts/index.gmi", "---\nfooter: /.footer\n---\n# Gemlog")
	g = sy.Resolve("/posts/", "").Page.Gemtext
	if strings.Contains(g, "back to the gemlog") {
		t.Errorf("the folder footer was still applied:\n%s", g)
	}
	if !strings.Contains(g, "SITE FOOTER") {
		t.Errorf("the named footer was not used:\n%s", g)
	}
	if !strings.Contains(g, "POSTS HEADER") {
		t.Errorf("naming a footer disturbed the header:\n%s", g)
	}

	// "none" still means none, and wins over any path
	save(t, st, "/posts/bare.gmi", "---\nheader: none\nfooter: none\n---\n# Bare")
	g = sy.Resolve("/posts/bare", "").Page.Gemtext
	if strings.Contains(g, "HEADER") || strings.Contains(g, "FOOTER") || strings.Contains(g, "back to") {
		t.Errorf("footer: none did not suppress:\n%s", g)
	}

	// a named file that does not exist means none — not a silent fallback to
	// the folder's, which would be the opposite of what was asked for
	save(t, st, "/posts/typo.gmi", "---\nfooter: /.doesnotexist\n---\n# Typo")
	g = sy.Resolve("/posts/typo", "").Page.Gemtext
	if strings.Contains(g, "back to the gemlog") || strings.Contains(g, "SITE FOOTER") {
		t.Errorf("a missing named footer fell back to something:\n%s", g)
	}

	// directives inside a named wrapper still expand
	save(t, st, "/.footer", "seen {{count}} times")
	g = sy.Resolve("/posts/", "").Page.Gemtext
	if strings.Contains(g, "{{count}}") {
		t.Errorf("directives in a named footer were not expanded:\n%s", g)
	}
}

// A date resolves to a day, so two posts written on the same day used to
// fall back to alphabetical order — which is not an order anyone asked for
// and reads as random. Within a day, the order written is the order shown.
func TestListKeepsChronologyWithinADay(t *testing.T) {
	sy, st := testSite(t)
	save(t, st, "/posts/"+store.FeedMarker, "title: Posts")
	// Same date. The later post is alphabetically LAST, so date order and
	// alphabetical order disagree — otherwise this test passes whether the
	// tiebreak works or not, which is how the bug reached the site.
	save(t, st, "/posts/2026-07-20-apple.gmi", "# Apple\n\nwritten first")
	time.Sleep(1100 * time.Millisecond) // created timestamps are per-second
	save(t, st, "/posts/2026-07-20-zebra.gmi", "# Zebra\n\nwritten second")
	save(t, st, "/posts/2026-07-19-older.gmi", "# Older\n\nyesterday")
	save(t, st, "/index.gmi", "# Home\n\n{{list /posts}}")

	g := sy.Resolve("/", "").Page.Gemtext
	apple := strings.Index(g, "Apple")
	zebra := strings.Index(g, "Zebra")
	older := strings.Index(g, "Older")
	if apple < 0 || zebra < 0 || older < 0 {
		t.Fatalf("listing is missing entries:\n%s", g)
	}
	if zebra > apple {
		t.Errorf("the later post of the day sorted below the earlier one:\n%s", g)
	}
	if apple > older {
		t.Errorf("a newer day sorted below an older one:\n%s", g)
	}

	// and the alphabetical order is available when asked for
	save(t, st, "/alpha.gmi", "# Alpha\n\n{{list /posts name}}")
	g = sy.Resolve("/alpha", "").Page.Gemtext
	if strings.Index(g, "Apple") > strings.Index(g, "Older") ||
		strings.Index(g, "Older") > strings.Index(g, "Zebra") {
		t.Errorf("{{list /posts name}} is not alphabetical:\n%s", g)
	}

	// with a limit too, and the limit still applies to the sorted order
	save(t, st, "/alpha2.gmi", "# Alpha\n\n{{list /posts 2 name}}")
	g = sy.Resolve("/alpha2", "").Page.Gemtext
	if strings.Count(g, "=>") != 2 {
		t.Errorf("limit ignored with an order word:\n%s", g)
	}
	if !strings.Contains(g, "Apple") || strings.Contains(g, "Zebra") {
		t.Errorf("{{list /posts 2 name}} took the wrong two:\n%s", g)
	}

	// a folder that happens to be called /name is still a folder
	save(t, st, "/name/thing.gmi", "# Thing in a folder")
	save(t, st, "/nm.gmi", "# NM\n\n{{list /name}}")
	if g := sy.Resolve("/nm", "").Page.Gemtext; !strings.Contains(g, "Thing in a folder") {
		t.Errorf("{{list /name}} stopped being a folder listing:\n%s", g)
	}
}
