package web

import (
	"archive/zip"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

// Backups are a zip of ordinary files, not a database dump: content/about.gmi
// is the page at /about.gmi, byte for byte. That means a backup can be read,
// diffed and edited with any tool you already have, and restoring it needs
// nothing this program knows that the filenames do not already say.
//
// What a file cannot carry — version history, view counts — is deliberately
// left behind. A backup is the content, and the content is what you would
// grieve for.

const (
	backupContentDir = "content/"
	backupKeysDir    = "keys/"
	backupManifest   = "BACKUP.txt"
)

// adminBackup is the screen: download on top, restore below it.
func (s *Server) adminBackup(w http.ResponseWriter, r *http.Request) {
	metas, err := s.Store.ListAll()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var total int64
	for _, m := range metas {
		total += m.Size
	}
	var b strings.Builder
	b.WriteString("<h1>Backup</h1>\n" + adminNav())
	if msg := r.URL.Query().Get("msg"); msg != "" {
		fmt.Fprintf(&b, `<p class="flash">%s</p>`+"\n", html.EscapeString(msg))
	}
	fmt.Fprintf(&b, `<p class="dim">A zip of plain files: <code>content/about.gmi</code> is the page at <code>/about.gmi</code>. Version history and view counts stay behind — this is the content, not the database.</p>`)

	fmt.Fprintf(&b, `<form class="admin" method="get" action="/admin/backup.zip">
<p>%d pages · %s</p>
<label class="check"><input type="checkbox" name="keys" value="1"><span>include keys and certificates <span class="dim">— tor hidden-service key, TLS certs, ssh host key. Restoring ignores them; keep this copy somewhere safe.</span></span></label>
<div class="bar"><button type="submit">download backup</button></div>
</form>`, len(metas), html.EscapeString(sizeStr(total)))

	fmt.Fprintf(&b, `<h2>Restore</h2>
<form class="admin" method="post" action="/admin/backup/restore" enctype="multipart/form-data">
<label for="file">backup zip (max %s)</label>
<input type="file" id="file" name="file" accept=".zip">
<label class="check"><input type="radio" name="mode" value="merge" checked><span>merge <span class="dim">— add and overwrite what the zip contains, leave everything else alone</span></span></label>
<label class="check"><input type="radio" name="mode" value="replace"><span>replace <span class="dim">— also delete pages the zip does not contain</span></span></label>
<div class="bar"><button type="submit">restore</button></div>
</form>
<p class="dim">Every page a restore overwrites keeps its history, so a restore is itself undoable page by page.</p>`,
		sizeStr(s.Cfg.MaxUploadBytes))
	s.adminRender(w, r, "backup", b.String())
}

// backupName is site_timestamp.zip — the site it came from and when, because
// a folder of backups called "backup.zip" tells you nothing.
func backupName(host string, now time.Time) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		case r == '.' || r == ':' || r == '_':
			return '_'
		}
		return -1
	}, host)
	if safe == "" {
		safe = "starpulse"
	}
	return fmt.Sprintf("%s_%s.zip", safe, now.Format("20060102-150405"))
}

// adminBackupZip streams the zip. It is written straight to the response, so
// a large site never has to fit in memory twice.
func (s *Server) adminBackupZip(w http.ResponseWriter, r *http.Request) {
	metas, err := s.Store.ListAll()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	withKeys := r.URL.Query().Get("keys") == "1"
	now := time.Now().In(s.loc())

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+backupName(s.Cfg.Hostname, now)+`"`)
	zw := zip.NewWriter(w)
	defer zw.Close()

	add := func(name string, mod time.Time, body []byte) error {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod}
		f, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = f.Write(body)
		return err
	}

	var manifest strings.Builder
	fmt.Fprintf(&manifest, "starpulse backup\nsite: %s\ntaken: %s\nversion: %s\npages: %d\n",
		s.Cfg.Hostname, now.Format(time.RFC3339), site.BuildVersion, len(metas))

	for _, m := range metas {
		pg, err := s.Store.GetPage(m.Path)
		if err != nil {
			continue // deleted between listing and reading
		}
		if err := add(backupContentDir+strings.TrimPrefix(pg.Path, "/"), pg.Updated, pg.Content); err != nil {
			return // client went away; nothing useful to report
		}
	}

	if withKeys {
		names, err := s.keyFiles()
		if err == nil {
			fmt.Fprintf(&manifest, "keys: %d (not restored by an import)\n", len(names))
			for _, rel := range names {
				body, err := os.ReadFile(filepath.Join(s.Cfg.DataDir, rel))
				if err != nil {
					continue
				}
				mod := time.Time{}
				if fi, err := os.Stat(filepath.Join(s.Cfg.DataDir, rel)); err == nil {
					mod = fi.ModTime()
				}
				if err := add(backupKeysDir+filepath.ToSlash(rel), mod, body); err != nil {
					return
				}
			}
		}
	}
	_ = add(backupManifest, now, []byte(manifest.String()))
}

// keyFiles lists the data-dir files worth keeping that are not content: the
// tor hidden-service key, TLS certificates, the ssh host key. The database
// is excluded — the pages are already in the zip as files, and shipping a
// second, authoritative copy of them invites the two to disagree.
func (s *Server) keyFiles() ([]string, error) {
	root := s.Cfg.DataDir
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable corner of the data dir: skip it, not the backup
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		base := d.Name()
		switch {
		case strings.HasPrefix(base, "starpulse.sqlite"): // db, wal, shm, .bak
			return nil
		case base == "torrc": // regenerated from config on every start
			return nil
		case strings.Contains(filepath.ToSlash(rel), "tor/state"):
			return nil // tor's own scratch space
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// adminBackupRestore reads an uploaded backup zip back into the database.
// (/admin/restore already means "restore a version of one page".)
func (s *Server) adminBackupRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/admin/backup?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		back("upload too large or malformed")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		back("no file provided")
		return
	}
	defer file.Close()
	if hdr.Size > s.Cfg.MaxUploadBytes {
		back("backup exceeds max upload size")
		return
	}
	zr, err := zip.NewReader(file, hdr.Size)
	if err != nil {
		back("not a zip file")
		return
	}

	replace := r.FormValue("mode") == "replace"
	seen := map[string]bool{}
	var written, skipped int
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if backupOwnFile(f.Name) {
			continue // our manifest and key copies: expected, not "skipped"
		}
		p, ok := backupEntryPath(f.Name)
		if !ok {
			skipped++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			skipped++
			continue
		}
		body, err := io.ReadAll(io.LimitReader(rc, s.Cfg.MaxUploadBytes))
		rc.Close()
		if err != nil {
			skipped++
			continue
		}
		if _, err := s.Store.SavePage(p, body, "", "restore"); err != nil {
			skipped++
			continue
		}
		seen[p] = true
		written++
	}
	if written == 0 {
		back("no content found in that zip — expected a content/ folder")
		return
	}

	deleted := 0
	if replace {
		metas, err := s.Store.ListAll()
		if err == nil {
			for _, m := range metas {
				if seen[m.Path] {
					continue
				}
				if err := s.Store.DeletePage(m.Path, "restore"); err == nil {
					deleted++
				}
			}
		}
	}
	msg := fmt.Sprintf("restored %d pages", written)
	if deleted > 0 {
		msg += fmt.Sprintf(", deleted %d not in the backup", deleted)
	}
	if skipped > 0 {
		msg += fmt.Sprintf(", skipped %d entries", skipped)
	}
	back(msg)
}

// backupOwnFile reports the parts of our own backups that a restore ignores
// by design, so they are not reported as problems.
func backupOwnFile(name string) bool {
	name = filepath.ToSlash(name)
	if i := strings.Index(name, "/"); i >= 0 && !strings.HasPrefix(name, backupContentDir) {
		if rest := name[i+1:]; strings.HasPrefix(rest, backupKeysDir) || rest == backupManifest {
			return true // inside a single wrapping folder
		}
	}
	return name == backupManifest || strings.HasPrefix(name, backupKeysDir)
}

// backupEntryPath maps a zip entry to a store path, and reports false for
// anything that is not restorable content. Zip entry names are attacker-
// controlled in the general case, so "content/../../etc/passwd" has to die
// here rather than deeper in.
func backupEntryPath(name string) (string, bool) {
	name = filepath.ToSlash(name)
	// some archivers prefix a single root folder; look past one level
	if !strings.HasPrefix(name, backupContentDir) {
		if i := strings.Index(name, "/"+backupContentDir); i > 0 && !strings.Contains(name[:i], "/") {
			name = name[i+1:]
		} else {
			return "", false
		}
	}
	rest := strings.TrimPrefix(name, backupContentDir)
	if rest == "" || strings.HasSuffix(rest, "/") {
		return "", false
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return store.CleanPath("/" + path.Clean(rest))
}
