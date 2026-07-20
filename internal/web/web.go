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
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/acme/autocert"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
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
<link rel="stylesheet" href="/_/style.css">
<link rel="alternate" type="application/atom+xml" title="feed" href="/feed.xml">
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
<p>{{if .EditPath}}<a class="edit-link" href="/admin/edit?path={{.EditPath}}">✎ edit</a> · <a class="edit-link" href="/admin">admin</a> · {{end}}also on gemini: <a href="gemini://{{.Host}}/">gemini://{{.Host}}/</a> · <a href="/search">search</a> · served by <a href="https://github.com/jclement/starpulse">starpulse</a></p>
</footer>
</body>
</html>
`))

type pageData struct {
	Title    string
	Desc     string
	Host     string
	Body     template.HTML
	Theme    template.CSS
	EditPath string // set when logged in and the page has an editable source
}

// Server is the web half of starpulse.
type Server struct {
	Cfg      *config.Config
	Store    *store.Store
	Site     *site.Site
	Log      *log.Logger
	Sessions *auth.Sessions
	// Onion returns the hidden-service hostname ("" when tor is off).
	Onion func() string
}

const sessionCookie = "starpulse_session"

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

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/feed.xml", s.handleFeed)
	mux.HandleFunc("/posts/feed.xml", s.handleFeed)

	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	s.registerAdmin(mux)
	s.registerAPI(mux)
	s.registerMCP(mux)

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
func (s *Server) proto(r *http.Request) string {
	if o := s.onion(); o != "" && strings.EqualFold(stripPort(r.Host), o) {
		return "http+tor"
	}
	return "http"
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, title, desc, theme, editPath string, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if !s.loggedIn(r) {
		editPath = ""
	}
	_ = pageTpl.Execute(w, pageData{
		Title:    title,
		Desc:     desc,
		Host:     s.Cfg.Hostname,
		Body:     template.HTML(wrapEmoji(body)),
		Theme:    template.CSS(theme),
		EditPath: editPath,
	})
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
	res := s.Site.Resolve(r.URL.Path, s.proto(r))
	switch res.Type {
	case site.RedirectResult:
		http.Redirect(w, r, res.Location, http.StatusMovedPermanently)
	case site.FileResult:
		w.Header().Set("Content-Type", res.File.Mime)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeContent(w, r, "", res.File.Updated, strings.NewReader(string(res.File.Content)))
	case site.PageResult:
		body := gemtextBody(res.Page.Gemtext)
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

// handleFeed emits an Atom feed of dated pages (site-wide, newest first).
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	metas, err := s.Store.ListAll()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type item struct {
		title, url, date string
	}
	var items []item
	for _, m := range metas {
		if m.Binary || store.Hidden(m.Path) || !strings.HasSuffix(m.Path, ".gmi") {
			continue
		}
		if d := datedName(m.Path); d != "" {
			title := m.Title
			if title == "" {
				title = m.Path
			}
			items = append(items, item{title: title, url: pageURL(m.Path), date: d})
		}
	}
	// newest first
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].date > items[i].date {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > 30 {
		items = items[:30]
	}

	base := "https://" + s.Cfg.Hostname
	updated := time.Now().UTC().Format(time.RFC3339)
	if len(items) > 0 {
		if t, err := time.Parse("2006-01-02", items[0].date); err == nil {
			updated = t.UTC().Format(time.RFC3339)
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">` + "\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(s.Cfg.Hostname))
	fmt.Fprintf(&b, `<link href="%s/"/>`+"\n", base)
	fmt.Fprintf(&b, `<link rel="self" href="%s/feed.xml"/>`+"\n", base)
	fmt.Fprintf(&b, "<id>%s/</id>\n<updated>%s</updated>\n", base, updated)
	for _, it := range items {
		t, err := time.Parse("2006-01-02", it.date)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "<entry>\n<title>%s</title>\n", html.EscapeString(it.title))
		fmt.Fprintf(&b, `<link href="%s%s"/>`+"\n", base, it.url)
		fmt.Fprintf(&b, "<id>%s%s</id>\n<updated>%s</updated>\n</entry>\n", base, it.url, t.UTC().Format(time.RFC3339))
	}
	b.WriteString("</feed>\n")
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func datedName(p string) string {
	base := p[strings.LastIndexByte(p, '/')+1:]
	if len(base) >= 11 && base[4] == '-' && base[7] == '-' && (base[10] == '-' || base[10] == '_') {
		return base[:10]
	}
	return ""
}

// ---- login --------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.AdminPassword == "" {
		s.render(w, r, http.StatusForbidden, "login", "login", "", "",
			`<h1>Login disabled</h1><p>No admin_password is configured.</p>`)
		return
	}
	if r.Method == http.MethodPost {
		if auth.CheckPassword(s.Cfg.AdminPassword, r.FormValue("password")) {
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
		s.Log.Warn("failed login", "remote", r.RemoteAddr)
		time.Sleep(time.Second) // soften brute force
		s.renderLogin(w, r, "Wrong password.")
		return
	}
	s.renderLogin(w, r, "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, errMsg string) {
	var b strings.Builder
	b.WriteString("<h1>Login</h1>\n")
	if errMsg != "" {
		fmt.Fprintf(&b, `<p class="flash err">%s</p>`+"\n", html.EscapeString(errMsg))
	}
	b.WriteString(`<form class="admin" method="post" action="/login">
<label for="password">admin password</label>
<input type="password" id="password" name="password" autofocus>
<div class="bar"><button type="submit">login</button></div>
</form>`)
	s.render(w, r, http.StatusOK, "login · "+s.Cfg.Hostname, "login", "", "", b.String())
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
				if o := s.onion(); o != "" && strings.EqualFold(stripPort(r.Host), o) {
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
