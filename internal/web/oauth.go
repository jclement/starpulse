package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jclement/starpulse/internal/auth"
)

// OAuth 2.1 for the /mcp endpoint, sized for a single-admin server.
//
// Clients that support typed-in credentials (Claude Desktop) use
// client_id "mcp" with the admin password as the client secret. Clients
// that discover dynamically get the same fixed client back from the
// registration endpoint. Access tokens are the same HMAC-signed tokens the
// web session uses, so no server-side token storage is needed.

const (
	oauthClientID   = "mcp"
	oauthCodeTTL    = 5 * time.Minute
	oauthTokenTTL   = 30 * 24 * time.Hour
	oauthRefreshTTL = 180 * 24 * time.Hour
)

// authCode is a pending authorization code (single use, short lived).
type authCode struct {
	challenge   string // PKCE code_challenge (S256); "" when not used
	redirectURI string
	expires     time.Time
}

type codeStore struct {
	mu    sync.Mutex
	codes map[string]authCode
}

func newCodeStore() *codeStore { return &codeStore{codes: map[string]authCode{}} }

func (c *codeStore) put(code string, ac authCode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// opportunistic sweep of expired codes
	now := time.Now()
	for k, v := range c.codes {
		if now.After(v.expires) {
			delete(c.codes, k)
		}
	}
	c.codes[code] = ac
}

// take returns and consumes a code (single use).
func (c *codeStore) take(code string) (authCode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ac, ok := c.codes[code]
	if !ok {
		return authCode{}, false
	}
	delete(c.codes, code)
	if time.Now().After(ac.expires) {
		return authCode{}, false
	}
	return ac, true
}

func (s *Server) codes() *codeStore {
	if s.oauthCodes == nil {
		s.oauthCodes = newCodeStore()
	}
	return s.oauthCodes
}

// baseURL is the public origin used in OAuth metadata.
func (s *Server) baseURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && !s.Cfg.HTTPS.Enabled {
		scheme = "http"
	}
	host := s.Cfg.Hostname
	if host == "" || host == "localhost" {
		host = r.Host
	}
	return scheme + "://" + host
}

// registerOAuth wires the discovery, authorize, token and registration
// endpoints.
func (s *Server) registerOAuth(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.oauthProtectedResource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.oauthProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.oauthServerMetadata)
	mux.HandleFunc("/oauth/authorize", s.oauthAuthorize)
	mux.HandleFunc("/oauth/token", s.oauthToken)
	mux.HandleFunc("/oauth/register", s.oauthRegister)
}

func (s *Server) oauthProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	jsonOut(w, http.StatusOK, map[string]any{
		"resource":                 base + "/mcp",
		"authorization_servers":    []string{base},
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) oauthServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	jsonOut(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "client_credentials"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic", "none"},
		"scopes_supported":                      []string{"mcp"},
	})
}

// oauthRegister implements just enough dynamic client registration to hand
// back the one fixed client this server has.
func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	jsonOut(w, http.StatusCreated, map[string]any{
		"client_id":                  oauthClientID,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// validRedirect rejects obviously unsafe redirect targets (open redirects).
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	default:
		// custom app schemes (desktop clients) are fine
		return u.Scheme != "javascript" && u.Scheme != "data"
	}
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// oauthAuthorize shows a password prompt, then redirects back with a code.
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	challenge := q.Get("code_challenge")
	method := q.Get("code_challenge_method")

	if redirectURI == "" || !validRedirect(redirectURI) {
		s.render(w, r, http.StatusBadRequest, "authorize", "oauth", "", "",
			`<h1>Invalid request</h1><p>Missing or unacceptable redirect_uri.</p>`)
		return
	}
	if challenge != "" && method != "S256" {
		s.redirectErr(w, r, redirectURI, state, "invalid_request", "only S256 PKCE is supported")
		return
	}
	if s.Cfg.AdminPassword == "" {
		s.redirectErr(w, r, redirectURI, state, "server_error", "no admin password configured")
		return
	}

	if r.Method == http.MethodPost {
		ip := clientIP(r)
		if s.authGate().blocked(ip, time.Now()) {
			s.renderAuthorize(w, r, "Too many attempts — try again later.")
			return
		}
		if !auth.CheckPassword(s.Cfg.AdminPassword, r.FormValue("password")) {
			s.authGate().fail(ip, time.Now())
			time.Sleep(time.Second)
			s.renderAuthorize(w, r, "Wrong password.")
			return
		}
		s.authGate().succeed(ip)

		code := randToken(24)
		s.codes().put(code, authCode{
			challenge:   challenge,
			redirectURI: redirectURI,
			expires:     time.Now().Add(oauthCodeTTL),
		})
		u, _ := url.Parse(redirectURI)
		qq := u.Query()
		qq.Set("code", code)
		if state != "" {
			qq.Set("state", state)
		}
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
		return
	}
	s.renderAuthorize(w, r, "")
}

func (s *Server) renderAuthorize(w http.ResponseWriter, r *http.Request, errMsg string) {
	var b strings.Builder
	b.WriteString("<h1>Authorize access</h1>\n")
	fmt.Fprintf(&b, `<p>An application is asking to read and edit <strong>%s</strong> through its MCP endpoint.</p>`+"\n",
		html.EscapeString(s.Cfg.Hostname))
	if errMsg != "" {
		fmt.Fprintf(&b, `<p class="flash err">%s</p>`+"\n", html.EscapeString(errMsg))
	}
	// preserve every OAuth parameter across the POST
	fmt.Fprintf(&b, `<form class="admin" method="post" action="/oauth/authorize?%s">`, html.EscapeString(r.URL.RawQuery))
	b.WriteString(`
<label for="password">admin password</label>
<input type="password" id="password" name="password" autofocus autocomplete="current-password">
<div class="bar"><button type="submit">authorize</button></div>
</form>`)
	s.render(w, r, http.StatusOK, "authorize · "+s.Cfg.Hostname, "authorize an MCP client", "", "", b.String())
}

func (s *Server) redirectErr(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, desc, http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// clientCreds pulls client_id/secret from the form or HTTP Basic auth.
func clientCreds(r *http.Request) (id, secret string) {
	if u, p, ok := r.BasicAuth(); ok {
		return u, p
	}
	return r.FormValue("client_id"), r.FormValue("client_secret")
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	grant := r.FormValue("grant_type")
	clientID, clientSecret := clientCreds(r)
	if clientID != "" && clientID != oauthClientID {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
		return
	}

	issue := func() {
		jsonOut(w, http.StatusOK, map[string]any{
			"access_token":  s.Sessions.Token(oauthTokenTTL),
			"token_type":    "Bearer",
			"expires_in":    int(oauthTokenTTL.Seconds()),
			"refresh_token": s.Sessions.Token(oauthRefreshTTL),
			"scope":         "mcp",
		})
	}

	switch grant {
	case "authorization_code":
		ac, ok := s.codes().take(r.FormValue("code"))
		if !ok {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "code invalid or expired")
			return
		}
		if ru := r.FormValue("redirect_uri"); ru != "" && ru != ac.redirectURI {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
			return
		}
		if ac.challenge != "" {
			verifier := r.FormValue("code_verifier")
			sum := sha256.Sum256([]byte(verifier))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != ac.challenge {
				oauthErr(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
				return
			}
		}
		issue()

	case "client_credentials":
		// the typed-in-credentials path: secret is the admin password
		ip := clientIP(r)
		if s.authGate().blocked(ip, time.Now()) {
			oauthErr(w, http.StatusTooManyRequests, "invalid_client", "too many attempts")
			return
		}
		if s.Cfg.AdminPassword == "" || !auth.CheckPassword(s.Cfg.AdminPassword, clientSecret) {
			s.authGate().fail(ip, time.Now())
			oauthErr(w, http.StatusUnauthorized, "invalid_client", "bad client credentials")
			return
		}
		s.authGate().succeed(ip)
		issue()

	case "refresh_token":
		if !s.Sessions.Valid(r.FormValue("refresh_token")) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "refresh token invalid or expired")
			return
		}
		issue()

	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	jsonOut(w, status, map[string]string{"error": code, "error_description": desc})
}
