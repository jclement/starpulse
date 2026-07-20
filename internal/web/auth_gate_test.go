package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every admin and API route must refuse an anonymous caller. This walks the
// route tables themselves rather than a hand-written list, so a new endpoint
// is covered the moment it is added — the failure mode being guarded against
// is someone adding a screen and forgetting the wrapper, which no amount of
// careful reading reliably catches.
func TestEveryAdminRouteIsGated(t *testing.T) {
	srv, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	// a client that does NOT follow redirects, so a 303 to /login is visible
	// as a refusal rather than as the login page's 200
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	routes := append(srv.adminRoutes(), srv.apiRoutes()...)
	if len(routes) < 20 {
		t.Fatalf("only %d routes found — did the tables move?", len(routes))
	}
	for _, rt := range routes {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			req, err := http.NewRequest(method, ts.URL+rt.path, strings.NewReader("path=/index.gmi&content=pwned"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, rt.path, err)
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				t.Errorf("%s %s returned %d to an anonymous caller:\n%s",
					method, rt.path, resp.StatusCode, truncate(string(body), 200))
			}
		}
	}
	// the anonymous requests above must not have changed anything
	if pg, err := st.GetPage("/index.gmi"); err != nil || string(pg.Content) != "# Home" {
		t.Errorf("an anonymous request mutated content: %v %q", err, pg.Content)
	}
	// a bad bearer token is no better than none
	req, _ := http.NewRequest("GET", ts.URL+"/api/pages", nil)
	req.Header.Set("Authorization", "Bearer not-the-password")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad bearer token got %d, want 401", resp.StatusCode)
	}
}

// The guard is applied by iterating the route tables. If a route is ever
// registered directly on the mux instead, it bypasses that — so nothing may
// register an /admin or /api pattern outside those two loops.
func TestNoAdminRouteRegisteredOutsideTheTables(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`mux\.HandleFunc\(\s*"(/admin|/api)`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range direct.FindAllString(string(src), -1) {
			t.Errorf("%s registers %q directly on the mux — add it to adminRoutes/apiRoutes "+
				"instead, so it cannot be registered without the guard", f, m)
		}
	}
}

// The public site must work with JavaScript switched off entirely, and ship
// none: it is served to gemini and terminal clients too, and a capsule that
// needs a script engine is not one.
func TestPublicPagesCarryNoJavaScript(t *testing.T) {
	srv, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home\n\n=> /about A link"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About\n\n```go\nfunc main() {}\n```"), "", "t")
	_, _ = st.SavePage("/posts/.feed", []byte("title: Posts"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-19-hi.gmi", []byte("# Hi"), "", "t")
	srv.Cfg.Feeds.Author = "t"

	for _, path := range []string{
		"/", "/about", "/posts/", "/search?q=home", "/login",
		"/nope-does-not-exist", "/posts/feed.xml", "/_/style.css",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		low := strings.ToLower(string(body))
		for _, bad := range []string{"<script", "javascript:", "onclick=", "onerror=", "onload="} {
			if strings.Contains(low, bad) {
				t.Errorf("%s contains %q:\n%s", path, bad, truncate(string(body), 300))
			}
		}
	}

	// ...and the test is only meaningful if it would notice script when there
	// is some, which the admin legitimately has
	client := login(t, ts, testPassword)
	resp, err := client.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<script") {
		t.Error("admin has no script tag — this test can no longer tell the difference")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
