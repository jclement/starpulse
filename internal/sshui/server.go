// Package sshui serves the capsule over SSH: a bubbletea TUI gemini browser
// for guests, with in-place editing (a pico-style full-screen editor) for
// the admin user. Pure Go, courtesy of charmbracelet/wish.
package sshui

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	btm "github.com/charmbracelet/wish/bubbletea"

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

	wish *ssh.Server
}

// New builds the wish server (host key persisted in the data dir).
func New(cfg *config.Config, st *store.Store, sy *site.Site, logger *log.Logger) (*Server, error) {
	s := &Server{Cfg: cfg, Store: st, Site: sy, Log: logger}

	srv, err := wish.NewServer(
		wish.WithAddress(cfg.SSH.Addr),
		wish.WithHostKeyPath(filepath.Join(cfg.DataDir, "ssh_host_ed25519")),
		wish.WithPasswordAuth(s.authenticate),
		// accept any public key: it identifies nobody, but lets clients that
		// insist on trying pubkey auth first fall through gracefully as guest
		wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			return ctx.User() != "admin"
		}),
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

// authenticate: "admin" needs the admin password; anyone else is a guest.
func (s *Server) authenticate(ctx ssh.Context, password string) bool {
	if ctx.User() == "admin" {
		if s.Cfg.AdminPassword == "" {
			return false
		}
		ok := auth.CheckPassword(s.Cfg.AdminPassword, password)
		if !ok {
			s.Log.Warn("failed admin login", "remote", ctx.RemoteAddr().String())
			time.Sleep(time.Second)
		}
		return ok
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
	admin := sess.User() == "admin"
	return newModel(s.Site, s.Store, s.Cfg.Hostname, admin, w, h), []tea.ProgramOption{tea.WithAltScreen()}
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
