package web

import (
	"crypto/tls"
	"strings"

	"github.com/jclement/starpulse/internal/certutil"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/render"
)

// gemtextBody renders assembled gemtext to an HTML fragment.
func (s *Server) gemtextBody(gmi string) string {
	return render.GemtextToHTMLOpts(gmi, s.renderOpts())
}

// isActiveMime reports whether a stored file's mime type can execute script
// in the site origin (so it must not be served inline as-is).
func isActiveMime(mime string) bool {
	m := strings.ToLower(mime)
	for _, bad := range []string{"text/html", "application/xhtml", "image/svg", "application/javascript", "text/javascript", "application/xml", "text/xml"} {
		if strings.HasPrefix(m, bad) {
			return true
		}
	}
	return false
}

// selfSigned returns a persistent self-signed cert for the https listener
// when ACME is disabled (dev / behind a proxy).
func selfSigned(cfg *config.Config) (tls.Certificate, error) {
	return certutil.LoadOrCreate(cfg.DataDir, cfg.Hostname)
}
