package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The whole point of drafts living in their own table is that no reader has
// to remember to exclude them. This checks the doors that share this
// process — the site resolver behind the web pages, feeds, search, listings
// and the API — for a draft that has never been published and for one
// sitting over a published page.
func TestDraftsReachNoPublicDoor(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home\n\n{{list /posts}}"), "", "t")
	_, _ = st.SavePage("/posts/.feed", []byte("title: Posts"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-19-live.gmi", []byte("# Live post\n\nPUBLISHED-BODY"), "", "t")

	// unpublished work: one brand new, one rewriting a published page
	_, _ = st.SaveDraft("/posts/2026-07-20-secret.gmi", []byte("# Secret post\n\nSECRET-BODY"), "", "web")
	_, _ = st.SaveDraft("/posts/2026-07-19-live.gmi", []byte("# Live post\n\nREWRITTEN-BODY"), "", "web")

	get := func(path string) (int, string) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}

	// the never-published page is not there at all
	if code, _ := get("/posts/2026-07-20-secret"); code != http.StatusNotFound {
		t.Errorf("a draft-only page answered %d, want 404", code)
	}
	// the published page still shows what was published
	code, body := get("/posts/2026-07-19-live")
	if code != 200 || !strings.Contains(body, "PUBLISHED-BODY") {
		t.Errorf("published page = %d, %q", code, firstLines(body))
	}
	if strings.Contains(body, "REWRITTEN-BODY") {
		t.Error("the draft was served instead of the published page")
	}

	// nothing anywhere mentions either draft
	for _, path := range []string{
		"/", "/posts/", "/posts/feed.xml", "/search?q=secret", "/search?q=rewritten",
	} {
		_, body := get(path)
		for _, leak := range []string{"SECRET-BODY", "REWRITTEN-BODY", "Secret post"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s leaked %q:\n%s", path, leak, firstLines(body))
			}
		}
	}

	// the API serves pages, not drafts
	client := login(t, ts, testPassword)
	resp, _ := client.Get(ts.URL + "/api/pages")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "secret") {
		t.Error("a draft appeared in /api/pages")
	}

	// ...and a backup is of what is published
	resp2, _ := client.Get(ts.URL + "/admin/backup.zip")
	zipBytes, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if strings.Contains(string(zipBytes), "SECRET-BODY") {
		t.Error("a draft was written into the backup as published content")
	}
}

// The editor is where drafts are visible: it loads one if there is one, and
// says so.
func TestEditorLoadsAndPublishesDrafts(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/page.gmi", []byte("# Page\n\nlive words"), "", "t")
	client := login(t, ts, testPassword)

	editor := func() string {
		resp, err := client.Get(ts.URL + "/admin/edit?path=" + url.QueryEscape("/page.gmi"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}

	// no draft yet: the editor shows what is live, and offers no discard
	if body := editor(); !strings.Contains(body, "live words") || strings.Contains(body, `formaction="/admin/discard"`) {
		t.Error("editor without a draft is wrong")
	}

	// a bare save makes a draft and leaves the page alone
	resp, err := client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/page.gmi"}, "oldpath": {"/page.gmi"}, "content": {"# Page\n\ndraft words"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if pg, _ := st.GetPage("/page.gmi"); !strings.Contains(string(pg.Content), "live words") {
		t.Errorf("saving changed the published page: %q", pg.Content)
	}
	if !st.HasDraft("/page.gmi") {
		t.Fatal("no draft after saving")
	}

	// re-opening the editor continues the draft, and says it is one
	body := editor()
	if !strings.Contains(body, "draft words") {
		t.Error("editor did not load the draft")
	}
	if !strings.Contains(body, `class="badge draft"`) {
		t.Error("no draft badge in the editor")
	}
	if !strings.Contains(body, `formaction="/admin/discard"`) {
		t.Error("no way to discard the draft")
	}

	// publishing promotes it and clears the draft
	resp2, _ := client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/page.gmi"}, "oldpath": {"/page.gmi"},
		"content": {"# Page\n\ndraft words"}, "publish": {"1"},
	})
	resp2.Body.Close()
	if pg, _ := st.GetPage("/page.gmi"); !strings.Contains(string(pg.Content), "draft words") {
		t.Errorf("publish did not update the page: %q", pg.Content)
	}
	if st.HasDraft("/page.gmi") {
		t.Error("the draft survived publishing")
	}
	if body := editor(); strings.Contains(body, `class="badge draft"`) {
		t.Error("the badge survived publishing")
	}
}

func TestDiscardDraftFromTheEditor(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/keep.gmi", []byte("# Keep\n\nlive"), "", "t")
	_, _ = st.SaveDraft("/keep.gmi", []byte("# Keep\n\nabandoned"), "", "web")
	_, _ = st.SaveDraft("/never.gmi", []byte("# Never published"), "", "web")
	client := login(t, ts, testPassword)

	// discarding over a published page leaves the page
	resp, err := client.PostForm(ts.URL+"/admin/discard", url.Values{"path": {"/keep.gmi"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.HasDraft("/keep.gmi") {
		t.Error("draft survived discard")
	}
	if pg, err := st.GetPage("/keep.gmi"); err != nil || !strings.Contains(string(pg.Content), "live") {
		t.Errorf("discard disturbed the published page: %v", err)
	}

	// discarding something never published removes it entirely
	resp2, _ := client.PostForm(ts.URL+"/admin/discard", url.Values{"path": {"/never.gmi"}})
	resp2.Body.Close()
	if st.HasDraft("/never.gmi") {
		t.Error("draft-only page survived discard")
	}
	if _, err := st.GetPage("/never.gmi"); err == nil {
		t.Error("discard published the page instead of removing it")
	}
}

// The browser has to union both tables, or a page started yesterday is
// invisible until it is published.
func TestBrowserListsAndBadgesDrafts(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/posts/live.gmi", []byte("# Live"), "", "t")
	_, _ = st.SavePage("/posts/plain.gmi", []byte("# Plain"), "", "t")
	_, _ = st.SaveDraft("/posts/live.gmi", []byte("# Live\n\nediting"), "", "web")
	_, _ = st.SaveDraft("/posts/newborn.gmi", []byte("# Newborn"), "", "web")
	client := login(t, ts, testPassword)

	resp, err := client.Get(ts.URL + "/admin?dir=/posts/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)
	if i := strings.Index(body, `<script id="page-index"`); i > 0 {
		body = body[:i]
	}

	// the draft-only page is listed at all
	if !strings.Contains(body, "newborn.gmi") {
		t.Error("a draft-only page is missing from the browser")
	}
	// both kinds of draft are badged, and the untouched page is not
	rows := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		for _, name := range []string{"live.gmi", "newborn.gmi", "plain.gmi"} {
			if strings.Contains(line, name+"</a>") || strings.Contains(line, name+"<") {
				rows[name] = strings.Contains(line, `badge draft`)
			}
		}
	}
	if !rows["live.gmi"] {
		t.Error("a draft over a published page is not badged")
	}
	if !rows["newborn.gmi"] {
		t.Error("a draft-only page is not badged")
	}
	if rows["plain.gmi"] {
		t.Error("a page with no draft was badged")
	}
}

func firstLines(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
