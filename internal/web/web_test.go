package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

const testPassword = "correct-horse"

func testServer(t *testing.T) (*Server, *store.Store, *httptest.Server) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Default()
	cfg.Hostname = "test.example"
	cfg.AdminPassword = testPassword

	sessions, _ := auth.NewSessions("")
	srv := &Server{
		Cfg:      cfg,
		Store:    st,
		Site:     site.New(st),
		Log:      log.New(io.Discard),
		Sessions: sessions,
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, st, ts
}

// postNote writes a short note the way the app does: a page in a stream
// folder, marked so its entries stay out of listings.
func postNote(t *testing.T, st *store.Store, folder, body string) string {
	t.Helper()
	if !st.IsFeedFolder(folder) {
		if _, err := st.SavePage(folder+store.FeedMarker,
			store.DefaultFeedMarker("Now", "", 30), "", "t"); err != nil {
			t.Fatal(err)
		}
	}
	p := st.NewStreamPath(folder, time.Now())
	if _, err := st.SavePage(p, []byte(body+"\n"), "", "t"); err != nil {
		t.Fatal(err)
	}
	return p
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPageServing(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Web Home\n=> /about.gmi About"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About"), "", "t")
	_, _ = st.SavePage("/.css", []byte("body{--x:1}"), "", "t")
	_, _ = st.SavePage("/file.txt", []byte("hi"), "", "t")

	code, body := get(t, ts, "/")
	if code != 200 || !strings.Contains(body, "<h1>Web Home</h1>") {
		t.Fatalf("home: %d\n%s", code, body)
	}
	// theme injected
	if !strings.Contains(body, "body{--x:1}") {
		t.Error("theme CSS not injected")
	}
	// .gmi link rewritten for web
	if !strings.Contains(body, `href="/about"`) {
		t.Error("gmi link not rewritten")
	}
	// logged out: no edit link
	if strings.Contains(body, "/admin/edit") {
		t.Error("edit link shown while logged out")
	}
	// extensionless page
	if code, body := get(t, ts, "/about"); code != 200 || !strings.Contains(body, "<h1>About</h1>") {
		t.Errorf("about: %d", code)
	}
	// static file
	if code, body := get(t, ts, "/file.txt"); code != 200 || body != "hi" {
		t.Errorf("file: %d %q", code, body)
	}
	// 404
	if code, _ := get(t, ts, "/nope"); code != 404 {
		t.Errorf("missing = %d", code)
	}
	// hidden specials not served
	if code, _ := get(t, ts, "/.css"); code != 404 {
		t.Errorf("hidden = %d", code)
	}
	// stats bumped under http
	hits, _ := st.Stats()
	for _, h := range hits {
		if h.Proto != "http" {
			t.Errorf("proto = %q", h.Proto)
		}
	}
}

func TestUploadedActiveContentNeutralized(t *testing.T) {
	_, st, ts := testServer(t)
	// an admin-uploaded html file must not be served as active html
	_, _ = st.SavePage("/evil.html", []byte("<script>alert(1)</script>"), "text/html", "t")
	_, _ = st.SavePage("/pic.svg", []byte("<svg onload=alert(1)></svg>"), "image/svg+xml", "t")
	_, _ = st.SavePage("/ok.png", []byte("\x89PNG"), "image/png", "t")

	resp, err := http.Get(ts.URL + "/evil.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/html") {
		t.Errorf("html served with active content-type %q", ct)
	}
	if resp.Header.Get("Content-Disposition") == "" {
		t.Error("html not forced to download")
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}

	// svg likewise
	resp2, _ := http.Get(ts.URL + "/pic.svg")
	if strings.HasPrefix(resp2.Header.Get("Content-Type"), "image/svg") {
		t.Error("svg served as active image/svg")
	}
	resp2.Body.Close()

	// real images still served inline with their type
	resp3, _ := http.Get(ts.URL + "/ok.png")
	if resp3.Header.Get("Content-Type") != "image/png" {
		t.Errorf("png type changed: %q", resp3.Header.Get("Content-Type"))
	}
	if resp3.Header.Get("Content-Disposition") != "" {
		t.Error("png forced to download")
	}
	resp3.Body.Close()
}

func TestAdminFolderBrowser(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	_, _ = st.SavePage("/posts/a.gmi", []byte("# Post A"), "", "t")
	_, _ = st.SavePage("/posts/b.gmi", []byte("# Post B"), "", "t")
	_, _ = st.SavePage("/posts/2026/deep.gmi", []byte("# Deep"), "", "t")
	// these bracket /posts/ alphabetically — the old interleaving trap
	_, _ = st.SavePage("/now.gmi", []byte("# Now"), "", "t")
	_, _ = st.SavePage("/projects.gmi", []byte("# Projects"), "", "t")
	client := login(t, ts, testPassword)

	get := func(path string) string {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	// the listing only — the inline search index below it holds every path
	// by design, so a naive Contains would always "find" everything
	browsed := func(path string) string {
		body := get(path)
		i := strings.Index(body, `<div id="browse">`)
		j := strings.Index(body, `<script id="page-index"`)
		if i < 0 || j < i {
			t.Fatalf("%s: no browse section", path)
		}
		return body[i:j]
	}

	// ---- root shows folders and root files, and nothing deeper ----
	root := browsed("/admin")
	for _, want := range []string{
		`href="/admin?dir=%2Fposts%2F"`, // the folder is a destination
		"now.gmi", "projects.gmi", "index.gmi",
	} {
		if !strings.Contains(root, want) {
			t.Errorf("root screen missing %q", want)
		}
	}
	if strings.Contains(root, "a.gmi") {
		t.Error("root screen leaked a page from inside /posts/")
	}
	// the subfolder count is every descendant, not just direct children
	if !strings.Contains(root, ">3<") {
		t.Error("/posts/ should count 3 descendants")
	}

	// ---- drilling in shows that folder, with a way back ----
	posts := browsed("/admin?dir=/posts/")
	for _, want := range []string{"a.gmi", "b.gmi", "2026/", `href="/admin?dir=%2F"`} {
		if !strings.Contains(posts, want) {
			t.Errorf("/posts/ screen missing %q", want)
		}
	}
	if strings.Contains(posts, "projects.gmi") {
		t.Error("/posts/ screen leaked a root page")
	}
	if !strings.Contains(posts, `action="/admin/feed"`) {
		t.Error("/posts/ screen missing its feed control")
	}
	// deep folders keep working, and the breadcrumb walks back up
	deep := browsed("/admin?dir=/posts/2026/")
	if !strings.Contains(deep, "deep.gmi") || !strings.Contains(deep, `href="/admin?dir=%2Fposts%2F"`) {
		t.Error("nested folder screen or its breadcrumb is wrong")
	}

	// ---- search cuts across folders, so drilling in hides nothing ----
	hits := browsed("/admin?q=post")
	for _, want := range []string{"/posts/a.gmi", "/posts/b.gmi"} {
		if !strings.Contains(hits, want) {
			t.Errorf("search missing %q", want)
		}
	}
	if strings.Contains(hits, "projects.gmi") {
		t.Error("search matched a page it should not have")
	}
	// and the same index is inlined for the live filter
	if full := get("/admin"); !strings.Contains(full, `id="page-index"`) || !strings.Contains(full, `"/posts/a.gmi"`) {
		t.Error("inline search index missing or incomplete")
	}
	// every file row still offers delete, carrying its folder for the return trip
	if !strings.Contains(posts, `<input type="hidden" name="path" value="/posts/a.gmi">`) ||
		!strings.Contains(posts, `<input type="hidden" name="dir" value="/posts/">`) {
		t.Error("row delete form missing its path or return folder")
	}
}

func TestNormFolderRejectsTraversal(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "/",
		"/":                 "/",
		"posts":             "/posts/",
		"/posts/":           "/posts/",
		"/posts/../../etc/": "/etc/", // climbing out lands inside, not above
		"/../..":            "/",
		"//posts//2026//":   "/posts/2026/",
	} {
		if got := normFolder(in); got != want {
			t.Errorf("normFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeleteFromList(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	_, _ = st.SavePage("/media/pic.png", []byte("\x89PNG"), "image/png", "t")
	client := login(t, ts, testPassword)

	// a binary/uploaded file can be deleted straight from the list
	resp, err := client.PostForm(ts.URL+"/admin/delete", url.Values{"path": {"/media/pic.png"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := st.GetPage("/media/pic.png"); err == nil {
		t.Error("uploaded file survived delete")
	}
	// ...and is still recoverable
	vs, _ := st.ListVersions("/media/pic.png")
	if len(vs) == 0 {
		t.Error("deleted file left no restorable version")
	}
}

func TestFolderFeeds(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/posts/"+store.FeedMarker, []byte("title: Gemlog\n"), "", "t")
	_, _ = st.SavePage("/projects/"+store.FeedMarker, []byte(""), "", "t")
	_, _ = st.SavePage("/posts/index.gmi", []byte("# Gemlog"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-20-hi.gmi", []byte("# Hi\n\nbody"), "", "t")
	_, _ = st.SavePage("/projects/2026-07-01-thing.gmi", []byte("# Thing\n\nbody"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About"), "", "t")

	// each folder that is turned on publishes its own feed
	code, posts := get(t, ts, "/posts/feed.xml")
	if code != 200 || !strings.Contains(posts, "<title>Gemlog</title>") || !strings.Contains(posts, "Hi") {
		t.Fatalf("auto posts feed: %d\n%s", code, posts)
	}
	code, proj := get(t, ts, "/projects/feed.xml")
	if code != 200 || !strings.Contains(proj, "Thing") {
		t.Fatalf("auto projects feed: %d", code)
	}
	if strings.Contains(proj, "Hi") {
		t.Error("projects feed leaked posts")
	}
	// a folder that was never turned on has no feed
	if code, _ := get(t, ts, "/media/feed.xml"); code != 404 {
		t.Errorf("feed invented for an unmarked folder: %d", code)
	}
	// both are advertised for discovery
	_, home := get(t, ts, "/")
	for _, want := range []string{`href="/posts/feed.xml"`, `href="/projects/feed.xml"`} {
		if !strings.Contains(home, want) {
			t.Errorf("auto feed not advertised: %s", want)
		}
	}
}

func TestNewPageNameFollowsTheFolder(t *testing.T) {
	_, st, ts := testServer(t)
	// a folder that publishes: dated names by default
	_, _ = st.SavePage("/posts/"+store.FeedMarker, []byte("title: Posts"), "", "t")
	// one that names its pages completely, for notes
	_, _ = st.SavePage("/now/"+store.FeedMarker, []byte("title: Now\nprefix: datetime"), "", "t")
	// one that publishes but wants plain names
	_, _ = st.SavePage("/docs/"+store.FeedMarker, []byte("title: Docs\nprefix: none"), "", "t")
	// and a folder with no feed at all
	_, _ = st.SavePage("/media/notes.gmi", []byte("# Notes"), "", "t")
	client := login(t, ts, testPassword)

	pathField := func(folder string) string {
		resp, _ := client.Get(ts.URL + "/admin/edit?new=1&path=" + url.QueryEscape(folder))
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		m := regexp.MustCompile(`id="path"[^>]*value="([^"]*)"`).FindStringSubmatch(string(b))
		if m == nil {
			m = regexp.MustCompile(`value="([^"]*)"[^>]*id="path"`).FindStringSubmatch(string(b))
		}
		if m == nil {
			t.Fatalf("no path field for %s", folder)
		}
		return m[1]
	}

	today := time.Now().Format("2006-01-02")
	if got, want := pathField("/posts/"), "/posts/"+today+"-"; got != want {
		t.Errorf("feed folder prefill = %q, want %q", got, want)
	}
	// datetime is a complete, saveable name — no slug to add
	got := pathField("/now/")
	if !regexp.MustCompile(`^/now/\d{4}-\d{2}-\d{2}-\d{4}\.gmi$`).MatchString(got) {
		t.Errorf("notes folder prefill = %q, want a complete dated filename", got)
	}
	if got, want := pathField("/docs/"), "/docs/"; got != want {
		t.Errorf("prefix:none prefill = %q, want %q", got, want)
	}
	if got, want := pathField("/media/"), "/media/"; got != want {
		t.Errorf("plain folder prefill = %q, want %q", got, want)
	}
	// there is exactly one create action, and it is the same link everywhere
	resp, _ := client.Get(ts.URL + "/admin")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "+ note") {
		t.Error("a second create verb came back")
	}
	if !strings.Contains(string(b), `href="/admin/edit?new=1&amp;path=%2Fposts%2F"`) {
		t.Error("no create link on the folder row")
	}
}

func TestPrefixToggleEditsOnlyItsLine(t *testing.T) {
	_, st, ts := testServer(t)
	// a .feed with comments, an unknown key and hand-chosen ordering: a
	// click must not flatten any of it
	body := "# my notes\nlimit: 5\nprefix: date\nauthor: jeff\nsomething_new: 1\n"
	_, _ = st.SavePage("/posts/"+store.FeedMarker, []byte(body), "", "t")
	client := login(t, ts, testPassword)

	form := url.Values{"folder": {"/posts/"}, "prefix": {"datetime"}}
	resp, err := client.PostForm(ts.URL+"/admin/prefix", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	pg, err := st.GetPage("/posts/" + store.FeedMarker)
	if err != nil {
		t.Fatal(err)
	}
	got := string(pg.Content)
	for _, want := range []string{"# my notes", "limit: 5", "prefix: datetime", "author: jeff", "something_new: 1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewrite lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "prefix: date\n") {
		t.Error("old value survived")
	}
	if st.NamePrefix("/posts/") != "datetime" {
		t.Error("setting did not take effect")
	}
	// an unsupported value is refused rather than written
	resp2, _ := client.PostForm(ts.URL+"/admin/prefix", url.Values{"folder": {"/posts/"}, "prefix": {"../evil"}})
	resp2.Body.Close()
	if st.NamePrefix("/posts/") != "datetime" {
		t.Error("junk value was accepted")
	}
}

// The preview must show what the saved page will look like — not the raw
// source. Front matter is the case that gave it away.
func TestFolderFileOrder(t *testing.T) {
	_, st, ts := testServer(t)
	// a folder that publishes: machinery first, then newest entry first
	_, _ = st.SavePage("/posts/"+store.FeedMarker, []byte("title: Posts"), "", "t")
	_, _ = st.SavePage("/posts/.header", []byte("hi"), "", "t")
	_, _ = st.SavePage("/posts/index.gmi", []byte("# Posts"), "", "t")
	_, _ = st.SavePage("/posts/2026-01-05-alpha.gmi", []byte("# Alpha"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-02-zulu.gmi", []byte("# Zulu"), "", "t")
	_, _ = st.SavePage("/posts/2026-03-11-mike.gmi", []byte("# Mike"), "", "t")
	// two on the same day: the time in the name is the only order there is
	_, _ = st.SavePage("/posts/2026-07-02-0900.gmi", []byte("morning"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-02-2130.gmi", []byte("evening"), "", "t")
	// an ordinary folder stays alphabetical: undated pages have no chronology
	_, _ = st.SavePage("/docs/zebra.gmi", []byte("# Z"), "", "t")
	_, _ = st.SavePage("/docs/index.gmi", []byte("# Docs"), "", "t")
	_, _ = st.SavePage("/docs/apple.gmi", []byte("# A"), "", "t")
	client := login(t, ts, testPassword)

	order := func(dir string) []string {
		resp, _ := client.Get(ts.URL + "/admin?dir=" + url.QueryEscape(dir))
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(b)
		if i := strings.Index(body, `<script id="page-index"`); i > 0 {
			body = body[:i] // the inline index lists every page; ignore it
		}
		var out []string
		for _, m := range regexp.MustCompile(`<tr class="page-row[^"]*"><td><a href="/admin/edit\?path=([^"]+)"`).FindAllStringSubmatch(body, -1) {
			p, _ := url.QueryUnescape(m[1])
			out = append(out, strings.TrimPrefix(p, dir))
		}
		return out
	}

	got := order("/posts/")
	want := []string{".feed", ".header", "index.gmi",
		"2026-07-02-zulu.gmi", "2026-07-02-2130.gmi", "2026-07-02-0900.gmi",
		"2026-03-11-mike.gmi", "2026-01-05-alpha.gmi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("feed folder order:\n got %v\nwant %v", got, want)
	}

	got = order("/docs/")
	want = []string{"index.gmi", "apple.gmi", "zebra.gmi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("plain folder order:\n got %v\nwant %v", got, want)
	}
}

func TestPreviewAssemblesLikeThePage(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/.header", []byte("=> / home"), "", "t")
	_, _ = st.SavePage("/posts/2026-01-01-old.gmi", []byte("# Old"), "", "t")
	client := login(t, ts, testPassword)

	draft := "---\ntitle: Draft\n---\n# Real Heading\n\n{{list /posts}}\n"
	resp, err := client.Post(ts.URL+"/api/preview?path="+url.QueryEscape("/posts/new.gmi"),
		"text/plain", strings.NewReader(draft))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := string(b)
	if strings.Contains(got, "---") || strings.Contains(got, "title: Draft") {
		t.Errorf("front matter leaked into the preview:\n%s", got)
	}
	if !strings.Contains(got, "Real Heading") {
		t.Errorf("body missing:\n%s", got)
	}
	if !strings.Contains(got, "home") {
		t.Errorf("inherited .header missing:\n%s", got)
	}
	if !strings.Contains(got, "Old") || strings.Contains(got, "{{list") {
		t.Errorf("directives not expanded:\n%s", got)
	}
	// front matter can still switch the header off, as on a real page
	resp2, _ := client.Post(ts.URL+"/api/preview?path="+url.QueryEscape("/posts/new.gmi"),
		"text/plain", strings.NewReader("---\nheader: none\n---\n# Bare\n"))
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if strings.Contains(string(b2), "home") {
		t.Errorf("header: none ignored in preview:\n%s", string(b2))
	}
	// previewing a brand-new page in a folder that does not exist must not panic
	resp3, _ := client.Post(ts.URL+"/api/preview?path="+url.QueryEscape("/brand/new/thing.gmi"),
		"text/plain", strings.NewReader("# Hi\n"))
	if resp3.StatusCode != 200 {
		t.Errorf("preview of an unsaved page: %d", resp3.StatusCode)
	}
	resp3.Body.Close()
}

func TestFeedToggle(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/journal/index.gmi", []byte("# Field Notes"), "", "t")
	_, _ = st.SavePage("/journal/hello-world.gmi", []byte("# Hello World\n\nbody"), "", "t")
	client := login(t, ts, testPassword)

	// undated pages in an unmarked folder: no feed
	if code, _ := get(t, ts, "/journal/feed.xml"); code != 404 {
		t.Fatalf("unmarked folder already has a feed: %d", code)
	}

	// a folder's own screen offers the toggle; the root screen does not
	resp, _ := client.Get(ts.URL + "/admin?dir=/journal/")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `action="/admin/feed"`) {
		t.Error("no feed toggle on the folder screen")
	}
	resp2, _ := client.Get(ts.URL + "/admin")
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if strings.Contains(string(b2), `action="/admin/feed"`) {
		t.Error("root folder should not offer a feed toggle")
	}

	// enable it
	resp2, err := client.PostForm(ts.URL+"/admin/feed", url.Values{
		"folder": {"/journal/"}, "enable": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if !st.IsFeedFolder("/journal/") {
		t.Fatal("folder not marked after enabling")
	}
	// now the undated page is a post, dated from the database
	code, xml := get(t, ts, "/journal/feed.xml")
	if code != 200 || !strings.Contains(xml, "Hello World") {
		t.Fatalf("feed after enable: %d\n%s", code, xml)
	}
	if !strings.Contains(xml, "<title>Field Notes</title>") {
		t.Error("feed title should come from the folder index")
	}
	// and it is advertised for discovery
	_, home := get(t, ts, "/")
	if !strings.Contains(home, `href="/journal/feed.xml"`) {
		t.Error("enabled feed not advertised in <head>")
	}

	// disable it again
	resp3, _ := client.PostForm(ts.URL+"/admin/feed", url.Values{
		"folder": {"/journal/"}, "enable": {"false"}})
	resp3.Body.Close()
	if st.IsFeedFolder("/journal/") {
		t.Error("folder still marked after disabling")
	}
	if code, _ := get(t, ts, "/journal/feed.xml"); code != 404 {
		t.Errorf("feed still served after disable: %d", code)
	}
}

func TestAdminManual(t *testing.T) {
	srv, st, _ := testServer(t)
	srv.Cfg.SSH = config.SSHService{Service: config.Service{Enabled: true, Addr: ":22"}}
	srv.Cfg.Telnet = config.Service{Enabled: true, Addr: ":23"}
	srv.Cfg.Gemini = config.Service{Enabled: true, Addr: ":1965"}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	client := login(t, ts, testPassword)
	resp, err := client.Get(ts.URL + "/admin/manual")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	// it documents the doors that are actually switched on, with this host
	for _, want := range []string{
		"ssh guest@test.example", "telnet test.example", "gemini://test.example/",
		"{{list [folder] [limit] [name]}}", ".feed", "feed on", "datetime",
		"YYYY-MM-DD-", "/mcp",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manual missing %q", want)
		}
	}
	// ...and not ones that are off
	srv.Cfg.Telnet.Enabled = false
	ts2 := httptest.NewServer(srv.Handler())
	defer ts2.Close()
	c2 := login(t, ts2, testPassword)
	r2, _ := c2.Get(ts2.URL + "/admin/manual")
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if strings.Contains(string(b2), "telnet test.example") {
		t.Error("manual lists a door that is switched off")
	}
	// the manual and the editor popover share one reference
	r3, _ := c2.Get(ts2.URL + "/admin/edit?path=/index.gmi")
	b3, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if !strings.Contains(string(b3), "{{stream [folder] [limit]}}") || !strings.Contains(string(b2), "{{stream [folder] [limit]}}") {
		t.Error("syntax reference not shared between editor and manual")
	}
}

func TestSearchPage(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/x.gmi", []byte("# Xylophones\n\nmusic and mallets"), "", "t")
	code, body := get(t, ts, "/search?q=mallets")
	if code != 200 || !strings.Contains(body, `href="/x"`) {
		t.Errorf("search: %d\n%s", code, body)
	}
}

func TestFeed(t *testing.T) {
	srv, st, _ := testServer(t)
	// a site-wide feed is one of the things config is still for
	srv.Cfg.Feeds.List = []config.Feed{{Path: "/feed.xml", Source: "/"}}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_, _ = st.SavePage("/posts/2026-07-19-hello.gmi", []byte("# Hello World"), "", "t")
	_, _ = st.SavePage("/posts/undated.gmi", []byte("# No date"), "", "t")
	code, body := get(t, ts, "/feed.xml")
	if code != 200 || !strings.Contains(body, "<title>Hello World</title>") {
		t.Errorf("feed: %d\n%s", code, body)
	}
	// outside a feed folder an undated page is a page, not a post
	if strings.Contains(body, "No date") {
		t.Error("undated page in a site-wide feed")
	}
}

func TestConfiguredFeeds(t *testing.T) {
	srv, st, _ := testServer(t)
	srv.Cfg.Feeds = config.Feeds{
		Author: "Jeff",
		List: []config.Feed{
			{Path: "/posts/feed.xml", Source: "/posts/", Title: "gemlog"},
			{Path: "/notes/feed.xml", Source: "/notes/", Title: "notes"},
		},
	}
	ts2 := httptest.NewServer(srv.Handler())
	defer ts2.Close()

	_, _ = st.SavePage("/posts/2026-07-19-hello.gmi", []byte("# Hello World\n\nbody"), "", "t")
	_, _ = st.SavePage("/elsewhere/2026-07-19-other.gmi", []byte("# Other"), "", "t")
	postNote(t, st, "/notes/", "a now update")

	code, posts := get(t, ts2, "/posts/feed.xml")
	if code != 200 || !strings.Contains(posts, "Hello World") {
		t.Fatalf("posts feed: %d\n%s", code, posts)
	}
	if strings.Contains(posts, "Other") {
		t.Error("posts feed leaked another folder")
	}
	if !strings.Contains(posts, "<author><name>Jeff</name></author>") {
		t.Error("configured author missing")
	}

	code, now := get(t, ts2, "/notes/feed.xml")
	if code != 200 || !strings.Contains(now, "a now update") {
		t.Fatalf("notes feed: %d\n%s", code, now)
	}
	if strings.Contains(now, "Hello World") {
		t.Error("notes feed contains other folders' pages")
	}

	// both feeds are advertised for discovery in the HTML head
	_, home := get(t, ts2, "/")
	for _, want := range []string{`href="/posts/feed.xml"`, `href="/notes/feed.xml"`} {
		if !strings.Contains(home, want) {
			t.Errorf("feed not advertised in <head>: %s", want)
		}
	}
}

func login(t *testing.T, ts *httptest.Server, password string) *http.Client {
	t.Helper()
	jar := newJar()
	client := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(ts.URL+"/login", url.Values{"password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return client
}

func TestLoginFlow(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	// wrong password: no cookie
	client := login(t, ts, "wrong")
	resp, _ := client.Get(ts.URL + "/admin")
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("admin without session = %d, want redirect", resp.StatusCode)
	}
	resp.Body.Close()

	// right password: session works, edit link appears
	client = login(t, ts, testPassword)
	resp, err := client.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `<h1><a href="/admin">/</a>`) {
		t.Fatalf("admin: %d", resp.StatusCode)
	}
	resp, _ = client.Get(ts.URL + "/")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "/admin/edit?path=%2findex.gmi") {
		t.Error("edit link missing when logged in")
	}
}

// The published lifecycle: publish, edit, rename, delete, restore. Every
// save here says publish=1, because a bare save is now a draft.
func TestAdminSaveDeleteRestore(t *testing.T) {
	_, st, ts := testServer(t)
	client := login(t, ts, testPassword)

	// create
	resp, err := client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/made.gmi"}, "content": {"# Made\r\nin a form"}, "publish": {"1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	pg, err := st.GetPage("/made.gmi")
	if err != nil || string(pg.Content) != "# Made\nin a form" {
		t.Fatalf("saved page: %v %q", err, pg.Content)
	}

	// edit → snapshots version
	resp, _ = client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/made.gmi"}, "oldpath": {"/made.gmi"}, "content": {"# Made v2"}, "publish": {"1"},
	})
	resp.Body.Close()

	// rename moves content, deletes old
	resp, _ = client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/renamed.gmi"}, "oldpath": {"/made.gmi"}, "content": {"# Renamed"}, "publish": {"1"},
	})
	resp.Body.Close()
	if _, err := st.GetPage("/made.gmi"); err == nil {
		t.Error("old path survived rename")
	}
	if _, err := st.GetPage("/renamed.gmi"); err != nil {
		t.Error("new path missing after rename")
	}

	// delete
	resp, _ = client.PostForm(ts.URL+"/admin/delete", url.Values{"path": {"/renamed.gmi"}})
	resp.Body.Close()
	if _, err := st.GetPage("/renamed.gmi"); err == nil {
		t.Error("page survived delete")
	}

	// restore from history
	vs, _ := st.ListVersions("/renamed.gmi")
	if len(vs) == 0 {
		t.Fatal("no versions after delete")
	}
	resp, _ = client.PostForm(ts.URL+"/admin/restore", url.Values{"id": {fmt.Sprint(vs[0].ID)}})
	resp.Body.Close()
	if _, err := st.GetPage("/renamed.gmi"); err != nil {
		t.Error("restore failed")
	}
}

func TestEditorPage(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/page.gmi", []byte("# Editable\n{{count}}"), "", "t")
	client := login(t, ts, testPassword)

	resp, err := client.Get(ts.URL + "/admin/edit?path=/page.gmi")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)
	for _, want := range []string{`id="ed"`, `id="content"`, `id="pv-toggle"`, "{{count}}", `class="editor-body"`} {
		if !strings.Contains(body, want) {
			t.Errorf("editor missing %s", want)
		}
	}
	// raw source must be escaped, not rendered
	if strings.Contains(body, "<h1>Editable</h1>") {
		t.Error("editor rendered content instead of source")
	}

	// new-page editor
	resp, _ = client.Get(ts.URL + "/admin/edit?path=&new=1")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `id="ed"`) || strings.Contains(string(b), `name="oldpath"`) {
		t.Error("new-page editor wrong")
	}
}

func TestAPIAuthAndCRUD(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	// no auth
	if code, _ := get(t, ts, "/api/pages"); code != 401 {
		t.Fatalf("unauthed = %d", code)
	}

	do := func(method, path, body, ctype string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testPassword)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// wrong bearer
	req, _ := http.NewRequest("GET", ts.URL+"/api/pages", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Errorf("bad bearer = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// list
	if code, body := do("GET", "/api/pages", "", ""); code != 200 || !strings.Contains(body, "/index.gmi") {
		t.Errorf("list: %d %s", code, body)
	}
	// put
	if code, _ := do("PUT", "/api/pages/api-made.gmi", "# Via API", "text/gemini"); code != 200 {
		t.Errorf("put failed")
	}
	// get
	code, body := do("GET", "/api/pages/api-made.gmi", "", "")
	if code != 200 || !strings.Contains(body, "# Via API") {
		t.Errorf("get: %d %s", code, body)
	}
	// search
	if code, body := do("GET", "/api/search?q=API", "", ""); code != 200 || !strings.Contains(body, "api-made") {
		t.Errorf("search: %d %s", code, body)
	}
	// now
	if code, _ := do("POST", "/api/now", `{"content":"api now post"}`, "application/json"); code != 200 {
		t.Errorf("now post failed")
	}
	if code, body := do("GET", "/api/now", "", ""); code != 200 || !strings.Contains(body, "api now post") {
		t.Errorf("now list: %d %s", code, body)
	}
	// versions + restore
	do("PUT", "/api/pages/api-made.gmi", "# Via API v2", "text/gemini")
	code, body = do("GET", "/api/versions?path=/api-made.gmi", "", "")
	if code != 200 || !strings.Contains(body, `"id"`) {
		t.Fatalf("versions: %d %s", code, body)
	}
	var vr struct {
		Versions []struct {
			ID int64 `json:"id"`
		} `json:"versions"`
	}
	_ = json.Unmarshal([]byte(body), &vr)
	if len(vr.Versions) == 0 {
		t.Fatal("no versions")
	}
	if code, _ := do("POST", fmt.Sprintf("/api/restore?id=%d", vr.Versions[0].ID), "", ""); code != 200 {
		t.Errorf("restore failed")
	}
	code, body = do("GET", "/api/pages/api-made.gmi", "", "")
	if !strings.Contains(body, "# Via API\n") && !strings.Contains(body, `"content":"# Via API"`) {
		t.Errorf("restored content: %s", body)
	}
	// delete
	if code, _ := do("DELETE", "/api/pages/api-made.gmi", "", ""); code != 200 {
		t.Errorf("delete failed")
	}
	// stats
	if code, _ := do("GET", "/api/stats", "", ""); code != 200 {
		t.Errorf("stats failed")
	}
	// oversized body rejected
	srvCfgTest := strings.Repeat("x", 11<<20)
	if code, _ := do("PUT", "/api/pages/big.gmi", srvCfgTest, "text/gemini"); code != 413 {
		t.Errorf("oversize accepted: %d", code)
	}
}

func TestLoginBruteForceLockout(t *testing.T) {
	srv, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	srv.authGate().SetMax(3)
	// exhaust the allowance with wrong passwords
	for i := 0; i < 3; i++ {
		resp, _ := http.PostForm(ts.URL+"/login", url.Values{"password": {"wrong"}})
		resp.Body.Close()
	}
	// now even the CORRECT password is refused while locked
	resp, err := http.PostForm(ts.URL+"/login", url.Values{"password": {testPassword}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "Too many attempts") == false && resp.StatusCode == http.StatusSeeOther {
		t.Error("correct password accepted despite lockout")
	}
}

func TestBearerBruteForceLockout(t *testing.T) {
	srv, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	srv.authGate().SetMax(3)
	bad := func() int {
		req, _ := http.NewRequest("GET", ts.URL+"/api/pages", nil)
		req.Header.Set("Authorization", "Bearer nope")
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 3; i++ {
		bad()
	}
	// correct bearer now rejected while locked out
	req, _ := http.NewRequest("GET", ts.URL+"/api/pages", nil)
	req.Header.Set("Authorization", "Bearer "+testPassword)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Error("correct bearer accepted despite lockout")
	}
}

func TestMCP(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	rpc := func(id, method, params string) (int, string) {
		t.Helper()
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%q,"params":%s}`, id, method, params)
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testPassword)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// unauthorized
	resp, _ := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if resp.StatusCode != 401 {
		t.Fatalf("unauthed mcp = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// initialize
	code, body := rpc("1", "initialize", `{}`)
	if code != 200 || !strings.Contains(body, "starpulse") || !strings.Contains(body, "protocolVersion") {
		t.Fatalf("initialize: %d %s", code, body)
	}
	// tools/list
	code, body = rpc("2", "tools/list", `{}`)
	if code != 200 || !strings.Contains(body, "write_page") || !strings.Contains(body, "post_now") {
		t.Fatalf("tools/list: %d %s", code, body)
	}
	// write + read roundtrip
	code, body = rpc("3", "tools/call", `{"name":"write_page","arguments":{"path":"/mcp.gmi","content":"# From MCP"}}`)
	if code != 200 || !strings.Contains(body, "saved /mcp.gmi") {
		t.Fatalf("write_page: %d %s", code, body)
	}
	code, body = rpc("4", "tools/call", `{"name":"read_page","arguments":{"path":"/mcp.gmi"}}`)
	if code != 200 || !strings.Contains(body, "# From MCP") {
		t.Fatalf("read_page: %d %s", code, body)
	}
	// search tool
	code, body = rpc("5", "tools/call", `{"name":"search","arguments":{"query":"MCP"}}`)
	if code != 200 || !strings.Contains(body, "/mcp.gmi") {
		t.Fatalf("search: %d %s", code, body)
	}
	// unknown tool is an in-band error
	code, body = rpc("6", "tools/call", `{"name":"explode","arguments":{}}`)
	if code != 200 || !strings.Contains(body, "unknown tool") {
		t.Fatalf("unknown tool: %d %s", code, body)
	}
	// unknown method is a JSON-RPC error
	code, body = rpc("7", "nope", `{}`)
	if code != 200 || !strings.Contains(body, "-32601") {
		t.Fatalf("unknown method: %d %s", code, body)
	}
}

func TestEditorSyntaxHelp(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	client := login(t, ts, testPassword)
	resp, err := client.Get(ts.URL + "/admin/edit?path=/index.gmi")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	for _, want := range []string{
		"syntax-help",
		"{{list [folder] [limit]}}",
		"{{include /path}}",
		"{{rev}}",
		".css",
		"header: none",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("editor help missing %q", want)
		}
	}
}

func TestPreviewEndpoint(t *testing.T) {
	_, _, ts := testServer(t)
	req, _ := http.NewRequest("POST", ts.URL+"/api/preview", strings.NewReader("# Preview me"))
	req.Header.Set("Authorization", "Bearer "+testPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "<h1>Preview me</h1>") {
		t.Errorf("preview: %s", b)
	}
}

// minimal cookie jar (no external deps)
type jarT struct{ cookies []*http.Cookie }

func newJar() *jarT { return &jarT{} }

func (j *jarT) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.cookies = append(j.cookies, cookies...)
}

func (j *jarT) Cookies(u *url.URL) []*http.Cookie { return j.cookies }

// Password managers classify a form by its contents. A lone password box is
// ambiguous — the username field is what a saved item attaches to, and the
// autocomplete tokens are what separate "sign in" from "change password".
// These are invisible in use and easy to drop in a redesign, so they are
// pinned here.
func TestLoginFormIsRecognizableToPasswordManagers(t *testing.T) {
	_, _, ts := testServer(t)
	resp, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)

	for _, want := range []string{
		`method="post" action="/login"`,   // a real form, posting to a login URL
		`autocomplete="username"`,         // something to attach the saved item to
		`autocomplete="current-password"`, // sign in, not change-password
		`type="password"`,
		`<button type="submit">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login form missing %s", want)
		}
	}
	// autocomplete="off" anywhere on this form defeats the whole point
	if strings.Contains(body, `autocomplete="off"`) {
		t.Error("login form disables autocomplete")
	}

	// the username is fixed, so it must not be able to lock anyone out: the
	// password alone decides, whatever the field says
	for _, user := range []string{"admin", "", "someone-else"} {
		form := url.Values{"password": {testPassword}}
		if user != "" {
			form.Set("username", user)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		r, err := client.PostForm(ts.URL+"/login", form)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusSeeOther {
			t.Errorf("username %q blocked a correct password: %d", user, r.StatusCode)
		}
	}
}

// The editor's textarea paints no glyphs of its own — the highlight layer
// beneath does — so it is tempting to make selected text transparent too.
// Safari ignores that and paints its own selection foreground, which lands
// as unreadable pale-on-orange. Selected text must name a real colour.
func TestEditorSelectionIsReadable(t *testing.T) {
	css, err := assets.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(css), "\n") {
		if !strings.Contains(line, "::selection") {
			continue
		}
		if strings.Contains(line, "color: transparent") {
			t.Errorf("a ::selection rule makes text transparent — Safari renders that unreadable:\n%s", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(string(css), "textarea::selection { background: var(--sel); color: var(--fg); }") {
		t.Error("the editor's selection rule is missing or changed shape")
	}
}

// The stats table has always had an https column and nothing ever filled it:
// every web request bucketed as plain http, so an encrypted visit could not
// be told from a cleartext one.
func TestStatsDistinguishHTTPFromHTTPS(t *testing.T) {
	srv, st, _ := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	plain := httptest.NewServer(srv.Handler())
	defer plain.Close()
	secure := httptest.NewTLSServer(srv.Handler())
	defer secure.Close()

	resp, err := http.Get(plain.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	r2, err := secure.Client().Get(secure.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()

	got := map[string]int64{}
	hits, _ := st.Stats()
	for _, h := range hits {
		got[h.Proto] += h.Count
	}
	if got["http"] != 1 {
		t.Errorf("http views = %d, want 1 (have %v)", got["http"], got)
	}
	if got["https"] != 1 {
		t.Errorf("https views = %d, want 1 (have %v)", got["https"], got)
	}
}

// The Host header is written by whoever is asking, so it is not evidence of
// having come through tor. Trusting it let anyone fetch the site in
// cleartext on port 80 — and be counted as a tor visitor — by sending the
// onion name.
func TestOnionClaimsNeedToComeFromTor(t *testing.T) {
	srv, st, _ := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	srv.Onion = func() string { return "abcdefghij.onion" } // as if one were running

	ask := func(remote string) string {
		r := httptest.NewRequest("GET", "http://abcdefghij.onion/", nil)
		r.RemoteAddr = remote
		return srv.proto(r)
	}
	if got := ask("127.0.0.1:41234"); got != "http+tor" {
		t.Errorf("tor's own forward bucketed as %q, want http+tor", got)
	}
	if got := ask("[::1]:41234"); got != "http+tor" {
		t.Errorf("ipv6 loopback bucketed as %q, want http+tor", got)
	}
	if got := ask("203.0.113.9:5555"); got != "http" {
		t.Errorf("a public address claiming to be the onion bucketed as %q, want http", got)
	}
	// and an ordinary request is unaffected
	plain := httptest.NewRequest("GET", "http://test.example/", nil)
	plain.RemoteAddr = "203.0.113.9:5555"
	if got := srv.proto(plain); got != "http" {
		t.Errorf("ordinary request bucketed as %q", got)
	}
}

// The "also on gemini" footer should point at the same page on gemini, not
// always the root.
func TestGeminiFooterLinksToTheSamePage(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	_, _ = st.SavePage("/posts/hi.gmi", []byte("# Hi"), "", "t")

	page := func(path string) string {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	if !strings.Contains(page("/posts/hi"), `href="gemini://test.example/posts/hi"`) {
		t.Error("footer does not link to the same page on gemini")
	}
	if !strings.Contains(page("/"), `href="gemini://test.example/"`) {
		t.Error("home footer should be the gemini root")
	}
	// admin has no gemini equivalent — falls back to root
	client := login(t, ts, testPassword)
	resp, _ := client.Get(ts.URL + "/admin")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), `gemini://test.example/admin`) {
		t.Error("admin page linked to a nonexistent gemini path")
	}
}
