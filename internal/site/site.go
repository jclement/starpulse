// Package site resolves URL paths against the page store and assembles
// gemtext documents: inherited .header/.footer, folder .theme CSS, and
// {{...}} directives ({{list}}, {{include}}, {{now}}, {{count}}, …).
package site

import (
	"fmt"
	"math/rand/v2"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/store"
)

// Build metadata, stamped via -ldflags -X at release time; the {{version}}
// and {{updated}} directives render them.
var (
	BuildVersion = "dev"
	BuildDate    = ""
)

// ResultType describes what a URL resolved to.
type ResultType int

const (
	NotFound ResultType = iota
	PageResult
	FileResult
	RedirectResult
)

// Page is a fully assembled gemtext document.
type Page struct {
	URLPath    string
	SourcePath string // storage path of the body ("" for synthetic pages)
	Title      string
	Gemtext    string // assembled: header + body + footer, directives expanded
	Theme      string // inherited .theme CSS ("" if none)
}

// Result is the outcome of resolving a URL path.
type Result struct {
	Type     ResultType
	Page     *Page
	File     *store.Page // FileResult
	Location string      // RedirectResult
}

// Site renders pages from a store.
type Site struct {
	Store *store.Store
	// Loc is the timezone for displayed timestamps (nil = server local).
	Loc *time.Location
}

// New creates a Site.
func New(st *store.Store) *Site { return &Site{Store: st} }

func (s *Site) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

// CleanURL validates and normalizes a request path; ok=false means reject.
// Dot-prefixed segments (special files) are never directly addressable.
func CleanURL(urlPath string) (string, bool) {
	if urlPath == "" {
		urlPath = "/"
	}
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	if strings.Contains(urlPath, "\x00") {
		return "", false
	}
	for _, seg := range strings.Split(urlPath, "/") {
		if seg == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(urlPath)
	if strings.Contains(cleaned, "..") {
		return "", false
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if strings.HasPrefix(seg, ".") && seg != "" {
			return "", false
		}
	}
	if strings.HasSuffix(urlPath, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned, true
}

// Resolve maps a URL path to a page, file, or redirect. When proto is
// non-empty ("gemini", "http", "gemini+tor", …) the page's hit counter for
// that protocol is incremented.
func (s *Site) Resolve(urlPath, proto string) *Result {
	cleaned, ok := CleanURL(urlPath)
	if !ok {
		return &Result{Type: NotFound}
	}

	if strings.HasSuffix(cleaned, "/") || cleaned == "/" {
		dir := cleaned
		if pg, err := s.Store.GetPage(indexPath(dir)); err == nil {
			return s.pageResult(dir, pg, proto)
		}
		if dir != "/" && !s.dirExists(dir) {
			return &Result{Type: NotFound}
		}
		return s.syntheticListing(dir, proto)
	}

	// exact match (static file or explicit .gmi path)
	if pg, err := s.Store.GetPage(cleaned); err == nil {
		if isGemtext(pg.Mime) {
			return s.pageResult(cleaned, pg, proto)
		}
		if proto != "" {
			s.Store.Bump(cleaned, proto)
		}
		return &Result{Type: FileResult, File: pg}
	}

	// extensionless page: /about -> /about.gmi
	if pg, err := s.Store.GetPage(cleaned + ".gmi"); err == nil {
		return s.pageResult(cleaned, pg, proto)
	}

	// directory without trailing slash
	if s.dirExists(cleaned + "/") {
		return &Result{Type: RedirectResult, Location: cleaned + "/"}
	}
	return &Result{Type: NotFound}
}

func indexPath(dir string) string {
	return strings.TrimSuffix(dir, "/") + "/index.gmi"
}

func isGemtext(mime string) bool { return strings.HasPrefix(mime, "text/gemini") }

func (s *Site) dirExists(dir string) bool {
	metas, err := s.Store.ListPrefix(dir)
	return err == nil && len(metas) > 0
}

// ---- assembly -----------------------------------------------------------

type frontMatter struct {
	Title, Date string
	NoHeader    bool
	NoFooter    bool
}

var fmKeyRe = regexp.MustCompile(`(?m)^(title|date|header|footer)\s*[:=]\s*(.+)$`)

// stripFrontMatter removes a leading --- ... --- block, returning the body
// and any recognized keys it declared.
func stripFrontMatter(src string) (string, frontMatter) {
	var fm frontMatter
	if !strings.HasPrefix(src, "---\n") && !strings.HasPrefix(src, "---\r\n") {
		return src, fm
	}
	rest := src[strings.Index(src, "\n")+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return src, fm
	}
	block := rest[:end]
	body := rest[end+4:]
	body = strings.TrimPrefix(strings.TrimPrefix(body, "\r"), "\n")
	for _, m := range fmKeyRe.FindAllStringSubmatch(block, -1) {
		val := strings.Trim(strings.TrimSpace(m[2]), `"'`)
		off := val == "none" || val == "off" || val == "false"
		switch m[1] {
		case "title":
			fm.Title = val
		case "date":
			if len(val) >= 10 {
				val = val[:10]
			}
			fm.Date = val
		case "header":
			fm.NoHeader = off
		case "footer":
			fm.NoFooter = off
		}
	}
	return body, fm
}

func (s *Site) pageResult(urlPath string, pg *store.Page, proto string) *Result {
	if proto != "" {
		s.Store.Bump(canonicalKey(urlPath), proto)
	}
	body, fm := stripFrontMatter(string(pg.Content))
	baseDir := path.Dir(strings.TrimSuffix(urlPath, "/"))
	if strings.HasSuffix(urlPath, "/") || urlPath == "/" {
		baseDir = strings.TrimSuffix(urlPath, "/")
		if baseDir == "" {
			baseDir = "/"
		}
	}

	ctx := expandCtx{urlPath: urlPath, page: pg}
	var parts []string
	if h := s.nearestSpecial(pg.Path, ".header"); h != "" && !fm.NoHeader {
		parts = append(parts, s.expand(h, path.Dir(pg.Path), ctx, 0))
	}
	parts = append(parts, s.expand(body, baseDir, ctx, 0))
	if f := s.nearestSpecial(pg.Path, ".footer"); f != "" && !fm.NoFooter {
		parts = append(parts, s.expand(f, path.Dir(pg.Path), ctx, 0))
	}

	title := fm.Title
	if title == "" {
		title = pg.Title
	}
	return &Result{Type: PageResult, Page: &Page{
		URLPath:    urlPath,
		SourcePath: pg.Path,
		Title:      title,
		Gemtext:    joinChunks(parts),
		Theme:      s.nearestSpecial(pg.Path, ".theme"),
	}}
}

func joinChunks(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(p, "\n") + "\n")
	}
	return b.String()
}

// canonicalKey is the stats key for a URL (trailing slash trimmed, "/" kept).
func canonicalKey(urlPath string) string {
	k := strings.TrimSuffix(urlPath, "/")
	if k == "" {
		return "/"
	}
	return k
}

// nearestSpecial finds the closest special file (".header", ".footer",
// ".theme") at or above the page's directory. Front matter is stripped;
// directive expansion is the caller's job.
func (s *Site) nearestSpecial(pagePath, name string) string {
	dir := path.Dir(pagePath)
	for {
		p := dir + "/" + name
		if dir == "/" {
			p = "/" + name
		}
		if pg, err := s.Store.GetPage(p); err == nil {
			body, _ := stripFrontMatter(string(pg.Content))
			return body
		}
		if dir == "/" || dir == "." {
			return ""
		}
		dir = path.Dir(dir)
	}
}

// syntheticListing renders a directory with no index.gmi as a listing page.
func (s *Site) syntheticListing(dir, proto string) *Result {
	if proto != "" {
		s.Store.Bump(canonicalKey(dir), proto)
	}
	name := path.Base(strings.TrimSuffix(dir, "/"))
	if name == "/" || name == "" || name == "." {
		name = "index"
	}
	src := fmt.Sprintf("# %s\n\n{{list}}\n", name)
	anchor := indexPath(dir)
	ctx := expandCtx{urlPath: dir}
	var parts []string
	if h := s.nearestSpecial(anchor, ".header"); h != "" {
		parts = append(parts, s.expand(h, path.Dir(anchor), ctx, 0))
	}
	parts = append(parts, s.expand(src, strings.TrimSuffix(dir, "/"), ctx, 0))
	if f := s.nearestSpecial(anchor, ".footer"); f != "" {
		parts = append(parts, s.expand(f, path.Dir(anchor), ctx, 0))
	}
	return &Result{Type: PageResult, Page: &Page{
		URLPath: dir,
		Title:   name,
		Gemtext: joinChunks(parts),
		Theme:   s.nearestSpecial(anchor, ".theme"),
	}}
}

// ---- directives ---------------------------------------------------------

var lineDirectiveRe = regexp.MustCompile(`(?m)^\{\{\s*(list|index|include|random|now)(?:\s+([^\s}]+))?(?:\s+(\d+))?\s*\}\}\s*$`)

const maxIncludeDepth = 4

// expandCtx is the served-page context inline tokens draw from.
type expandCtx struct {
	urlPath string
	page    *store.Page // body source page; nil for synthetic pages
}

// expand replaces directives in a document. baseDir is the URL directory of
// the containing document; ctx describes the page being served ({{count}},
// {{updated}}, {{rev}}).
func (s *Site) expand(body, baseDir string, ctx expandCtx, depth int) string {
	if depth > maxIncludeDepth {
		return body
	}
	// inline tokens work mid-sentence
	body = strings.ReplaceAll(body, "{{version}}", BuildVersion)
	if strings.Contains(body, "{{updated}}") {
		body = strings.ReplaceAll(body, "{{updated}}", s.updatedString(ctx))
	}
	if strings.Contains(body, "{{rev}}") {
		body = strings.ReplaceAll(body, "{{rev}}", s.revString(ctx))
	}
	if strings.Contains(body, "{{count}}") {
		body = strings.ReplaceAll(body, "{{count}}", fmt.Sprintf("%d", s.Store.Count(canonicalKey(ctx.urlPath))))
	}
	if strings.Contains(body, "{{latest_now") {
		post := s.latestNow()
		content, date := "", ""
		if post != nil {
			content = strings.TrimSpace(post.Content)
			date = post.Created.In(s.loc()).Format("2006-01-02")
		}
		body = strings.ReplaceAll(body, "{{latest_now_date}}", date)
		body = strings.ReplaceAll(body, "{{latest_now}}", content)
	}

	return lineDirectiveRe.ReplaceAllStringFunc(body, func(m string) string {
		parts := lineDirectiveRe.FindStringSubmatch(m)
		verb, arg, numStr := parts[1], parts[2], parts[3]
		// {{now 5}} puts the number in arg's slot
		if verb == "now" && numStr == "" {
			if _, err := strconv.Atoi(arg); err == nil {
				numStr = arg
				arg = ""
			}
		}
		num := 0
		if numStr != "" {
			num, _ = strconv.Atoi(numStr)
		}
		switch verb {
		case "list", "index": // "index" kept for owg-capsule compatibility
			dir := baseDir
			if arg != "" {
				dir = resolveRef(baseDir, arg)
			}
			return s.renderList(dir, num)
		case "include":
			ref := resolveRef(baseDir, arg)
			pg, err := s.Store.GetPage(ref)
			if err != nil {
				// allow extensionless include refs
				if pg, err = s.Store.GetPage(ref + ".gmi"); err != nil {
					return fmt.Sprintf("(include %s: not found)", arg)
				}
			}
			inner, _ := stripFrontMatter(string(pg.Content))
			return s.expand(inner, path.Dir(pg.Path), ctx, depth+1)
		case "random":
			ref := resolveRef(baseDir, arg)
			pg, err := s.Store.GetPage(ref)
			if err != nil {
				return ""
			}
			var lines []string
			for _, l := range strings.Split(string(pg.Content), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					lines = append(lines, l)
				}
			}
			if len(lines) == 0 {
				return ""
			}
			return lines[rand.IntN(len(lines))]
		case "now":
			if numStr == "" {
				num = 5
			}
			return s.renderNow(num)
		}
		return m
	})
}

func resolveRef(baseDir, ref string) string {
	if strings.HasPrefix(ref, "/") {
		return path.Clean(ref)
	}
	if baseDir == "" {
		baseDir = "/"
	}
	return path.Clean(path.Join(baseDir, ref))
}

// updatedString renders {{updated}}: the served page's last-edit date, or
// "recently" on synthetic pages.
func (s *Site) updatedString(ctx expandCtx) string {
	if ctx.page != nil {
		return ctx.page.Updated.In(s.loc()).Format("2006-01-02")
	}
	return "recently"
}

// latestNow returns the newest now-post, or nil.
func (s *Site) latestNow() *store.NowPost {
	posts, err := s.Store.ListNow(1)
	if err != nil || len(posts) == 0 {
		return nil
	}
	return &posts[0]
}

// revString renders {{rev}}: the served page's revision number (saved
// versions + 1).
func (s *Site) revString(ctx expandCtx) string {
	if ctx.page == nil {
		return "1"
	}
	return fmt.Sprintf("%d", s.Store.CountVersions(ctx.page.Path)+1)
}

// Entry is one row of a directory listing.
type Entry struct {
	URL   string
	Title string
	Date  string
	IsDir bool
}

var dateNameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[-_]`)

// List returns the visible entries of a content directory (non-recursive):
// gemtext pages and sub-directories. Dated entries first (newest first),
// then alphabetical.
func (s *Site) List(urlDir string) []Entry {
	dir := strings.TrimSuffix(urlDir, "/")
	if dir == "" {
		dir = ""
	}
	prefix := dir + "/"
	metas, err := s.Store.ListPrefix(prefix)
	if err != nil {
		return nil
	}
	marked := dir != "" && s.Store.IsMarkedLog(prefix)
	var out []Entry
	seenDirs := map[string]bool{}
	for _, m := range metas {
		rest := strings.TrimPrefix(m.Path, prefix)
		if rest == "" || strings.HasPrefix(rest, ".") {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			sub := rest[:i]
			if strings.HasPrefix(sub, ".") || seenDirs[sub] {
				continue
			}
			seenDirs[sub] = true
			e := Entry{URL: prefix + sub + "/", Title: sub + "/", IsDir: true}
			if idx, err := s.Store.GetPage(prefix + sub + "/index.gmi"); err == nil && idx.Title != "" {
				body, fm := stripFrontMatter(string(idx.Content))
				_ = body
				if fm.Title != "" {
					e.Title = fm.Title
				} else {
					e.Title = idx.Title
				}
			}
			out = append(out, e)
			continue
		}
		if !isGemtext(m.Mime) {
			continue
		}
		stem := strings.TrimSuffix(rest, path.Ext(rest))
		if stem == "index" {
			continue
		}
		e := Entry{URL: prefix + stem, Title: m.Title}
		if e.Title == "" {
			e.Title = stem
		}
		e.Date = s.Store.EffectiveDate(m, marked)
		// front-matter date/title override
		if pg, err := s.Store.GetPage(m.Path); err == nil {
			if _, fm := stripFrontMatter(string(pg.Content)); fm.Date != "" || fm.Title != "" {
				if fm.Date != "" {
					e.Date = fm.Date
				}
				if fm.Title != "" {
					e.Title = fm.Title
				}
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := out[i].Date, out[j].Date
		if (di != "") != (dj != "") {
			return di != "" // dated entries first
		}
		if di != dj {
			return di > dj // newest first
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

func (s *Site) renderList(urlDir string, limit int) string {
	entries := s.List(urlDir)
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	var b strings.Builder
	for _, e := range entries {
		label := e.Title
		if e.Date != "" {
			label = e.Date + " " + e.Title
		}
		fmt.Fprintf(&b, "=> %s %s\n", e.URL, label)
	}
	if b.Len() == 0 {
		return "(nothing here yet)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderNow renders the latest now posts as gemtext (limit 0 = all).
func (s *Site) renderNow(limit int) string {
	posts, err := s.Store.ListNow(limit)
	if err != nil || len(posts) == 0 {
		return "(nothing yet)"
	}
	var b strings.Builder
	for i, p := range posts {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n", p.Created.In(s.loc()).Format("2006-01-02 15:04"), strings.TrimSpace(p.Content))
	}
	return strings.TrimRight(b.String(), "\n")
}
