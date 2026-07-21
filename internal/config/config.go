// Package config loads starpulse configuration from a YAML file with
// environment-variable overrides (env wins over file, file wins over default).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Service is one network listener toggle.
type Service struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

// HTTPSService adds ACME settings to a listener.
type HTTPSService struct {
	Service   `yaml:",inline"`
	ACME      bool   `yaml:"acme"`
	ACMEEmail string `yaml:"acme_email"`
}

// SSHService configures the SSH TUI door.
type SSHService struct {
	Service `yaml:",inline"`
	// AuthorizedKeys holds public keys (authorized_keys format) allowed to
	// log in as admin. When any are set, admin PASSWORD auth over SSH is
	// disabled — keys only. Guests are unaffected.
	AuthorizedKeys []string `yaml:"authorized_keys"`
}

// Feed is one Atom feed: where it is served, and what it draws from.
type Feed struct {
	// Path is the URL the feed is served at, e.g. "/feed.xml".
	Path string `yaml:"path"`
	// Source is a folder of dated pages ("/posts/"), "/" for the whole
	// site, or the literal "now" for the notes folder.
	Source string `yaml:"source"`
	// Page is the human-readable page this feed represents (used for the
	// alternate link and the feed id). Defaults to Source, or "/".
	Page     string `yaml:"page"`
	Title    string `yaml:"title"`
	Subtitle string `yaml:"subtitle"`
	// Author overrides feeds.author for this feed.
	Author string `yaml:"author"`
	Limit  int    `yaml:"limit"`
}

// Feeds configures the Atom feeds this site publishes.
type Feeds struct {
	// Author is the name used in every feed's <author>.
	Author string `yaml:"author"`
	// Limit caps entries per feed when a feed does not set its own.
	Limit int `yaml:"limit"`
	// List holds any other feeds (a site-wide one, for instance). Folder
	// feeds are not listed here — they are turned on per folder.
	List []Feed `yaml:"list"`
}

// Highlight configures server-side syntax highlighting of fenced code
// blocks on the web rendering. Gemini always gets the plain text.
type Highlight struct {
	Enabled bool `yaml:"enabled"`
	// Style / DarkStyle are chroma palette names.
	Style     string `yaml:"style"`
	DarkStyle string `yaml:"dark_style"`
}

// Titan configures titan:// uploads over the gemini listener.
type Titan struct {
	Enabled bool `yaml:"enabled"`
	// CertFingerprints is the allowlist of SHA-256 client-certificate
	// fingerprints (hex, case/colon-insensitive) permitted to write.
	CertFingerprints []string `yaml:"cert_fingerprints"`
}

// Tor configures the managed tor hidden service.
type Tor struct {
	Enabled bool   `yaml:"enabled"`
	Binary  string `yaml:"binary"` // tor executable, default "tor"
	// Onion overrides the hidden-service hostname when tor is managed
	// outside starpulse (e.g. a system tor with its own HiddenServiceDir).
	Onion string `yaml:"onion"`
}

// Config is the full runtime configuration.
type Config struct {
	Hostname string `yaml:"hostname"`
	// AdminPassword authorizes web login, /api and /mcp bearer tokens.
	// Plaintext, or a bcrypt hash (starts with $2).
	AdminPassword string `yaml:"admin_password"`
	DataDir       string `yaml:"data_dir"`

	Gemini Service      `yaml:"gemini"`
	HTTP   Service      `yaml:"http"`
	HTTPS  HTTPSService `yaml:"https"`
	// SSH serves a TUI gemini browser (and, for the admin user, a
	// full-screen editor) over ssh.
	SSH SSHService `yaml:"ssh"`
	// Telnet serves the same TUI browser read-only (guest, no auth).
	Telnet Service `yaml:"telnet"`

	Titan Titan `yaml:"titan"`
	Tor   Tor   `yaml:"tor"`

	// Feeds are the Atom feeds published by this site.
	Feeds Feeds `yaml:"feeds"`

	// Highlight controls syntax highlighting of code blocks.
	Highlight Highlight `yaml:"highlight"`

	// NowFolder is where {{now}} and the note-posting doors (API, MCP, ssh,
	// titan) write. It is an ordinary folder of ordinary pages; posting
	// without a filename simply generates one.
	NowFolder string `yaml:"now_folder"`

	// Timezone is an IANA zone name (e.g. "America/Edmonton") used when
	// rendering timestamps (notes, {{updated}}, admin displays).
	// Empty = the server's local time.
	Timezone string `yaml:"timezone"`

	// OAuthRedirectHosts are the hosts an MCP client may be sent back to
	// after authorizing. Loopback addresses and this site's own hostname are
	// always allowed; everything else must be named here, because a callback
	// host is where the resulting admin credential lands. Defaults to the
	// hosted Claude clients; set it to replace that list.
	OAuthRedirectHosts []string `yaml:"oauth_redirect_hosts"`

	// MaxUploadBytes caps a single file upload (web, api, mcp, titan).
	MaxUploadBytes int64 `yaml:"max_upload_bytes"`
	// KeepVersions is how many historical versions to retain per page.
	KeepVersions int `yaml:"keep_versions"`

	// Path the config was loaded from ("" if defaults only).
	Source string `yaml:"-"`
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Hostname: "localhost",
		DataDir:  "data",
		Gemini:   Service{Enabled: true, Addr: ":1965"},
		HTTP:     Service{Enabled: true, Addr: ":80"},
		HTTPS:    HTTPSService{Service: Service{Enabled: false, Addr: ":443"}, ACME: true},
		SSH:      SSHService{Service: Service{Enabled: false, Addr: ":2222"}},
		Telnet:   Service{Enabled: false, Addr: ":23"},
		Titan:    Titan{Enabled: false},
		Tor:      Tor{Enabled: false, Binary: "tor"},

		Feeds:     Feeds{Limit: 30},
		NowFolder: "/now/",
		Highlight: Highlight{Enabled: true, Style: "github", DarkStyle: "github-dark"},

		// the hosted Claude clients this MCP endpoint exists for; desktop
		// clients use loopback, which is always allowed. Setting the key in
		// config replaces this list.
		OAuthRedirectHosts: []string{"claude.ai", "claude.com"},

		MaxUploadBytes: 10 << 20,
		KeepVersions:   25,
	}
}

// SearchPaths returns the config file locations checked in order.
func SearchPaths() []string {
	paths := []string{}
	if v := os.Getenv("STARPULSE_CONFIG"); v != "" {
		paths = append(paths, v)
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "starpulse", "config.yaml"))
	}
	paths = append(paths, "/etc/starpulse/config.yaml")
	return paths
}

// Load reads configuration: explicit path (may be "") → search paths → env.
func Load(explicit string) (*Config, error) {
	c := Default()

	path := explicit
	if path == "" {
		for _, p := range SearchPaths() {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, c); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
		c.Source = path
	}

	applyEnv(c)

	if c.Tor.Binary == "" {
		c.Tor.Binary = "tor"
	}
	if c.KeepVersions <= 0 {
		c.KeepVersions = 25
	}
	if c.MaxUploadBytes <= 0 {
		c.MaxUploadBytes = 10 << 20
	}
	return c, nil
}

func applyEnv(c *Config) {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := os.LookupEnv(key); ok {
			switch strings.ToLower(v) {
			case "1", "true", "yes", "on":
				*dst = true
			case "0", "false", "no", "off":
				*dst = false
			}
		}
	}
	i64 := func(key string, dst *int64) {
		if v, ok := os.LookupEnv(key); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				*dst = n
			}
		}
	}

	str("STARPULSE_HOSTNAME", &c.Hostname)
	str("STARPULSE_TIMEZONE", &c.Timezone)
	str("STARPULSE_NOW_FOLDER", &c.NowFolder)
	str("STARPULSE_ADMIN_PASSWORD", &c.AdminPassword)
	str("STARPULSE_DATA_DIR", &c.DataDir)

	boolean("STARPULSE_GEMINI", &c.Gemini.Enabled)
	str("STARPULSE_GEMINI_ADDR", &c.Gemini.Addr)
	boolean("STARPULSE_HTTP", &c.HTTP.Enabled)
	str("STARPULSE_HTTP_ADDR", &c.HTTP.Addr)
	boolean("STARPULSE_HTTPS", &c.HTTPS.Enabled)
	str("STARPULSE_HTTPS_ADDR", &c.HTTPS.Addr)
	boolean("STARPULSE_ACME", &c.HTTPS.ACME)
	str("STARPULSE_ACME_EMAIL", &c.HTTPS.ACMEEmail)

	boolean("STARPULSE_SSH", &c.SSH.Enabled)
	str("STARPULSE_SSH_ADDR", &c.SSH.Addr)
	boolean("STARPULSE_TELNET", &c.Telnet.Enabled)
	str("STARPULSE_TELNET_ADDR", &c.Telnet.Addr)

	boolean("STARPULSE_TITAN", &c.Titan.Enabled)
	if v, ok := os.LookupEnv("STARPULSE_TITAN_CERTS"); ok {
		c.Titan.CertFingerprints = strings.Split(v, ",")
	}

	boolean("STARPULSE_TOR", &c.Tor.Enabled)
	str("STARPULSE_TOR_BINARY", &c.Tor.Binary)
	str("STARPULSE_ONION", &c.Tor.Onion)

	i64("STARPULSE_MAX_UPLOAD_BYTES", &c.MaxUploadBytes)
}

// EffectiveFeeds returns the explicitly configured feeds with defaults
// filled in. Folder feeds are not listed here — those are turned on per
// folder and discovered from the store.
func (c *Config) EffectiveFeeds() []Feed {
	list := c.Feeds.List
	out := make([]Feed, 0, len(list))
	for _, f := range list {
		if f.Path == "" {
			continue // a feed with nowhere to live is ignored
		}
		if !strings.HasPrefix(f.Path, "/") {
			f.Path = "/" + f.Path
		}
		if f.Source == "" {
			f.Source = "/"
		}
		if f.Page == "" {
			f.Page = f.Source
		}
		if f.Title == "" {
			f.Title = c.Hostname
		}
		if f.Limit <= 0 {
			f.Limit = c.Feeds.Limit
		}
		if f.Limit <= 0 {
			f.Limit = 30
		}
		out = append(out, f)
	}
	return out
}

// NormalizedFingerprints returns the titan cert allowlist lowercased with
// colons/whitespace stripped.
func (c *Config) NormalizedFingerprints() []string {
	var out []string
	for _, fp := range c.Titan.CertFingerprints {
		fp = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(fp, ":", "")))
		if fp != "" {
			out = append(out, fp)
		}
	}
	return out
}

// Validate checks the config for fatal problems before serving.
func (c *Config) Validate() error {
	if c.Hostname == "" {
		return fmt.Errorf("hostname must be set")
	}
	if _, err := c.Location(); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Timezone, err)
	}
	if c.Titan.Enabled && len(c.NormalizedFingerprints()) == 0 {
		return fmt.Errorf("titan enabled but no cert_fingerprints configured")
	}
	if !c.Gemini.Enabled && !c.HTTP.Enabled && !c.HTTPS.Enabled && !c.SSH.Enabled && !c.Telnet.Enabled {
		return fmt.Errorf("no services enabled")
	}
	return nil
}

// Location resolves the configured timezone (server local when unset).
func (c *Config) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.Local, nil
	}
	return time.LoadLocation(c.Timezone)
}

// Sample renders an annotated sample config.
func Sample(hostname, password, dataDir string) string {
	return fmt.Sprintf(`# starpulse configuration
# Everything here can be overridden with STARPULSE_* environment variables.

hostname: %s

# Hosts an MCP client may be redirected back to after you authorize it.
# Loopback and this site's own hostname are always allowed, as are the hosted
# Claude clients by default. Setting this replaces that default list — a
# callback host receives a credential with admin rights, so name only hosts
# you mean to trust.
# oauth_redirect_hosts:
#   - claude.ai

# Password for the web admin UI, /api and /mcp bearer tokens.
# Plaintext, or a bcrypt hash (generate one with: starpulse hash-password)
admin_password: %q

# Persistent state: starpulse.sqlite, TLS certs, tor keys.
data_dir: %s

gemini:
  enabled: true
  addr: ":1965"

http:
  enabled: true
  addr: ":80"

https:
  enabled: true
  addr: ":443"
  acme: true          # Let's Encrypt via tls-alpn / http-01 (needs ports 80+443)
  acme_email: ""

# SSH door: a TUI gemini browser for anyone (ssh guest@host -p 2222), plus
# full-screen editing when logging in as admin with the admin password.
ssh:
  enabled: false
  addr: ":2222"
  # Public keys allowed to log in as admin. When any are listed, admin
  # password auth over SSH is DISABLED (keys only). Guests are unaffected.
  authorized_keys: []
  #  - "ssh-ed25519 AAAA... you@laptop"

# Telnet door: same TUI browser, read-only guest, no encryption — retro fun.
telnet:
  enabled: false
  addr: ":23"

# titan:// uploads (edit content from a gemini client with a client cert).
titan:
  enabled: false
  cert_fingerprints: []   # sha256 hex fingerprints of allowed client certs

# Tor hidden service. Runs a private tor instance (requires a tor binary)
# and registers both HTTP and gemini automatically.
tor:
  enabled: false
  binary: tor
  # onion: xyz.onion     # set instead if tor is managed outside starpulse

# Atom feeds. Folders publish a feed when you turn one on in the admin (it
# writes a .feed file there); these entries are for anything else — the
# notes folder, or a site-wide feed. On gemini the idiomatic feed is just
# a page of dated link lines, which {{list}} already emits.
feeds:
  author: ""
  limit: 30
  # feeds beyond the per-folder ones, e.g. a site-wide feed of every dated page
  list: []

# Short notes ({{now}}, the "post a note" button) are pages in this folder.
now_folder: /now/

# Syntax highlighting for fenced code blocks on the web. The language comes
# from the text after the opening fence (go, python, sh, ...). Palettes are
# chroma style names.
highlight:
  enabled: true
  style: github
  dark_style: github-dark

# IANA timezone for displayed timestamps (notes, {{updated}}, admin).
# Empty = server local time.
timezone: ""

max_upload_bytes: 10485760
keep_versions: 25
`, hostname, password, dataDir)
}
