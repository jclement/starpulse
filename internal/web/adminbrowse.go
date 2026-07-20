package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/store"
)

// The admin index is a folder browser: one screen shows one folder — its
// subfolders, then its files — with a breadcrumb back up. Showing the whole
// site at once made every folder's controls compete with every other one's.
//
// Drilling in only works if nothing gets hidden by it, so search stays
// site-wide: `?q=` renders a flat result list across all folders, and the
// same search runs live in the browser against an inline index.

// adminHome renders either the browser or a search result list.
func (s *Server) adminHome(w http.ResponseWriter, r *http.Request) {
	metas, err := s.Store.ListAll()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	dir := normFolder(r.URL.Query().Get("dir"))

	var b strings.Builder
	fmt.Fprintf(&b, "<h1>%s</h1>\n%s", breadcrumb(dir), adminNav())
	if msg := r.URL.Query().Get("msg"); msg != "" {
		fmt.Fprintf(&b, `<p class="flash">%s</p>`+"\n", html.EscapeString(msg))
	}
	// a real form, so search works with JS off; the script upgrades it to
	// filter as you type
	fmt.Fprintf(&b, `<form class="filterbar" method="get" action="/admin">`+
		`<input type="hidden" name="dir" value="%s">`+
		`<input type="search" id="page-filter" class="filter" name="q" value="%s" placeholder="search %d pages by path or title…" autocomplete="off" autofocus>`+
		`</form>`+"\n",
		html.EscapeString(dir), html.EscapeString(q), len(metas))
	b.WriteString(`<p id="filter-count" class="dim" hidden></p>` + "\n")
	b.WriteString(`<div id="search-results" hidden></div>` + "\n")

	b.WriteString(`<div id="browse">` + "\n")
	if q != "" {
		s.searchList(&b, metas, q)
	} else {
		s.folderScreen(&b, metas, dir)
	}
	b.WriteString("</div>\n")
	b.WriteString(pageIndex(metas))
	s.adminRender(w, r, "pages", b.String())
}

// folderScreen renders one folder: settings, subfolders, then files.
func (s *Server) folderScreen(b *strings.Builder, metas []store.Meta, dir string) {
	subCount := map[string]int{}
	var subs []string
	var files []store.Meta
	for _, m := range metas {
		if !strings.HasPrefix(m.Path, dir) {
			continue
		}
		rest := m.Path[len(dir):]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			sub := dir + rest[:i+1]
			if _, seen := subCount[sub]; !seen {
				subs = append(subs, sub)
			}
			subCount[sub]++ // descendants, not just direct children
			continue
		}
		files = append(files, m)
	}
	sort.Strings(subs)

	if dir != "/" {
		s.folderSettings(b, dir)
	}

	b.WriteString(`<table class="admin browse">` + "\n")
	if dir != "/" {
		fmt.Fprintf(b, `<tr class="up"><td colspan="3"><a href="/admin?dir=%s">../</a></td></tr>`+"\n",
			url.QueryEscape(parentFolder(dir)))
	}
	for _, sub := range subs {
		badge := ""
		if s.Store.IsFeedFolder(sub) {
			badge = ` <span class="badge">feed</span>`
		}
		fmt.Fprintf(b, `<tr class="dir-row"><td><a class="dirlink" href="/admin?dir=%s">%s</a>%s</td><td class="dim num">%d</td><td class="acts">%s</td></tr>`+"\n",
			url.QueryEscape(sub), html.EscapeString(strings.TrimSuffix(sub[len(dir):], "/")+"/"), badge,
			subCount[sub], newLinks(sub))
	}
	for _, m := range files {
		s.fileRow(b, m, m.Path[len(dir):])
	}
	if len(subs) == 0 && len(files) == 0 {
		b.WriteString(`<tr><td colspan="3" class="dim">empty folder</td></tr>` + "\n")
	}
	b.WriteString("</table>\n")

	fmt.Fprintf(b, `<p class="newhere">%s</p>`+"\n", newLinks(dir))
}

// fileRow is one page: its name links to the editor, which is where history
// and everything else already lives.
func (s *Server) fileRow(b *strings.Builder, m store.Meta, label string) {
	view := ""
	if !store.Hidden(m.Path) {
		target := m.Path
		if !m.Binary && strings.HasSuffix(m.Path, ".gmi") {
			target = pageURL(m.Path)
		}
		view = fmt.Sprintf(`<a href="%s">view</a> <span class="dim">·</span> `, html.EscapeString(target))
	}
	cls := "page-row"
	if store.Hidden(m.Path) {
		cls += " special" // .feed, .header, .footer, .css: machinery, not content
	}
	fmt.Fprintf(b, `<tr class="%s"><td><a href="/admin/edit?path=%s" title="%s">%s</a></td>`+
		`<td class="dim num">%s</td>`+
		`<td class="acts dim">%s<form class="inline del" method="post" action="/admin/delete">`+
		`<input type="hidden" name="path" value="%s"><input type="hidden" name="dir" value="%s">`+
		`<button class="linkish" type="submit" data-path="%s">delete</button></form></td></tr>`+"\n",
		cls, url.QueryEscape(m.Path), html.EscapeString(titleHint(m.Path, m.Title)), html.EscapeString(label),
		html.EscapeString(s.whenStr(m.Updated)),
		view, html.EscapeString(m.Path), html.EscapeString(pageFolder(m.Path)), html.EscapeString(m.Path))
}

// folderSettings is the three-line block at the top of a folder screen.
// Every control writes a file — the .feed page — so the same settings are
// editable over Titan or from any gemini client.
func (s *Server) folderSettings(b *strings.Builder, dir string) {
	feed := s.Store.IsFeedFolder(dir)
	b.WriteString(`<dl class="fset">` + "\n")

	b.WriteString(`<div><dt>feed</dt><dd>`)
	if feed {
		fmt.Fprintf(b, `<span class="on">publishing</span> <span class="dim">·</span> `+
			`<a href="/admin/edit?path=%s">edit .feed</a> <span class="dim">·</span> %s`,
			url.QueryEscape(dir+store.FeedMarker), feedForm(dir, false, "stop publishing"))
	} else {
		fmt.Fprintf(b, `<span class="dim">off</span> <span class="dim">·</span> %s`, feedForm(dir, true, "publish feed"))
	}
	b.WriteString("</dd></div>\n")

	b.WriteString(`<div><dt>inherits</dt><dd>`)
	for i, name := range []string{".header", ".footer", ".css"} {
		if i > 0 {
			b.WriteString(` <span class="dim">·</span> `)
		}
		p := dir + name
		if _, err := s.Store.GetPage(p); err == nil {
			fmt.Fprintf(b, `<a href="/admin/edit?path=%s">%s</a>`, url.QueryEscape(p), name)
		} else {
			fmt.Fprintf(b, `<a class="absent" href="/admin/edit?new=1&amp;path=%s">%s</a>`, url.QueryEscape(p), name)
		}
	}
	b.WriteString("</dd></div>\n</dl>\n")
}

func feedForm(dir string, enable bool, label string) string {
	return fmt.Sprintf(`<form class="inline feedtoggle" method="post" action="/admin/feed">`+
		`<input type="hidden" name="folder" value="%s"><input type="hidden" name="enable" value="%t">`+
		`<button class="linkish" type="submit">%s</button></form>`,
		html.EscapeString(dir), enable, html.EscapeString(label))
}

// newLinks are the two ways to create in a folder, and the only difference
// between a "note" and a "page": who names the file. Both work anywhere, so
// a folder has no kind to remember.
func newLinks(dir string) string {
	return fmt.Sprintf(`<a class="newpost" href="/admin/edit?new=1&amp;path=%s">+ page</a>`+
		` <span class="dim">·</span> `+
		`<a class="newpost" href="/admin/now?folder=%s">+ note</a>`,
		url.QueryEscape(dir), url.QueryEscape(dir))
}

// searchList is the flat, folder-qualified result list — the no-JS half of
// the filter, and the shape the script reproduces live.
func (s *Server) searchList(b *strings.Builder, metas []store.Meta, q string) {
	terms := strings.Fields(strings.ToLower(q))
	b.WriteString(`<table class="admin browse">` + "\n")
	n := 0
	for _, m := range metas {
		key := strings.ToLower(m.Path + " " + m.Title)
		hit := true
		for _, t := range terms {
			if !strings.Contains(key, t) {
				hit = false
				break
			}
		}
		if !hit {
			continue
		}
		n++
		s.fileRow(b, m, m.Path)
	}
	if n == 0 {
		b.WriteString(`<tr><td colspan="3" class="dim">nothing matches</td></tr>` + "\n")
	}
	b.WriteString("</table>\n")
	fmt.Fprintf(b, `<p class="dim">%d of %d pages</p>`+"\n", n, len(metas))
}

// pageIndex inlines every path so the filter can run without a round trip.
// At tens or hundreds of pages this is a few KB; a site large enough for
// that to matter would want the server-side ?q= path anyway.
func pageIndex(metas []store.Meta) string {
	type row struct {
		P string `json:"p"`
		T string `json:"t,omitempty"`
		V string `json:"v,omitempty"`
	}
	rows := make([]row, 0, len(metas))
	for _, m := range metas {
		r := row{P: m.Path, T: m.Title}
		if !store.Hidden(m.Path) {
			r.V = m.Path
			if !m.Binary && strings.HasSuffix(m.Path, ".gmi") {
				r.V = pageURL(m.Path)
			}
		}
		rows = append(rows, r)
	}
	blob, err := json.Marshal(rows)
	if err != nil {
		return ""
	}
	// json.Marshal escapes <, > and & to \u00xx, so the payload can never
	// close the script element early
	return `<script id="page-index" type="application/json">` + string(blob) + "</script>\n"
}

// ---- small helpers ---------------------------------------------------

// normFolder makes a folder query parameter safe and canonical.
func normFolder(d string) string {
	if d == "" || strings.Contains(d, "\x00") {
		return "/"
	}
	if !strings.HasPrefix(d, "/") {
		d = "/" + d
	}
	d = path.Clean(d)
	if d == "/" || d == "." {
		return "/"
	}
	return d + "/"
}

func parentFolder(dir string) string {
	return pageFolder(strings.TrimSuffix(dir, "/"))
}

// breadcrumb is the heading: every ancestor is a link back up.
func breadcrumb(dir string) string {
	if dir == "/" {
		return `<a href="/admin">/</a>`
	}
	var b strings.Builder
	b.WriteString(`<a href="/admin">/</a>`)
	at := "/"
	for _, seg := range strings.Split(strings.Trim(dir, "/"), "/") {
		at += seg + "/"
		fmt.Fprintf(&b, ` <span class="dim">›</span> <a href="/admin?dir=%s">%s</a>`,
			url.QueryEscape(at), html.EscapeString(seg))
	}
	return b.String()
}

// whenStr keeps recent edits legible and old ones short. The exact minute
// was never the point, and it cost the width that broke the phone layout.
func (s *Server) whenStr(t time.Time) string {
	t = t.In(s.loc())
	now := time.Now().In(s.loc())
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("2006-01-02")
	}
}
