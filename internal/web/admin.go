package web

import (
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

// route is one admin endpoint. Routes are declared as data and the guard is
// applied to all of them in one place, so a new admin screen cannot be added
// unguarded by forgetting to wrap it — which is the one mistake in this file
// that would be silent, and catastrophic.
type route struct {
	path string
	fn   http.HandlerFunc
}

// adminRoutes is every /admin endpoint. TestEveryAdminRouteIsGated walks this
// list and proves each one refuses an anonymous request.
func (s *Server) adminRoutes() []route {
	return []route{
		{"/admin", s.adminHome},
		{"/admin/edit", s.adminEdit},
		{"/admin/save", s.adminSave},
		{"/admin/discard", s.adminDiscard},
		{"/admin/delete", s.adminDelete},
		{"/admin/versions", s.adminVersions},
		{"/admin/version", s.adminVersion},
		{"/admin/restore", s.adminRestore},
		{"/admin/upload", s.adminUpload},
		{"/admin/stats", s.adminStats},
		{"/admin/feed", s.adminFeedToggle},
		{"/admin/prefix", s.adminPrefix},
		{"/admin/backup", s.adminBackup},
		{"/admin/backup.zip", s.adminBackupZip},
		{"/admin/backup/restore", s.adminBackupRestore},
		{"/admin/manual", s.adminManual},
	}
}

// requireSession is the /admin gate: a valid session cookie, checked before
// the handler runs and before the request body is touched.
func (s *Server) requireSession(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		fn(w, r)
	}
}

// registerAdmin wires up the /admin UI (session-cookie gated, no JS).
func (s *Server) registerAdmin(mux *http.ServeMux) {
	for _, rt := range s.adminRoutes() {
		mux.HandleFunc(rt.path, s.requireSession(rt.fn))
	}
}

func (s *Server) adminRender(w http.ResponseWriter, r *http.Request, title, body string) {
	noStore(w)
	body = s.updateBanner() + body
	body += `<script src="/_/admin.js?v=` + site.BuildVersion + `" defer></script>`
	s.render(w, r, http.StatusOK, title+" · admin · "+s.Cfg.Hostname, "admin", "", "", body)
}

func adminNav() string {
	// a row of quiet text links, not chunky buttons: eight boxes wrapped
	// into a ragged block on anything narrower than a desktop
	return `<nav class="anav">
<a href="/admin">pages</a>
<a class="new" href="/admin/edit?path=&new=1">+ page</a>
<a href="/admin/upload">upload</a>
<a href="/admin/stats">stats</a>
<a href="/admin/backup">backup</a>
<a href="/admin/manual">manual</a>
<a class="far" href="/">view site</a>
<form class="inline" method="post" action="/logout"><button class="linkish" type="submit">logout</button></form>
</nav>`
}

// noStore keeps admin screens off the browser's shelves. Closing the editor
// and going back showed the listing as it was before the save — including a
// page you had just created, missing — because the browser answered from its
// own cache rather than asking. Admin pages describe state that changes
// under you; they are never worth reusing.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
}

// titleHint is the hover tooltip for a row: the full path, plus the page
// title when there is one (the title column is not displayed).
func titleHint(path, title string) string {
	if title == "" {
		return path
	}
	return path + " — " + title
}

// pageFolder returns the containing folder of a storage path ("/foo/bar.gmi"
// → "/foo/", "/x.gmi" → "/").
func pageFolder(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i+1]
	}
	return "/"
}

func sizeStr(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// editorHelpHTML is the syntax cheat-sheet shown by the editor's "syntax"
// popover. Kept out of the template so directive braces stay literal.
const editorHelpHTML = `<details class="help" id="syntax-help">
<summary class="btn quiet">syntax</summary>
<div class="help-panel">` + editorHelpBodyHTML + `</div>
</details>`

// editorHelpBody is the syntax reference, shared by the editor popover and
// the manual page so they can never drift apart.
func editorHelpBody() string { return editorHelpBodyHTML }

const editorHelpBodyHTML = `
<h3>Gemtext</h3>
<pre># Heading 1        ## Heading 2       ### Heading 3
=&gt; /path Link label
=&gt; https://ex.example External link
=&gt; /media/cat.jpg Images render inline on the web
* list item
&gt; quoted text
` + "```" + `
preformatted block (alt text after the first fence)
` + "```" + `</pre>
<h3>Directives</h3>
<table>
<tr><td><code>{{list [folder] [limit] [name]}}</code></td><td>link list of a folder&#39;s pages: dated first, newest first, and same-day posts in the order written. Add <code>name</code> for alphabetical instead</td></tr>
<tr><td><code>{{include /path}}</code></td><td>another page&#39;s content, inline</td></tr>
<tr><td><code>{{stream [folder] [limit]}}</code></td><td>a folder&#39;s entries in full, newest first (0 = all)</td></tr>
<tr><td><code>{{now [limit]}}</code></td><td>the same, for the notes folder (default 5)</td></tr>
<tr><td><code>{{latest /folder [part]}}</code></td><td>one piece of that folder&#39;s newest entry, inline: <code>body</code> (default), <code>title</code>, <code>date</code> or <code>link</code>. <code>.</code> means this page&#39;s own folder</td></tr>
<tr><td><code>{{random /path}}</code></td><td>one random non-empty line from a file</td></tr>
<tr><td><code>{{count}}</code></td><td>this page&#39;s view counter</td></tr>
<tr><td><code>{{rev}}</code></td><td>this page&#39;s revision number</td></tr>
<tr><td><code>{{updated}}</code></td><td>this page&#39;s last-edit date</td></tr>
<tr><td><code>{{version}}</code></td><td>server build version</td></tr>
</table>
<h3>Special files (inherited down folders)</h3>
<table>
<tr><td><code>.header</code> / <code>.footer</code></td><td>gemtext above/below every page in the folder &amp; below</td></tr>
<tr><td><code>.css</code></td><td>CSS applied to the web rendering of the folder &amp; below</td></tr>
</table>
<h3>Front matter (optional, top of page)</h3>
<pre>---
title: Custom title
date: 2026-07-20
header: none          # or a path: /.header
footer: /.footer      # use that file instead of this folder&#39;s
---</pre>
<p class="dim">A folder&#39;s <code>.header</code>/<code>.footer</code> replaces the inherited one rather than adding to it, so naming a file is how a page asks for a different one — a gemlog index wanting the site-wide footer instead of its folder&#39;s &ldquo;back to the gemlog&rdquo; line.</p>
<p class="dim">Dated filenames (<code>/posts/2026-07-20-hi.gmi</code>) sort newest-first in listings and feeds.</p>`

var editorTpl = template.Must(template.New("editor").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · edit · {{.Host}}</title>
<link rel="stylesheet" href="/_/style.css?v={{.AssetV}}">
<link rel="icon" href="data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 100 100%22><text y=%22.9em%22 font-size=%2290%22>✎</text></svg>">
</head>
<body class="editor-body">
<form id="ed" method="post" action="/admin/save">
<div class="ed-bar">
<input type="text" name="path" id="path" value="{{.Path}}" placeholder="/about.gmi" spellcheck="false" autocomplete="off"{{if not .Path}} autofocus{{end}}>
{{if .OldPath}}<input type="hidden" name="oldpath" value="{{.OldPath}}">{{end}}
<span id="ed-status" class="dim"></span>
{{if .Draft}}<span class="badge draft" title="unpublished work — the site still shows the published version">draft</span>{{end}}
<span class="ed-spacer"></span>
<button type="submit" name="publish" value="0" id="ed-save" title="keep this to yourself; the site keeps showing the published version">save draft</button>
<button type="submit" name="publish" value="1" id="ed-publish" class="publish" title="make this what the site shows">publish</button>
<button type="button" id="pv-toggle" hidden>preview</button>
{{.Help}}
{{if .Draft}}<button class="btn quiet" id="ed-discard" type="submit" formaction="/admin/discard" formmethod="post" data-published="{{.Published}}" data-path="{{.Path}}" title="throw the draft away">discard</button>{{end}}
{{if .OldPath}}<a class="btn quiet" href="/admin/versions?path={{.OldPath}}">history</a>{{if .Published}}<a class="btn quiet" href="{{.ViewURL}}">view</a>{{end}}{{end}}
<a class="btn quiet" id="ed-close" href="/admin">close</a>
</div>
<div class="ed-main">
<textarea name="content" id="content" spellcheck="false" placeholder="# A fresh page

Gemtext goes here — or CSS if the path is a .css file.

Directives: {{"{{"}}list [folder] [limit]{{"}}"}} · {{"{{"}}include /path{{"}}"}} · {{"{{"}}now [limit]{{"}}"}} · {{"{{"}}random /path{{"}}"}} · {{"{{"}}count{{"}}"}} · {{"{{"}}rev{{"}}"}} · {{"{{"}}updated{{"}}"}}"{{if .Path}} autofocus{{end}}>{{.Content}}</textarea>
<div id="pv-pane" class="ed-preview" hidden><div id="preview"></div></div>
</div>
</form>
<script src="/_/admin.js?v={{.AssetV}}" defer></script>
</body>
</html>
`))

type editorData struct {
	Title   string
	Host    string
	Path    string
	OldPath string
	ViewURL string
	Content string
	AssetV  string
	Help    template.HTML
	// Draft: this page has unpublished work, and it is what is loaded.
	// Published: a live version exists — so "view" shows something, and
	// discarding leaves that behind rather than removing the page.
	Draft     bool
	Published bool
}

func (s *Server) adminEdit(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	isNew := r.URL.Query().Get("new") == "1" || p == ""
	// a folder target means "a new page in here": offer whatever this folder
	// names its pages. It lands in the editable path field, so it is a
	// suggestion, not a rule.
	if isNew && strings.HasSuffix(p, "/") {
		p = s.Store.NewPagePath(p, time.Now().In(s.loc()))
	}
	content := ""
	// starting points for the special files, so they are editable rather
	// than blank puzzles
	if isNew {
		switch {
		case strings.HasSuffix(p, store.FeedMarker):
			author := s.Cfg.Feeds.Author
			if author == "" {
				author = s.Cfg.Hostname
			}
			content = string(store.DefaultFeedMarker(strings.Trim(pageFolder(p), "/"), author, 30))
		case strings.HasSuffix(p, ".css"):
			content = defaultThemeCSS()
		}
	}
	// Unpublished work wins, and is looked for FIRST: a page that exists
	// only as a draft has no pages row, so checking that first bounced the
	// author out to the listing with "no such page" — their own writing,
	// saved, listed, and unreachable.
	draft := false
	if d, err := s.Store.GetDraft(p); err == nil && !d.Binary {
		content = string(d.Content)
		draft = true
		isNew = false
	}
	if !isNew && !draft {
		pg, err := s.Store.GetPage(p)
		if err != nil {
			http.Redirect(w, r, "/admin?msg="+url.QueryEscape("no such page: "+p), http.StatusSeeOther)
			return
		}
		if pg.Binary {
			var b strings.Builder
			fmt.Fprintf(&b, "<h1>%s</h1>\n%s", html.EscapeString(p), adminNav())
			fmt.Fprintf(&b, `<p>Binary file (%s, %s). Re-upload to replace it.</p>`, html.EscapeString(pg.Mime), sizeStr(int64(len(pg.Content))))
			fmt.Fprintf(&b, `<form class="inline" method="post" action="/admin/delete"><input type="hidden" name="path" value="%s"><button class="danger" type="submit">delete</button></form>`, html.EscapeString(p))
			s.adminRender(w, r, p, b.String())
			return
		}
		content = string(pg.Content)
	}
	title := p
	if isNew {
		title = "new page"
	}
	published := false
	if _, err := s.Store.GetPage(p); err == nil {
		published = true
	}
	noStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// a page that does not exist yet has nothing to rename from: sending a
	// prefilled path back as "oldpath" made saving under a name you typed
	// look like a rename of something that was never there
	oldPath := p
	if isNew && !draft {
		oldPath = ""
	}
	_ = editorTpl.Execute(w, editorData{
		Title:     title,
		Host:      s.Cfg.Hostname,
		Path:      p,
		OldPath:   oldPath,
		ViewURL:   pageURL(p),
		Content:   content,
		AssetV:    site.BuildVersion,
		Help:      template.HTML(editorHelpHTML),
		Draft:     draft,
		Published: published,
	})
}

func (s *Server) adminSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The editor saves with fetch, which follows redirects — so a handler
	// that redirected on failure looked like success, and the editor said
	// "saved" over work it had just discarded. When the editor asks, answer
	// with a status it cannot misread.
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	fail := func(code int, msg string) {
		if wantsJSON {
			jsonErr(w, code, msg)
			return
		}
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	p := strings.TrimSpace(r.FormValue("path"))
	content := r.FormValue("content")
	// textarea newlines arrive as \r\n
	content = strings.ReplaceAll(content, "\r\n", "\n")
	cp, ok := store.CleanPath(store.DefaultExt(p))
	if !ok {
		fail(http.StatusBadRequest, "invalid path: "+p)
		return
	}
	// A rename moves the page and its history rather than leaving a copy —
	// but only when there is something at the old path to move. Treating a
	// name typed into a new page as a rename, and then abandoning the save
	// when that rename failed, threw the author's work away.
	renamed := ""
	if oldPath := r.FormValue("oldpath"); oldPath != "" && oldPath != cp {
		switch {
		case s.Store.PageExists(oldPath):
			if _, err := s.Store.RenamePage(oldPath, cp, "web"); err != nil {
				renamed = "rename failed (" + err.Error() + "), saved as " + cp
			}
		case s.Store.HasDraft(oldPath):
			// an unpublished page being renamed: carry the draft across and
			// leave nothing behind at the old name
			if d, err := s.Store.GetDraft(oldPath); err == nil {
				if _, err := s.Store.SaveDraft(cp, d.Content, d.Mime, "web"); err == nil {
					_ = s.Store.DiscardDraft(oldPath)
				}
			}
		}
	}
	// the editor only produces text — never store it as an opaque blob
	mime := store.TextMime(store.MimeFor(cp))

	// "save" keeps the work to yourself; "publish" is what the world sees.
	// Publishing writes the page directly rather than saving a draft and
	// promoting it, so it is one commit in the page's history either way.
	publish := r.FormValue("publish") == "1"
	if publish {
		if _, err := s.Store.SavePage(cp, []byte(content), mime, "web"); err != nil {
			fail(http.StatusBadRequest, "publish failed: "+err.Error())
			return
		}
		_ = s.Store.DiscardDraft(cp) // no error if there was never a draft
	} else if _, err := s.Store.SaveDraft(cp, []byte(content), mime, "web"); err != nil {
		fail(http.StatusBadRequest, "save failed: "+err.Error())
		return
	}
	if wantsJSON {
		// confirmed from the store, not from having reached this line
		saved := s.Store.HasDraft(cp)
		if publish {
			saved = s.Store.PageExists(cp)
		}
		if !saved {
			jsonErr(w, http.StatusInternalServerError, "save did not stick — nothing was written")
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{
			"path": cp, "published": publish, "msg": renamed,
		})
		return
	}
	to := "/admin/edit?path=" + url.QueryEscape(cp)
	if renamed != "" {
		to += "&msg=" + url.QueryEscape(renamed)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// adminDiscard throws away a draft. If the page was never published this
// removes it entirely, which is what discarding means for something that
// only ever existed unpublished.
func (s *Server) adminDiscard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.FormValue("path")
	msg := "discarded the draft of " + p
	if _, err := s.Store.GetPage(p); err != nil {
		msg = "discarded " + p + " — it was never published"
	}
	if err := s.Store.DiscardDraft(p); err != nil {
		http.Redirect(w, r, "/admin/edit?path="+url.QueryEscape(p)+
			"&msg="+url.QueryEscape("nothing to discard"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?dir="+url.QueryEscape(pageFolder(p))+
		"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) adminDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.FormValue("path")
	if err := s.Store.DeletePage(p, "web"); err != nil {
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape("delete failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	back := "/admin?dir=" + url.QueryEscape(normFolder(r.FormValue("dir")))
	http.Redirect(w, r, back+"&msg="+url.QueryEscape("deleted "+p+" — recoverable from its history"), http.StatusSeeOther)
}

func (s *Server) adminVersions(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	versions, err := s.Store.ListVersions(p)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>History: %s</h1>\n%s", html.EscapeString(p), adminNav())
	if len(versions) == 0 {
		b.WriteString("<p>No saved versions yet — versions appear after the first edit.</p>")
	} else {
		b.WriteString(`<table class="admin"><tr><th>saved</th><th>author</th><th class="right">size</th><th></th></tr>` + "\n")
		for _, v := range versions {
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td class="right dim">%s</td><td><a href="/admin/version?id=%d">view</a> · <form class="inline" method="post" action="/admin/restore"><input type="hidden" name="id" value="%d"><button class="quiet" type="submit">restore</button></form></td></tr>`+"\n",
				v.SavedAt.In(s.loc()).Format("2006-01-02 15:04:05"), html.EscapeString(v.Author), sizeStr(v.Size), v.ID, v.ID)
		}
		b.WriteString("</table>\n")
	}
	s.adminRender(w, r, "history", b.String())
}

func (s *Server) adminVersion(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	v, err := s.Store.GetVersion(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>%s @ %s</h1>\n%s", html.EscapeString(v.Path), v.SavedAt.In(s.loc()).Format("2006-01-02 15:04:05"), adminNav())
	textual := strings.HasPrefix(v.Mime, "text/") || strings.Contains(v.Mime, "json") || strings.Contains(v.Mime, "xml")
	if textual {
		// what this version would change if restored: its content against
		// what is live now
		cur := ""
		if pg, err := s.Store.GetPage(v.Path); err == nil {
			cur = string(pg.Content)
		}
		if cur == string(v.Content) {
			b.WriteString(`<p class="dim">Identical to the current page.</p>`)
		} else {
			b.WriteString(`<p class="dim">Changes restoring this version would make:</p>`)
			b.WriteString(renderDiff(cur, string(v.Content)))
		}
		fmt.Fprintf(&b, `<details><summary class="btn quiet">view full content</summary><pre>%s</pre></details>`, html.EscapeString(string(v.Content)))
	} else {
		fmt.Fprintf(&b, "<p>Binary content (%s, %s).</p>", html.EscapeString(v.Mime), sizeStr(v.Size))
	}
	fmt.Fprintf(&b, `<form class="inline" method="post" action="/admin/restore"><input type="hidden" name="id" value="%d"><button type="submit">restore this version</button></form>`, v.ID)
	s.adminRender(w, r, "version", b.String())
}

func (s *Server) adminRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	pg, err := s.Store.RestoreVersion(id, "web restore")
	if err != nil {
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape("restore failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/edit?path="+url.QueryEscape(pg.Path), http.StatusSeeOther)
}

func (s *Server) adminUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.MaxUploadBytes+1<<20)
		if err := r.ParseMultipartForm(s.Cfg.MaxUploadBytes); err != nil {
			http.Redirect(w, r, "/admin/upload?msg="+url.QueryEscape("upload too large or malformed"), http.StatusSeeOther)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			http.Redirect(w, r, "/admin/upload?msg="+url.QueryEscape("no file provided"), http.StatusSeeOther)
			return
		}
		defer f.Close()
		content, err := io.ReadAll(io.LimitReader(f, s.Cfg.MaxUploadBytes+1))
		if err != nil || int64(len(content)) > s.Cfg.MaxUploadBytes {
			http.Redirect(w, r, "/admin/upload?msg="+url.QueryEscape("file exceeds max upload size"), http.StatusSeeOther)
			return
		}
		p := strings.TrimSpace(r.FormValue("path"))
		if p == "" {
			p = "/media/" + hdr.Filename
		}
		cp, ok := store.CleanPath(p)
		if !ok {
			http.Redirect(w, r, "/admin/upload?msg="+url.QueryEscape("invalid path: "+p), http.StatusSeeOther)
			return
		}
		mime := hdr.Header.Get("Content-Type")
		if mime == "" || mime == "application/octet-stream" {
			mime = store.MimeFor(cp)
		}
		if _, err := s.Store.SavePage(cp, content, mime, "web upload"); err != nil {
			http.Redirect(w, r, "/admin/upload?msg="+url.QueryEscape("save failed: "+err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape("uploaded "+cp), http.StatusSeeOther)
		return
	}
	var b strings.Builder
	b.WriteString("<h1>Upload</h1>\n" + adminNav())
	if msg := r.URL.Query().Get("msg"); msg != "" {
		fmt.Fprintf(&b, `<p class="flash err">%s</p>`+"\n", html.EscapeString(msg))
	}
	fmt.Fprintf(&b, `<form class="admin" method="post" action="/admin/upload" enctype="multipart/form-data">
<label for="file">file (max %s)</label>
<input type="file" id="file" name="file">
<label for="path">destination path (blank = /media/&lt;filename&gt;)</label>
<input type="text" id="path" name="path" placeholder="/media/photo.jpg">
<div class="bar"><button type="submit">upload</button></div>
</form>`, sizeStr(s.Cfg.MaxUploadBytes))
	b.WriteString(`<p class="dim">Reference an image from a page with <code>=&gt; /media/photo.jpg A photo</code> — it renders inline on the web and as a link on gemini.</p>`)
	s.adminRender(w, r, "upload", b.String())
}

// adminFeedToggle turns a folder's Atom feed on or off by creating or
// removing its .feed marker (which is just a page, so it versions and syncs
// like everything else).
func (s *Server) adminFeedToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	folder := r.FormValue("folder")
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	marker := folder + store.FeedMarker
	msg := ""
	if r.FormValue("enable") == "true" {
		title := strings.Trim(folder, "/")
		if pg, err := s.Store.GetPage(folder + "index.gmi"); err == nil && pg.Title != "" {
			title = pg.Title
		}
		author := s.Cfg.Feeds.Author
		if author == "" {
			author = s.Cfg.Hostname
		}
		limit := s.Cfg.Feeds.Limit
		if limit <= 0 {
			limit = 30
		}
		// seed an editable config rather than an opaque marker
		body := store.DefaultFeedMarker(title, author, limit)
		if _, err := s.Store.SavePage(marker, body, "", "web"); err != nil {
			msg = "could not enable feed: " + err.Error()
		} else {
			msg = "feed enabled for " + folder + " — edit " + marker + " to set title, author or limit"
		}
	} else {
		if err := s.Store.DeletePage(marker, "web"); err != nil {
			msg = "feed already off for " + folder
		} else {
			msg = "feed disabled for " + folder
		}
	}
	http.Redirect(w, r, "/admin?dir="+url.QueryEscape(folder)+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	hits, err := s.Store.Stats()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// pivot into per-page rows with per-proto columns
	protos := []string{"http", "https", "gemini", "ssh", "telnet", "http+tor", "gemini+tor"}
	type row struct {
		total int64
		by    map[string]int64
	}
	rows := map[string]*row{}
	var order []string
	for _, h := range hits {
		rw := rows[h.Path]
		if rw == nil {
			rw = &row{by: map[string]int64{}}
			rows[h.Path] = rw
			order = append(order, h.Path)
		}
		rw.by[h.Proto] += h.Count
		rw.total += h.Count
	}
	var b strings.Builder
	b.WriteString("<h1>Stats</h1>\n" + adminNav())
	if len(rows) == 0 {
		b.WriteString("<p>No hits recorded yet.</p>")
	} else {
		b.WriteString(`<table class="admin"><tr><th>page</th><th class="right">total</th>`)
		for _, pr := range protos {
			fmt.Fprintf(&b, `<th class="right">%s</th>`, pr)
		}
		b.WriteString("</tr>\n")
		for _, p := range order {
			rw := rows[p]
			fmt.Fprintf(&b, `<tr><td><a href="%s">%s</a></td><td class="right">%d</td>`, html.EscapeString(p), html.EscapeString(p), rw.total)
			for _, pr := range protos {
				if v := rw.by[pr]; v > 0 {
					fmt.Fprintf(&b, `<td class="right">%d</td>`, v)
				} else {
					b.WriteString(`<td class="right dim">·</td>`)
				}
			}
			b.WriteString("</tr>\n")
		}
		b.WriteString("</table>\n")
	}
	s.adminRender(w, r, "stats", b.String())
}
