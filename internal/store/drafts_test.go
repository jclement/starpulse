package store

import (
	"strings"
	"testing"
)

// The point of the parallel table: nothing that reads pages can see a draft.
// These are the properties the rest of the program relies on without asking.
func TestDraftsAreInvisibleToEverythingThatReadsPages(t *testing.T) {
	st := openTest(t)

	// a draft of something never published
	if _, err := st.SaveDraft("/secret.gmi", []byte("# Secret\n\nnot ready"), "", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPage("/secret.gmi"); err == nil {
		t.Error("a draft appeared as a page")
	}
	metas, err := st.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if m.Path == "/secret.gmi" {
			t.Error("a draft appeared in the page listing")
		}
	}
	if hits, _ := st.Search("Secret", 10); len(hits) != 0 {
		t.Errorf("a draft is searchable: %+v", hits)
	}

	// a draft over a published page leaves the published one alone
	if _, err := st.SavePage("/live.gmi", []byte("# Live\n\npublished words"), "", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveDraft("/live.gmi", []byte("# Live\n\nrewritten words"), "", "web"); err != nil {
		t.Fatal(err)
	}
	pg, err := st.GetPage("/live.gmi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pg.Content), "published words") {
		t.Errorf("the draft overwrote the published page: %q", pg.Content)
	}
	if hits, _ := st.Search("rewritten", 10); len(hits) != 0 {
		t.Error("draft text is searchable")
	}
}

// Publishing is one commit: however many times a draft was saved, the
// published history gains a single entry.
func TestPublishIsOneCommit(t *testing.T) {
	st := openTest(t)
	if _, err := st.SavePage("/post.gmi", []byte("# Post\n\nv1"), "", "t"); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{"draft a", "draft b", "draft c"} {
		if _, err := st.SaveDraft("/post.gmi", []byte("# Post\n\n"+body), "", "web"); err != nil {
			t.Fatal(err)
		}
	}
	// the draft has its own history, so nothing typed is lost meanwhile
	dvs, err := st.ListDraftVersions("/post.gmi")
	if err != nil {
		t.Fatal(err)
	}
	if len(dvs) != 2 { // a and b; c is the current draft
		t.Errorf("draft versions = %d, want 2", len(dvs))
	}

	before, _ := st.ListVersions("/post.gmi")
	pg, err := st.PublishDraft("/post.gmi", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pg.Content), "draft c") {
		t.Errorf("published content = %q", pg.Content)
	}
	after, _ := st.ListVersions("/post.gmi")
	if len(after) != len(before)+1 {
		t.Errorf("publishing added %d versions, want 1", len(after)-len(before))
	}
	// the draft and its transcript are gone
	if st.HasDraft("/post.gmi") {
		t.Error("the draft survived publishing")
	}
	if dvs, _ := st.ListDraftVersions("/post.gmi"); len(dvs) != 0 {
		t.Errorf("draft history survived publishing: %d entries", len(dvs))
	}
	// and the previous published text is recoverable, as always
	vs, _ := st.ListVersions("/post.gmi")
	v, err := st.GetVersion(vs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v.Content), "v1") {
		t.Errorf("previous published version not in history: %q", v.Content)
	}
}

func TestDiscardDraft(t *testing.T) {
	st := openTest(t)
	_, _ = st.SavePage("/keep.gmi", []byte("# Keep\n\nlive text"), "", "t")
	_, _ = st.SaveDraft("/keep.gmi", []byte("# Keep\n\nabandoned"), "", "web")
	_, _ = st.SaveDraft("/keep.gmi", []byte("# Keep\n\nabandoned twice"), "", "web")

	if err := st.DiscardDraft("/keep.gmi"); err != nil {
		t.Fatal(err)
	}
	if st.HasDraft("/keep.gmi") {
		t.Error("draft survived being discarded")
	}
	if dvs, _ := st.ListDraftVersions("/keep.gmi"); len(dvs) != 0 {
		t.Errorf("draft history survived: %d", len(dvs))
	}
	pg, err := st.GetPage("/keep.gmi")
	if err != nil || !strings.Contains(string(pg.Content), "live text") {
		t.Errorf("discarding a draft disturbed the published page: %v %q", err, pg.Content)
	}

	// discarding a draft that was never published leaves nothing behind —
	// that is what deleting means for something only ever drafted
	_, _ = st.SaveDraft("/never.gmi", []byte("# Never"), "", "web")
	if err := st.DiscardDraft("/never.gmi"); err != nil {
		t.Fatal(err)
	}
	if st.HasDraft("/never.gmi") {
		t.Error("draft-only page survived")
	}
	if _, err := st.GetPage("/never.gmi"); err == nil {
		t.Error("discarding created a published page")
	}
	// discarding nothing is an error, not a silent success
	if err := st.DiscardDraft("/never.gmi"); err == nil {
		t.Error("discarding a nonexistent draft reported success")
	}
}

// A draft belongs to its page: renaming or deleting the page must take it
// along, or it becomes a row nobody can reach and nobody can see.
func TestDraftFollowsRenameAndDelete(t *testing.T) {
	st := openTest(t)
	_, _ = st.SavePage("/old.gmi", []byte("# Old"), "", "t")
	_, _ = st.SaveDraft("/old.gmi", []byte("# Old\n\nunpublished"), "", "web")
	_, _ = st.SaveDraft("/old.gmi", []byte("# Old\n\nunpublished again"), "", "web")

	if _, err := st.RenamePage("/old.gmi", "/new.gmi", "t"); err != nil {
		t.Fatal(err)
	}
	if st.HasDraft("/old.gmi") {
		t.Error("draft left behind at the old path")
	}
	d, err := st.GetDraft("/new.gmi")
	if err != nil {
		t.Fatalf("draft did not follow the rename: %v", err)
	}
	if !strings.Contains(string(d.Content), "unpublished again") {
		t.Errorf("draft content after rename: %q", d.Content)
	}
	if dvs, _ := st.ListDraftVersions("/new.gmi"); len(dvs) != 1 {
		t.Errorf("draft history did not follow the rename: %d", len(dvs))
	}

	if err := st.DeletePage("/new.gmi", "t"); err != nil {
		t.Fatal(err)
	}
	if st.HasDraft("/new.gmi") {
		t.Error("draft outlived the page it belonged to")
	}
	if dvs, _ := st.ListDraftVersions("/new.gmi"); len(dvs) != 0 {
		t.Errorf("draft history outlived the page: %d", len(dvs))
	}
}

func TestDraftListingAndBadges(t *testing.T) {
	st := openTest(t)
	_, _ = st.SavePage("/a.gmi", []byte("# A"), "", "t")
	_, _ = st.SavePage("/b.gmi", []byte("# B"), "", "t")
	_, _ = st.SaveDraft("/a.gmi", []byte("# A\n\nediting"), "", "web")
	_, _ = st.SaveDraft("/c.gmi", []byte("# C\n\nbrand new"), "", "web")

	paths, err := st.DraftPaths()
	if err != nil {
		t.Fatal(err)
	}
	if !paths["/a.gmi"] || !paths["/c.gmi"] || paths["/b.gmi"] {
		t.Errorf("draft paths = %v", paths)
	}
	drafts, err := st.ListDrafts()
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 {
		t.Fatalf("drafts = %d, want 2", len(drafts))
	}
	// titles are derived, so a draft-only page can be listed properly
	for _, d := range drafts {
		if d.Path == "/c.gmi" && d.Title != "C" {
			t.Errorf("draft title = %q, want C", d.Title)
		}
	}
}
