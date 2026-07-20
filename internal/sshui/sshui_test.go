package sshui

import (
	"io"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"
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
	cfg.SSH = config.Service{Enabled: true, Addr: "127.0.0.1:0"}
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
	out    chan byte
	seen   strings.Builder
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
	ts := &tuiSession{t: t, sess: sess, stdin: stdin, out: make(chan byte, 65536)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			for i := 0; i < n; i++ {
				select {
				case ts.out <- buf[i]:
				default:
				}
			}
			if err != nil {
				close(ts.out)
				return
			}
		}
	}()
	t.Cleanup(func() { sess.Close() })
	return ts
}

// expect waits until the (ANSI-stripped) output stream contains substr.
func (ts *tuiSession) expect(substr string) {
	ts.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if strings.Contains(ansiRe.ReplaceAllString(ts.seen.String(), ""), substr) {
			return
		}
		select {
		case b, ok := <-ts.out:
			if !ok {
				ts.t.Fatalf("stream closed waiting for %q; saw:\n%s", substr,
					tail(ansiRe.ReplaceAllString(ts.seen.String(), ""), 2000))
			}
			ts.seen.WriteByte(b)
		case <-deadline:
			ts.t.Fatalf("timeout waiting for %q; saw:\n%s", substr,
				tail(ansiRe.ReplaceAllString(ts.seen.String(), ""), 2000))
		}
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
	ts.send("\t")   // select link
	ts.send("\r")   // open
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
	ts.expect("now post published")
	posts, _ := st.ListNow(0)
	if len(posts) != 1 || posts[0].Content != "posted from the terminal" {
		t.Errorf("now posts = %+v", posts)
	}

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

func TestAdminLoginDisabledWithoutPassword(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.AdminPassword = ""
	cfg.SSH = config.Service{Enabled: true, Addr: "127.0.0.1:0"}
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
