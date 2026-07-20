package gemini

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

func makeCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{cn, "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type testServer struct {
	addr   string
	cfg    *config.Config
	st     *store.Store
	client tls.Certificate // authorized titan client cert
}

func startServer(t *testing.T) *testServer {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	clientCert := makeCert(t, "editor")
	fp := auth.Fingerprint(sha256.Sum256(clientCert.Certificate[0]))

	cfg := config.Default()
	cfg.Hostname = "localhost"
	cfg.Gemini.Addr = "127.0.0.1:0"
	cfg.Titan.Enabled = true
	cfg.Titan.CertFingerprints = []string{fp}

	srv := &Server{
		Cfg:   cfg,
		Store: st,
		Site:  site.New(st),
		Log:   log.New(io.Discard),
		TLS: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{makeCert(t, "localhost")},
			ClientAuth:   tls.RequestClientCert,
		},
	}
	ln, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return &testServer{addr: ln.Addr().String(), cfg: cfg, st: st, client: clientCert}
}

// request performs one gemini/titan exchange, returning the full response.
func (ts *testServer) request(t *testing.T, reqLine string, clientCert *tls.Certificate, body []byte) string {
	t.Helper()
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	if clientCert != nil {
		tlsCfg.Certificates = []tls.Certificate{*clientCert}
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ts.addr, tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "%s\r\n", reqLine)
	if body != nil {
		_, _ = conn.Write(body)
	}
	resp, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return string(resp)
}

func TestServePage(t *testing.T) {
	ts := startServer(t)
	_, _ = ts.st.SavePage("/index.gmi", []byte("# Hello Gemini"), "", "t")
	_, _ = ts.st.SavePage("/.footer", []byte("views: {{count}}"), "", "t")

	resp := ts.request(t, "gemini://localhost/", nil, nil)
	if !strings.HasPrefix(resp, "20 text/gemini") {
		t.Fatalf("resp = %q", resp)
	}
	if !strings.Contains(resp, "# Hello Gemini") || !strings.Contains(resp, "views: 1") {
		t.Errorf("body wrong:\n%s", resp)
	}

	// stats recorded under gemini proto
	hits, _ := ts.st.Stats()
	if len(hits) != 1 || hits[0].Proto != "gemini" {
		t.Errorf("stats = %+v", hits)
	}
}

func TestNotFoundAndProxyRefusal(t *testing.T) {
	ts := startServer(t)
	if resp := ts.request(t, "gemini://localhost/nope", nil, nil); !strings.HasPrefix(resp, "51 ") {
		t.Errorf("missing page: %q", resp)
	}
	if resp := ts.request(t, "gemini://evil.example/", nil, nil); !strings.HasPrefix(resp, "53 ") {
		t.Errorf("proxy request: %q", resp)
	}
	if resp := ts.request(t, "https://localhost/", nil, nil); !strings.HasPrefix(resp, "53 ") {
		t.Errorf("wrong scheme: %q", resp)
	}
}

func TestSearchGemini(t *testing.T) {
	ts := startServer(t)
	_, _ = ts.st.SavePage("/notes.gmi", []byte("# Notes\n\nquantum unicycles"), "", "t")

	if resp := ts.request(t, "gemini://localhost/search", nil, nil); !strings.HasPrefix(resp, "10 ") {
		t.Errorf("search prompt: %q", resp)
	}
	resp := ts.request(t, "gemini://localhost/search?quantum", nil, nil)
	if !strings.Contains(resp, "=> /notes Notes") {
		t.Errorf("search results:\n%s", resp)
	}
}

func TestFeedOverGemini(t *testing.T) {
	ts := startServer(t)
	_, _ = ts.st.SavePage("/posts/2026-07-19-hi.gmi", []byte("# Hi There\n\nbody"), "", "t")
	// dated pages alone publish nothing — the folder must be turned on
	if resp := ts.request(t, "gemini://localhost/posts/feed.xml", nil, nil); !strings.HasPrefix(resp, "51 ") {
		t.Fatalf("unmarked folder served a feed: %q", resp)
	}
	_, _ = ts.st.SavePage("/posts/"+store.FeedMarker, []byte("title: Posts\n"), "", "t")
	resp := ts.request(t, "gemini://localhost/posts/feed.xml", nil, nil)
	if !strings.HasPrefix(resp, "20 application/atom+xml") {
		t.Fatalf("feed status/mime: %q", resp[:min(60, len(resp))])
	}
	if !strings.Contains(resp, "<title>Hi There</title>") {
		t.Errorf("feed body:\n%s", resp)
	}
	if !strings.Contains(resp, "gemini://localhost/posts/2026-07-19-hi") {
		t.Error("gemini feed should use gemini:// URLs")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestTitanUploadAuth(t *testing.T) {
	ts := startServer(t)
	body := []byte("# Uploaded\n\nvia titan")
	req := fmt.Sprintf("titan://localhost/up.gmi;mime=text/gemini;size=%d", len(body))

	// no cert → 60
	if resp := ts.request(t, req, nil, body); !strings.HasPrefix(resp, "60 ") {
		t.Errorf("no cert: %q", resp)
	}
	// wrong cert → 61
	bad := makeCert(t, "intruder")
	if resp := ts.request(t, req, &bad, body); !strings.HasPrefix(resp, "61 ") {
		t.Errorf("bad cert: %q", resp)
	}
	// authorized cert → 30 redirect to the page
	resp := ts.request(t, req, &ts.client, body)
	if !strings.HasPrefix(resp, "30 gemini://localhost/up") {
		t.Fatalf("upload: %q", resp)
	}
	pg, err := ts.st.GetPage("/up.gmi")
	if err != nil || string(pg.Content) != string(body) {
		t.Errorf("stored content: err=%v page=%+v", err, pg)
	}

	// second upload snapshots a version
	body2 := []byte("# Uploaded v2")
	req2 := fmt.Sprintf("titan://localhost/up.gmi;mime=text/gemini;size=%d", len(body2))
	_ = ts.request(t, req2, &ts.client, body2)
	vs, _ := ts.st.ListVersions("/up.gmi")
	if len(vs) != 1 {
		t.Errorf("versions = %d, want 1", len(vs))
	}

	// zero size deletes
	if resp := ts.request(t, "titan://localhost/up.gmi;size=0", &ts.client, nil); !strings.HasPrefix(resp, "20 ") {
		t.Errorf("delete: %q", resp)
	}
	if _, err := ts.st.GetPage("/up.gmi"); err == nil {
		t.Error("page still exists after titan delete")
	}
}

func TestTitanEditsIndexAndKeepsGemtext(t *testing.T) {
	ts := startServer(t)
	_, _ = ts.st.SavePage("/index.gmi", []byte("# Old Home"), "", "seed")

	// editing the ROOT (path "/") must target /index.gmi, and Lagrange's
	// text/plain mime must not downgrade the gemtext page
	body := []byte("# New Home\n\nvia titan at root")
	req := fmt.Sprintf("titan://localhost/;mime=text/plain;size=%d", len(body))
	resp := ts.request(t, req, &ts.client, body)
	if !strings.HasPrefix(resp, "30 ") {
		t.Fatalf("root titan upload: %q", resp)
	}
	pg, err := ts.st.GetPage("/index.gmi")
	if err != nil || string(pg.Content) != string(body) {
		t.Fatalf("index not updated: err=%v content=%q", err, pg.Content)
	}
	if !strings.HasPrefix(pg.Mime, "text/gemini") {
		t.Errorf("gemtext page stored as %q (text/plain leaked through)", pg.Mime)
	}

	// a subfolder directory target maps to its index too
	body2 := []byte("# Posts Index")
	req2 := fmt.Sprintf("titan://localhost/posts/;mime=text/plain;size=%d", len(body2))
	if resp := ts.request(t, req2, &ts.client, body2); !strings.HasPrefix(resp, "30 ") {
		t.Fatalf("dir titan upload: %q", resp)
	}
	if pg, err := ts.st.GetPage("/posts/index.gmi"); err != nil || string(pg.Content) != string(body2) {
		t.Errorf("posts index not created: %v", err)
	}

	// an extensionless page target becomes .gmi
	body3 := []byte("# Fresh")
	req3 := fmt.Sprintf("titan://localhost/fresh;mime=text/plain;size=%d", len(body3))
	if resp := ts.request(t, req3, &ts.client, body3); !strings.HasPrefix(resp, "30 ") {
		t.Fatalf("extensionless titan upload: %q", resp)
	}
	if _, err := ts.st.GetPage("/fresh.gmi"); err != nil {
		t.Error("extensionless page not created as .gmi")
	}

	// a real image keeps its binary mime
	png := []byte("\x89PNG\r\n")
	req4 := fmt.Sprintf("titan://localhost/media/x.png;mime=text/plain;size=%d", len(png))
	if resp := ts.request(t, req4, &ts.client, png); !strings.HasPrefix(resp, "30 ") {
		t.Fatalf("image titan upload: %q", resp)
	}
	if pg, _ := ts.st.GetPage("/media/x.png"); pg == nil || pg.Mime != "image/png" {
		t.Errorf("png mime wrong: %v", pg)
	}
}

func TestEditorSeesRawSourceRoundTrip(t *testing.T) {
	ts := startServer(t)
	// a page with inherited chrome and directives — the classic double-include trap
	_, _ = ts.st.SavePage("/.header", []byte("=> / HOME"), "", "seed")
	_, _ = ts.st.SavePage("/.footer", []byte("viewed {{count}} times"), "", "seed")
	src := "# Real Source\n\n{{now 3}}\n\nbody text"
	_, _ = ts.st.SavePage("/index.gmi", []byte(src), "", "seed")

	// a normal reader (no cert) gets the ASSEMBLED page
	plain := ts.request(t, "gemini://localhost/", nil, nil)
	if !strings.Contains(plain, "HOME") || !strings.Contains(plain, "viewed ") {
		t.Errorf("reader should see assembled page:\n%s", plain)
	}
	if strings.Contains(plain, "{{now 3}}") {
		t.Errorf("reader saw an unexpanded directive:\n%s", plain)
	}

	// an EDITOR (authorized cert) gets the raw stored source, verbatim
	raw := ts.request(t, "gemini://localhost/", &ts.client, nil)
	body := raw[strings.Index(raw, "\r\n")+2:]
	if body != src {
		t.Fatalf("editor should get raw source.\n got: %q\nwant: %q", body, src)
	}

	// round-trip: upload exactly what the editor was shown, unchanged
	req := fmt.Sprintf("titan://localhost/;mime=text/plain;size=%d", len(body))
	if resp := ts.request(t, req, &ts.client, []byte(body)); !strings.HasPrefix(resp, "30 ") {
		t.Fatalf("round-trip upload: %q", resp)
	}
	pg, _ := ts.st.GetPage("/index.gmi")
	if string(pg.Content) != src {
		t.Errorf("round-trip corrupted the page:\n got: %q\nwant: %q", pg.Content, src)
	}
	// specifically: chrome must not have been frozen into the body
	if strings.Contains(string(pg.Content), "HOME") || strings.Contains(string(pg.Content), "viewed ") {
		t.Error("header/footer got frozen into the page body")
	}

	// a reader still sees assembled chrome afterwards
	after := ts.request(t, "gemini://localhost/", nil, nil)
	if !strings.Contains(after, "HOME") {
		t.Error("chrome lost after round-trip")
	}
}

func TestTitanLimitsAndBadPaths(t *testing.T) {
	ts := startServer(t)
	huge := ts.cfg.MaxUploadBytes + 1
	req := fmt.Sprintf("titan://localhost/x.gmi;mime=text/gemini;size=%d", huge)
	if resp := ts.request(t, req, &ts.client, nil); !strings.HasPrefix(resp, "59 ") {
		t.Errorf("oversize: %q", resp)
	}
	if resp := ts.request(t, "titan://localhost/../x;size=3", &ts.client, []byte("abc")); !strings.HasPrefix(resp, "59 ") {
		t.Errorf("traversal: %q", resp)
	}
}

func TestRawSourceRequiresCert(t *testing.T) {
	ts := startServer(t)
	_, _ = ts.st.SavePage("/page.gmi", []byte("# Src\n{{count}}"), "", "t")

	if resp := ts.request(t, "gemini://localhost/raw/page.gmi", nil, nil); !strings.HasPrefix(resp, "60 ") {
		t.Errorf("raw without cert: %q", resp)
	}
	resp := ts.request(t, "gemini://localhost/raw/page.gmi", &ts.client, nil)
	if !strings.Contains(resp, "{{count}}") {
		t.Errorf("raw source not verbatim:\n%s", resp)
	}
}

func TestTitanDisabled(t *testing.T) {
	ts := startServer(t)
	ts.cfg.Titan.Enabled = false
	resp := ts.request(t, "titan://localhost/x.gmi;size=3", &ts.client, []byte("abc"))
	if !strings.HasPrefix(resp, "53 ") {
		t.Errorf("titan disabled: %q", resp)
	}
}

// Posting a note over titan: upload to the stream folder itself and each
// upload becomes a new dated entry instead of overwriting an index.
func TestTitanPostsNotesToStreamFolder(t *testing.T) {
	ts := startServer(t)
	marker := store.DefaultFeedMarker("Now", "Jeff", 30)
	_, _ = ts.st.SavePage("/now/"+store.FeedMarker, marker, "", "seed")

	post := func(body string) string {
		req := fmt.Sprintf("titan://localhost/now/;mime=text/plain;size=%d", len(body))
		resp := ts.request(t, req, &ts.client, []byte(body))
		if !strings.HasPrefix(resp, "30 ") {
			t.Fatalf("note upload: %q", resp)
		}
		return resp
	}
	post("a first note")
	post("a second note")

	pages := ts.st.StreamPages("/now/", 0)
	if len(pages) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(pages))
	}
	// neither overwrote an index page
	if _, err := ts.st.GetPage("/now/index.gmi"); err == nil {
		t.Error("note upload clobbered the folder index")
	}
	bodies := string(pages[0].Content) + string(pages[1].Content)
	for _, want := range []string{"a first note", "a second note"} {
		if !strings.Contains(bodies, want) {
			t.Errorf("note %q not stored", want)
		}
	}

	// an ordinary folder still edits its index
	_, _ = ts.st.SavePage("/posts/2026-01-01-x.gmi", []byte("# X"), "", "seed")
	body := "# Posts Index"
	req := fmt.Sprintf("titan://localhost/posts/;mime=text/plain;size=%d", len(body))
	if resp := ts.request(t, req, &ts.client, []byte(body)); !strings.HasPrefix(resp, "30 ") {
		t.Fatalf("index upload: %q", resp)
	}
	if pg, err := ts.st.GetPage("/posts/index.gmi"); err != nil || string(pg.Content) != body {
		t.Error("ordinary folder upload should edit its index")
	}
}

// An empty or blank-only allowlist must authorize nobody. The dangerous
// shape here is a loop over an empty list that "matches" by falling through
// to a permissive default.
func TestEmptyFingerprintAllowlistAuthorizesNobody(t *testing.T) {
	for _, list := range [][]string{nil, {}, {""}, {"  "}, {"::"}} {
		cfg := &config.Config{Titan: config.Titan{Enabled: true, CertFingerprints: list}}
		if fps := cfg.NormalizedFingerprints(); len(fps) != 0 {
			t.Errorf("allowlist %q normalised to %q, want empty", list, fps)
		}
	}
	// a configured fingerprint still matches regardless of case and colons
	cfg := &config.Config{Titan: config.Titan{Enabled: true, CertFingerprints: []string{"AB:CD:ef"}}}
	if got := cfg.NormalizedFingerprints(); len(got) != 1 || got[0] != "abcdef" {
		t.Errorf("normalised = %q", got)
	}
}
