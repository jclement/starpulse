package web

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jclement/starpulse/internal/site"
)

// updateReleaseURL is the same endpoint `starpulse self-update` reads, so the
// admin banner's "available" means exactly "self-update would act".
const updateReleaseURL = "https://api.github.com/repos/jclement/starpulse/releases/latest"

const (
	updateCheckTTL = 6 * time.Hour    // how long a good result is trusted
	updateRetryTTL = 30 * time.Minute // back-off after a failed check
)

// updateHTTPClient is the (short-timeout, direct) client for the release
// check. GitHub's unauthenticated API allows ~60 requests/hour/IP; a 6h TTL
// keeps us far under it.
var updateHTTPClient = &http.Client{Timeout: 15 * time.Second}

// updateChecker caches the latest published release tag. It never blocks a
// request: a stale cache triggers a background refresh and the last known
// value (or "") is returned immediately. It needs no privilege — it is a
// read-only GET to GitHub — so it is safe from the sandboxed web process, and
// deliberately does not (cannot) apply the update itself.
type updateChecker struct {
	mu        sync.Mutex
	latest    string    // latest release tag, e.g. "v0.34.0"
	nextCheck time.Time // earliest time the cache may be refreshed again
	fetching  bool
}

func (s *Server) updates() *updateChecker {
	s.updOnce.Do(func() { s.updChk = &updateChecker{} })
	return s.updChk
}

// latestTag returns the cached latest release tag (possibly ""), kicking off a
// background refresh when the cache is stale. It returns immediately.
func (u *updateChecker) latestTag(client *http.Client) string {
	return u.latestTagFrom(client, updateReleaseURL)
}

// latestTagFrom is latestTag with the endpoint injected (for tests).
func (u *updateChecker) latestTagFrom(client *http.Client, url string) string {
	u.mu.Lock()
	tag := u.latest
	if time.Now().After(u.nextCheck) && !u.fetching {
		u.fetching = true
		u.mu.Unlock()
		go u.refresh(client, url)
		return tag
	}
	u.mu.Unlock()
	return tag
}

func (u *updateChecker) refresh(client *http.Client, url string) {
	tag, err := fetchLatestTag(client, url)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fetching = false
	if err != nil {
		u.nextCheck = time.Now().Add(updateRetryTTL) // don't hammer on failure
		return
	}
	u.latest = tag
	u.nextCheck = time.Now().Add(updateCheckTTL)
}

func fetchLatestTag(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("release check: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", err
	}
	return strings.TrimSpace(rel.TagName), nil
}

// updateBanner is the admin-only "a newer release exists" callout, or "" when
// the running build is current (or ahead, or an unversioned dev build, or the
// first check has not answered yet). There is no button on purpose: applying
// the update needs privilege the sandboxed service deliberately lacks — run
// `sudo starpulse self-update` on the host.
func (s *Server) updateBanner() string {
	return updateBannerHTML(site.BuildVersion, s.updates().latestTag(updateHTTPClient))
}

// updateBannerHTML is the pure decision + markup, split out so it is testable
// without a network or the build-version global.
func updateBannerHTML(current, latest string) string {
	if current == "" || current == "dev" {
		return "" // unversioned local build — nothing meaningful to compare
	}
	if latest == "" || !versionNewer(latest, current) {
		return ""
	}
	return `<div class="update-banner">` +
		`<span class="ub-mark">&#8593;</span> starpulse <strong>` + html.EscapeString(latest) +
		`</strong> is available — this server runs ` + html.EscapeString(current) + `. ` +
		`Update on the host with <code>sudo starpulse self-update</code>, then restart.` +
		`</div>`
}

// versionNewer reports whether release tag a is strictly newer than b. Both
// look like "v1.2.3"; if either will not parse the comparison falls back to a
// plain inequality (conservative — only flags a genuinely different tag).
func versionNewer(a, b string) bool {
	pa, oka := parseVer(a)
	pb, okb := parseVer(b)
	if !oka || !okb {
		return a != b && a > b
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVer(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		// drop any pre-release/build suffix: "3-rc1" or "3+meta" -> "3"
		if j := strings.IndexAny(p, "-+"); j >= 0 {
			p = p[:j]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
