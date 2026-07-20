package web

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/jclement/starpulse/internal/site"
)

// adminManual is the in-app reference: everything you need to run the site
// without leaving it. It is generated from the running config, so ports,
// hostnames and enabled doors are the real ones rather than examples.
func (s *Server) adminManual(w http.ResponseWriter, r *http.Request) {
	host := s.Cfg.Hostname
	var b strings.Builder
	b.WriteString("<h1>Manual</h1>\n" + adminNav())
	b.WriteString(`<p class="dim">This page describes <em>this</em> site: the doors that are switched on, and the syntax it understands.</p>`)

	// ---- doors -------------------------------------------------------
	b.WriteString("<h2>Ways in</h2>\n<table class=\"kv\">\n")
	row := func(what, how, note string) {
		fmt.Fprintf(&b, "<tr><td>%s</td><td><code>%s</code></td><td class=\"dim\">%s</td></tr>\n",
			html.EscapeString(what), html.EscapeString(how), note)
	}
	if s.Cfg.HTTPS.Enabled {
		row("Web", "https://"+host+"/", "Let's Encrypt certificate, renewed automatically")
	} else if s.Cfg.HTTP.Enabled {
		row("Web", "http://"+host+"/", "")
	}
	if s.Cfg.Gemini.Enabled {
		row("Gemini", "gemini://"+host+"/", "port "+portOnly(s.Cfg.Gemini.Addr))
	}
	if s.Cfg.SSH.Enabled {
		note := "read-only TUI browser"
		if len(s.Cfg.SSH.AuthorizedKeys) > 0 {
			note += "; admin login is key-only"
		}
		row("SSH", "ssh guest@"+host+portFlag(s.Cfg.SSH.Addr, " -p "), note)
		row("SSH (admin)", "ssh admin@"+host+portFlag(s.Cfg.SSH.Addr, " -p "), "same browser, plus editing")
	}
	if s.Cfg.Telnet.Enabled {
		row("Telnet", "telnet "+host+portFlag(s.Cfg.Telnet.Addr, " "), "read-only, unencrypted")
	}
	if o := s.onion(); o != "" {
		row("Tor", "http://"+o+"/", "every enabled door is mirrored on the onion")
	}
	if s.Cfg.Titan.Enabled {
		row("Titan", "titan://"+host+"/page.gmi", "edit from a gemini client with an allowed certificate")
	}
	b.WriteString("</table>\n")

	// ---- writing -----------------------------------------------------
	b.WriteString(`<h2>Writing pages</h2>
<p>Pages are <a href="https://geminiprotocol.net/docs/gemtext.gmi">gemtext</a>. A path with no extension gets <code>.gmi</code>, so typing <code>/about</code> creates <code>/about.gmi</code> and serves at <code>/about</code>. Renaming a page in the editor moves its history and view counts with it.</p>`)
	b.WriteString(editorHelpBody())

	// ---- feeds -------------------------------------------------------
	b.WriteString(`<h2>Backups</h2>
<p><a href="/admin/backup">Backup</a> downloads a zip of plain files — <code>content/about.gmi</code> is the page at <code>/about.gmi</code>, byte for byte — named for this site and the moment it was taken. Version history and view counts stay behind: a backup is the content.</p>
<p>Restoring the zip <strong>merges</strong> by default (adds and overwrites what it contains, leaves the rest); <strong>replace</strong> also deletes pages the backup does not contain. Overwritten pages keep their history, so a restore is undoable page by page. Optionally the zip can carry your tor hidden-service key, TLS certificates and ssh host key under <code>keys/</code> — restoring never touches those, so keep that copy somewhere safe.</p>

<h2>Posts and feeds</h2>
<p>A folder publishes an Atom feed when you turn one on — the <strong>feed on</strong> toggle at the top of that folder's screen in <a href="/admin">pages</a>. Nothing publishes by itself. Turning it on writes a <code>.feed</code> file in the folder holding the feed's title, author, length and naming rule; the toggles edit that file, and so can you.</p>
<p>The <strong>names</strong> toggle decides what a new page in the folder is called before you type anything: <code>none</code> leaves it to you, <code>date</code> offers <code>2026-07-20-</code> for you to finish, and <code>datetime</code> offers a complete name like <code>2026-07-20-1423.gmi</code> for short notes you never title. It only ever fills the editor's path field, so you can always change it before saving.</p>
<p>Inside a feed folder <em>every</em> page is a post — except <code>index.gmi</code> and dot-files, which are never entries. A post's date is resolved in this order:</p>
<ol>
<li>a <code>YYYY-MM-DD-</code> prefix on the filename — authoritative, and it sorts itself</li>
<li>a <code>date:</code> in front matter</li>
<li>the day the page was created, from the database</li>
</ol>
<p>Outside a feed folder only the first two count, so an ordinary page stays undated and lists alphabetically.</p>
<p class="dim">On gemini the idiomatic feed is simply a page of dated link lines, which <code>{{list}}</code> already produces — a folder index is subscribable in Lagrange or Amfora with nothing switched on at all.</p>`)

	// ---- editing elsewhere -------------------------------------------
	fmt.Fprintf(&b, `<h2>Editing from elsewhere</h2>
<table class="kv">
<tr><td>REST</td><td><code>Authorization: Bearer &lt;admin password&gt;</code></td><td class="dim">%s/api/pages, /api/now, /api/search, /api/versions, /api/stats</td></tr>
<tr><td>MCP</td><td><code>%s/mcp</code></td><td class="dim">same bearer token, or OAuth with client id <code>mcp</code> and the admin password as the secret</td></tr>
<tr><td>SSH</td><td><code>e</code> edit · <code>c</code> new · <code>n</code> note · <code>x</code> delete</td><td class="dim">ctrl+s saves, ctrl+g shows syntax help</td></tr>
</table>`, "https://"+host, "https://"+host)

	b.WriteString(`<h2>Everything is versioned</h2>
<p>Every save keeps the previous content, deletions included — <strong>history</strong> beside any page restores it. Nothing you do here is unrecoverable.</p>`)

	fmt.Fprintf(&b, `<p class="dim">starpulse %s · <a href="https://github.com/jclement/starpulse">source and full documentation</a></p>`,
		html.EscapeString(site.BuildVersion))
	s.adminRender(w, r, "manual", b.String())
}

// portOnly extracts "1965" from ":1965".
func portOnly(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// portFlag renders a port suffix (" -p 2222") unless the port is the default
// for that scheme, in which case it stays quiet.
func portFlag(addr, prefix string) string {
	p := portOnly(addr)
	switch p {
	case "22", "23", "":
		return ""
	}
	return prefix + p
}
