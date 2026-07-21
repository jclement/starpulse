package sshui

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/x/ansi"
	gossh "golang.org/x/crypto/ssh"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

const adminPW = "ssh-test-pw"

func startServer(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, _ = st.SavePage("/index.gmi", []byte("# SSH Home\n\n=> /about About page\n\nwelcome text"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About Me"), "", "t")

	cfg := config.Default()
	cfg.Hostname = "test.example"
	cfg.AdminPassword = adminPW
	cfg.SSH = config.SSHService{Service: config.Service{Enabled: true, Addr: "127.0.0.1:0"}}
	cfg.DataDir = t.TempDir()

	srv, err := New(cfg, st, site.New(st), log.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	// bind manually so we learn the port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.wish.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, st, ln.Addr().String()
}

func dial(t *testing.T, addr, user, password string) (*gossh.Client, error) {
	t.Helper()
	return gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{gossh.Password(password)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[>=<]`)

// tuiSession opens a PTY shell and gives helpers to interact with the TUI.
type tuiSession struct {
	t      *testing.T
	sess   *gossh.Session
	stdin  io.WriteCloser
	mu     sync.Mutex
	seen   []byte
	closed bool
}

func openTUI(t *testing.T, client *gossh.Client) *tuiSession {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	if err := sess.RequestPty("xterm-256color", 24, 100, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	ts := &tuiSession{t: t, sess: sess, stdin: stdin}
	// lossless collector: append every byte to an unbounded buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				ts.mu.Lock()
				ts.seen = append(ts.seen, buf[:n]...)
				ts.mu.Unlock()
			}
			if err != nil {
				ts.mu.Lock()
				ts.closed = true
				ts.mu.Unlock()
				return
			}
		}
	}()
	t.Cleanup(func() { sess.Close() })
	return ts
}

func (ts *tuiSession) text() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ansiRe.ReplaceAllString(string(ts.seen), "")
}

// expect waits until the (ANSI-stripped) output stream contains substr.
func (ts *tuiSession) expect(substr string) {
	ts.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if strings.Contains(ts.text(), substr) {
			return
		}
		ts.mu.Lock()
		done := ts.closed
		ts.mu.Unlock()
		if done && !strings.Contains(ts.text(), substr) {
			ts.t.Fatalf("stream closed waiting for %q; saw:\n%s", substr, tail(ts.text(), 2000))
		}
		if time.Now().After(deadline) {
			ts.t.Fatalf("timeout waiting for %q; saw:\n%s", substr, tail(ts.text(), 2000))
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func (ts *tuiSession) send(s string) {
	ts.t.Helper()
	if _, err := io.WriteString(ts.stdin, s); err != nil {
		ts.t.Fatal(err)
	}
}

func TestAuth(t *testing.T) {
	_, _, addr := startServer(t)

	// guest: any user/password gets in
	c, err := dial(t, addr, "guest", "")
	if err != nil {
		t.Fatalf("guest auth failed: %v", err)
	}
	c.Close()

	// admin with wrong password rejected
	if _, err := dial(t, addr, "admin", "nope"); err == nil {
		t.Fatal("admin with wrong password accepted")
	}

	// admin with right password accepted
	c, err = dial(t, addr, "admin", adminPW)
	if err != nil {
		t.Fatalf("admin auth failed: %v", err)
	}
	c.Close()
}

func TestGuestBrowse(t *testing.T) {
	_, st, addr := startServer(t)
	c, err := dial(t, addr, "guest", "x")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ts := openTUI(t, c)

	// branding header + home page render
	ts.expect("test.example")
	ts.expect("SSH Home")
	ts.expect("[1] About page")

	// follow the first link
	ts.send("\t") // select link
	ts.send("\r") // open
	ts.expect("About Me")

	// search
	ts.send("/")
	ts.expect("search: ")
	ts.send("welcome\r")
	ts.expect("Search: welcome")
	ts.expect("SSH Home") // hit title in results

	// quit cleanly
	ts.send("q")

	// stats recorded under ssh proto
	time.Sleep(200 * time.Millisecond)
	hits, _ := st.Stats()
	found := false
	for _, h := range hits {
		if h.Proto == "ssh" {
			found = true
		}
		if h.Proto != "ssh" {
			t.Errorf("unexpected proto %q", h.Proto)
		}
	}
	if !found {
		t.Error("no ssh stats recorded")
	}
}

func TestAdminEditAndNow(t *testing.T) {
	_, st, addr := startServer(t)
	c, err := dial(t, addr, "admin", adminPW)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ts := openTUI(t, c)

	ts.expect("SSH Home")
	ts.expect("admin") // header shows role

	// edit the current page
	ts.send("e")
	ts.expect("editing /index.gmi")
	ts.send(" EDITED-VIA-SSH")
	ts.send("\x13") // ctrl+s
	ts.expect("saved /index.gmi")

	pg, err := st.GetPage("/index.gmi")
	if err != nil || !strings.Contains(string(pg.Content), "EDITED-VIA-SSH") {
		t.Fatalf("ssh edit not persisted: %v %q", err, pg.Content)
	}
	vs, _ := st.ListVersions("/index.gmi")
	if len(vs) != 1 {
		t.Errorf("versions = %d, want 1", len(vs))
	}

	// leave editor, create a new page
	ts.send("\x11") // ctrl+q
	ts.expect("SSH Home")
	ts.send("c")
	ts.expect("new page path")
	ts.send("/ssh-made\r")
	ts.expect("editing /ssh-made.gmi")
	ts.send("# Made over SSH")
	ts.send("\x13")
	ts.expect("saved /ssh-made.gmi")
	if _, err := st.GetPage("/ssh-made.gmi"); err != nil {
		t.Error("created page missing")
	}

	// now post
	ts.send("\x11")
	ts.send("n")
	ts.expect("new now post")
	ts.send("posted from the terminal")
	ts.send("\x13")
	ts.expect("note published")
	// a note is just a page in the notes folder, named for you
	notes := st.StreamPages("/now/", 0)
	if len(notes) != 1 || !strings.Contains(string(notes[0].Content), "posted from the terminal") {
		t.Errorf("notes = %+v", notes)
	}
	if st.IsFeedFolder("/now/") {
		t.Error("posting a note must not switch a feed on by itself")
	}

	ts.send("q")
}

func TestHelp(t *testing.T) {
	_, st, addr := startServer(t)
	c, err := dial(t, addr, "admin", adminPW)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ts := openTUI(t, c)
	ts.expect("SSH Home")

	// browse help via ?
	ts.send("?")
	ts.expect("Browser keys")
	ts.expect("Admin keys")
	ts.send(" ") // page down to the syntax sections
	ts.send(" ")
	ts.expect("{{version}}")
	ts.send("b") // back to home
	ts.expect("SSH Home")

	// editor help via ctrl+g preserves editor state
	ts.send("e")
	ts.expect("editing /index.gmi")
	ts.send("KEEP-ME")
	ts.send("\x07") // ctrl+g
	ts.expect("syntax help")
	ts.send(" ") // scroll down to the directives
	ts.send(" ")
	ts.expect("{{version}}")
	ts.send("\x1b") // any key returns to editor
	ts.expect("editing /index.gmi")
	ts.send("\x13") // ctrl+s
	ts.expect("saved /index.gmi")
	pg, _ := st.GetPage("/index.gmi")
	if pg == nil || !strings.Contains(string(pg.Content), "KEEP-ME") {
		t.Error("editor content lost across help overlay")
	}
	ts.send("\x11")
	ts.send("q")
}

func TestFuzzyGoto(t *testing.T) {
	_, st, addr := startServer(t)
	_, _ = st.SavePage("/posts/2026-07-19-hello.gmi", []byte("# Hello Post"), "", "t")
	_, _ = st.SavePage("/contact.gmi", []byte("# Contact Me"), "", "t")
	c, err := dial(t, addr, "guest", "x")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ts := openTUI(t, c)
	ts.expect("SSH Home")

	// open goto, type a fuzzy fragment, confirm the match list + navigation
	ts.send("g")
	ts.expect("goto (fuzzy)")
	ts.send("hello")
	ts.expect("/posts/2026-07-19-hello")
	ts.send("\r") // opens the top match
	ts.expect("Hello Post")
	ts.send("q")
}

func TestGuestCannotEdit(t *testing.T) {
	_, st, addr := startServer(t)
	c, err := dial(t, addr, "guest", "x")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ts := openTUI(t, c)
	ts.expect("SSH Home")

	before, _ := st.GetPage("/index.gmi")
	ts.send("e") // should do nothing for guests
	ts.send("x")
	ts.send("c")
	time.Sleep(300 * time.Millisecond)
	after, err := st.GetPage("/index.gmi")
	if err != nil || string(after.Content) != string(before.Content) {
		t.Error("guest keystrokes mutated content")
	}
	// still browsing (no editor opened): send q to quit without error
	ts.send("q")
}

func TestAuthorizedKeysDisablePassword(t *testing.T) {
	// generate a client keypair
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubLine := string(gossh.MarshalAuthorizedKey(signer.PublicKey()))

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _ = st.SavePage("/index.gmi", []byte("# Keyed Home"), "", "t")

	cfg := config.Default()
	cfg.Hostname = "test.example"
	cfg.AdminPassword = adminPW
	cfg.SSH = config.SSHService{
		Service:        config.Service{Enabled: true, Addr: "127.0.0.1:0"},
		AuthorizedKeys: []string{pubLine},
	}
	cfg.DataDir = t.TempDir()
	srv, err := New(cfg, st, site.New(st), log.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.wish.Serve(ln) }()
	defer srv.Close()
	addr := ln.Addr().String()

	// admin password must now be REJECTED even though it is correct
	if _, err := dial(t, addr, "admin", adminPW); err == nil {
		t.Fatal("admin password accepted despite authorized_keys")
	}

	// admin with the configured key gets in
	c, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            "admin",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("admin key auth failed: %v", err)
	}
	c.Close()

	// admin with a DIFFERENT key is rejected
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := gossh.NewSignerFromKey(otherPriv)
	if _, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            "admin",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(otherSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}); err == nil {
		t.Fatal("unauthorized key accepted for admin")
	}

	// guests still get in with a password
	c2, err := dial(t, addr, "guest", "anything")
	if err != nil {
		t.Fatalf("guest auth broken: %v", err)
	}
	c2.Close()
}

func TestBadAuthorizedKeyRejectedAtStartup(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	cfg := config.Default()
	cfg.SSH = config.SSHService{
		Service:        config.Service{Enabled: true, Addr: "127.0.0.1:0"},
		AuthorizedKeys: []string{"not a key"},
	}
	cfg.DataDir = t.TempDir()
	if _, err := New(cfg, st, site.New(st), log.New(io.Discard)); err == nil {
		t.Fatal("unparseable authorized key accepted")
	}
}

func TestAdminLoginDisabledWithoutPassword(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.AdminPassword = ""
	cfg.SSH = config.SSHService{Service: config.Service{Enabled: true, Addr: "127.0.0.1:0"}}
	cfg.DataDir = t.TempDir()
	srv, err := New(cfg, st, site.New(st), log.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.wish.Serve(ln) }()
	defer srv.Close()

	if _, err := dial(t, ln.Addr().String(), "admin", ""); err == nil {
		t.Fatal("admin accepted with no password configured")
	}
}

// Authentication and the TUI must agree on who "admin" is, exactly. A
// mismatch would let a lookalike username authenticate as a guest and then
// be handed the editor.
func TestAdminUserIsExact(t *testing.T) {
	if !adminUser("admin") {
		t.Error(`"admin" should be the admin`)
	}
	for _, u := range []string{"Admin", "ADMIN", "admin ", " admin", "admin2", "guest", "", "root"} {
		if adminUser(u) {
			t.Errorf("%q was treated as the admin", u)
		}
	}
}

// A long line must wrap inside the terminal, not run off the edge.
//
// This drives the model's own View() rather than a real PTY: over an ssh
// terminal the pty itself soft-wraps whatever it is sent, so the byte stream
// looks the same whether the editor wrapped the text or simply emitted a
// 340-column line. Only the rendered frame can tell the difference, and it
// is the difference that matters — a line the editor believes is one row
// wide but the terminal draws as four breaks cursor and scroll arithmetic.
func TestEditorWrapsLongLines(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sy := site.New(st)
	long := strings.Repeat("wrap-me ", 40) + "END" // ~330 columns
	_, _ = st.SavePage("/long.gmi", []byte("# Long\n\n"+long), "", "t")

	const cols = 100
	m := newProtoModel(sy, st, "test.example", true, cols, 24, lipgloss.DefaultRenderer(), "ssh")
	m.navigate("/long", false)

	for _, stage := range []string{"browser", "editor"} {
		if stage == "editor" {
			m.startEdit("/long.gmi", false)
		}
		frame := m.View()
		for i, line := range strings.Split(frame, "\n") {
			if w := ansi.StringWidth(line); w > cols {
				t.Errorf("%s: row %d is %d columns wide, terminal is %d:\n%q",
					stage, i, w, cols, ansi.Strip(line))
			}
		}
		if !strings.Contains(ansi.Strip(frame), "END") {
			t.Errorf("%s: the end of the long line never appears — it is cut off, not wrapped", stage)
		}
	}
}

// The ssh door must throttle admin password guesses the way the web login
// does. Before this, a wrong password cost only a one-second sleep — and
// nothing stopped an attacker opening connections in parallel.
func TestAdminPasswordIsThrottled(t *testing.T) {
	srv, _, addr := startServer(t)

	// burn through the allowance; each attempt is its own connection, which
	// is exactly how a guesser would do it
	for i := 0; i < 10; i++ {
		if c, err := dial(t, addr, "admin", "wrong-password"); err == nil {
			c.Close()
			t.Fatalf("attempt %d: a wrong password was accepted", i)
		}
	}
	if !srv.gate.Blocked("127.0.0.1", time.Now()) {
		t.Fatal("ten failures did not lock the address out")
	}
	// now even the right password is refused while the lockout stands
	if c, err := dial(t, addr, "admin", adminPW); err == nil {
		c.Close()
		t.Error("lockout did not apply to a subsequent login")
	}
	// ...and a guest is unaffected: the site stays readable
	c, err := dial(t, addr, "guest", "")
	if err != nil {
		t.Errorf("lockout blocked a guest: %v", err)
	} else {
		c.Close()
	}
	// once it expires, the right password works again
	srv.gate.Succeed("127.0.0.1")
	c2, err := dial(t, addr, "admin", adminPW)
	if err != nil {
		t.Errorf("admin still locked out after the record cleared: %v", err)
	} else {
		c2.Close()
	}
}

// Mouse: the wheel scrolls, a click on a link follows it, a click on the
// bottom bar does what that key does.
func TestMouseClicksLinksAndTheBar(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, _ = st.SavePage("/index.gmi", []byte("# Home\n\nsome words\n\n=> /about About page\n=> /other Other page"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About Me"), "", "t")
	_, _ = st.SavePage("/other.gmi", []byte("# Other"), "", "t")

	newM := func() *model {
		m := newProtoModel(site.New(st), st, "test.example", false, 80, 24, lipgloss.DefaultRenderer(), "ssh")
		m.navigate("/", false)
		return m
	}
	click := func(m *model, x, y int) {
		m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	}

	// a click on the second link's row opens that link, not the first
	m := newM()
	if len(m.links) != 2 {
		t.Fatalf("links = %d, want 2", len(m.links))
	}
	click(m, 4, 1+m.links[1].Line)
	if m.url != "/other" {
		t.Errorf("clicking the second link went to %q, want /other", m.url)
	}

	// a click on a line with no link changes nothing
	m = newM()
	before := m.url
	click(m, 2, 1) // the "# Home" heading
	if m.url != before {
		t.Errorf("clicking a heading navigated to %q", m.url)
	}

	// a click on "home" in the bottom bar goes home
	m = newM()
	m.navigate("/about", false)
	if m.url != "/about" {
		t.Fatalf("setup: url = %q", m.url)
	}
	var homeX int
	for _, z := range barZones(m.browsePairs()) {
		if z.key == "h" {
			homeX = z.x0 + 1
		}
	}
	if homeX == 0 {
		t.Fatal("no home zone on the bar")
	}
	// find the bar in the frame itself rather than assuming which row it is
	// on: asserting a row number the code also assumed is how clicking the
	// bar shipped broken with a passing test
	barY := -1
	for i, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(ansi.Strip(line), "quit") {
			barY = i
		}
	}
	if barY < 0 {
		t.Fatal("no bottom bar in the frame")
	}
	click(m, homeX, barY)
	if m.url != "/" {
		t.Errorf("clicking home went to %q, want /", m.url)
	}

	// the bar the mouse reads is the bar that is drawn: every zone's key
	// must appear in the rendered footer
	m = newM()
	frame := ansi.Strip(m.View())
	for _, z := range barZones(m.browsePairs()) {
		if !strings.Contains(frame, z.key) {
			t.Errorf("bar zone %q is not in the rendered frame", z.key)
		}
	}

	// the wheel scrolls the page
	_, _ = st.SavePage("/long.gmi", []byte("# Long\n\n"+strings.Repeat("line of text\n\n", 40)), "", "t")
	m = newM()
	m.navigate("/long", false)
	if m.vp.YOffset != 0 {
		t.Fatalf("setup: offset = %d", m.vp.YOffset)
	}
	m.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.vp.YOffset == 0 {
		t.Error("the wheel did not scroll the page")
	}
	down := m.vp.YOffset
	m.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.vp.YOffset >= down {
		t.Error("the wheel did not scroll back up")
	}
}

// A guest must not reach admin actions by clicking, either: the bar it is
// shown carries no editing keys.
func TestMouseBarRespectsGuest(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")

	guest := newProtoModel(site.New(st), st, "h", false, 80, 24, lipgloss.DefaultRenderer(), "ssh")
	for _, z := range barZones(guest.browsePairs()) {
		switch z.key {
		case "e", "c", "n", "x":
			t.Errorf("guest bar offers %q", z.key)
		}
	}
	admin := newProtoModel(site.New(st), st, "h", true, 80, 24, lipgloss.DefaultRenderer(), "ssh")
	var sawEdit bool
	for _, z := range barZones(admin.browsePairs()) {
		if z.key == "e" {
			sawEdit = true
		}
	}
	if !sawEdit {
		t.Error("admin bar has no edit key")
	}
}

// End to end over a real ssh session: the model handling mouse messages is
// no use unless the program actually asked the terminal to report them and
// parses what comes back. This sends the escape sequence a terminal emits
// for a left click and expects the link under it to open.
func TestMouseOverARealSession(t *testing.T) {
	_, st, addr := startServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# SSH Home\n\n=> /about About page"), "", "t")

	c, err := dial(t, addr, "guest", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ts := openTUI(t, c)
	ts.expect("SSH Home")
	ts.expect("About page")

	// SGR press/release on the link's row: rows are 1-based on the wire, the
	// header occupies row 1, and the link is the third line of the document
	const col, row = 5, 4
	ts.send(fmt.Sprintf("\x1b[<0;%d;%dM", col, row))
	ts.send(fmt.Sprintf("\x1b[<0;%d;%dm", col, row))
	ts.expect("About Me")
}
