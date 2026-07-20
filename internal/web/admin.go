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
	b.WriteString(`<table class="admin"><tr><th>path</th><th>title</th><th class="right">size</th><th>updated</th><th></th></tr>` + "\n")
	for _, m := range metas {
		title := m.Title
		view := ""
		if !m.Binary && !store.Hidden(m.Path) && strings.HasSuffix(m.Path, ".gmi") {
			view = fmt.Sprintf(` <a href="%s">view</a>`, html.EscapeString(pageURL(m.Path)))
		} else if m.Binary || !store.Hidden(m.Path) {
			view = fmt.Sprintf(` <a href="%s">view</a>`, html.EscapeString(m.Path))
		}
		fmt.Fprintf(&b, `<tr><td><a href="/admin/edit?path=%s">%s</a></td><td>%s</td><td class="right dim">%s</td><td class="dim">%s</td><td class="dim">%s · <a href="/admin/versions?path=%s">history</a></td></tr>`+"\n",
			url.QueryEscape(m.Path), html.EscapeString(m.Path), html.EscapeString(title),
			sizeStr(m.Size), m.Updated.Format("2006-01-02 15:04"), view, url.QueryEscape(m.Path))
	}
	b.WriteString("</table>\n")
	b.WriteString(`<p class="dim">Special files: <code>.header</code> and <code>.footer</code> (gemtext, inherited down folders), <code>.theme</code> (CSS, inherited down folders). Create them like any page, e.g. <code>/posts/.header</code>.</p>`)
	s.adminRender(w, r, "pages", b.String())
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
}

func (s *Server) adminEdit(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	isNew := r.URL.Query().Get("new") == "1" || p == ""
	content := ""
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
	cp, ok := store.CleanPath(p)
	if !ok {
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape("invalid path: "+p), http.StatusSeeOther)
		return
	}
	if _, err := s.Store.SavePage(cp, []byte(content), "", "web"); err != nil {
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape("save failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	// renamed? remove the old page (its history is preserved under the old path)
	if old := r.FormValue("oldpath"); old != "" && old != cp {
		_ = s.Store.DeletePage(old, "web (renamed to "+cp+")")
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
	http.Redirect(w, r, "/admin?msg="+url.QueryEscape("deleted "+p+" (recoverable from history)"), http.StatusSeeOther)
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
				v.SavedAt.Format("2006-01-02 15:04:05"), html.EscapeString(v.Author), sizeStr(v.Size), v.ID, v.ID)
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
	fmt.Fprintf(&b, "<h1>%s @ %s</h1>\n%s", html.EscapeString(v.Path), v.SavedAt.Format("2006-01-02 15:04:05"), adminNav())
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
<div class="bar"><button type="submit">post</button><a class="btn quiet" href="/now">view /now</a></div>
</form>`)
	for _, p := range posts {
		fmt.Fprintf(&b, `<div class="hit"><p class="dim">%s · <form class="inline" method="post" action="/admin/now/delete"><input type="hidden" name="id" value="%d"><button class="quiet" type="submit">delete</button></form></p><pre>%s</pre></div>`+"\n",
			p.Created.Format("2006-01-02 15:04"), p.ID, html.EscapeString(p.Content))
	}
	b.WriteString(`<p class="dim">Embed the latest posts in any page with <code>{{now 3}}</code>. The built-in <a href="/now">/now</a> page lists them all.</p>`)
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

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	hits, err := s.Store.Stats()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// pivot into per-page rows with per-proto columns
	protos := []string{"http", "https", "gemini", "http+tor", "gemini+tor"}
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
