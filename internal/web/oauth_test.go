package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func jsonGet(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOAuthDiscovery(t *testing.T) {
	_, _, ts := testServer(t)

	pr := jsonGet(t, ts.URL+"/.well-known/oauth-protected-resource")
	if pr["resource"] == nil || !strings.HasSuffix(pr["resource"].(string), "/mcp") {
		t.Errorf("protected-resource metadata wrong: %v", pr)
	}
	if _, ok := pr["authorization_servers"].([]any); !ok {
		t.Errorf("no authorization_servers: %v", pr)
	}

	as := jsonGet(t, ts.URL+"/.well-known/oauth-authorization-server")
	for _, k := range []string{"issuer", "authorization_endpoint", "token_endpoint", "registration_endpoint"} {
		if as[k] == nil {
			t.Errorf("AS metadata missing %s", k)
		}
	}
	grants, _ := as["grant_types_supported"].([]any)
	var have []string
	for _, g := range grants {
		have = append(have, g.(string))
	}
	for _, want := range []string{"authorization_code", "client_credentials"} {
		found := false
		for _, g := range have {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("grant %q not advertised (have %v)", want, have)
		}
	}
}

// an unauthenticated /mcp must point clients at the discovery document
func TestMCPUnauthorizedAdvertisesDiscovery(t *testing.T) {
	_, _, ts := testServer(t)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	wa := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(wa, "resource_metadata=") || !strings.Contains(wa, "/.well-known/oauth-protected-resource") {
		t.Errorf("WWW-Authenticate lacks discovery hint: %q", wa)
	}
}

// the typed-in-credentials path Claude Desktop uses: client_id=mcp with the
// admin password as the client secret
func TestOAuthClientCredentials(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"mcp"},
		"client_secret": {testPassword},
	}
	resp, err := http.PostForm(ts.URL+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("token = %d: %s", resp.StatusCode, b)
	}
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	access, _ := tok["access_token"].(string)
	if access == "" || tok["token_type"] != "Bearer" {
		t.Fatalf("bad token response: %v", tok)
	}

	// the issued token must actually work on /mcp
	req, _ := http.NewRequest("POST", ts.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	b, _ := io.ReadAll(r2.Body)
	if r2.StatusCode != 200 || !strings.Contains(string(b), "write_page") {
		t.Fatalf("mcp with oauth token: %d %s", r2.StatusCode, b)
	}

	// wrong secret is refused
	bad := url.Values{"grant_type": {"client_credentials"}, "client_id": {"mcp"}, "client_secret": {"nope"}}
	r3, _ := http.PostForm(ts.URL+"/oauth/token", bad)
	if r3.StatusCode == 200 {
		t.Error("wrong client secret accepted")
	}
	r3.Body.Close()

	// unknown client_id is refused
	r4, _ := http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {"someone-else"}, "client_secret": {testPassword}})
	if r4.StatusCode == 200 {
		t.Error("unknown client_id accepted")
	}
	r4.Body.Close()
}

// full authorization-code + PKCE flow
func TestOAuthAuthorizationCodePKCE(t *testing.T) {
	srv, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	// the operator has decided to trust this callback host
	srv.Cfg.OAuthRedirectHosts = []string{"claude.ai"}

	verifier := "a-test-verifier-value-that-is-long-enough-1234"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "https://claude.ai/api/mcp/auth_callback"

	authURL := ts.URL + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {"mcp"},
		"redirect_uri":          {redirect},
		"state":                 {"xyz"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()

	// the consent page renders
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Authorize access") {
		t.Fatalf("no consent page: %s", b)
	}

	// posting the admin password redirects back with a code
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp2, err := noRedirect.PostForm(authURL, url.Values{"password": {testPassword}})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound {
		t.Fatalf("authorize POST = %d", resp2.StatusCode)
	}
	loc, _ := url.Parse(resp2.Header.Get("Location"))
	code := loc.Query().Get("code")
	if code == "" || loc.Query().Get("state") != "xyz" {
		t.Fatalf("bad redirect: %s", resp2.Header.Get("Location"))
	}

	// wrong verifier fails PKCE
	badTok, _ := http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirect}, "code_verifier": {"wrong"}, "client_id": {"mcp"}})
	if badTok.StatusCode == 200 {
		t.Error("PKCE verification bypassed")
	}
	badTok.Body.Close()

	// that consumed the code — get a fresh one and redeem it correctly
	resp3, _ := noRedirect.PostForm(authURL, url.Values{"password": {testPassword}})
	resp3.Body.Close()
	loc3, _ := url.Parse(resp3.Header.Get("Location"))
	code3 := loc3.Query().Get("code")

	tokResp, err := http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code3},
		"redirect_uri": {redirect}, "code_verifier": {verifier}, "client_id": {"mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tokResp.Body.Close()
	if tokResp.StatusCode != 200 {
		bb, _ := io.ReadAll(tokResp.Body)
		t.Fatalf("token exchange = %d: %s", tokResp.StatusCode, bb)
	}
	var tok map[string]any
	_ = json.NewDecoder(tokResp.Body).Decode(&tok)
	access, _ := tok["access_token"].(string)
	if access == "" {
		t.Fatal("no access token")
	}

	// token works, and codes are single-use
	req, _ := http.NewRequest("GET", ts.URL+"/api/pages", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 200 {
		t.Errorf("oauth token rejected by API: %d", r.StatusCode)
	}
	r.Body.Close()

	reuse, _ := http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code3},
		"redirect_uri": {redirect}, "code_verifier": {verifier}})
	if reuse.StatusCode == 200 {
		t.Error("authorization code was reusable")
	}
	reuse.Body.Close()
}

func TestOAuthWrongPasswordAndBadRedirect(t *testing.T) {
	_, _, ts := testServer(t)

	// an unacceptable redirect_uri is refused outright
	resp, _ := http.Get(ts.URL + "/oauth/authorize?redirect_uri=" + url.QueryEscape("javascript:alert(1)"))
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Invalid request") {
		t.Error("javascript: redirect_uri not rejected")
	}

	// wrong password does not mint a code (PKCE is required to get this far)
	authURL := ts.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {"mcp"},
		"redirect_uri":          {"http://localhost:9999/cb"},
		"code_challenge":        {"Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyMDA"},
		"code_challenge_method": {"S256"},
	}.Encode()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	r2, _ := noRedirect.PostForm(authURL, url.Values{"password": {"wrong"}})
	bb, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode == http.StatusFound {
		t.Error("wrong password still redirected with a code")
	}
	if !strings.Contains(string(bb), "Wrong password") {
		t.Error("no error shown for wrong password")
	}
}

func TestOAuthRefreshToken(t *testing.T) {
	_, _, ts := testServer(t)
	resp, _ := http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {"mcp"}, "client_secret": {testPassword}})
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	refresh, _ := tok["refresh_token"].(string)
	if refresh == "" {
		t.Fatal("no refresh token issued")
	}
	r2, err := http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatalf("refresh = %d", r2.StatusCode)
	}
	var tok2 map[string]any
	_ = json.NewDecoder(r2.Body).Decode(&tok2)
	if tok2["access_token"] == "" {
		t.Error("refresh produced no access token")
	}
	// a garbage refresh token is refused
	r3, _ := http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {"garbage"}})
	if r3.StatusCode == 200 {
		t.Error("garbage refresh token accepted")
	}
	r3.Body.Close()
}

// The authorization code is a credential, and redirect_uri decides where it
// is delivered. Dynamic registration records no callback URL, so accepting
// any host let an attacker send the admin a link to this very site — genuine
// consent page, right domain — and collect the code themselves.
func TestAuthorizeRefusesUnapprovedRedirectHosts(t *testing.T) {
	srv, _, ts := testServer(t)
	srv.Cfg.OAuthRedirectHosts = []string{"claude.ai"}
	srv.Cfg.Hostname = "test.example"

	ask := func(redirect string) int {
		u := ts.URL + "/oauth/authorize?" + url.Values{
			"response_type": {"code"}, "client_id": {"mcp"},
			"redirect_uri":          {redirect},
			"code_challenge":        {"Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyMDA"},
			"code_challenge_method": {"S256"},
		}.Encode()
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	for _, bad := range []string{
		"https://evil.example/cb",         // the attack
		"https://claude.ai.evil.example/", // suffix trickery
		"https://evil.example/?x=claude.ai",
		"http://evil.example/cb", // cleartext, non-loopback
		"myapp://callback",       // unattributable custom scheme
		"javascript:alert(1)",    //
		"//evil.example/cb",      // scheme-relative
		"https://",               // no host
	} {
		if code := ask(bad); code != http.StatusBadRequest {
			t.Errorf("redirect_uri %q was accepted (%d)", bad, code)
		}
	}
	for _, ok := range []string{
		"https://claude.ai/api/mcp/auth_callback", // named by the operator
		"https://test.example/cb",                 // this site
		"http://127.0.0.1:8976/callback",          // a desktop client
		"http://localhost:1455/oauth",
	} {
		if code := ask(ok); code != http.StatusOK {
			t.Errorf("redirect_uri %q was refused (%d)", ok, code)
		}
	}
}

// PKCE is mandatory: a code with no verifier bound to it is usable by
// whoever ends up holding it.
func TestAuthorizeRequiresPKCE(t *testing.T) {
	srv, _, ts := testServer(t)
	srv.Cfg.Hostname = "test.example"
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, q := range []url.Values{
		{"response_type": {"code"}, "client_id": {"mcp"}, "redirect_uri": {"https://test.example/cb"}},
		{"response_type": {"code"}, "client_id": {"mcp"}, "redirect_uri": {"https://test.example/cb"},
			"code_challenge": {"plain-value"}, "code_challenge_method": {"plain"}},
	} {
		resp, err := noRedirect.Get(ts.URL + "/oauth/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("consent page shown without S256 PKCE: %v", q)
		}
	}
}
