package telnet

import (
	"io"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

func startServer(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, _ = st.SavePage("/index.gmi", []byte("# Telnet Home\n\n=> /about About page"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About Telnet"), "", "t")

	cfg := config.Default()
	cfg.Hostname = "tel.example"
	cfg.Telnet = config.Service{Enabled: true, Addr: "127.0.0.1:0"}

	srv := &Server{Cfg: cfg, Store: st, Site: site.New(st), Log: log.New(io.Discard)}
	ln, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return st, ln.Addr().String()
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[>=<]`)

type client struct {
	t    *testing.T
	conn net.Conn
	seen strings.Builder
}

func connect(t *testing.T, addr string) *client {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &client{t: t, conn: conn}
}

// expect reads (stripping telnet commands + ANSI) until substr appears.
func (c *client) expect(substr string) {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	buf := make([]byte, 4096)
	for {
		clean := ansiRe.ReplaceAllString(stripIAC(c.seen.String()), "")
		if strings.Contains(clean, substr) {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("timeout waiting for %q; saw:\n%s", substr, tail(clean, 1500))
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := c.conn.Read(buf)
		if n > 0 {
			c.seen.Write(buf[:n])
		}
		if err != nil && !isTimeout(err) {
			c.t.Fatalf("read: %v (waiting for %q; saw:\n%s)", err, substr, tail(clean, 1500))
		}
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// stripIAC removes telnet command sequences from raw bytes-as-string.
func stripIAC(s string) string {
	var b strings.Builder
	data := []byte(s)
	for i := 0; i < len(data); i++ {
		if data[i] != cmdIAC {
			b.WriteByte(data[i])
			continue
		}
		if i+1 >= len(data) {
			break
		}
		switch data[i+1] {
		case cmdWILL, cmdWONT, cmdDO, cmdDONT:
			i += 2
		case cmdSB:
			for i++; i+1 < len(data) && !(data[i] == cmdIAC && data[i+1] == cmdSE); i++ {
			}
			i++
		default:
			i++
		}
	}
	return b.String()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// pump performs one short read into the buffer (for raw-byte polling).
func (c *client) pump() {
	buf := make([]byte, 4096)
	_ = c.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, _ := c.conn.Read(buf)
	if n > 0 {
		c.seen.Write(buf[:n])
	}
}

func (c *client) send(s string) {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

func TestNegotiationAndBrowse(t *testing.T) {
	st, addr := startServer(t)
	c := connect(t, addr)

	// server must open with WILL ECHO / WILL SGA / DO NAWS
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c.seen.String(), string([]byte{cmdIAC, cmdWILL, optEcho})) {
		if time.Now().After(deadline) {
			t.Fatal("no WILL ECHO negotiation")
		}
		c.pump()
	}

	// send NAWS window size 100x30 and confirm content renders
	c.send(string([]byte{cmdIAC, cmdWILL, optNAWS}))
	c.send(string([]byte{cmdIAC, cmdSB, optNAWS, 0, 100, 0, 30, cmdIAC, cmdSE}))
	c.expect("Telnet Home")
	c.expect("tel.example")
	c.expect("guest") // header shows guest — never admin over telnet

	// navigate
	c.send("\t")
	c.send("\r")
	c.expect("About Telnet")

	// admin keys must be inert
	c.send("e")
	c.send("c")
	time.Sleep(200 * time.Millisecond)

	// quit
	c.send("q")

	time.Sleep(300 * time.Millisecond)
	hits, _ := st.Stats()
	for _, h := range hits {
		if h.Proto != "telnet" {
			t.Errorf("unexpected proto %q", h.Proto)
		}
	}
	if len(hits) == 0 {
		t.Error("no telnet stats recorded")
	}
	pg, _ := st.GetPage("/index.gmi")
	if pg == nil || !strings.Contains(string(pg.Content), "Telnet Home") {
		t.Error("content mutated over telnet")
	}
}

func TestReaderFiltersIAC(t *testing.T) {
	var resized [2]int
	pr, pw := io.Pipe()
	tr := &telnetReader{r: pr, w: io.Discard, onResize: func(w, h int) { resized = [2]int{w, h} }}
	go func() {
		pw.Write([]byte{'a', cmdIAC, cmdDO, optEcho, 'b', cmdIAC, cmdIAC, 'c'})
		pw.Write([]byte{cmdIAC, cmdSB, optNAWS, 0, 80, 0, 24, cmdIAC, cmdSE, 'd'})
		pw.Close()
	}()
	got, err := io.ReadAll(tr)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if string(got) != "ab\xffcd" {
		t.Errorf("filtered stream = %q, want %q", got, "ab\xffcd")
	}
	if resized != [2]int{80, 24} {
		t.Errorf("resize = %v, want [80 24]", resized)
	}
}

func TestDeclinesUnknownOptions(t *testing.T) {
	_, addr := startServer(t)
	c := connect(t, addr)
	// offer linemode (34) — server must refuse with DONT
	c.send(string([]byte{cmdIAC, cmdWILL, 34}))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c.seen.String(), string([]byte{cmdIAC, cmdDONT, 34})) {
		if time.Now().After(deadline) {
			t.Fatal("no DONT linemode reply")
		}
		c.pump()
	}
	c.send("q")
}
