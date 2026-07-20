package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

const testPassword = "correct-horse"

func testServer(t *testing.T) (*Server, *store.Store, *httptest.Server) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Default()
	cfg.Hostname = "test.example"
	cfg.AdminPassword = testPassword

	sessions, _ := auth.NewSessions("")
	srv := &Server{
		Cfg:      cfg,
		Store:    st,
		Site:     site.New(st),
		Log:      log.New(io.Discard),
		Sessions: sessions,
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, st, ts
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPageServing(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Web Home\n=> /about.gmi About"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About"), "", "t")
	_, _ = st.SavePage("/.theme", []byte("body{--x:1}"), "", "t")
	_, _ = st.SavePage("/file.txt", []byte("hi"), "", "t")

	code, body := get(t, ts, "/")
	if code != 200 || !strings.Contains(body, "<h1>Web Home</h1>") {
		t.Fatalf("home: %d\n%s", code, body)
	}
	// theme injected
	if !strings.Contains(body, "body{--x:1}") {
		t.Error("theme CSS not injected")
	}
	// .gmi link rewritten for web
	if !strings.Contains(body, `href="/about"`) {
		t.Error("gmi link not rewritten")
	}
	// logged out: no edit link
	if strings.Contains(body, "/admin/edit") {
		t.Error("edit link shown while logged out")
	}
	// extensionless page
	if code, body := get(t, ts, "/about"); code != 200 || !strings.Contains(body, "<h1>About</h1>") {
		t.Errorf("about: %d", code)
	}
	// static file
	if code, body := get(t, ts, "/file.txt"); code != 200 || body != "hi" {
		t.Errorf("file: %d %q", code, body)
	}
	// 404
	if code, _ := get(t, ts, "/nope"); code != 404 {
		t.Errorf("missing = %d", code)
	}
	// hidden specials not served
	if code, _ := get(t, ts, "/.theme"); code != 404 {
		t.Errorf("hidden = %d", code)
	}
	// stats bumped under http
	hits, _ := st.Stats()
	for _, h := range hits {
		if h.Proto != "http" {
			t.Errorf("proto = %q", h.Proto)
		}
	}
}

func TestSearchPage(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/x.gmi", []byte("# Xylophones\n\nmusic and mallets"), "", "t")
	code, body := get(t, ts, "/search?q=mallets")
	if code != 200 || !strings.Contains(body, `href="/x"`) {
		t.Errorf("search: %d\n%s", code, body)
	}
}

func TestFeed(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/posts/2026-07-19-hello.gmi", []byte("# Hello World"), "", "t")
	_, _ = st.SavePage("/posts/undated.gmi", []byte("# No date"), "", "t")
	code, body := get(t, ts, "/feed.xml")
	if code != 200 || !strings.Contains(body, "<title>Hello World</title>") {
		t.Errorf("feed: %d\n%s", code, body)
	}
	if strings.Contains(body, "No date") {
		t.Error("undated page in feed")
	}
}

func login(t *testing.T, ts *httptest.Server, password string) *http.Client {
	t.Helper()
	jar := newJar()
	client := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(ts.URL+"/login", url.Values{"password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return client
}

func TestLoginFlow(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	// wrong password: no cookie
	client := login(t, ts, "wrong")
	resp, _ := client.Get(ts.URL + "/admin")
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("admin without session = %d, want redirect", resp.StatusCode)
	}
	resp.Body.Close()

	// right password: session works, edit link appears
	client = login(t, ts, testPassword)
	resp, err := client.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "<h1>Pages</h1>") {
		t.Fatalf("admin: %d", resp.StatusCode)
	}
	resp, _ = client.Get(ts.URL + "/")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "/admin/edit?path=%2findex.gmi") {
		t.Error("edit link missing when logged in")
	}
}

func TestAdminSaveDeleteRestore(t *testing.T) {
	_, st, ts := testServer(t)
	client := login(t, ts, testPassword)

	// create
	resp, err := client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/made.gmi"}, "content": {"# Made\r\nin a form"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	pg, err := st.GetPage("/made.gmi")
	if err != nil || string(pg.Content) != "# Made\nin a form" {
		t.Fatalf("saved page: %v %q", err, pg.Content)
	}

	// edit → snapshots version
	resp, _ = client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/made.gmi"}, "oldpath": {"/made.gmi"}, "content": {"# Made v2"},
	})
	resp.Body.Close()

	// rename moves content, deletes old
	resp, _ = client.PostForm(ts.URL+"/admin/save", url.Values{
		"path": {"/renamed.gmi"}, "oldpath": {"/made.gmi"}, "content": {"# Renamed"},
	})
	resp.Body.Close()
	if _, err := st.GetPage("/made.gmi"); err == nil {
		t.Error("old path survived rename")
	}
	if _, err := st.GetPage("/renamed.gmi"); err != nil {
		t.Error("new path missing after rename")
	}

	// delete
	resp, _ = client.PostForm(ts.URL+"/admin/delete", url.Values{"path": {"/renamed.gmi"}})
	resp.Body.Close()
	if _, err := st.GetPage("/renamed.gmi"); err == nil {
		t.Error("page survived delete")
	}

	// restore from history
	vs, _ := st.ListVersions("/renamed.gmi")
	if len(vs) == 0 {
		t.Fatal("no versions after delete")
	}
	resp, _ = client.PostForm(ts.URL+"/admin/restore", url.Values{"id": {fmt.Sprint(vs[0].ID)}})
	resp.Body.Close()
	if _, err := st.GetPage("/renamed.gmi"); err != nil {
		t.Error("restore failed")
	}
}

func TestEditorPage(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/page.gmi", []byte("# Editable\n{{count}}"), "", "t")
	client := login(t, ts, testPassword)

	resp, err := client.Get(ts.URL + "/admin/edit?path=/page.gmi")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)
	for _, want := range []string{`id="ed"`, `id="content"`, `id="pv-toggle"`, "{{count}}", `class="editor-body"`} {
		if !strings.Contains(body, want) {
			t.Errorf("editor missing %s", want)
		}
	}
	// raw source must be escaped, not rendered
	if strings.Contains(body, "<h1>Editable</h1>") {
		t.Error("editor rendered content instead of source")
	}

	// new-page editor
	resp, _ = client.Get(ts.URL + "/admin/edit?path=&new=1")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `id="ed"`) || strings.Contains(string(b), `name="oldpath"`) {
		t.Error("new-page editor wrong")
	}
}

func TestAPIAuthAndCRUD(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	// no auth
	if code, _ := get(t, ts, "/api/pages"); code != 401 {
		t.Fatalf("unauthed = %d", code)
	}

	do := func(method, path, body, ctype string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testPassword)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// wrong bearer
	req, _ := http.NewRequest("GET", ts.URL+"/api/pages", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Errorf("bad bearer = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// list
	if code, body := do("GET", "/api/pages", "", ""); code != 200 || !strings.Contains(body, "/index.gmi") {
		t.Errorf("list: %d %s", code, body)
	}
	// put
	if code, _ := do("PUT", "/api/pages/api-made.gmi", "# Via API", "text/gemini"); code != 200 {
		t.Errorf("put failed")
	}
	// get
	code, body := do("GET", "/api/pages/api-made.gmi", "", "")
	if code != 200 || !strings.Contains(body, "# Via API") {
		t.Errorf("get: %d %s", code, body)
	}
	// search
	if code, body := do("GET", "/api/search?q=API", "", ""); code != 200 || !strings.Contains(body, "api-made") {
		t.Errorf("search: %d %s", code, body)
	}
	// now
	if code, _ := do("POST", "/api/now", `{"content":"api now post"}`, "application/json"); code != 200 {
		t.Errorf("now post failed")
	}
	if code, body := do("GET", "/api/now", "", ""); code != 200 || !strings.Contains(body, "api now post") {
		t.Errorf("now list: %d %s", code, body)
	}
	// versions + restore
	do("PUT", "/api/pages/api-made.gmi", "# Via API v2", "text/gemini")
	code, body = do("GET", "/api/versions?path=/api-made.gmi", "", "")
	if code != 200 || !strings.Contains(body, `"id"`) {
		t.Fatalf("versions: %d %s", code, body)
	}
	var vr struct {
		Versions []struct {
			ID int64 `json:"id"`
		} `json:"versions"`
	}
	_ = json.Unmarshal([]byte(body), &vr)
	if len(vr.Versions) == 0 {
		t.Fatal("no versions")
	}
	if code, _ := do("POST", fmt.Sprintf("/api/restore?id=%d", vr.Versions[0].ID), "", ""); code != 200 {
		t.Errorf("restore failed")
	}
	code, body = do("GET", "/api/pages/api-made.gmi", "", "")
	if !strings.Contains(body, "# Via API\n") && !strings.Contains(body, `"content":"# Via API"`) {
		t.Errorf("restored content: %s", body)
	}
	// delete
	if code, _ := do("DELETE", "/api/pages/api-made.gmi", "", ""); code != 200 {
		t.Errorf("delete failed")
	}
	// stats
	if code, _ := do("GET", "/api/stats", "", ""); code != 200 {
		t.Errorf("stats failed")
	}
	// oversized body rejected
	srvCfgTest := strings.Repeat("x", 11<<20)
	if code, _ := do("PUT", "/api/pages/big.gmi", srvCfgTest, "text/gemini"); code != 413 {
		t.Errorf("oversize accepted: %d", code)
	}
}

func TestMCP(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	rpc := func(id, method, params string) (int, string) {
		t.Helper()
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%q,"params":%s}`, id, method, params)
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testPassword)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// unauthorized
	resp, _ := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if resp.StatusCode != 401 {
		t.Fatalf("unauthed mcp = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// initialize
	code, body := rpc("1", "initialize", `{}`)
	if code != 200 || !strings.Contains(body, "starpulse") || !strings.Contains(body, "protocolVersion") {
		t.Fatalf("initialize: %d %s", code, body)
	}
	// tools/list
	code, body = rpc("2", "tools/list", `{}`)
	if code != 200 || !strings.Contains(body, "write_page") || !strings.Contains(body, "post_now") {
		t.Fatalf("tools/list: %d %s", code, body)
	}
	// write + read roundtrip
	code, body = rpc("3", "tools/call", `{"name":"write_page","arguments":{"path":"/mcp.gmi","content":"# From MCP"}}`)
	if code != 200 || !strings.Contains(body, "saved /mcp.gmi") {
		t.Fatalf("write_page: %d %s", code, body)
	}
	code, body = rpc("4", "tools/call", `{"name":"read_page","arguments":{"path":"/mcp.gmi"}}`)
	if code != 200 || !strings.Contains(body, "# From MCP") {
		t.Fatalf("read_page: %d %s", code, body)
	}
	// search tool
	code, body = rpc("5", "tools/call", `{"name":"search","arguments":{"query":"MCP"}}`)
	if code != 200 || !strings.Contains(body, "/mcp.gmi") {
		t.Fatalf("search: %d %s", code, body)
	}
	// unknown tool is an in-band error
	code, body = rpc("6", "tools/call", `{"name":"explode","arguments":{}}`)
	if code != 200 || !strings.Contains(body, "unknown tool") {
		t.Fatalf("unknown tool: %d %s", code, body)
	}
	// unknown method is a JSON-RPC error
	code, body = rpc("7", "nope", `{}`)
	if code != 200 || !strings.Contains(body, "-32601") {
		t.Fatalf("unknown method: %d %s", code, body)
	}
}

func TestPreviewEndpoint(t *testing.T) {
	_, _, ts := testServer(t)
	req, _ := http.NewRequest("POST", ts.URL+"/api/preview", strings.NewReader("# Preview me"))
	req.Header.Set("Authorization", "Bearer "+testPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "<h1>Preview me</h1>") {
		t.Errorf("preview: %s", b)
	}
}

// minimal cookie jar (no external deps)
type jarT struct{ cookies []*http.Cookie }

func newJar() *jarT { return &jarT{} }

func (j *jarT) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.cookies = append(j.cookies, cookies...)
}

func (j *jarT) Cookies(u *url.URL) []*http.Cookie { return j.cookies }
