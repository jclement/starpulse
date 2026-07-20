package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/config"
)

const (
	installBin  = "/opt/starpulse/bin/starpulse"
	configDir   = "/etc/starpulse"
	dataDir     = "/var/lib/starpulse"
	unitPath    = "/etc/systemd/system/starpulse.service"
	serviceUser = "starpulse"
)

const systemdUnit = `[Unit]
Description=starpulse smolweb server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=` + serviceUser + `
Group=` + serviceUser + `
ExecStart=` + installBin + ` serve
Restart=on-failure
RestartSec=3

# bind :80/:443/:1965 without running as root
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

# hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=` + dataDir + `

[Install]
WantedBy=multi-user.target
`

// Install sets starpulse up as a systemd service (Linux, root required).
func Install(hostname string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("install only supports Linux (this is %s); run 'starpulse serve' directly instead", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("install must run as root (try: sudo starpulse install)")
	}
	if hostname == "" {
		if h, err := os.Hostname(); err == nil {
			hostname = h
		} else {
			hostname = "localhost"
		}
	}

	// 1. binary
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(installBin), 0o755); err != nil {
		return err
	}
	if self != installBin {
		if err := copyFile(self, installBin, 0o755); err != nil {
			return fmt.Errorf("installing binary: %w", err)
		}
	}
	step("installed binary → " + installBin)

	// 2. service user
	if _, err := exec.Command("id", "-u", serviceUser).Output(); err != nil {
		if out, err := exec.Command("useradd", "--system", "--home-dir", dataDir,
			"--shell", "/usr/sbin/nologin", serviceUser).CombinedOutput(); err != nil {
			return fmt.Errorf("creating user %s: %v (%s)", serviceUser, err, out)
		}
		step("created system user " + serviceUser)
	}

	// 3. config
	password := ""
	cfgPath := filepath.Join(configDir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			return err
		}
		password = auth.RandomPassword()
		if err := os.WriteFile(cfgPath, []byte(config.Sample(hostname, password, dataDir)), 0o640); err != nil {
			return err
		}
		if err := chown(cfgPath, serviceUser); err != nil {
			return err
		}
		step("wrote sample config → " + cfgPath)
	} else {
		step("kept existing config " + cfgPath)
	}

	// 4. data dir
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := chown(dataDir, serviceUser); err != nil {
		return err
	}
	step("data dir ready → " + dataDir)

	// 5. systemd
	if err := os.WriteFile(unitPath, []byte(systemdUnit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "--now", "starpulse"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v (%s)", strings.Join(args, " "), err, out)
		}
	}
	step("systemd service enabled and started")

	fmt.Println()
	fmt.Println("starpulse is installed.")
	fmt.Println("  config:  " + cfgPath)
	fmt.Println("  data:    " + dataDir)
	fmt.Println("  status:  systemctl status starpulse")
	fmt.Println("  logs:    journalctl -u starpulse -f")
	if password != "" {
		fmt.Println()
		fmt.Println("  admin password: " + password)
		fmt.Println("  (also in " + cfgPath + " — change it or replace with a bcrypt hash via 'starpulse hash-password')")
	}
	return nil
}

// Uninstall removes the service and binary, prompting about config/data.
func Uninstall(purge, yes bool) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("uninstall only supports Linux")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("uninstall must run as root (try: sudo starpulse uninstall)")
	}
	_ = exec.Command("systemctl", "disable", "--now", "starpulse").Run()
	_ = os.Remove(unitPath)
	_ = exec.Command("systemctl", "daemon-reload").Run()
	step("service stopped and removed")

	_ = os.RemoveAll("/opt/starpulse")
	step("removed /opt/starpulse")

	removeData := purge
	if !purge && !yes {
		fmt.Printf("Also delete config (%s) and data (%s)? Your entire site lives in the data dir. [y/N] ", configDir, dataDir)
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		removeData = strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
	}
	if removeData {
		_ = os.RemoveAll(configDir)
		_ = os.RemoveAll(dataDir)
		step("removed config and data")
	} else {
		step("kept config and data (remove later with: rm -rf " + configDir + " " + dataDir + ")")
	}
	fmt.Println("starpulse uninstalled.")
	return nil
}

func step(msg string) { fmt.Println("==> " + msg) }

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func chown(path, user string) error {
	out, err := exec.Command("chown", "-R", user+":"+user, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("chown %s: %v (%s)", path, err, out)
	}
	return nil
}
