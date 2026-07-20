package store

import (
	"bytes"
	"strings"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCleanPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/index.gmi", "/index.gmi", true},
		{"index.gmi", "/index.gmi", true},
		{"/posts/2026-01-01-hi.gmi", "/posts/2026-01-01-hi.gmi", true},
		{"/posts/.header", "/posts/.header", true},
		{"/.theme", "/.theme", true},
		{"/../etc/passwd", "", false},
		{"/a/../../b", "", false},
		{"/.hidden/дir/x", "", false},
		{"/.git/config", "", false},
		{"/", "", false},
		{"", "", false},
		{"/a//b", "/a/b", true},
		{"/a/./b.gmi", "/a/b.gmi", true},
		{"/x\x00y", "", false},
	}
	for _, c := range cases {
		got, ok := CleanPath(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("CleanPath(%q) = %q,%v; want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSaveGetDelete(t *testing.T) {
	st := openTest(t)
	pg, err := st.SavePage("/about.gmi", []byte("# About me\n\nHello."), "", "test")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if pg.Title != "About me" {
		t.Errorf("title = %q, want About me", pg.Title)
	}
	if pg.Mime != "text/gemini; charset=utf-8" {
		t.Errorf("mime = %q", pg.Mime)
	}
	if pg.Binary {
		t.Error("gemtext marked binary")
	}

	got, err := st.GetPage("/about.gmi")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got.Content, pg.Content) {
		t.Error("content mismatch")
	}

	if err := st.DeletePage("/about.gmi", "test"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetPage("/about.gmi"); err != ErrNotFound {
		t.Errorf("get after delete: %v, want ErrNotFound", err)
	}
	// deletion snapshots a restorable version
	vs, err := st.ListVersions("/about.gmi")
	if err != nil || len(vs) != 1 {
		t.Fatalf("versions after delete = %d,%v; want 1", len(vs), err)
	}
	if _, err := st.RestoreVersion(vs[0].ID, "test"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, err := st.GetPage("/about.gmi"); err != nil || string(got.Content) != "# About me\n\nHello." {
		t.Errorf("restored content wrong: %v", err)
	}
}

func TestVersioningAndPrune(t *testing.T) {
	st := openTest(t)
	st.KeepVersions = 3
	for i := 0; i < 6; i++ {
		content := []byte(strings.Repeat("x", i+1))
		if _, err := st.SavePage("/v.gmi", content, "", "test"); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	vs, err := st.ListVersions("/v.gmi")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("kept %d versions, want 3", len(vs))
	}
	// newest retained snapshot is the 5th save ("xxxxx")
	v, err := st.GetVersion(vs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Content) != "xxxxx" {
		t.Errorf("newest version content = %q, want xxxxx", v.Content)
	}
}

func TestBinaryPage(t *testing.T) {
	st := openTest(t)
	blob := []byte{0x89, 'P', 'N', 'G', 0, 1, 2}
	pg, err := st.SavePage("/media/x.png", blob, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !pg.Binary || pg.Mime != "image/png" {
		t.Errorf("binary=%v mime=%q", pg.Binary, pg.Mime)
	}
	// binary pages must not pollute the search index
	hits, _ := st.Search("PNG", 10)
	if len(hits) != 0 {
		t.Errorf("binary content found in search: %v", hits)
	}
}

func TestSearchFTS(t *testing.T) {
	st := openTest(t)
	must := func(p, c string) {
		t.Helper()
		if _, err := st.SavePage(p, []byte(c), "", "t"); err != nil {
			t.Fatal(err)
		}
	}
	must("/a.gmi", "# Tailscale tricks\n\nWireguard mesh networking made easy.")
	must("/b.gmi", "# Cooking\n\nSourdough starter maintenance.")
	must("/c.gmi", "# Unicycles\n\nWheels and networking with one wheel.")
	must("/.header", "networking should not be indexed here")

	hits, err := st.Search("networking", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (got %+v)", len(hits), hits)
	}
	// title match ranks first
	hits, _ = st.Search("tailscale", 10)
	if len(hits) != 1 || hits[0].Path != "/a.gmi" {
		t.Errorf("tailscale hits = %+v", hits)
	}
	// prefix match
	hits, _ = st.Search("sourdo", 10)
	if len(hits) != 1 || hits[0].Path != "/b.gmi" {
		t.Errorf("prefix hits = %+v", hits)
	}
	// updated content leaves the index
	must("/a.gmi", "# Totally different\n\nNothing to see.")
	hits, _ = st.Search("tailscale", 10)
	if len(hits) != 0 {
		t.Errorf("stale index hit: %+v", hits)
	}
	// hostile query must not error
	if _, err := st.Search(`"unbalanced OR ( NEAR/3`, 10); err != nil {
		t.Errorf("hostile query errored: %v", err)
	}
}

func TestHits(t *testing.T) {
	st := openTest(t)
	st.Bump("/", "http")
	st.Bump("/", "gemini")
	st.Bump("/", "gemini")
	st.Bump("/x", "http+tor")
	if n := st.Count("/"); n != 3 {
		t.Errorf("count(/) = %d, want 3", n)
	}
	hits, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Errorf("stats rows = %d, want 3", len(hits))
	}
	if hits[0].Path != "/" || hits[0].Proto != "gemini" || hits[0].Count != 2 {
		t.Errorf("top stat = %+v", hits[0])
	}
}

func TestNowPosts(t *testing.T) {
	st := openTest(t)
	if _, err := st.AddNow("   "); err == nil {
		t.Error("empty now post accepted")
	}
	p1, err := st.AddNow("first!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddNow("second"); err != nil {
		t.Fatal(err)
	}
	posts, err := st.ListNow(0)
	if err != nil || len(posts) != 2 {
		t.Fatalf("list = %d,%v", len(posts), err)
	}
	if posts[0].Content != "second" {
		t.Errorf("newest first violated: %+v", posts)
	}
	if got, _ := st.ListNow(1); len(got) != 1 {
		t.Errorf("limit ignored")
	}
	if err := st.DeleteNow(p1.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNow(p1.ID); err != ErrNotFound {
		t.Errorf("double delete: %v", err)
	}
}

func TestSettings(t *testing.T) {
	st := openTest(t)
	if v := st.GetSetting("nope"); v != "" {
		t.Errorf("unset = %q", v)
	}
	if err := st.SetSetting("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v := st.GetSetting("k"); v != "v2" {
		t.Errorf("get = %q, want v2", v)
	}
}

func TestListPrefix(t *testing.T) {
	st := openTest(t)
	for _, p := range []string{"/index.gmi", "/posts/a.gmi", "/posts/b.gmi", "/posts/deep/c.gmi", "/postscript.gmi"} {
		if _, err := st.SavePage(p, []byte("# x"), "", "t"); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := st.ListPrefix("/posts/")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Errorf("prefix /posts/ = %d entries (%+v), want 3", len(metas), metas)
	}
}

func TestMimeFor(t *testing.T) {
	cases := map[string]string{
		"/a.gmi":     "text/gemini; charset=utf-8",
		"/x/.header": "text/gemini; charset=utf-8",
		"/x/.theme":  "text/css; charset=utf-8",
		"/a.png":     "image/png",
		"/a.txt":     "text/plain; charset=utf-8",
		"/a.unknown": "application/octet-stream",
	}
	for p, want := range cases {
		if got := MimeFor(p); got != want {
			t.Errorf("MimeFor(%q) = %q, want %q", p, got, want)
		}
	}
}
