package web

import (
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

// registerAdmin wires up the /admin UI (session-cookie gated, no JS).
func (s *Server) registerAdmin(mux *http.ServeMux) {
	guard := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.loggedIn(r) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("/admin", guard(s.adminHome))
	mux.HandleFunc("/admin/edit", guard(s.adminEdit))
	mux.HandleFunc("/admin/save", guard(s.adminSave))
	mux.HandleFunc("/admin/delete", guard(s.adminDelete))
	mux.HandleFunc("/admin/versions", guard(s.adminVersions))
	mux.HandleFunc("/admin/version", guard(s.adminVersion))
	mux.HandleFunc("/admin/restore", guard(s.adminRestore))
	mux.HandleFunc("/admin/upload", guard(s.adminUpload))
	mux.HandleFunc("/admin/now", guard(s.adminNow))
	mux.HandleFunc("/admin/now/delete", guard(s.adminNowDelete))
	mux.HandleFunc("/admin/stats", guard(s.adminStats))
	mux.HandleFunc("/admin/feed", guard(s.adminFeedToggle))
	mux.HandleFunc("/admin/manual", guard(s.adminManual))
}

func (s *Server) adminRender(w http.ResponseWriter, r *http.Request, title, body string) {
	body += `<script src="/_/admin.js?v=` + site.BuildVersion + `" defer></script>`
	s.render(w, r, http.StatusOK, title+" · admin · "+s.Cfg.Hostname, "admin", "", "", body)
}

func adminNav() string {
	return `<div class="bar">
<a class="btn quiet" href="/admin">pages</a>
<a class="btn quiet" href="/admin/edit?path=&new=1">new page</a>
<a class="btn quiet" href="/admin/upload">upload</a>
<a class="btn quiet" href="/admin/now">now</a>
<a class="btn quiet" href="/admin/stats">stats</a>
<a class="btn quiet" href="/admin/manual">manual</a>
<a class="btn quiet" href="/">view site</a>
<form class="inline" method="post" action="/logout"><button class="quiet" type="submit">logout</button></form>
</div>`
}

func (s *Server) adminHome(w http.ResponseWriter, r *http.Request) {
	metas, err := s.Store.ListAll()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString("<h1>Pages</h1>\n" + adminNav())
	if msg := r.URL.Query().Get("msg"); msg != "" {
		fmt.Fprintf(&b, `<p class="flash">%s</p>`+"\n", html.EscapeString(msg))
	}
	fmt.Fprintf(&b, `<input type="search" id="page-filter" class="filter" placeholder="filter %d pages by path or title…" autocomplete="off" autofocus>`+"\n", len(metas))
	b.WriteString(`<p id="filter-count" class="dim" hidden></p>` + "\n")
	b.WriteString(`<table class="admin" id="pages-table"><thead><tr><th>path</th><th class="right">size</th><th>updated</th><th></th></tr></thead>` + "\n")

	// bucket by folder first — a flat path sort interleaves subfolder pages
	// with root pages (/posts/… sorts between /now.gmi and /projects.gmi),
	// which would split a folder into several groups.
	feedFolders := s.Store.FeedFolders()
	byFolder := map[string][]store.Meta{}
	var folders []string
	for _, m := range metas {
		f := pageFolder(m.Path)
		if _, seen := byFolder[f]; !seen {
			folders = append(folders, f)
		}
		byFolder[f] = append(byFolder[f], m)
	}
	sort.Slice(folders, func(i, j int) bool {
		if (folders[i] == "/") != (folders[j] == "/") {
			return folders[i] == "/" // root first
		}
		return folders[i] < folders[j]
	})

	for _, folder := range folders {
		rows := byFolder[folder]
		label := folder
		if label == "/" {
			label = "/ (root)"
		}
		_, isFeed := feedFolders[folder]
		actions := ""
		if isFeed {
			actions += fmt.Sprintf(` <span class="dim">·</span> <a class="newpost" href="/admin/edit?new=1&amp;path=%s">new post</a>`,
				url.QueryEscape(folder))
		}
		// every non-root folder can publish (or stop publishing) a feed
		if folder != "/" {
			label := "enable feed"
			if isFeed {
				label = "disable feed"
			}
			actions += fmt.Sprintf(
				` <span class="dim">·</span> <form class="inline feedtoggle" method="post" action="/admin/feed">`+
					`<input type="hidden" name="folder" value="%s">`+
					`<input type="hidden" name="enable" value="%t">`+
					`<button class="linkish" type="submit" title="Atom feed for this folder">%s</button></form>`,
				html.EscapeString(folder), !isFeed, label)
		}
		fmt.Fprintf(&b, `<tbody class="folder-group" data-folder="%s"><tr class="folder-row"><td colspan="4">%s <span class="dim">%d</span>%s</td></tr>`+"\n",
			html.EscapeString(strings.ToLower(folder)), html.EscapeString(label), len(rows), actions)
		for _, m := range rows {
			title := m.Title
			view := ""
			if !m.Binary && !store.Hidden(m.Path) && strings.HasSuffix(m.Path, ".gmi") {
				view = fmt.Sprintf(`<a href="%s">view</a> · `, html.EscapeString(pageURL(m.Path)))
			} else if m.Binary || !store.Hidden(m.Path) {
				view = fmt.Sprintf(`<a href="%s">view</a> · `, html.EscapeString(m.Path))
			}
			// show just the file name in the folder view; full path on hover
			name := m.Path[len(folder):]
			fmt.Fprintf(&b, `<tr class="page-row" data-key="%s"><td><a href="/admin/edit?path=%s" title="%s">%s</a></td><td class="right dim">%s</td><td class="dim">%s</td><td class="dim">%s<a href="/admin/versions?path=%s">history</a> · <form class="inline del" method="post" action="/admin/delete"><input type="hidden" name="path" value="%s"><button class="linkish" type="submit" data-path="%s">delete</button></form></td></tr>`+"\n",
				html.EscapeString(strings.ToLower(m.Path+" "+title)),
				url.QueryEscape(m.Path), html.EscapeString(titleHint(m.Path, title)), html.EscapeString(name),
				sizeStr(m.Size), m.Updated.In(s.loc()).Format("2006-01-02 15:04"), view, url.QueryEscape(m.Path),
				html.EscapeString(m.Path), html.EscapeString(m.Path))
		}
		b.WriteString("</tbody>\n")
	}
	b.WriteString("</table>\n")
	b.WriteString(`<p class="dim">Special files: <code>.header</code> and <code>.footer</code> (gemtext, inherited down folders), <code>.theme</code> (CSS, inherited down folders). Create them like any page, e.g. <code>/posts/.header</code>.</p>`)
	s.adminRender(w, r, "pages", b.String())
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
<tr><td><code>{{list [folder] [limit]}}</code></td><td>link list of a folder&#39;s pages, dated first, newest first</td></tr>
<tr><td><code>{{include /path}}</code></td><td>another page&#39;s content, inline</td></tr>
<tr><td><code>{{now [limit]}}</code></td><td>latest now-posts (0 = all)</td></tr>
<tr><td><code>{{latest_now}}</code></td><td>just the newest now-post&#39;s text (inline)</td></tr>
<tr><td><code>{{latest_now_date}}</code></td><td>the newest now-post&#39;s date (inline)</td></tr>
<tr><td><code>{{random /path}}</code></td><td>one random non-empty line from a file</td></tr>
<tr><td><code>{{count}}</code></td><td>this page&#39;s view counter</td></tr>
<tr><td><code>{{rev}}</code></td><td>this page&#39;s revision number</td></tr>
<tr><td><code>{{updated}}</code></td><td>this page&#39;s last-edit date</td></tr>
<tr><td><code>{{version}}</code></td><td>server build version</td></tr>
</table>
<h3>Special files (inherited down folders)</h3>
<table>
<tr><td><code>.header</code> / <code>.footer</code></td><td>gemtext above/below every page in the folder &amp; below</td></tr>
<tr><td><code>.theme</code></td><td>CSS applied to the web rendering of the folder &amp; below</td></tr>
</table>
<h3>Front matter (optional, top of page)</h3>
<pre>---
title: Custom title
date: 2026-07-20
header: none
footer: none
---</pre>
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
<span class="ed-spacer"></span>
<button type="submit">save</button>
<button type="button" id="pv-toggle" hidden>preview</button>
{{.Help}}
{{if .OldPath}}<a class="btn quiet" href="/admin/versions?path={{.OldPath}}">history</a><a class="btn quiet" href="{{.ViewURL}}">view</a>{{end}}
<a class="btn quiet" href="/admin">close</a>
</div>
<div class="ed-main">
<textarea name="content" id="content" spellcheck="false" placeholder="# A fresh page

Gemtext goes here — or CSS if the path is a .theme file.

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
}

func (s *Server) adminEdit(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	isNew := r.URL.Query().Get("new") == "1" || p == ""
	// creating inside a log folder? offer today's date-stamped filename
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
		case strings.HasSuffix(p, ".theme"):
			content = defaultThemeCSS()
		}
	}
	if !isNew {
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = editorTpl.Execute(w, editorData{
		Title:   title,
		Host:    s.Cfg.Hostname,
		Path:    p,
		OldPath: p,
		ViewURL: pageURL(p),
		Content: content,
		AssetV:  site.BuildVersion,
		Help:    template.HTML(editorHelpHTML),
	})
}

func (s *Server) adminSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := strings.TrimSpace(r.FormValue("path"))
	content := r.FormValue("content")
	// textarea newlines arrive as \r\n
	content = strings.ReplaceAll(content, "\r\n", "\n")
	cp, ok := store.CleanPath(store.DefaultExt(p))
	if !ok {
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape("invalid path: "+p), http.StatusSeeOther)
		return
	}
	// a rename moves the page and its history rather than leaving a copy
	if oldPath := r.FormValue("oldpath"); oldPath != "" && oldPath != cp {
		if _, err := s.Store.RenamePage(oldPath, cp, "web"); err != nil {
			http.Redirect(w, r, "/admin/edit?path="+url.QueryEscape(oldPath)+
				"&msg="+url.QueryEscape("rename failed: "+err.Error()), http.StatusSeeOther)
			return
		}
	}
	// the editor only produces text — never store it as an opaque blob
	mime := store.TextMime(store.MimeFor(cp))
	if _, err := s.Store.SavePage(cp, []byte(content), mime, "web"); err != nil {
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape("save failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/edit?path="+url.QueryEscape(cp), http.StatusSeeOther)
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
	http.Redirect(w, r, "/admin?msg="+url.QueryEscape("deleted "+p+" — recoverable from its history"), http.StatusSeeOther)
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
	if strings.HasPrefix(v.Mime, "text/") || strings.Contains(v.Mime, "json") || strings.Contains(v.Mime, "xml") {
		fmt.Fprintf(&b, "<pre>%s</pre>\n", html.EscapeString(string(v.Content)))
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

func (s *Server) adminNow(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if _, err := s.Store.AddNow(strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")); err != nil {
			http.Redirect(w, r, "/admin/now?msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/now", http.StatusSeeOther)
		return
	}
	posts, _ := s.Store.ListNow(0)
	var b strings.Builder
	b.WriteString("<h1>Now</h1>\n" + adminNav())
	if msg := r.URL.Query().Get("msg"); msg != "" {
		fmt.Fprintf(&b, `<p class="flash err">%s</p>`+"\n", html.EscapeString(msg))
	}
	b.WriteString(`<form class="admin" method="post" action="/admin/now">
<label for="content">what's happening? (gemtext)</label>
<textarea id="content" name="content" style="min-height:6em" autofocus></textarea>
<div class="bar"><button type="submit">post</button></div>
</form>`)
	for _, p := range posts {
		fmt.Fprintf(&b, `<div class="hit"><p class="dim">%s · <form class="inline" method="post" action="/admin/now/delete"><input type="hidden" name="id" value="%d"><button class="quiet" type="submit">delete</button></form></p><pre>%s</pre></div>`+"\n",
			p.Created.In(s.loc()).Format("2006-01-02 15:04"), p.ID, html.EscapeString(p.Content))
	}
	b.WriteString(`<p class="dim">Embed the latest posts in any page with <code>{{now 3}}</code> — or make a page like <code>/now.gmi</code> containing <code>{{now 0}}</code> to list them all.</p>`)
	s.adminRender(w, r, "now", b.String())
}

func (s *Server) adminNowDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	_ = s.Store.DeleteNow(id)
	http.Redirect(w, r, "/admin/now", http.StatusSeeOther)
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
	http.Redirect(w, r, "/admin?msg="+url.QueryEscape(msg), http.StatusSeeOther)
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
