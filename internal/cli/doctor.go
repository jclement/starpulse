package cli

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"path"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/gemtext"
	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

type check struct {
	ok   bool
	warn bool
	msg  string
}

// Doctor runs connectivity and configuration checks, printing a ✓/✗ report.
// Returns an error if any hard check fails.
func Doctor(cfg *config.Config) error {
	var checks []check
	add := func(ok bool, format string, args ...any) {
		checks = append(checks, check{ok: ok, msg: fmt.Sprintf(format, args...)})
	}
	warn := func(format string, args ...any) {
		checks = append(checks, check{ok: true, warn: true, msg: fmt.Sprintf(format, args...)})
	}

	fmt.Printf("\n%s%s🩺 starpulse doctor%s %s@ %s%s\n\n", cBold, cPink, cReset, cDim, cfg.Hostname, cReset)

	// config
	if cfg.Source != "" {
		add(true, "config loaded from %s", cfg.Source)
	} else {
		warn("no config file found — running on defaults + env (looked in: %s)", strings.Join(config.SearchPaths(), ", "))
	}
	if err := cfg.Validate(); err != nil {
		add(false, "config invalid: %v", err)
	} else {
		add(true, "config valid")
	}
	if cfg.AdminPassword == "" {
		warn("admin_password not set — web/api/mcp editing is disabled")
	} else if strings.HasPrefix(cfg.AdminPassword, "$2") {
		add(true, "admin_password set (bcrypt hash)")
	} else {
		warn("admin_password is plaintext — consider 'starpulse hash-password'")
	}

	// data dir + database
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		add(false, "data dir %s not writable: %v", cfg.DataDir, err)
	} else if probe := filepath.Join(cfg.DataDir, ".doctor-probe"); true {
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			add(false, "data dir %s not writable: %v", cfg.DataDir, err)
		} else {
			os.Remove(probe)
			add(true, "data dir writable (%s)", cfg.DataDir)
		}
	}
	dbPath := filepath.Join(cfg.DataDir, "starpulse.sqlite")
	if st, err := store.Open(dbPath); err != nil {
		add(false, "database %s: %v", dbPath, err)
	} else {
		pages, _, _ := st.Totals()
		st.Close()
		if pages == 0 {
			warn("database opens but has no pages yet (a starter site is seeded on first serve)")
		} else {
			add(true, "database opens (%d pages)", pages)
		}
	}

	// listeners
	if cfg.HTTP.Enabled {
		ok, latency := probeHealth(cfg)
		if ok {
			add(true, "http %s healthy (%s)", cfg.HTTP.Addr, latency.Round(time.Millisecond))
		} else {
			add(false, "http %s not responding — service down?", cfg.HTTP.Addr)
		}
	}
	if cfg.HTTPS.Enabled {
		if exp, err := tlsExpiry(localAddr(cfg.HTTPS.Addr), cfg.Hostname); err != nil {
			add(false, "https %s: %v", cfg.HTTPS.Addr, err)
		} else {
			left := time.Until(exp)
			if left < 14*24*time.Hour {
				warn("https certificate expires soon: %s (%s left)", exp.Format("2006-01-02"), left.Round(time.Hour))
			} else {
				add(true, "https %s cert ok (expires %s)", cfg.HTTPS.Addr, exp.Format("2006-01-02"))
			}
		}
	}
	if cfg.Gemini.Enabled {
		if exp, err := tlsExpiry(localAddr(cfg.Gemini.Addr), cfg.Hostname); err != nil {
			add(false, "gemini %s: %v", cfg.Gemini.Addr, err)
		} else {
			add(true, "gemini %s cert ok (expires %s)", cfg.Gemini.Addr, exp.Format("2006-01-02"))
		}
	}

	if cfg.SSH.Enabled {
		conn, err := net.DialTimeout("tcp", localAddr(cfg.SSH.Addr), 3*time.Second)
		if err != nil {
			add(false, "ssh %s not reachable: %v", cfg.SSH.Addr, err)
		} else {
			conn.Close()
			add(true, "ssh %s reachable", cfg.SSH.Addr)
		}
	}

	if cfg.Telnet.Enabled {
		conn, err := net.DialTimeout("tcp", localAddr(cfg.Telnet.Addr), 3*time.Second)
		if err != nil {
			add(false, "telnet %s not reachable: %v", cfg.Telnet.Addr, err)
		} else {
			conn.Close()
			add(true, "telnet %s reachable", cfg.Telnet.Addr)
		}
	}

	// dns
	if cfg.Hostname != "localhost" {
		if addrs, err := net.LookupHost(cfg.Hostname); err != nil {
			warn("hostname %s does not resolve: %v", cfg.Hostname, err)
		} else {
			add(true, "hostname %s resolves (%s)", cfg.Hostname, strings.Join(addrs, ", "))
		}
	}

	// titan
	if cfg.Titan.Enabled {
		add(len(cfg.NormalizedFingerprints()) > 0, "titan enabled with %d authorized cert(s)", len(cfg.NormalizedFingerprints()))
	}

	// tor
	if cfg.Tor.Enabled {
		if _, err := exec.LookPath(cfg.Tor.Binary); err != nil {
			add(false, "tor enabled but binary %q not found in PATH", cfg.Tor.Binary)
		} else {
			add(true, "tor binary found (%s)", cfg.Tor.Binary)
		}
	}
	if o := onionOf(cfg); o != "" {
		add(true, "onion mirror: %s", o)
	}

	// report
	failed := 0
	for _, c := range checks {
		switch {
		case !c.ok:
			failed++
			fmt.Printf("  %s✗%s %s\n", cRed, cReset, c.msg)
		case c.warn:
			fmt.Printf("  %s!%s %s\n", cAccent, cReset, c.msg)
		default:
			fmt.Printf("  %s✓%s %s\n", cGreen, cReset, c.msg)
		}
	}
	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Printf("%sall checks passed%s\n\n", cGreen, cReset)
	return nil
}

func localAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

// tlsExpiry dials a TLS listener and returns the leaf certificate expiry.
func tlsExpiry(addr, serverName string) (time.Time, error) {
	d := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true,
	})
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, fmt.Errorf("no certificate presented")
	}
	return certs[0].NotAfter, nil
}

// DoctorLinks walks every gemtext page and reports internal => links whose
// target does not resolve — the health check that fits a link-per-line
// format best. External links (with a scheme) are left alone.
func DoctorLinks(cfg *config.Config) error {
	st, err := store.Open(filepath.Join(cfg.DataDir, "starpulse.sqlite"))
	if err != nil {
		return err
	}
	defer st.Close()
	sy := site.New(st)

	metas, err := st.ListAll()
	if err != nil {
		return err
	}
	fmt.Printf("\n%s%s🔗 starpulse link check%s %s@ %s%s\n\n", cBold, cPink, cReset, cDim, cfg.Hostname, cReset)

	var checked, dead int
	for _, m := range metas {
		if m.Binary || store.Hidden(m.Path) || !strings.HasSuffix(m.Path, ".gmi") {
			continue
		}
		pg, err := st.GetPage(m.Path)
		if err != nil {
			continue
		}
		from := strings.TrimSuffix(m.Path, ".gmi") // the page's served URL
		for _, line := range gemtext.Parse(string(pg.Content)) {
			if line.Type != gemtext.Link {
				continue
			}
			u := line.URL
			if u == "" || strings.Contains(u, "://") || strings.HasPrefix(u, "//") ||
				strings.HasPrefix(u, "mailto:") || strings.HasPrefix(u, "#") {
				continue // external or non-navigational
			}
			target := u
			if i := strings.IndexAny(target, "?#"); i >= 0 {
				target = target[:i]
			}
			if !strings.HasPrefix(target, "/") {
				target = path.Join(path.Dir(from), target) // relative to the page
			}
			// endpoints served outside the page resolver: search, and the
			// per-folder Atom feeds. site.Resolve does not know them, so
			// they would read as dead when they are not.
			if target == "/search" || target == "/search/" || strings.HasSuffix(target, "/feed.xml") || target == "/feed.xml" {
				continue
			}
			checked++
			if sy.Resolve(target, "").Type == site.NotFound {
				dead++
				fmt.Printf("  %s✗%s %s%s%s  →  %s\n", cRed, cReset, cDim, from, cReset, u)
			}
		}
	}

	fmt.Println()
	if dead == 0 {
		fmt.Printf("%s%d internal links, all resolve%s\n\n", cGreen, checked, cReset)
		return nil
	}
	fmt.Printf("%s%d of %d internal links are dead%s\n\n", cRed, dead, checked, cReset)
	return fmt.Errorf("%d dead link(s)", dead)
}
