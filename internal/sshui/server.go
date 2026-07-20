// Package sshui serves the capsule over SSH: a bubbletea TUI gemini browser
// for guests, with in-place editing (a pico-style full-screen editor) for
// the admin user. Pure Go, courtesy of charmbracelet/wish.
package sshui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	btm "github.com/charmbracelet/wish/bubbletea"
	"github.com/muesli/termenv"
	gossh "golang.org/x/crypto/ssh"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

// Server is the SSH door.
type Server struct {
	Cfg   *config.Config
	Store *store.Store
	Site  *site.Site
	Log   *log.Logger

	wish      *ssh.Server
	adminKeys []ssh.PublicKey
	gate      *auth.Throttle // per-IP admin password limiter
}

// New builds the wish server (host key persisted in the data dir).
func New(cfg *config.Config, st *store.Store, sy *site.Site, logger *log.Logger) (*Server, error) {
	// the same limits the web login uses: an ssh door that lets you guess
	// the admin password at one attempt per second, in parallel, is not
	// protected by the one-second sleep that used to be here
	s := &Server{Cfg: cfg, Store: st, Site: sy, Log: logger,
		gate: auth.NewThrottle(10, 5*time.Minute)}
	for i, line := range cfg.SSH.AuthorizedKeys {
		key, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("ssh authorized_keys[%d] unparseable: %w", i, err)
		}
		s.adminKeys = append(s.adminKeys, key)
	}

	srv, err := wish.NewServer(
		wish.WithAddress(cfg.SSH.Addr),
		wish.WithHostKeyPath(filepath.Join(cfg.DataDir, "ssh_host_ed25519")),
		wish.WithPasswordAuth(s.authenticate),
		wish.WithPublicKeyAuth(s.pubkeyAuth),
		wish.WithMiddleware(
			btm.Middleware(s.teaHandler),
			activeterm.Middleware(),
			s.logMiddleware,
		),
	)
	if err != nil {
		return nil, err
	}
	s.wish = srv
	return s, nil
}

// pubkeyAuth: admin must present one of the configured authorized keys;
// any key is fine for guests (it identifies nobody, but lets clients that
// try pubkey first fall through gracefully).
// remoteIP is the throttle key for an ssh connection: the address without
// the ephemeral port, so a new connection per guess does not reset it.
func remoteIP(addr net.Addr) string {
	if h, _, err := net.SplitHostPort(addr.String()); err == nil {
		return h
	}
	return addr.String()
}

// adminUser decides who is asking for admin powers. Authentication and the
// TUI must agree exactly: if one of them were case-insensitive and the other
// were not, "Admin" would authenticate as a guest and then be handed the
// editor. One function, used by both.
func adminUser(u string) bool { return u == "admin" }

func (s *Server) pubkeyAuth(ctx ssh.Context, key ssh.PublicKey) bool {
	if !adminUser(ctx.User()) {
		return true
	}
	for _, k := range s.adminKeys {
		if ssh.KeysEqual(k, key) {
			return true
		}
	}
	return false
}

// authenticate: "admin" needs the admin password — unless authorized_keys
// are configured, which disables admin password auth entirely; anyone else
// is a guest.
func (s *Server) authenticate(ctx ssh.Context, password string) bool {
	if adminUser(ctx.User()) {
		if len(s.adminKeys) > 0 {
			s.Log.Warn("admin password auth rejected (authorized_keys configured)", "remote", ctx.RemoteAddr().String())
			return false
		}
		if s.Cfg.AdminPassword == "" {
			return false
		}
		ip := remoteIP(ctx.RemoteAddr())
		if s.gate.Blocked(ip, time.Now()) {
			s.Log.Warn("admin login refused (too many failures)", "remote", ip)
			return false
		}
		if auth.CheckPassword(s.Cfg.AdminPassword, password) {
			s.gate.Succeed(ip)
			return true
		}
		if s.gate.Fail(ip, time.Now()) {
			s.Log.Warn("admin login locked out", "remote", ip)
		} else {
			s.Log.Warn("failed admin login", "remote", ip)
		}
		time.Sleep(time.Second)
		return false
	}
	return true
}

func (s *Server) logMiddleware(next ssh.Handler) ssh.Handler {
	return func(sess ssh.Session) {
		start := time.Now()
		s.Log.Info("session start", "user", sess.User(), "remote", sess.RemoteAddr().String())
		next(sess)
		s.Log.Info("session end", "user", sess.User(), "dur", time.Since(start).Round(time.Second))
	}
}

func (s *Server) teaHandler(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
	pty, _, _ := sess.Pty()
	w, h := pty.Window.Width, pty.Window.Height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	admin := adminUser(sess.User())
	// build the renderer from TERM alone — probing the terminal (OSC/DA
	// queries) stalls against clients that never answer
	renderer := lipgloss.NewRenderer(sess, termenv.WithColorCache(true))
	renderer.SetColorProfile(profileFor(pty.Term))
	renderer.SetHasDarkBackground(true)
	return newModel(s.Site, s.Store, s.Cfg.Hostname, admin, w, h, renderer), []tea.ProgramOption{tea.WithAltScreen()}
}

func profileFor(term string) termenv.Profile {
	term = strings.ToLower(term)
	switch {
	case strings.Contains(term, "truecolor"), strings.Contains(term, "direct"):
		return termenv.TrueColor
	case strings.Contains(term, "256color"):
		return termenv.ANSI256
	case term == "" || term == "dumb":
		return termenv.Ascii
	default:
		return termenv.ANSI
	}
}

// ListenAndServe runs the SSH server until it fails or is shut down.
func (s *Server) ListenAndServe() error {
	s.Log.Info("ssh listening", "addr", s.Cfg.SSH.Addr)
	err := s.wish.ListenAndServe()
	if err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		return err
	}
	return nil
}

// Close shuts the server down.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.wish.Shutdown(ctx)
}
