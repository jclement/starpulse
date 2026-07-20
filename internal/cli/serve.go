// Package cli implements the starpulse command-line interface.
package cli

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/log"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/certutil"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/gemini"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
	"github.com/jclement/starpulse/internal/tor"
	"github.com/jclement/starpulse/internal/web"
)

const banner = `
  ▄▄▄▄ ▄▄▄▄▄▄ ▄▄▄▄▄ ▄▄▄▄▄  ▄▄▄▄▄  ▄   ▄ ▄▄▄   ▄▄▄▄ ▄▄▄▄▄
 █▀    █    █ █   █ █   █  █   █  █   █ █    █▀    █
 ▀▀▀▄  █    █ █▄▄▄█ █▄▄▄▀  █▄▄▄▀  █   █ █     ▀▀▀▄ █▄▄▄
 ▄▄▄▀  █    █ █   █ █  ▀▄  █      █▄▄▄█ █▄▄▄ ▄▄▄▀  █▄▄▄▄
     s t a r p u l s e · smolweb, one binary`

// Serve runs the server until a listener fails.
func Serve(cfg *config.Config, logger *log.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\x1b[38;5;219m"+banner+"\x1b[0m")
	logger.Info("starting starpulse",
		"version", site.BuildVersion,
		"hostname", cfg.Hostname,
		"data", cfg.DataDir,
		"config", orDefault(cfg.Source, "(defaults)"),
		"gemini", cfg.Gemini.Enabled,
		"http", cfg.HTTP.Enabled,
		"https", cfg.HTTPS.Enabled,
		"titan", cfg.Titan.Enabled,
		"tor", cfg.Tor.Enabled)
	if cfg.AdminPassword == "" {
		logger.Warn("no admin_password configured — editing via web/api/mcp is disabled")
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "starpulse.sqlite"))
	if err != nil {
		return err
	}
	defer st.Close()
	st.KeepVersions = cfg.KeepVersions
	seedIfEmpty(st, logger)

	sessions, secret := auth.NewSessions(st.GetSetting("session_secret"))
	_ = st.SetSetting("session_secret", secret)

	sy := site.New(st)

	// hidden service: managed tor, or an externally-managed onion hostname
	var torMgr *tor.Manager
	onion := func() string { return cfg.Tor.Onion }
	if cfg.Tor.Enabled {
		torMgr = &tor.Manager{
			Binary:     cfg.Tor.Binary,
			DataDir:    cfg.DataDir,
			HTTPAddr:   cfg.HTTP.Addr,
			GeminiAddr: cfg.Gemini.Addr,
			Log:        logger.With("proto", "tor"),
		}
		if err := torMgr.Start(); err != nil {
			return err
		}
		defer torMgr.Stop()
		if cfg.Tor.Onion == "" {
			onion = torMgr.Onion
		}
	}

	errCh := make(chan error, 2)

	if cfg.Gemini.Enabled {
		cert, err := certutil.LoadOrCreate(cfg.DataDir, cfg.Hostname)
		if err != nil {
			return fmt.Errorf("gemini cert: %w", err)
		}
		gemSrv := &gemini.Server{
			Cfg:   cfg,
			Store: st,
			Site:  sy,
			Log:   logger.With("proto", "gemini"),
			Onion: onion,
			TLS: &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{cert},
				// request (never require) a client cert so titan can identify users
				ClientAuth: tls.RequestClientCert,
			},
		}
		go func() { errCh <- gemSrv.ListenAndServe() }()
	}

	if cfg.HTTP.Enabled || cfg.HTTPS.Enabled {
		webSrv := &web.Server{
			Cfg:      cfg,
			Store:    st,
			Site:     sy,
			Log:      logger.With("proto", "web"),
			Sessions: sessions,
			Onion:    onion,
		}
		go func() { errCh <- webSrv.Serve() }()
	}

	return <-errCh
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// seedIfEmpty gives a fresh database a small starter site.
func seedIfEmpty(st *store.Store, logger *log.Logger) {
	metas, err := st.ListAll()
	if err != nil || len(metas) > 0 {
		return
	}
	logger.Info("empty database — seeding starter content")
	pages := map[string]string{
		"/index.gmi": `# Welcome to starpulse ✨

Your site is alive. This page lives in the database as /index.gmi.

## Getting started

=> /now Now — micro-posts, straight from the admin
=> /search Search this site

Log in at /login with your admin password to edit any page (look for the ✎ link in the footer), upload files, and post updates.

## Recent

{{list / 10}}
`,
		"/.footer": `
──────────────────────────

Viewed {{count}} times · powered by starpulse {{version}}
`,
	}
	for p, content := range pages {
		if _, err := st.SavePage(p, []byte(content), "", "seed"); err != nil {
			logger.Warn("seed failed", "path", p, "err", err)
		}
	}
}
