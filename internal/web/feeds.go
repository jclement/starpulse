package web

import (
	"net/http"
	"strings"

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

// serveFeed writes the feed for path if one exists there. Feeds are resolved
// per request rather than registered up front, because log folders are
// discovered from content and can appear while the server is running.
func (s *Server) serveFeed(w http.ResponseWriter, r *http.Request, path string) bool {
	if !strings.HasSuffix(path, ".xml") {
		return false
	}
	f, ok := feed.Resolve(s.Cfg, s.Store, path)
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=900")
	_, _ = w.Write([]byte(s.feedBuilder().Build(f, s.baseURL(r))))
	return true
}
