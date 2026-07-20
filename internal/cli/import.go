package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/store"
)

// Import loads a content directory tree (owg-capsule style) into the
// database. Translations applied:
//
//	_header.gmi / _footer.gmi  →  .header / .footer  (inherited specials)
//	other _name.ext            →  .name.ext          (hidden includes)
//	{{index …}}                →  {{list …}}
//	{{counter}}                →  {{count}}
//	directive refs to /_x      →  /.x
func Import(cfg *config.Config, logger *log.Logger, dir string) error {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "starpulse.sqlite"))
	if err != nil {
		return err
	}
	defer st.Close()
	st.KeepVersions = cfg.KeepVersions

	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	count := 0
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		dst := "/" + filepath.ToSlash(rel)
		dst = translatePath(dst)
		cp, ok := store.CleanPath(dst)
		if !ok {
			logger.Warn("skipping unimportable path", "src", rel)
			return nil
		}

		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mime := store.MimeFor(cp)
		if !strings.HasPrefix(mime, "text/gemini") && strings.HasSuffix(strings.ToLower(name), ".gmi") {
			mime = "text/gemini; charset=utf-8"
		}
		if strings.HasPrefix(mime, "text/") {
			raw = []byte(translateContent(string(raw)))
		}
		if _, err := st.SavePage(cp, raw, mime, "import"); err != nil {
			return fmt.Errorf("importing %s: %w", rel, err)
		}
		logger.Info("imported", "src", rel, "dst", cp, "bytes", len(raw))
		count++
		return nil
	})
	if err != nil {
		return err
	}
	logger.Info("import complete", "pages", count, "db", filepath.Join(cfg.DataDir, "starpulse.sqlite"))
	return nil
}

// translatePath maps owg-capsule naming to starpulse naming.
func translatePath(p string) string {
	dir, base := filepath.ToSlash(filepath.Dir(p)), filepath.Base(p)
	switch base {
	case "_header.gmi", "_header.md":
		base = ".header"
	case "_footer.gmi", "_footer.md":
		base = ".footer"
	default:
		if strings.HasPrefix(base, "_") {
			base = "." + strings.TrimPrefix(base, "_")
		}
	}
	if dir == "/" || dir == "." {
		return "/" + base
	}
	return dir + "/" + base
}

// translateContent maps owg-capsule directives to starpulse directives.
func translateContent(src string) string {
	r := strings.NewReplacer(
		"{{index}}", "{{list}}",
		"{{index ", "{{list ",
		"{{counter}}", "{{count}}",
		"{{hash}}", "r{{rev}}",
		"{{random /_", "{{random /.",
		"{{include /_", "{{include /.",
	)
	return r.Replace(src)
}
