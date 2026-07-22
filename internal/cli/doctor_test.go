package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/store"
)

func TestDoctorLinksFindsDeadInternalLinks(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "starpulse.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.SavePage("/index.gmi", []byte(
		"# Home\n\n=> /about a real page\n=> /gone a dead one\n"+
			"=> https://example.org external, ignored\n=> /posts/ a folder\n"+
			"=> /search the search endpoint\n=> /posts/feed.xml a feed\n"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About\n\n=> ./sibling relative dead\n"), "", "t")
	_, _ = st.SavePage("/posts/index.gmi", []byte("# Posts"), "", "t")
	st.Close()

	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Hostname = "test"

	// no dead links case would return nil; here we expect two dead ones
	err = DoctorLinks(cfg)
	if err == nil {
		t.Fatal("expected dead links to be reported as an error")
	}
	if !strings.Contains(err.Error(), "2 dead") {
		t.Errorf("want 2 dead links, got: %v", err)
	}
}

func TestDoctorLinksAllowsScriptPages(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "starpulse.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.SavePage("/index.gmi", []byte("# Home\n\n=> /word a game\n"), "", "t")
	_, _ = st.SavePage("/word.gmi.cgi", []byte("write(\"hi\")"), "", "t")
	st.Close()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Hostname = "test"
	if err := DoctorLinks(cfg); err != nil {
		t.Errorf("a link to a script page was reported dead: %v", err)
	}
}
