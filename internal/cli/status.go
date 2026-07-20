package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

const (
	cReset  = "\x1b[0m"
	cDim    = "\x1b[38;5;245m"
	cAccent = "\x1b[38;5;215m"
	cPink   = "\x1b[38;5;219m"
	cGreen  = "\x1b[38;5;114m"
	cRed    = "\x1b[38;5;203m"
	cBold   = "\x1b[1m"
)

// Status prints a TUI-style overview: service health, protocol graphs, and
// the most popular pages.
func Status(cfg *config.Config) error {
	fmt.Printf("\n%s%s✨ starpulse%s %s@ %s · %s%s\n\n", cBold, cPink, cReset, cDim, cfg.Hostname, site.BuildVersion, cReset)

	// service health via the local http listener
	health, latency := probeHealth(cfg)
	if health {
		fmt.Printf("  service   %s● running%s %s(healthz %s)%s\n", cGreen, cReset, cDim, latency.Round(time.Millisecond), cReset)
	} else {
		fmt.Printf("  service   %s○ not responding%s %s(is it running? systemctl status starpulse)%s\n", cRed, cReset, cDim, cReset)
	}

	dbPath := filepath.Join(cfg.DataDir, "starpulse.sqlite")
	fi, err := os.Stat(dbPath)
	if err != nil {
		fmt.Printf("  database  %s✗ %s not found%s\n\n", cRed, dbPath, cReset)
		return fmt.Errorf("no database at %s", dbPath)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	pages, versions, nows := st.Totals()
	fmt.Printf("  database  %s %s(%s · %d pages · %d versions · %d now posts)%s\n",
		dbPath, cDim, human(fi.Size()), pages, versions, nows, cReset)

	doors := []string{}
	if cfg.Gemini.Enabled {
		doors = append(doors, "gemini "+cfg.Gemini.Addr)
	}
	if cfg.HTTP.Enabled {
		doors = append(doors, "http "+cfg.HTTP.Addr)
	}
	if cfg.HTTPS.Enabled {
		doors = append(doors, "https "+cfg.HTTPS.Addr)
	}
	if cfg.SSH.Enabled {
		doors = append(doors, "ssh "+cfg.SSH.Addr)
	}
	if cfg.Telnet.Enabled {
		doors = append(doors, "telnet "+cfg.Telnet.Addr)
	}
	if o := onionOf(cfg); o != "" {
		doors = append(doors, "🧅 "+o)
	}
	fmt.Printf("  doors     %s\n", strings.Join(doors, cDim+" · "+cReset))

	hits, err := st.Stats()
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Printf("\n  %sno views recorded yet%s\n\n", cDim, cReset)
		return nil
	}

	// views by protocol
	byProto := map[string]int64{}
	byPage := map[string]int64{}
	var total int64
	for _, h := range hits {
		byProto[h.Proto] += h.Count
		byPage[h.Path] += h.Count
		total += h.Count
	}
	fmt.Printf("\n%s  views by protocol%s %s(%d total)%s\n\n", cBold, cReset, cDim, total, cReset)
	protoOrder := []string{"http", "https", "gemini", "ssh", "telnet", "http+tor", "gemini+tor"}
	var maxProto int64
	for _, n := range byProto {
		if n > maxProto {
			maxProto = n
		}
	}
	for _, p := range protoOrder {
		n, ok := byProto[p]
		if !ok {
			continue
		}
		fmt.Printf("  %-11s %s%s%s %d\n", p, cAccent, bar(n, maxProto, 28), cReset, n)
		delete(byProto, p)
	}
	for p, n := range byProto { // anything unexpected
		fmt.Printf("  %-11s %s%s%s %d\n", p, cAccent, bar(n, maxProto, 28), cReset, n)
	}

	// top pages
	type pv struct {
		path string
		n    int64
	}
	tops := make([]pv, 0, len(byPage))
	for p, n := range byPage {
		tops = append(tops, pv{p, n})
	}
	sort.Slice(tops, func(i, j int) bool {
		if tops[i].n != tops[j].n {
			return tops[i].n > tops[j].n
		}
		return tops[i].path < tops[j].path
	})
	if len(tops) > 10 {
		tops = tops[:10]
	}
	fmt.Printf("\n%s  top pages%s\n\n", cBold, cReset)
	maxTop := tops[0].n
	for i, t := range tops {
		fmt.Printf("  %s%2d.%s %-28s %s%s%s %d\n", cDim, i+1, cReset, trunc(t.path, 28), cPink, bar(t.n, maxTop, 20), cReset, t.n)
	}
	fmt.Println()
	return nil
}

func probeHealth(cfg *config.Config) (bool, time.Duration) {
	if !cfg.HTTP.Enabled {
		return false, 0
	}
	addr := cfg.HTTP.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	start := time.Now()
	client := &http.Client{Timeout: 3 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200, time.Since(start)
}

func onionOf(cfg *config.Config) string {
	if cfg.Tor.Onion != "" {
		return cfg.Tor.Onion
	}
	if cfg.Tor.Enabled {
		raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "tor", "hidden_service", "hostname"))
		if err == nil {
			return strings.TrimSpace(string(raw))
		}
	}
	return ""
}

func bar(n, max int64, width int) string {
	if max <= 0 {
		return ""
	}
	w := int(n * int64(width) / max)
	if w == 0 && n > 0 {
		w = 1
	}
	return strings.Repeat("█", w)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
