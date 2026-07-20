package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // don't pick up a real ~/.config
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Hostname != "localhost" || !c.Gemini.Enabled || c.Gemini.Addr != ":1965" {
		t.Errorf("defaults wrong: %+v", c)
	}
}

func TestLoadFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(`
hostname: example.org
admin_password: pw
titan:
  enabled: true
  cert_fingerprints: ["AA:BB:cc", "dd"]
https:
  enabled: true
  acme: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STARPULSE_HOSTNAME", "env.example")
	t.Setenv("STARPULSE_HTTP", "false")

	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hostname != "env.example" {
		t.Errorf("env override failed: %q", c.Hostname)
	}
	if c.HTTP.Enabled {
		t.Error("env bool override failed")
	}
	if !c.HTTPS.Enabled || c.HTTPS.ACME {
		t.Errorf("https block wrong: %+v", c.HTTPS)
	}
	fps := c.NormalizedFingerprints()
	if len(fps) != 2 || fps[0] != "aabbcc" || fps[1] != "dd" {
		t.Errorf("fingerprints = %v", fps)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	c.Titan.Enabled = true
	if err := c.Validate(); err == nil {
		t.Error("titan without certs validated")
	}
	c = Default()
	c.Gemini.Enabled = false
	c.HTTP.Enabled = false
	c.HTTPS.Enabled = false
	if err := c.Validate(); err == nil {
		t.Error("no services validated")
	}
}

func TestSampleParses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(p, []byte(Sample("h.example", "pw", "/var/lib/starpulse")), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("sample config does not parse: %v", err)
	}
	if c.Hostname != "h.example" || c.AdminPassword != "pw" || c.DataDir != "/var/lib/starpulse" {
		t.Errorf("sample roundtrip: %+v", c)
	}
}
