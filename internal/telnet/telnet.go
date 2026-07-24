// Package telnet serves the capsule TUI browser over raw telnet —
// read-only guest access, old-school BBS style. It speaks just enough of
// the telnet protocol to get character-at-a-time input (WILL ECHO +
// WILL SGA) and window-size updates (DO NAWS).
package telnet

import (
	"io"
	"net"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/muesli/termenv"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/sshui"
	"github.com/jclement/starpulse/internal/store"
)

// telnet protocol bytes
const (
	cmdSE   = 240
	cmdSB   = 250
	cmdWILL = 251
	cmdWONT = 252
	cmdDO   = 253
	cmdDONT = 254
	cmdIAC  = 255

	optEcho = 1
	optSGA  = 3
	optNAWS = 31
)

// Session bounds. Telnet is anonymous guest access hammered constantly by
// scanners, so every connection is aggressively bounded: no session outlives
// sessionLimit, a connection that sends nothing for idleTimeout is dropped,
// a client that will not read our output for writeTimeout is dropped, and at
// most maxSessions run at once. Without these a scanner that opens a
// connection and sits leaks a live bubbletea program until the process
// swap-thrashes.
const (
	defaultSessionLimit = 1 * time.Hour    // hard ceiling on any one session
	defaultIdleTimeout  = 10 * time.Minute // drop a connection with no input
	writeTimeout        = 30 * time.Second // drop a client that will not read
	defaultMaxSessions  = 64               // concurrent telnet sessions
)

// Server is the telnet door.
type Server struct {
	Cfg   *config.Config
	Store *store.Store
	Site  *site.Site
	Log   *log.Logger

	ln  net.Listener
	sem chan struct{} // caps concurrent sessions; sized maxSessions

	// Session bounds; zero selects the default. Tests set them small to
	// exercise the idle-timeout, hard-limit and capacity paths without
	// real-time waits.
	sessionLimit time.Duration
	idleTimeout  time.Duration
	maxSessions  int

	// onEnd, if set, is called as each session ends — a test hook to observe
	// that a session actually tore down rather than leaking.
	onEnd func()
}

// resolveBounds fills any unset session bound with its package default.
func (s *Server) resolveBounds() {
	if s.sessionLimit == 0 {
		s.sessionLimit = defaultSessionLimit
	}
	if s.idleTimeout == 0 {
		s.idleTimeout = defaultIdleTimeout
	}
	if s.maxSessions == 0 {
		s.maxSessions = defaultMaxSessions
	}
}

// Listen opens the listener (split out for tests).
func (s *Server) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", s.Cfg.Telnet.Addr)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	return ln, nil
}

// Serve accepts telnet connections on ln until the listener fails. Beyond
// maxSessions concurrent sessions new connections are refused, so a flood of
// scanner connections cannot grow the process without bound.
func (s *Server) Serve(ln net.Listener) error {
	s.resolveBounds()
	if s.sem == nil {
		s.sem = make(chan struct{}, s.maxSessions)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		select {
		case s.sem <- struct{}{}:
			go func() {
				defer func() { <-s.sem }()
				s.handle(conn)
			}()
		default:
			// at capacity — turn the connection away rather than pile on
			s.Log.Warn("session refused (at capacity)", "remote", conn.RemoteAddr().String())
			_, _ = conn.Write([]byte("The capsule is busy right now — please try again shortly.\r\n"))
			_ = conn.Close()
		}
	}
}

// ListenAndServe accepts telnet connections until the listener fails.
func (s *Server) ListenAndServe() error {
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	s.Log.Info("telnet listening", "addr", s.Cfg.Telnet.Addr)
	return s.Serve(ln)
}

// Close stops the listener.
func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	start := time.Now()
	remote := conn.RemoteAddr().String()
	s.Log.Info("session start", "remote", remote)

	w := &lockedWriter{conn: conn}

	// negotiate: we echo, we suppress go-ahead, tell us your window size
	_, _ = w.Write([]byte{
		cmdIAC, cmdWILL, optEcho,
		cmdIAC, cmdWILL, optSGA,
		cmdIAC, cmdDO, optNAWS,
	})

	// telnet clients speak basic ANSI; assume dark background
	renderer := lipgloss.NewRenderer(w, termenv.WithColorCache(true))
	renderer.SetColorProfile(termenv.ANSI)
	renderer.SetHasDarkBackground(true)

	model := sshui.NewBrowserModel(s.Site, s.Store, s.Cfg.Hostname, 80, 24, renderer, "telnet")

	var p *tea.Program
	// teardown ends the session exactly once. Killing the program makes
	// p.Run() return (so the goroutine unwinds and the deferred Close runs);
	// closing the conn unblocks any read or write in flight at that instant.
	// This is the fix for the leak: a dead or idle input now actually ends
	// the session instead of leaving a live program resident forever.
	var once sync.Once
	teardown := func() {
		once.Do(func() {
			if p != nil {
				p.Kill()
			}
			_ = conn.Close()
		})
	}

	reader := &telnetReader{
		r:       conn,
		conn:    conn,
		w:       w,
		idle:    s.idleTimeout,
		onClose: teardown,
		onResize: func(width, height int) {
			if p != nil {
				p.Send(tea.WindowSizeMsg{Width: width, Height: height})
			}
		},
	}
	p = tea.NewProgram(model,
		tea.WithInput(reader),
		tea.WithOutput(w),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// hard ceiling: no session outlives sessionLimit, whatever it is doing —
	// a timer we control, not a socket deadline the read loop can ignore.
	hard := time.AfterFunc(s.sessionLimit, teardown)
	defer hard.Stop()

	_, err := p.Run()
	if err != nil && err != tea.ErrProgramKilled {
		s.Log.Warn("session error", "remote", remote, "err", err)
	}
	s.Log.Info("session end", "remote", remote, "dur", time.Since(start).Round(time.Second))
	if s.onEnd != nil {
		s.onEnd()
	}
}

// lockedWriter serializes writes: bubbletea frames and negotiation replies
// must not interleave mid-sequence. Each write carries a deadline so a client
// that has stopped reading cannot block the render goroutine indefinitely.
type lockedWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return l.conn.Write(p)
}

// telnetReader filters IAC command sequences out of the byte stream,
// answers negotiation minimally, and reports NAWS window sizes.
type telnetReader struct {
	r        io.Reader
	conn     net.Conn      // for the per-read idle deadline; nil in unit tests
	idle     time.Duration // idle read timeout; 0 disables it (unit tests)
	w        io.Writer
	onResize func(w, h int)
	onClose  func() // called once when the input ends, to tear the session down

	buf  [512]byte
	out  []byte
	sub  []byte // current subnegotiation payload
	mode int    // parser state
	cmd  byte   // pending WILL/WONT/DO/DONT verb
}

const (
	stData = iota
	stIAC
	stCmd // saw IAC WILL/WONT/DO/DONT, next byte is the option
	stSub // inside IAC SB ... IAC SE
	stSubIAC
)

func (t *telnetReader) Read(p []byte) (int, error) {
	for len(t.out) == 0 {
		// idle timeout: a connection that sends nothing for idleTimeout — the
		// overwhelming majority of them, scanners that connect and sit — is
		// dropped. Reset before every read so it measures silence, not age.
		if t.conn != nil && t.idle > 0 {
			_ = t.conn.SetReadDeadline(time.Now().Add(t.idle))
		}
		n, err := t.r.Read(t.buf[:])
		if n > 0 {
			t.feed(t.buf[:n])
		}
		if err != nil {
			if len(t.out) > 0 {
				break
			}
			// the input has ended (peer close, idle timeout, or a killed
			// session's forced close): tear the whole session down so the
			// program stops and the goroutine is released.
			if t.onClose != nil {
				t.onClose()
			}
			return 0, err
		}
	}
	n := copy(p, t.out)
	t.out = t.out[n:]
	return n, nil
}

func (t *telnetReader) feed(data []byte) {
	for _, b := range data {
		switch t.mode {
		case stData:
			if b == cmdIAC {
				t.mode = stIAC
			} else {
				t.out = append(t.out, b)
			}
		case stIAC:
			switch b {
			case cmdIAC: // escaped 255
				t.out = append(t.out, b)
				t.mode = stData
			case cmdWILL, cmdWONT, cmdDO, cmdDONT:
				t.cmd = b
				t.mode = stCmd
			case cmdSB:
				t.sub = t.sub[:0]
				t.mode = stSub
			default: // NOP, GA, etc.
				t.mode = stData
			}
		case stCmd:
			t.respond(t.cmd, b)
			t.mode = stData
		case stSub:
			if b == cmdIAC {
				t.mode = stSubIAC
			} else {
				if len(t.sub) < 64 {
					t.sub = append(t.sub, b)
				}
			}
		case stSubIAC:
			switch b {
			case cmdSE:
				t.endSub()
				t.mode = stData
			case cmdIAC:
				if len(t.sub) < 64 {
					t.sub = append(t.sub, cmdIAC)
				}
				t.mode = stSub
			default:
				t.mode = stSub
			}
		}
	}
}

// respond answers client negotiation: accept what we asked for, decline
// everything else.
func (t *telnetReader) respond(cmd, opt byte) {
	switch {
	case cmd == cmdDO && (opt == optEcho || opt == optSGA):
		// client agreeing to our WILLs — no reply needed
	case cmd == cmdWILL && opt == optNAWS:
		// client agreeing to send window sizes — no reply needed
	case cmd == cmdDO:
		_, _ = t.w.Write([]byte{cmdIAC, cmdWONT, opt})
	case cmd == cmdWILL:
		_, _ = t.w.Write([]byte{cmdIAC, cmdDONT, opt})
		// WONT/DONT need no acknowledgement
	}
}

func (t *telnetReader) endSub() {
	if len(t.sub) == 5 && t.sub[0] == optNAWS {
		w := int(t.sub[1])<<8 | int(t.sub[2])
		h := int(t.sub[3])<<8 | int(t.sub[4])
		if w > 0 && h > 0 && t.onResize != nil {
			t.onResize(w, h)
		}
	}
}
