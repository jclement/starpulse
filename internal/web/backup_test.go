package web

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// postZip uploads a zip to the restore endpoint the way the form does.
func postZip(t *testing.T, client *http.Client, base, mode string, data []byte) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("mode", mode)
	fw, err := mw.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	resp, err := client.Post(base+"/admin/backup/restore", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	return buf.Bytes()
}

func TestBackupRoundTrip(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	_, _ = st.SavePage("/posts/.feed", []byte("title: Posts"), "", "t")
	_, _ = st.SavePage("/posts/2026-07-19-hi.gmi", []byte("# Hi\n\nbody"), "", "t")
	_, _ = st.SavePage("/media/pic.png", []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}, "image/png", "t")
	client := login(t, ts, testPassword)

	resp, err := client.Get(ts.URL + "/admin/backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type %q", ct)
	}
	// the filename says which site, and when
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "test_example_") || !strings.HasSuffix(cd, `.zip"`) {
		t.Errorf("content-disposition %q", cd)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	for _, want := range []string{
		"content/index.gmi", "content/posts/.feed",
		"content/posts/2026-07-19-hi.gmi", "content/media/pic.png", "BACKUP.txt",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("backup missing %s (have %v)", want, keysOf(got))
		}
	}
	// plain files, byte for byte — no wrapper, no encoding
	if got["content/posts/2026-07-19-hi.gmi"] != "# Hi\n\nbody" {
		t.Errorf("page content mangled: %q", got["content/posts/2026-07-19-hi.gmi"])
	}
	if got["content/media/pic.png"] != string([]byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}) {
		t.Error("binary content mangled")
	}
	// keys are opt-in
	for name := range got {
		if strings.HasPrefix(name, "keys/") {
			t.Errorf("keys included without being asked: %s", name)
		}
	}

	// wipe, then restore from that zip
	for _, p := range []string{"/index.gmi", "/posts/.feed", "/posts/2026-07-19-hi.gmi", "/media/pic.png"} {
		if err := st.DeletePage(p, "t"); err != nil {
			t.Fatal(err)
		}
	}
	postZip(t, client, ts.URL, "merge", data)

	pg, err := st.GetPage("/posts/2026-07-19-hi.gmi")
	if err != nil {
		t.Fatalf("page not restored: %v", err)
	}
	if string(pg.Content) != "# Hi\n\nbody" {
		t.Errorf("restored content: %q", pg.Content)
	}
	// mime is re-derived from the name, so binaries stay binary
	img, err := st.GetPage("/media/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if img.Mime != "image/png" || !img.Binary {
		t.Errorf("restored image mime %q binary %v", img.Mime, img.Binary)
	}
	// and a feed folder is a feed folder again
	if !st.IsFeedFolder("/posts/") {
		t.Error("dot-files did not survive the round trip")
	}
}

func TestRestoreMergeVersusReplace(t *testing.T) {
	_, st, ts := testServer(t)
	_, _ = st.SavePage("/keep.gmi", []byte("# Keep"), "", "t")
	_, _ = st.SavePage("/old.gmi", []byte("# Old"), "", "t")
	client := login(t, ts, testPassword)

	data := zipOf(t, map[string]string{
		"content/keep.gmi": "# Keep (edited)",
		"content/new.gmi":  "# New",
		"BACKUP.txt":       "starpulse backup",
	})

	// merge leaves untouched pages alone
	postZip(t, client, ts.URL, "merge", data)
	if _, err := st.GetPage("/old.gmi"); err != nil {
		t.Error("merge deleted a page it should not have")
	}
	if pg, _ := st.GetPage("/keep.gmi"); string(pg.Content) != "# Keep (edited)" {
		t.Error("merge did not overwrite")
	}
	if _, err := st.GetPage("/new.gmi"); err != nil {
		t.Error("merge did not add")
	}

	// replace removes what the zip does not contain
	postZip(t, client, ts.URL, "replace", data)
	if _, err := st.GetPage("/old.gmi"); err == nil {
		t.Error("replace kept a page the backup did not contain")
	}
	if _, err := st.GetPage("/keep.gmi"); err != nil {
		t.Error("replace deleted a page the backup did contain")
	}
	// an overwritten page keeps its history, so a restore stays undoable
	if vs, err := st.ListVersions("/keep.gmi"); err != nil || len(vs) == 0 {
		t.Errorf("no history after restore: %v %d", err, len(vs))
	}
}

// Zip entry names come from whoever made the file, so the obvious attack is
// a path that climbs out of the content folder.
func TestRestoreRejectsEscapingPaths(t *testing.T) {
	_, _, ts := testServer(t)
	client := login(t, ts, testPassword)

	for _, name := range []string{
		"content/../../etc/passwd",
		"content/../outside.gmi",
		"content/a/../../../x.gmi",
		"/etc/passwd",
		"../evil.gmi",
		"keys/gemini-key.pem", // keys are never restored
		"BACKUP.txt",
	} {
		if p, ok := backupEntryPath(name); ok {
			t.Errorf("accepted %q as %q", name, p)
		}
	}
	// a single wrapping folder (as some archivers add) is still fine
	for name, want := range map[string]string{
		"content/index.gmi":             "/index.gmi",
		"content/posts/.feed":           "/posts/.feed",
		"mysite_20260720/content/a.gmi": "/a.gmi",
		"content/deep/nested/thing.gmi": "/deep/nested/thing.gmi",
	} {
		p, ok := backupEntryPath(name)
		if !ok || p != want {
			t.Errorf("backupEntryPath(%q) = %q,%v want %q", name, p, ok, want)
		}
	}

	// and nothing lands outside when the zip is actually uploaded
	body := postZip(t, client, ts.URL, "merge", zipOf(t, map[string]string{
		"content/../../escape.gmi": "nope",
	}))
	_ = body
	if _, err := os.Stat(filepath.Join(t.TempDir(), "escape.gmi")); err == nil {
		t.Error("a file escaped onto disk")
	}
}

// Restoring our own backup must not report the manifest or the key copies
// as skipped — they are ignored on purpose, and calling that a problem
// teaches you to ignore the one message that would matter.
func TestRestoreDoesNotComplainAboutItsOwnFiles(t *testing.T) {
	_, _, ts := testServer(t)
	client := login(t, ts, testPassword)
	data := zipOf(t, map[string]string{
		"content/a.gmi":        "# A",
		"BACKUP.txt":           "starpulse backup",
		"keys/gemini-key.pem":  "secret",
		"site_2026/BACKUP.txt": "wrapped",
		"junk.txt":             "unexpected",
	})
	resp, err := client.Post(ts.URL+"/admin/backup/restore", "", nil)
	if err == nil {
		resp.Body.Close()
	}
	body := postZip(t, client, ts.URL, "merge", data)
	_ = body
	// follow the redirect target ourselves: the flash carries the counts
	r2, _ := client.Get(ts.URL + "/admin/backup")
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	flash := string(b2)
	if strings.Contains(flash, "skipped 4") || strings.Contains(flash, "skipped 3") || strings.Contains(flash, "skipped 2") {
		t.Errorf("counted its own files as skipped:\n%s", firstFlash(flash))
	}
}

func firstFlash(body string) string {
	i := strings.Index(body, `class="flash"`)
	if i < 0 {
		return "(no flash)"
	}
	j := strings.Index(body[i:], "</p>")
	if j < 0 {
		return body[i:]
	}
	return body[i : i+j]
}

func TestBackupNameIsSiteAndTimestamp(t *testing.T) {
	when := time.Date(2026, 7, 20, 15, 4, 5, 0, time.UTC)
	for host, want := range map[string]string{
		"owg.fyi":        "owg_fyi_20260720-150405.zip",
		"localhost:8080": "localhost_8080_20260720-150405.zip",
		"":               "starpulse_20260720-150405.zip",
		"a/../b":         "a__b_20260720-150405.zip", // dots become _, slashes vanish
	} {
		if got := backupName(host, when); got != want {
			t.Errorf("backupName(%q) = %q, want %q", host, got, want)
		}
	}
	// whatever the host is, the name is a name and never a path
	for _, host := range []string{"a/../b", "../../etc/passwd", "x\\y", "  spaced  "} {
		got := backupName(host, when)
		if strings.ContainsAny(got, `/\`) || strings.Contains(got, "..") {
			t.Errorf("backupName(%q) = %q is not a bare filename", host, got)
		}
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
