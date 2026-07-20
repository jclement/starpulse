package web

import (
	"net/http"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/feed"
)

// feedBuilder constructs the shared Atom renderer for this server.
func (s *Server) feedBuilder() *feed.Builder {
	return &feed.Builder{
		Store:    s.Store,
		Hostname: s.Cfg.Hostname,
		Author:   s.Cfg.Feeds.Author,
		Loc:      s.loc(),
	}
}

// registerFeeds serves every configured feed at its own path.
func (s *Server) registerFeeds(mux *http.ServeMux) {
	b := s.feedBuilder()
	seen := map[string]bool{}
	for _, f := range s.Cfg.EffectiveFeeds() {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		cfg := f // capture
		mux.HandleFunc(cfg.Path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=900")
			_, _ = w.Write([]byte(b.Build(cfg, s.baseURL(r))))
		})
	}
	// keep the historical alias working when it isn't explicitly configured
	if !seen["/posts/feed.xml"] && !seen["/feed.xml"] {
		return
	}
	if !seen["/posts/feed.xml"] {
		alias := firstFeed(s.Cfg.EffectiveFeeds())
		mux.HandleFunc("/posts/feed.xml", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
			_, _ = w.Write([]byte(b.Build(alias, s.baseURL(r))))
		})
	}
}

func firstFeed(fs []config.Feed) config.Feed {
	if len(fs) > 0 {
		return fs[0]
	}
	return config.Feed{Path: "/feed.xml", Source: "/"}
}
