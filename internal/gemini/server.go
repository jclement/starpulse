// Package gemini implements the gemini:// server and titan:// uploads.
// Titan writes go straight into the page store (versioned), gated by an
// allowlist of client-certificate fingerprints.
package gemini

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/feed"
	"github.com/jclement/starpulse/internal/script"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

const (
	maxRequestLen = 1024
	ioTimeout     = 30 * time.Second
)

// Server is a gemini + titan protocol server.
type Server struct {
	Cfg   *config.Config
	Store *store.Store
	Site  *site.Site
	Log   *log.Logger
	TLS   *tls.Config
	// Loc is the timezone used in feed timestamps.
	Loc *time.Location
	// Onion returns the hidden-service hostname ("" when tor is off).
	Onion func() string

	ln net.Listener
}

// Listen opens the TLS listener without serving (split out for tests).
func (s *Server) Listen() (net.Listener, error) {
	ln, err := tls.Listen("tcp", s.Cfg.Gemini.Addr, s.TLS)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	return ln, nil
}

// Serve accepts gemini connections on ln until the listener fails.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

// ListenAndServe accepts gemini connections until the listener fails.
func (s *Server) ListenAndServe() error {
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	s.Log.Info("gemini listening", "addr", s.Cfg.Gemini.Addr, "host", s.Cfg.Hostname)
	return s.Serve(ln)
}

// Close stops the listener.
func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

func (s *Server) onion() string {
	if s.Onion != nil {
		return s.Onion()
	}
	return ""
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	start := time.Now()

	r := bufio.NewReaderSize(conn, maxRequestLen+2)
	line, err := readRequestLine(r)
	if err != nil {
		respond(conn, 59, "bad request")
		return
	}
	u, err := url.Parse(line)
	if err != nil || u.Host == "" || u.User != nil {
		respond(conn, 59, "bad request")
		return
	}

	status, meta := 0, ""
	switch u.Scheme {
	case "gemini":
		status, meta = s.serveGemini(conn, u)
	case "titan":
		status, meta = s.serveTitan(conn, r, u)
	default:
		status, meta = 53, "proxy request refused"
		respond(conn, status, meta)
	}

	logFn := s.Log.Info
	if status >= 40 {
		logFn = s.Log.Warn
	}
	logFn("req",
		"status", status,
		"url", line,
		"remote", remoteIP(conn),
		"dur", time.Since(start).Round(time.Millisecond),
		"meta", meta)
}

func readRequestLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) > maxRequestLen+2 {
		return "", fmt.Errorf("request too long")
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if line == "" {
		return "", fmt.Errorf("empty request")
	}
	return line, nil
}

func remoteIP(conn net.Conn) string {
	if h, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
		return h
	}
	return conn.RemoteAddr().String()
}

func respond(w io.Writer, status int, meta string) {
	fmt.Fprintf(w, "%d %s\r\n", status, meta)
}

func (s *Server) hostAllowed(host string) bool {
	return strings.EqualFold(host, s.Cfg.Hostname) || strings.EqualFold(host, "localhost") ||
		(s.onion() != "" && strings.EqualFold(host, s.onion()))
}

func (s *Server) protoFor(host string) string {
	if s.onion() != "" && strings.EqualFold(host, s.onion()) {
		return "gemini+tor"
	}
	return "gemini"
}

func (s *Server) serveGemini(conn net.Conn, u *url.URL) (int, string) {
	if !s.hostAllowed(u.Hostname()) {
		respond(conn, 53, "proxy request refused")
		return 53, "proxy request refused"
	}

	if u.Path == "/search" || u.Path == "/search/" {
		return s.serveSearch(conn, u)
	}
	if raw, found := strings.CutPrefix(u.Path, "/raw/"); found {
		return s.serveRaw(conn, "/"+raw)
	}
	// Atom feeds are served on gemini too — the idiomatic gemini feed is a
	// page of dated links, but some clients want Atom
	if strings.HasSuffix(u.Path, ".xml") {
		if f, ok := feed.Resolve(s.Cfg, s.Store, u.Path); ok {
			b := &feed.Builder{Store: s.Store, Hostname: s.Cfg.Hostname,
				Author: s.Cfg.Feeds.Author, Loc: s.Loc}
			respond(conn, 20, "application/atom+xml")
			_, _ = io.WriteString(conn, b.Build(f, "gemini://"+s.Cfg.Hostname))
			return 20, "feed " + f.Path
		}
	}

	// executable pages, run with the client certificate as identity
	if sp, gmi, ok := s.Site.ScriptFor(u.Path); ok {
		return s.serveScriptGemini(conn, u, sp, gmi)
	}

	// Editors (clients presenting an authorized cert) get the RAW stored
	// source, not the assembled page. This makes titan round-trips clean:
	// Lagrange pre-fills its editor from what it fetches, so serving the
	// rendered page (header/footer + expanded directives) would freeze all
	// of that into the body on save. With an active identity you see/edit
	// source; without it you read the rendered page.
	if _, ok := s.authorizedCert(conn); ok {
		if pg := s.editableSource(u.Path); pg != nil {
			respond(conn, 20, "text/gemini; charset=utf-8")
			_, _ = conn.Write(pg.Content)
			return 20, "raw " + pg.Path
		}
	}

	res := s.Site.Resolve(u.Path, s.protoFor(u.Hostname()))
	switch res.Type {
	case site.RedirectResult:
		respond(conn, 31, res.Location)
		return 31, res.Location
	case site.FileResult:
		respond(conn, 20, res.File.Mime)
		_, _ = conn.Write(res.File.Content)
		return 20, res.File.Mime
	case site.PageResult:
		respond(conn, 20, "text/gemini; charset=utf-8")
		_, _ = io.WriteString(conn, res.Page.Gemtext)
		return 20, "text/gemini"
	default:
		respond(conn, 51, "not found")
		return 51, "not found"
	}
}

// editableSource resolves a request path to its stored source page (the raw
// gemtext an editor should see), or nil if the path has no editable source
// (missing, a directory listing, a static file, or the built-in search).
func (s *Server) editableSource(urlPath string) *store.Page {
	res := s.Site.Resolve(urlPath, "") // proto "" = don't count editor views
	if res.Type != site.PageResult || res.Page.SourcePath == "" {
		return nil
	}
	pg, err := s.Store.GetPage(res.Page.SourcePath)
	if err != nil {
		return nil
	}
	return pg
}

// serveRaw returns a page's unrendered source (for titan editing round
// trips). Requires an allowlisted client certificate.
func (s *Server) serveRaw(conn net.Conn, p string) (int, string) {
	if _, ok := s.authorizedCert(conn); !ok {
		respond(conn, 60, "client certificate required")
		return 60, "client certificate required"
	}
	pg, err := s.Store.GetPage(p)
	if err != nil {
		respond(conn, 51, "not found")
		return 51, "not found"
	}
	mime := pg.Mime
	if strings.HasPrefix(mime, "text/gemini") {
		mime = "text/gemini; charset=utf-8"
	}
	respond(conn, 20, mime)
	_, _ = conn.Write(pg.Content)
	return 20, "raw " + p
}

func (s *Server) serveSearch(conn net.Conn, u *url.URL) (int, string) {
	if u.RawQuery == "" {
		respond(conn, 10, "Search this capsule:")
		return 10, "input"
	}
	q, err := url.QueryUnescape(u.RawQuery)
	if err != nil || strings.TrimSpace(q) == "" {
		respond(conn, 10, "Search this capsule:")
		return 10, "input"
	}
	hits, _ := s.Store.Search(q, 20)
	var b strings.Builder
	fmt.Fprintf(&b, "# Search: %s\n\n", q)
	if len(hits) == 0 {
		b.WriteString("Nothing found. Try fewer or different words.\n")
	} else {
		fmt.Fprintf(&b, "%d result(s):\n\n", len(hits))
		for _, h := range hits {
			fmt.Fprintf(&b, "=> %s %s\n", webToGeminiURL(h.Path), titleOr(h.Title, h.Path))
			if h.Snippet != "" {
				fmt.Fprintf(&b, "> …%s…\n", h.Snippet)
			}
		}
	}
	b.WriteString("\n=> / Home\n")
	respond(conn, 20, "text/gemini; charset=utf-8")
	_, _ = io.WriteString(conn, b.String())
	return 20, "search"
}

func titleOr(t, fallback string) string {
	if t != "" {
		return t
	}
	return fallback
}

// webToGeminiURL converts a storage path to its served URL.
func webToGeminiURL(p string) string {
	u := strings.TrimSuffix(p, ".gmi")
	if strings.HasSuffix(u, "/index") {
		u = strings.TrimSuffix(u, "index")
	}
	return u
}

// ---- titan uploads ------------------------------------------------------

// serveScriptGemini runs an executable page over gemini. Identity is the
// client certificate (proof, not a bearer token); a line of input arrives as
// the URL query, which is exactly gemini's status-10 round trip. Since a
// gemini response is either a prompt (10/11) or a body (20), never both, the
// rule is: ask when no line has been given, otherwise show what the script
// produced.
func (s *Server) serveScriptGemini(conn net.Conn, u *url.URL, storePath string, gmi bool) (int, string) {
	req := script.Request{Proto: s.protoFor(u.Hostname()), Host: s.Cfg.Hostname, Query: map[string]string{}}
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if certs := tlsConn.ConnectionState().PeerCertificates; len(certs) > 0 {
			req.Identity = auth.Fingerprint(sha256.Sum256(certs[0].Raw))
			req.IdentityKind, req.Verified = "cert", true
			req.IdentityName = certs[0].Subject.CommonName
		}
	}
	// Following the "make a guess" link asks for a line (status 10). A gemini
	// response is a prompt or a body, never both, so a bare visit shows the
	// body and offers this link rather than jumping straight to the prompt.
	if u.RawQuery == geminiPromptMarker {
		res, err := s.Site.RunScript(context.Background(), storePath, u.Path, req)
		if err != nil {
			respond(conn, 40, "script error")
			return 40, err.Error()
		}
		if res.NeedInput {
			code := 10
			if res.Sensitive {
				code = 11
			}
			respond(conn, code, res.Prompt)
			return code, "input"
		}
		// the page no longer wants input — fall through and show it
		return s.writeScriptBody(conn, res, gmi, u.Path, storePath)
	}

	if u.RawQuery != "" {
		if dec, err := url.QueryUnescape(u.RawQuery); err == nil {
			req.Input, req.HasInput = dec, true
		}
	}
	res, err := s.Site.RunScript(context.Background(), storePath, u.Path, req)
	if err != nil {
		respond(conn, 40, "script error")
		return 40, err.Error()
	}
	return s.writeScriptBody(conn, res, gmi, u.Path, storePath)
}

// geminiPromptMarker is the query a "make a guess" link carries; the door
// treats it as "ask the reader for a line" rather than as input to process.
const geminiPromptMarker = "_ask"

func (s *Server) writeScriptBody(conn net.Conn, res site.ScriptResult, gmi bool, urlPath, storePath string) (int, string) {
	mime := "text/gemini; charset=utf-8"
	if !gmi {
		mime = "text/plain; charset=utf-8"
	}
	body := res.Body
	// the page wants a line: offer the way to give one, since gemini cannot
	// show the body and a prompt at once
	if res.NeedInput && gmi {
		label := res.Prompt
		if label == "" {
			label = "continue"
		}
		body += "\n=> " + urlPath + "?" + geminiPromptMarker + " " + label + "\n"
	}
	respond(conn, 20, mime)
	_, _ = io.WriteString(conn, body)
	return 20, "script " + storePath
}

// authorizedCert returns the client cert fingerprint if the connection
// presented a cert on the configured allowlist.
func (s *Server) authorizedCert(conn net.Conn) (string, bool) {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", false
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", false
	}
	fp := auth.Fingerprint(sha256.Sum256(certs[0].Raw))
	for _, want := range s.Cfg.NormalizedFingerprints() {
		if want == fp {
			return fp, true
		}
	}
	return fp, false
}

// titanTarget resolves a titan upload path to a storage path, mapping
// directory-style targets to their index.gmi. Returns "" for invalid paths.
func titanTarget(st *store.Store, rawPath string) string {
	if rawPath == "" {
		rawPath = "/"
	}
	if rawPath == "/" || strings.HasSuffix(rawPath, "/") {
		// uploading to a folder that publishes a feed posts a new entry,
		// because that is the only sensible reading of "here is another
		// thing for /now/". Everywhere else a folder means its index page.
		if rawPath != "/" && st.IsFeedFolder(rawPath) {
			return st.NewStreamPath(rawPath, time.Now())
		}
		idx := strings.TrimSuffix(rawPath, "/") + "/index.gmi"
		if cp, ok := store.CleanPath(idx); ok {
			return cp
		}
		return ""
	}
	// an extensionless path that already exists as a directory → its index;
	// but if it exists (or resolves) as a page, keep it as-is
	cp, ok := store.CleanPath(rawPath)
	if !ok {
		return ""
	}
	if store.MimeFor(cp) == "application/octet-stream" && !strings.Contains(pathExt(cp), ".") {
		// no file extension: prefer an existing .gmi page, else treat the
		// extensionless name as a .gmi page to create
		if st.PageExists(cp) {
			return cp
		}
		return cp + ".gmi"
	}
	return cp
}

func pathExt(p string) string {
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		return base[i:]
	}
	return ""
}

// serveTitan handles titan://host/path;mime=...;size=N uploads into the
// page store. Zero size deletes, per titan convention.
func (s *Server) serveTitan(conn net.Conn, r *bufio.Reader, u *url.URL) (int, string) {
	if !s.Cfg.Titan.Enabled {
		respond(conn, 53, "titan disabled")
		return 53, "titan disabled"
	}
	if !s.hostAllowed(u.Hostname()) {
		respond(conn, 53, "proxy request refused")
		return 53, "proxy request refused"
	}
	fp, ok := s.authorizedCert(conn)
	if !ok {
		if fp == "" {
			respond(conn, 60, "client certificate required")
			return 60, "client certificate required"
		}
		respond(conn, 61, "certificate not authorized")
		return 61, "certificate not authorized"
	}

	// path;mime=text/gemini;size=1234[;token=x]
	segs := strings.Split(u.Path, ";")
	rawPath := segs[0]
	params := map[string]string{}
	for _, kv := range segs[1:] {
		if k, v, found := strings.Cut(kv, "="); found {
			params[k] = v
		}
	}
	size, err := strconv.ParseInt(params["size"], 10, 64)
	if err != nil || size < 0 || size > s.Cfg.MaxUploadBytes {
		respond(conn, 59, "bad or excessive size")
		return 59, "bad size"
	}

	// a directory-style target (/, /posts/, or the extensionless URL of a
	// directory) edits that directory's index.gmi — so titan-editing the
	// page you're viewing works even at the site root.
	cleaned := titanTarget(s.Store, rawPath)
	if cleaned == "" {
		respond(conn, 59, "bad path")
		return 59, "bad path"
	}

	author := "titan:" + fp[:12]

	if size == 0 {
		if err := s.Store.DeletePage(cleaned, author); err != nil {
			respond(conn, 51, "not found")
			return 51, "not found"
		}
		respond(conn, 20, "text/gemini")
		fmt.Fprintf(conn, "# Deleted %s\n=> gemini://%s/ Home\n", cleaned, s.Cfg.Hostname)
		return 20, "deleted " + cleaned
	}

	content := make([]byte, size)
	if _, err := io.ReadFull(r, content); err != nil {
		respond(conn, 40, "short read")
		return 40, "short read"
	}
	mime, err := url.QueryUnescape(params["mime"])
	if err != nil {
		mime = ""
	}
	// the path extension is authoritative for known text/image types —
	// gemini clients (Lagrange) upload with a generic text/plain, which
	// would otherwise store gemtext pages as plain text.
	if byExt := store.MimeFor(cleaned); byExt != "application/octet-stream" {
		mime = byExt
	} else if mime == "" {
		mime = byExt
	}
	if _, err := s.Store.SavePage(cleaned, content, mime, author); err != nil {
		respond(conn, 40, "save failed")
		return 40, "save failed"
	}
	geminiURL := "gemini://" + s.Cfg.Hostname + webToGeminiURL(cleaned)
	respond(conn, 30, geminiURL)
	return 30, "uploaded " + cleaned
}
