package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jclement/starpulse/internal/site"
)

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.34.0", "v0.33.0", true},
		{"v0.33.0", "v0.33.0", false}, // equal is not newer
		{"v0.33.0", "v0.34.0", false}, // older
		{"v1.0.0", "v0.99.99", true},
		{"v0.33.1", "v0.33.0", true},
		{"v0.34.0-rc1", "v0.33.0", true}, // pre-release suffix tolerated
		{"garbage", "v0.33.0", false},    // unparseable, not greater
	}
	for _, c := range cases {
		if got := versionNewer(c.a, c.b); got != c.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUpdateBannerHTML(t *testing.T) {
	// a newer release shows a banner naming both versions
	b := updateBannerHTML("v0.33.0", "v0.34.0")
	if !strings.Contains(b, "v0.34.0") || !strings.Contains(b, "v0.33.0") {
		t.Errorf("banner missing versions: %q", b)
	}
	if !strings.Contains(b, "self-update") {
		t.Errorf("banner should tell the operator how to update: %q", b)
	}
	// current, ahead, empty, and dev builds show nothing
	for _, c := range []struct{ current, latest string }{
		{"v0.33.0", "v0.33.0"}, // current
		{"v0.34.0", "v0.33.0"}, // running ahead of latest
		{"v0.33.0", ""},        // check hasn't answered
		{"dev", "v9.9.9"},      // unversioned local build
		{"", "v9.9.9"},         // no build version
	} {
		if got := updateBannerHTML(c.current, c.latest); got != "" {
			t.Errorf("updateBannerHTML(%q, %q) = %q, want empty", c.current, c.latest, got)
		}
	}
}

// TestUpdateCheckerNonBlocking: latestTag returns immediately with the cached
// value and refreshes in the background; it never waits on the network.
func TestUpdateCheckerNonBlocking(t *testing.T) {
	hits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer srv.Close()

	u := &updateChecker{}
	// first call: cache empty and stale -> returns "" now, refresh in the bg
	if got := u.latestTagFrom(srv.Client(), srv.URL); got != "" {
		t.Fatalf("first call should return empty immediately, got %q", got)
	}
	select {
	case <-hits:
	case <-time.After(3 * time.Second):
		t.Fatal("background refresh never hit the endpoint")
	}
	// give refresh a moment to store the result, then the cached value shows
	deadline := time.Now().Add(3 * time.Second)
	for {
		u.mu.Lock()
		got := u.latest
		u.mu.Unlock()
		if got == "v9.9.9" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache never populated; latest=%q", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// a fresh cache is not re-fetched
	if got := u.latestTagFrom(srv.Client(), srv.URL); got != "v9.9.9" {
		t.Fatalf("cached call = %q, want v9.9.9", got)
	}
	select {
	case <-hits:
		t.Fatal("fresh cache should not have triggered another fetch")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestUpdateBannerOnAdminPage: a logged-in admin page shows the banner when a
// newer release is cached, and nothing when the running build is current. The
// cache is pre-seeded with nextCheck in the future so no network call happens.
func TestUpdateBannerOnAdminPage(t *testing.T) {
	saved := site.BuildVersion
	site.BuildVersion = "v0.33.0"
	defer func() { site.BuildVersion = saved }()

	srv, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	client := login(t, ts, testPassword)

	// seed a newer release into the cache, frozen (no refresh)
	u := srv.updates()
	u.mu.Lock()
	u.latest = "v0.34.0"
	u.nextCheck = time.Now().Add(time.Hour)
	u.mu.Unlock()

	body := adminGet(t, client, ts.URL+"/admin")
	if !strings.Contains(body, "update-banner") || !strings.Contains(body, "v0.34.0") {
		t.Errorf("admin page missing update banner for newer release")
	}

	// now the cache says we are current — no banner
	u.mu.Lock()
	u.latest = "v0.33.0"
	u.mu.Unlock()
	if body := adminGet(t, client, ts.URL+"/admin"); strings.Contains(body, "update-banner") {
		t.Errorf("banner shown while running the current release")
	}
}

func adminGet(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
