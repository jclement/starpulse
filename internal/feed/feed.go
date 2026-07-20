// Package feed renders Atom feeds from the page store.
//
// Two conventions matter for a capsule like this:
//
//   - On gemini, the idiomatic feed is the page itself: a list of link lines
//     whose labels start with an ISO date ("=> /posts/x 2026-07-20 Title").
//     Lagrange and Amfora subscribe to those directly, so {{list}} output is
//     already a feed — no XML required.
//   - On the web, subscribers want Atom. That is what this package builds,
//     and starpulse serves the same document over gemini too for clients
//     that prefer it.
package feed

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/config"
	"github.com/jclement/starpulse/internal/gemtext"
	"github.com/jclement/starpulse/internal/store"
)

// Builder renders configured feeds.
type Builder struct {
	Store    *store.Store
	Hostname string
	Author   string
	Loc      *time.Location
}

func (b *Builder) loc() *time.Location {
	if b.Loc != nil {
		return b.Loc
	}
	return time.UTC
}

// entry is one feed item.
type entry struct {
	title     string
	id        string
	link      string // absolute URL, "" when the item has no page of its own
	published time.Time
	updated   time.Time
	summary   string
}

// Build renders one feed as an Atom document.
func (b *Builder) Build(f config.Feed, baseURL string) string {
	entries := b.pageEntries(f, baseURL)

	limit := f.Limit
	if limit <= 0 {
		limit = 30
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}

	title := f.Title
	if title == "" {
		title = b.Hostname
	}
	author := f.Author
	if author == "" {
		author = b.Author
	}
	if author == "" {
		author = b.Hostname
	}

	// the feed's own <updated> is the newest entry (or now, when empty)
	updated := time.Now()
	if len(entries) > 0 {
		updated = entries[0].updated
	}

	page := f.Page
	if page == "" {
		page = "/"
	}

	var s strings.Builder
	s.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	s.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">` + "\n")
	fmt.Fprintf(&s, "  <title>%s</title>\n", html.EscapeString(title))
	if f.Subtitle != "" {
		fmt.Fprintf(&s, "  <subtitle>%s</subtitle>\n", html.EscapeString(f.Subtitle))
	}
	fmt.Fprintf(&s, "  <id>%s%s</id>\n", baseURL, page)
	fmt.Fprintf(&s, `  <link rel="alternate" type="text/gemini" href="%s%s"/>`+"\n", baseURL, page)
	fmt.Fprintf(&s, `  <link rel="self" type="application/atom+xml" href="%s%s"/>`+"\n", baseURL, f.Path)
	fmt.Fprintf(&s, "  <updated>%s</updated>\n", updated.In(b.loc()).Format(time.RFC3339))
	fmt.Fprintf(&s, "  <author><name>%s</name></author>\n", html.EscapeString(author))
	fmt.Fprintf(&s, "  <generator uri=\"https://github.com/jclement/starpulse\">starpulse</generator>\n")

	for _, e := range entries {
		s.WriteString("  <entry>\n")
		fmt.Fprintf(&s, "    <title>%s</title>\n", html.EscapeString(e.title))
		fmt.Fprintf(&s, "    <id>%s</id>\n", html.EscapeString(e.id))
		if e.link != "" {
			fmt.Fprintf(&s, `    <link rel="alternate" href="%s"/>`+"\n", html.EscapeString(e.link))
		}
		fmt.Fprintf(&s, "    <published>%s</published>\n", e.published.In(b.loc()).Format(time.RFC3339))
		fmt.Fprintf(&s, "    <updated>%s</updated>\n", e.updated.In(b.loc()).Format(time.RFC3339))
		if e.summary != "" {
			fmt.Fprintf(&s, "    <summary type=\"text\">%s</summary>\n", html.EscapeString(e.summary))
		}
		s.WriteString("  </entry>\n")
	}
	s.WriteString("</feed>\n")
	return s.String()
}

// pageEntries collects dated pages from a folder (or the whole site when the
// source is "/" or empty), newest first.
func (b *Builder) pageEntries(f config.Feed, baseURL string) []entry {
	metas, err := b.Store.ListAll()
	if err != nil {
		return nil
	}
	prefix := f.Source
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	// every page in a feed folder is a post, so undated ones fall back to
	// their creation date
	inFeedFolder := prefix != "/" && b.Store.IsFeedFolder(prefix)

	var out []entry
	for _, m := range metas {
		if m.Binary || store.Hidden(m.Path) || !strings.HasSuffix(m.Path, ".gmi") {
			continue
		}
		if prefix != "/" && !strings.HasPrefix(m.Path, prefix) {
			continue
		}
		if strings.HasSuffix(m.Path, "/index.gmi") {
			continue // the folder's own page is not one of its posts
		}
		date := b.Store.EffectiveDate(m, inFeedFolder)
		if date == "" {
			continue // only dated pages are feed-worthy
		}
		published, err := time.ParseInLocation("2006-01-02", date, b.loc())
		if err != nil {
			continue
		}
		url := baseURL + PageURL(m.Path)
		title, summary := b.titleAndSummary(m)
		e := entry{
			title:     title,
			id:        url,
			link:      url,
			published: published,
			updated:   m.Updated,
			summary:   summary,
		}
		// a page edited before its own date (imported content) reads oddly
		if e.updated.Before(published) {
			e.updated = published
		}
		out = append(out, e)
	}
	sortByPublishedDesc(out)
	return out
}

// titleAndSummary derives an entry's title and excerpt from a page.
//
// A document leads with a heading, which becomes the title and is then left
// out of the summary. A short note has no heading at all — its first line is
// the note — so it supplies the title and the text stays whole. Getting this
// wrong swallows notes entirely, which is exactly what it used to do.
func (b *Builder) titleAndSummary(m store.Meta) (string, string) {
	pg, err := b.Store.GetPage(m.Path)
	if err != nil {
		return fallbackTitle(m), ""
	}
	// work from the raw gemtext: PlainText has already stripped the "#", so
	// a heading is indistinguishable from a first line by then
	raw := strings.TrimSpace(stripFrontMatter(string(pg.Content)))
	if raw == "" {
		return fallbackTitle(m), ""
	}
	lines := strings.Split(raw, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) {
		return fallbackTitle(m), ""
	}
	first := strings.TrimSpace(lines[i])

	title := m.Title
	body := lines[i:]
	if strings.HasPrefix(first, "#") {
		// a document: the heading is the title, so leave it out of the summary
		if title == "" {
			title = strings.TrimSpace(strings.TrimLeft(first, "# "))
		}
		body = lines[i+1:]
	} else if title == "" || title == stem(m.Path) {
		// a note: its opening line names it, and stays in the body
		title = truncate(first, 70)
	}

	summary := strings.TrimSpace(gemtext.PlainText(strings.Join(body, "\n")))
	return title, truncate(strings.Join(strings.Fields(summary), " "), 300)
}

// stripFrontMatter drops a leading --- ... --- block.
func stripFrontMatter(src string) string {
	if !strings.HasPrefix(src, "---\n") && !strings.HasPrefix(src, "---\r\n") {
		return src
	}
	rest := src[strings.Index(src, "\n")+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return src
	}
	return strings.TrimLeft(rest[end+4:], "\r\n")
}

func fallbackTitle(m store.Meta) string {
	if m.Title != "" {
		return m.Title
	}
	return m.Path
}

func stem(p string) string {
	base := p[strings.LastIndexByte(p, '/')+1:]
	return strings.TrimSuffix(base, ".gmi")
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

func firstLine(s string, max int) string {
	line := s
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(strings.TrimLeft(line, "#* >"))
	if line == "" {
		line = "note"
	}
	if len([]rune(line)) > max {
		r := []rune(line)
		line = strings.TrimSpace(string(r[:max])) + "…"
	}
	return line
}

func sortByPublishedDesc(e []entry) {
	for i := 1; i < len(e); i++ {
		for j := i; j > 0 && e[j].published.After(e[j-1].published); j-- {
			e[j], e[j-1] = e[j-1], e[j]
		}
	}
}

// DatedName returns the YYYY-MM-DD prefix of a filename, or "". Prefer the
// page's stored Date, which also honours a front-matter "date:".
func DatedName(p string) string {
	base := p[strings.LastIndexByte(p, '/')+1:]
	if len(base) >= 11 && base[4] == '-' && base[7] == '-' && (base[10] == '-' || base[10] == '_') {
		return base[:10]
	}
	return ""
}

// PageURL converts a storage path to its served URL.
func PageURL(p string) string {
	u := strings.TrimSuffix(p, ".gmi")
	if strings.HasSuffix(u, "/index") {
		u = strings.TrimSuffix(u, "index")
	}
	return u
}

// ---- log folders --------------------------------------------------------
//
// A "log folder" is any folder holding date-stamped pages — /posts/,
// /projects/, whatever you like. They are discovered from the content
// itself rather than configured, and each one publishes its own Atom feed
// at <folder>feed.xml.

// FeedFolders returns the folders publishing a feed.
func FeedFolders(st *store.Store) map[string]bool { return st.FeedFolders() }

// Resolve maps a request path to the feed it should serve, if any. Explicit
// configuration wins; otherwise a <folder>feed.xml under a log folder is
// generated automatically.
func Resolve(cfg *config.Config, st *store.Store, path string) (config.Feed, bool) {
	for _, f := range cfg.EffectiveFeeds() {
		if f.Path == path {
			return f, true
		}
	}
	if !strings.HasSuffix(path, "/feed.xml") {
		return config.Feed{}, false
	}
	folder := strings.TrimSuffix(path, "feed.xml")
	if !st.IsFeedFolder(folder) {
		return config.Feed{}, false
	}
	limit := cfg.Feeds.Limit
	if limit <= 0 {
		limit = 30
	}
	fs := st.FeedInfo(folder)
	if fs.Title == "" {
		fs.Title = folderTitle(st, folder, cfg.Hostname)
	}
	if fs.Limit > 0 {
		limit = fs.Limit
	}
	return config.Feed{
		Path:     path,
		Source:   folder,
		Page:     folder,
		Title:    fs.Title,
		Subtitle: fs.Subtitle,
		Author:   fs.Author,
		Limit:    limit,
	}, true
}

// folderTitle names an auto feed after the folder's index page, falling back
// to the folder name.
func folderTitle(st *store.Store, folder, hostname string) string {
	if pg, err := st.GetPage(folder + "index.gmi"); err == nil && pg.Title != "" {
		return pg.Title
	}
	name := strings.Trim(folder, "/")
	if name == "" {
		return hostname
	}
	return hostname + " · " + name
}
