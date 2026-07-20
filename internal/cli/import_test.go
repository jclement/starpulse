package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

func TestTranslatePath(t *testing.T) {
	cases := map[string]string{
		"/_header.gmi":       "/.header",
		"/_footer.gmi":       "/.footer",
		"/posts/_header.gmi": "/posts/.header",
		"/_taglines.txt":     "/.taglines.txt",
		"/index.gmi":         "/index.gmi",
		"/posts/a.gmi":       "/posts/a.gmi",
	}
	for in, want := range cases {
		if got := translatePath(in); got != want {
			t.Errorf("translatePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranslateContent(t *testing.T) {
	in := "{{index /posts 6}}\n{{index}}\n{{counter}}\n{{hash}}\n{{random /_taglines.txt}}"
	got := translateContent(in)
	want := "{{list /posts 6}}\n{{list}}\n{{count}}\nr{{rev}}\n{{random /.taglines.txt}}"
	if got != want {
		t.Errorf("translateContent:\n%s\nwant:\n%s", got, want)
	}
}

func TestImportEndToEnd(t *testing.T) {
	src := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.gmi", "# Home\n\n{{index /posts 3}}\n\nViewed {{counter}} times")
	write("_header.gmi", "=> / home")
	write("posts/2026-01-01-a.gmi", "# First post")
	write("media/pixel.png", "\x89PNG-ish")
	write(".data/secret", "must not import")

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	if err := Import(cfg, log.New(io.Discard), src); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "starpulse.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pg, err := st.GetPage("/index.gmi")
	if err != nil {
		t.Fatal("index missing")
	}
	if string(pg.Content) != "# Home\n\n{{list /posts 3}}\n\nViewed {{count}} times" {
		t.Errorf("directives not translated: %q", pg.Content)
	}
	if _, err := st.GetPage("/.header"); err != nil {
		t.Error("_header.gmi not mapped to /.header")
	}
	if _, err := st.GetPage("/posts/2026-01-01-a.gmi"); err != nil {
		t.Error("post missing")
	}
	if pg, err := st.GetPage("/media/pixel.png"); err != nil || !pg.Binary {
		t.Error("binary not imported as binary")
	}
	if _, err := st.GetPage("/.data/secret"); err == nil {
		t.Error("dot-dir content imported")
	}

	// the imported site actually renders
	sy := site.New(st)
	r := sy.Resolve("/", "")
	if r.Type != site.PageResult {
		t.Fatal("imported site does not resolve /")
	}
	g := r.Page.Gemtext
	for _, want := range []string{"=> / home", "# Home", "=> /posts/2026-01-01-a 2026-01-01 First post", "Viewed 0 times"} {
		if !containsStr(g, want) {
			t.Errorf("rendered import missing %q:\n%s", want, g)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
