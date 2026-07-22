// Package web serves the site over HTTP/HTTPS with a thin no-JS HTML
// wrapper, plus the admin UI, /api REST endpoints, and the /mcp server.
package web

import (
	"crypto/tls"
	"embed"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/acme/autocert"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/feed"
	"github.com/jclement/starpulse/internal/highlight"
	"github.com/jclement/starpulse/internal/render"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

//go:embed assets
var assets embed.FS

var pageTpl = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<meta name="description" content="{{.Desc}}">
<link rel="stylesheet" href="/_/style.css?v={{.AssetV}}">
{{if .Highlight}}<link rel="stylesheet" href="/_/highlight.css?v={{.AssetV}}">{{end}}
{{range .Feeds}}<link rel="alternate" type="application/atom+xml" title="{{.Title}}" href="{{.Path}}">
{{end}}
<link rel="icon" href="data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 100 100%22><text y=%22.9em%22 font-size=%2290%22>✨</text></svg>">
{{if .Theme}}<style>
{{.Theme}}
</style>{{end}}
</head>
<body>
<main>
{{.Body}}
</main>
<footer class="site">
<p>{{if .EditPath}}<a class="edit-link" href="/admin/edit?path={{.EditPath}}">✎ edit</a> · <a class="edit-link" href="/admin">admin</a> · {{end}}also on gemini: <a href="gemini://{{.Host}}{{.GemPath}}">gemini://{{.Host}}{{.GemPath}}</a> · <a href="/search">search</a> · served by <a href="https://github.com/jclement/starpulse">starpulse</a></p>
</footer>
</body>
</html>
`))

type pageData struct {
	Title     string
	Desc      string
	Host      string
	Body      template.HTML
	Theme     template.CSS
	EditPath  string // set when logged in and the page has an editable source
	GemPath   string // this page's path on gemini (root for admin/login)
	AssetV    string // cache-buster for embedded assets
	Feeds     []config.Feed
	Highlight bool
}

// Server is the web half of starpulse.
type Server struct {
	Cfg      *config.Config
	Store    *store.Store
	Site     *site.Site
	Log      *log.Logger
	Sessions *auth.Sessions
	// Loc is the timezone for displayed timestamps (nil = server local).
	Loc *time.Location
	// Onion returns the hidden-service hostname ("" when tor is off).
	Onion func() string

	throttle     *auth.Throttle
	throttleOnce sync.Once
	oauthCodes     *codeStore
	oauthCodesOnce sync.Once
	hl             *highlight.Highlighter
	hlOnce         sync.Once
}

// highlighter builds (once) the syntax highlighter for code blocks.
func (s *Server) highlighter() *highlight.Highlighter {
	s.hlOnce.Do(func() {
		s.hl = highlight.New(s.Cfg.Highlight.Style, s.Cfg.Highlight.DarkStyle)
	})
	return s.hl
}

// renderOpts carries the highlighter into the gemtext renderer when enabled.
func (s *Server) renderOpts() render.Options {
	if !s.Cfg.Highlight.Enabled {
		return render.Options{}
	}
	return render.Options{Highlight: s.highlighter().Render}
}

const sessionCookie = "starpulse_session"

// authGate is the shared per-IP failed-auth limiter: 10 failures in five
// minutes locks that address out for five. sync.Once because two concurrent
// first requests would otherwise each build one and one would be discarded,
// quietly forgetting the failures recorded in it.
func (s *Server) authGate() *auth.Throttle {
	s.throttleOnce.Do(func() { s.throttle = auth.NewThrottle(10, 5*time.Minute) })
	return s.throttle
}

// nowFolder is where short notes live.
func (s *Server) nowFolder() string {
	f := s.Cfg.NowFolder
	if f == "" {
		f = "/now/"
	}
	if !strings.HasSuffix(f, "/") {
		f += "/"
	}
	return f
}

func (s *Server) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

func (s *Server) onion() string {
	if s.Onion != nil {
		return s.Onion()
	}
	return ""
}

// Handler builds the full HTTP handler tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(assets, "assets")
	mux.Handle("/_/", http.StripPrefix("/_/", cacheControl(http.FileServer(http.FS(sub)))))

	mux.HandleFunc("/_/highlight.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(s.highlighter().CSS()))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/search", s.handleSearch)

	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	s.registerAdmin(mux)
	s.registerAPI(mux)
	s.registerMCP(mux)
	s.registerOAuth(mux)

	mux.HandleFunc("/", s.handlePage)

	return s.logMiddleware(mux)
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		next.ServeHTTP(w, r)
	})
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/_/") || r.URL.Path == "/healthz" {
			return
		}
		logFn := s.Log.Info
		if rec.status >= 400 {
			logFn = s.Log.Warn
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		logFn("req",
			"status", rec.status,
			"method", r.Method,
			"path", r.URL.RequestURI(),
			"remote", host,
			"dur", time.Since(start).Round(time.Millisecond))
	})
}

// loggedIn reports whether the request carries a valid admin session.
func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.Sessions.Valid(c.Value)
}

// proto returns the stats bucket for a request.
// viaOnion reports whether a request really arrived through the hidden
// service. The Host header alone is not evidence — it is written by whoever
// is asking — and trusting it meant anyone could fetch the site in cleartext
// on port 80, and be counted as a tor visitor, by sending the onion name.
// Tor forwards from the loopback interface (HiddenServicePort … 127.0.0.1),
// so a request claiming to be onion traffic from a public address is not.
func (s *Server) viaOnion(r *http.Request) bool {
	o := s.onion()
	if o == "" || !strings.EqualFold(stripPort(r.Host), o) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) proto(r *http.Request) string {
	if s.viaOnion(r) {
		return "http+tor" // tor carries its own encryption; TLS on top is rare
	}
	// the stats table always had a column for https and nothing ever filled
	// it: every web request was bucketed as plain http, so an encrypted
	// visit was indistinguishable from a cleartext one
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, title, desc, theme, editPath string, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if !s.loggedIn(r) {
		editPath = ""
	}
	// the "also on gemini" link points at this same page; admin and login
	// pages have no gemini equivalent, so they fall back to the capsule root
	gemPath := r.URL.Path
	if gemPath == "" || strings.HasPrefix(gemPath, "/admin") || strings.HasPrefix(gemPath, "/login") {
		gemPath = "/"
	}
	_ = pageTpl.Execute(w, pageData{
		Title:     title,
		Desc:      desc,
		Host:      s.Cfg.Hostname,
		Body:      template.HTML(wrapEmoji(body)),
		Theme:     template.CSS(theme),
		EditPath:  editPath,
		GemPath:   gemPath,
		AssetV:    site.BuildVersion,
		Feeds:     s.discoverableFeeds(),
		Highlight: s.Cfg.Highlight.Enabled,
	})
}

// discoverableFeeds lists every feed worth advertising in <head>:
// configured ones plus every folder that publishes one.
func (s *Server) discoverableFeeds() []config.Feed {
	out := s.Cfg.EffectiveFeeds()
	seen := map[string]bool{}
	for _, f := range out {
		seen[f.Path] = true
	}
	for folder := range s.Store.FeedFolders() {
		path := folder + "feed.xml"
		if seen[path] {
			continue
		}
		if f, ok := feed.Resolve(s.Cfg, s.Store, path); ok {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (s *Server) pageTitle(t string) string {
	if t == "" {
		return s.Cfg.Hostname
	}
	if strings.Contains(strings.ToLower(t), strings.ToLower(s.Cfg.Hostname)) {
		return t
	}
	return t + " · " + s.Cfg.Hostname
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.serveFeed(w, r, r.URL.Path) {
		return
	}
	if s.serveScript(w, r) {
		return
	}
	res := s.Site.Resolve(r.URL.Path, s.proto(r))
	switch res.Type {
	case site.RedirectResult:
		http.Redirect(w, r, res.Location, http.StatusMovedPermanently)
	case site.FileResult:
		mime := res.File.Mime
		// uploaded files could carry active content (html, svg with script).
		// they render on the site origin, so neutralize active types:
		// relabel as text/plain and force download rather than inline exec.
		if isActiveMime(mime) {
			mime = "text/plain; charset=utf-8"
			w.Header().Set("Content-Disposition", "attachment")
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeContent(w, r, "", res.File.Updated, strings.NewReader(string(res.File.Content)))
	case site.PageResult:
		body := s.gemtextBody(res.Page.Gemtext)
		editPath := res.Page.SourcePath
		s.render(w, r, http.StatusOK, s.pageTitle(res.Page.Title), s.Cfg.Hostname, res.Page.Theme, editPath, body)
	default:
		notFound := `<h1>not found</h1><p>That page isn't here. It may have drifted off into gemini-space.</p><p class="lnk"><a href="/">Back home</a></p>`
		s.render(w, r, http.StatusNotFound, "not found · "+s.Cfg.Hostname, "not found", "", "", notFound)
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var b strings.Builder
	b.WriteString("<h1>Search</h1>\n")
	fmt.Fprintf(&b, `<form class="search" action="/search" method="get"><input type="text" name="q" value="%s" placeholder="search this site…" autofocus><button type="submit">go</button></form>`+"\n", html.EscapeString(q))
	if q != "" {
		hits, err := s.Store.Search(q, 20)
		if err != nil {
			s.Log.Warn("search", "err", err)
		}
		if len(hits) == 0 {
			b.WriteString("<p>Nothing found. Try fewer or different words.</p>\n")
		} else {
			fmt.Fprintf(&b, "<p>%d result(s):</p>\n", len(hits))
			for _, h := range hits {
				title := h.Title
				if title == "" {
					title = h.Path
				}
				fmt.Fprintf(&b, `<div class="hit"><p class="lnk"><a href="%s">%s</a></p><p class="snip">…%s…</p></div>`+"\n",
					pageURL(h.Path), html.EscapeString(title), html.EscapeString(h.Snippet))
			}
		}
	}
	b.WriteString(`<p class="lnk"><a href="/">Back home</a></p>`)
	s.render(w, r, http.StatusOK, "search · "+s.Cfg.Hostname, "search this site", "", "", b.String())
}

// pageURL converts a storage path to its served URL.
func pageURL(p string) string {
	u := strings.TrimSuffix(p, ".gmi")
	if strings.HasSuffix(u, "/index") {
		u = strings.TrimSuffix(u, "index")
	}
	return u
}

// ---- login --------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.AdminPassword == "" {
		s.render(w, r, http.StatusForbidden, "login", "login", "", "",
			`<h1>Login disabled</h1><p>No admin_password is configured.</p>`)
		return
	}
	if r.Method == http.MethodPost {
		ip := auth.ClientIP(r)
		if s.authGate().Blocked(ip, time.Now()) {
			s.Log.Warn("login throttled", "remote", ip)
			s.renderLogin(w, r, "Too many attempts — try again later.")
			return
		}
		if auth.CheckPassword(s.Cfg.AdminPassword, r.FormValue("password")) {
			s.authGate().Succeed(ip)
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookie,
				Value:    s.Sessions.Token(30 * 24 * time.Hour),
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
				Secure:   r.TLS != nil,
				MaxAge:   30 * 24 * 3600,
			})
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if s.authGate().Fail(ip, time.Now()) {
			s.Log.Warn("login lockout", "remote", ip)
		} else {
			s.Log.Warn("failed login", "remote", ip)
		}
		time.Sleep(time.Second) // soften brute force
		s.renderLogin(w, r, "Wrong password.")
		return
	}
	s.renderLogin(w, r, "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, errMsg string) {
	var b strings.Builder
	b.WriteString("<h1>Sign in</h1>\n")
	if errMsg != "" {
		fmt.Fprintf(&b, `<p class="flash err">%s</p>`+"\n", html.EscapeString(errMsg))
	}
	// Password managers classify a form by what it contains, and a lone
	// password box with no autocomplete hints is ambiguous — 1Password and
	// Safari want a username field to attach the saved item to, and the
	// autocomplete tokens to tell "sign in here" from "change your password
	// here". There is only one account, so the username is fixed and shown
	// rather than asked for; the server does not read it.
	b.WriteString(`<form class="admin" method="post" action="/login">
<label for="username">user</label>
<input type="text" id="username" name="username" value="admin" autocomplete="username" spellcheck="false" autocapitalize="none" readonly>
<label for="password">admin password</label>
<input type="password" id="password" name="password" autocomplete="current-password" autofocus>
<div class="bar"><button type="submit">sign in</button></div>
</form>`)
	s.render(w, r, http.StatusOK, "Sign in · "+s.Cfg.Hostname, "login", "", "", b.String())
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---- serving ------------------------------------------------------------

// Serve starts the configured web listeners and blocks until one fails.
func (s *Server) Serve() error {
	h := s.Handler()

	useACME := s.Cfg.HTTPS.Enabled && s.Cfg.HTTPS.ACME

	var mgr *autocert.Manager
	if useACME {
		mgr = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(s.Cfg.Hostname),
			Cache:      autocert.DirCache(s.Cfg.DataDir + "/certs"),
			Email:      s.Cfg.HTTPS.ACMEEmail,
		}
	}

	// wrap: advertise the onion mirror
	public := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := s.onion(); o != "" {
			w.Header().Set("Onion-Location", "http://"+o+r.URL.RequestURI())
		}
		h.ServeHTTP(w, r)
	})

	errCh := make(chan error, 2)
	started := 0

	if s.Cfg.HTTP.Enabled {
		var handler http.Handler = public
		if s.Cfg.HTTPS.Enabled {
			// plain HTTP redirects to https — except onion visitors (tor is
			// already end-to-end encrypted) and ACME challenges
			redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthz" {
					fmt.Fprintln(w, "ok")
					return
				}
				if s.viaOnion(r) {
					h.ServeHTTP(w, r)
					return
				}
				http.Redirect(w, r, "https://"+s.Cfg.Hostname+r.URL.RequestURI(), http.StatusMovedPermanently)
			})
			handler = redirect
			if mgr != nil {
				handler = mgr.HTTPHandler(redirect)
			}
		}
		started++
		go func() {
			s.Log.Info("http listening", "addr", s.Cfg.HTTP.Addr)
			errCh <- http.ListenAndServe(s.Cfg.HTTP.Addr, handler)
		}()
	}

	if s.Cfg.HTTPS.Enabled {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if mgr != nil {
			tlsCfg.GetCertificate = mgr.GetCertificate
		} else {
			// self-signed fallback so https still works without ACME
			cert, err := selfSigned(s.Cfg)
			if err != nil {
				return err
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		srv := &http.Server{Addr: s.Cfg.HTTPS.Addr, Handler: public, TLSConfig: tlsCfg}
		started++
		go func() {
			s.Log.Info("https listening", "addr", s.Cfg.HTTPS.Addr, "acme", useACME)
			errCh <- srv.ListenAndServeTLS("", "")
		}()
	}

	if started == 0 {
		return fmt.Errorf("no web listeners enabled")
	}
	return <-errCh
}
