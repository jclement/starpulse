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
	mux.HandleFunc("/admin/stats", guard(s.adminStats))
	mux.HandleFunc("/admin/feed", guard(s.adminFeedToggle))
	mux.HandleFunc("/admin/prefix", guard(s.adminPrefix))
	mux.HandleFunc("/admin/manual", guard(s.adminManual))
}

func (s *Server) adminRender(w http.ResponseWriter, r *http.Request, title, body string) {
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
<a href="/admin/manual">manual</a>
<a class="far" href="/">view site</a>
<form class="inline" method="post" action="/logout"><button class="linkish" type="submit">logout</button></form>
</nav>`
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
