package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/site"
)

const releasesURL = "https://api.github.com/repos/jclement/starpulse/releases/latest"

// SelfUpdate downloads the latest GitHub release binary for this platform
// and atomically replaces the running executable.
func SelfUpdate() error {
	client := &http.Client{Timeout: 60 * time.Second}

	resp, err := client.Get(releasesURL)
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("checking latest release: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return err
	}

	current := site.BuildVersion
	if rel.TagName == current {
		fmt.Printf("already up to date (%s)\n", current)
		return nil
	}

	want := fmt.Sprintf("starpulse_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	assetURL := ""
	for _, a := range rel.Assets {
		if a.Name == want {
			assetURL = a.URL
			break
		}
	}
	if assetURL == "" {
		return fmt.Errorf("release %s has no asset %s", rel.TagName, want)
	}
	fmt.Printf("updating %s → %s (%s)\n", current, rel.TagName, want)

	dl, err := client.Get(assetURL)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != 200 {
		return fmt.Errorf("downloading: HTTP %d", dl.StatusCode)
	}

	gz, err := gzip.NewReader(dl.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var binReader io.Reader
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if strings.TrimPrefix(hdr.Name, "./") == "starpulse" {
			binReader = tr
			break
		}
	}
	if binReader == nil {
		return fmt.Errorf("archive %s does not contain a 'starpulse' binary", want)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	tmp := self + ".update"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("writing update (need write access to %s — try sudo): %w", self, err)
	}
	if _, err := io.Copy(out, binReader); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, self); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", self, err)
	}
	fmt.Printf("updated to %s — restart the service to pick it up (systemctl restart starpulse)\n", rel.TagName)
	return nil
}
