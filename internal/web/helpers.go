package web

import (
	"crypto/tls"

	"github.com/jclement/starpulse/internal/certutil"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/render"
)

// gemtextBody renders assembled gemtext to an HTML fragment.
func gemtextBody(gmi string) string {
	return render.GemtextToHTML(gmi)
}

// selfSigned returns a persistent self-signed cert for the https listener
// when ACME is disabled (dev / behind a proxy).
func selfSigned(cfg *config.Config) (tls.Certificate, error) {
	return certutil.LoadOrCreate(cfg.DataDir, cfg.Hostname)
}
