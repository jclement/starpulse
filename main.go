// starpulse — a single-binary smolweb CMS.
//
// One SQLite database, four doors in: gemini (+titan editing), HTTP, HTTPS
// and an optional tor hidden service. Content is gemtext, authored in the
// no-frills web admin, over titan, or through the /api and /mcp endpoints.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/term"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/cli"
	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/site"
)

const usage = `starpulse — a single-binary smolweb CMS

Usage:
  starpulse serve          run the server (default command)
  starpulse install        install as a systemd service (Linux, root)
  starpulse uninstall      remove the systemd service [--purge|--yes]
  starpulse self-update    update from the latest GitHub release
  starpulse import <dir>   import a content directory into the database
  starpulse hash-password  bcrypt-hash a password for config.yaml
  starpulse version        print version
  starpulse health         exit 0 if the local server is healthy

Options:
  -config <path>           config file (default: $STARPULSE_CONFIG,
                           ~/.config/starpulse/config.yaml, /etc/starpulse/config.yaml)
`

func main() {
	logger := log.NewWithOptions(os.Stderr, log.Options{
		ReportTimestamp: true,
		TimeFormat:      "15:04:05",
	})

	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "", "config file path")
	hostname := fs.String("hostname", "", "hostname (install)")
	purge := fs.Bool("purge", false, "uninstall: also remove config and data")
	yes := fs.Bool("yes", false, "uninstall: no prompts, keep data")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	var rest []string
	for len(args) > 0 {
		if isFlag(args[0]) {
			_ = fs.Parse(args)
			args = nil
		} else {
			rest = append(rest, args[0])
			args = args[1:]
		}
	}

	run := func(err error) {
		if err != nil {
			logger.Fatal(cmd, "err", err)
		}
	}

	switch cmd {
	case "serve":
		cfg, err := config.Load(*cfgPath)
		run(err)
		run(cli.Serve(cfg, logger))
	case "install":
		run(cli.Install(*hostname))
	case "uninstall":
		run(cli.Uninstall(*purge, *yes))
	case "self-update", "selfupdate", "update":
		run(cli.SelfUpdate())
	case "import":
		if len(rest) != 1 {
			logger.Fatal("usage: starpulse import <content-dir>")
		}
		cfg, err := config.Load(*cfgPath)
		run(err)
		run(cli.Import(cfg, logger, rest[0]))
	case "hash-password":
		run(hashPassword())
	case "version":
		v := site.BuildVersion
		if site.BuildDate != "" {
			v += " (" + site.BuildDate + ")"
		}
		fmt.Println("starpulse", v)
	case "health":
		os.Exit(healthCheck())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func hashPassword() error {
	fmt.Fprint(os.Stderr, "password: ")
	raw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("empty password")
	}
	h, err := auth.Hash(string(raw))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

// healthCheck hits the local web listener's /healthz (docker HEALTHCHECK).
func healthCheck() int {
	url := os.Getenv("STARPULSE_HEALTH_URL")
	if url == "" {
		url = "http://127.0.0.1:80/healthz"
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintln(os.Stderr, "unhealthy: status", resp.StatusCode)
		return 1
	}
	fmt.Println("ok")
	return 0
}
