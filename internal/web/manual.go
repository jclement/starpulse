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
	b.WriteString(`<h2>Connecting an AI client</h2>
<p><code>/mcp</code> speaks MCP over HTTP. A client authorizing through OAuth must use PKCE, and is only redirected back to a loopback address, this site, or a host named in <code>oauth_redirect_hosts</code> — that host receives a credential with full admin rights, so listing one is a deliberate act. Claude Desktop can skip the flow: client id <code>mcp</code>, client secret = the admin password.</p>

<h2>Drafts</h2>
<p>The editor has two verbs. <strong>Save draft</strong> keeps the work to yourself: the site carries on showing the published version, and a page that has never been published simply is not there — 404 on every door, absent from listings, feeds and search. <strong>Publish</strong> is what the world sees. Ctrl/⌘-S saves a draft; it never publishes.</p>
<p>Opening a page that has a draft continues the draft rather than starting from what is live, so there is no way to lose it by accident. Drafts keep their own history while you work, and publishing records <em>one</em> entry in the page's history however many times you saved along the way. <strong>Discard</strong> throws the draft away; if the page was never published, that removes it entirely.</p>
<p>Only this editor writes drafts. Titan, the API, MCP and the terminal editors publish directly — there is no way to express "unpublished" over those, so they say what they mean. Backups carry drafts under <code>drafts/</code>, and restoring puts them back as drafts.</p>

<h2>Executable pages</h2>
<p>A page whose name ends <code>.cgi</code> is a program. <code>/game.cgi</code> is served at <code>/game</code>, its output rendered as gemtext (name it <code>.txt.cgi</code> for plain text instead); the raw source is never served. They are templates, like PHP: text is emitted as-is, code lives in <code>&lt;? … ?&gt;</code>, and <code>&lt;?= expr ?&gt;</code> writes a value. A page that is all program simply opens with <code>&lt;?</code> and never closes.</p>
<pre># Hello
Hi <?= sp.identity() ?>, it is <?= request.now ?>.
&lt;? for _, e in ipairs(sp.list("guests")) do ?&gt;
* <?= e ?>
&lt;? end ?&gt;</pre>
<p>The sandbox is Lua with no filesystem, network or clock beyond what it is handed: <code>request</code> (path, query, proto, host, and identity), <code>write()</code>, <code>prompt()</code> to ask the reader for a line (a web form, gemini status 10, the terminal prompt), and a per-page key/value <code>store</code>. The <code>sp</code> library wraps the common patterns — <code>sp.require_identity()</code>, <code>sp.require_strong()</code> (a certificate or ssh key, not a cookie), <code>sp.get/set</code>, <code>sp.list/push</code>. CPU and output are capped. Script data is included in backups.</p>

<h2>Backups</h2>
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
