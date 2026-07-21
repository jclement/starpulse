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

const sessionLimit = 4 * time.Hour

// Server is the telnet door.
type Server struct {
	Cfg   *config.Config
	Store *store.Store
	Site  *site.Site
	Log   *log.Logger

	ln net.Listener
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

// Serve accepts telnet connections on ln until the listener fails.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
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
	_ = conn.SetDeadline(time.Now().Add(sessionLimit))

	w := &lockedWriter{w: conn}

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
	reader := &telnetReader{
		r: conn,
		w: w,
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
	_, err := p.Run()
	if err != nil && err != tea.ErrProgramKilled {
		s.Log.Warn("session error", "remote", remote, "err", err)
	}
	s.Log.Info("session end", "remote", remote, "dur", time.Since(start).Round(time.Second))
}

// lockedWriter serializes writes: bubbletea frames and negotiation replies
// must not interleave mid-sequence.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// telnetReader filters IAC command sequences out of the byte stream,
// answers negotiation minimally, and reports NAWS window sizes.
type telnetReader struct {
	r        io.Reader
	w        io.Writer
	onResize func(w, h int)

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
		n, err := t.r.Read(t.buf[:])
		if n > 0 {
			t.feed(t.buf[:n])
		}
		if err != nil {
			if len(t.out) > 0 {
				break
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
