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
	var entries []entry
	if f.IsNow() {
		entries = b.nowEntries(f, baseURL)
	} else {
		entries = b.pageEntries(f, baseURL)
	}

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
	author := b.Author
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

	var out []entry
	for _, m := range metas {
		if m.Binary || store.Hidden(m.Path) || !strings.HasSuffix(m.Path, ".gmi") {
			continue
		}
		if prefix != "/" && !strings.HasPrefix(m.Path, prefix) {
			continue
		}
		date := DatedName(m.Path)
		if date == "" {
			continue // only dated pages are feed-worthy
		}
		published, err := time.ParseInLocation("2006-01-02", date, b.loc())
		if err != nil {
			continue
		}
		url := baseURL + PageURL(m.Path)
		title := m.Title
		if title == "" {
			title = m.Path
		}
		e := entry{
			title:     title,
			id:        url,
			link:      url,
			published: published,
			updated:   m.Updated,
			summary:   b.summarize(m.Path),
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

// nowEntries turns now-posts into feed items. They have no page of their own,
// so they get tag: ids and link at the feed's page.
func (b *Builder) nowEntries(f config.Feed, baseURL string) []entry {
	limit := f.Limit
	if limit <= 0 {
		limit = 30
	}
	posts, err := b.Store.ListNow(limit)
	if err != nil {
		return nil
	}
	link := ""
	if f.Page != "" {
		link = baseURL + f.Page
	}
	var out []entry
	for _, p := range posts {
		text := strings.TrimSpace(p.Content)
		out = append(out, entry{
			title:     firstLine(text, 70),
			id:        fmt.Sprintf("tag:%s,%s:now/%d", b.Hostname, p.Created.In(b.loc()).Format("2006-01-02"), p.ID),
			link:      link,
			published: p.Created,
			updated:   p.Created,
			summary:   text,
		})
	}
	return out
}

// summarize returns a short plain-text excerpt of a page.
func (b *Builder) summarize(path string) string {
	pg, err := b.Store.GetPage(path)
	if err != nil {
		return ""
	}
	text := gemtext.PlainText(string(pg.Content))
	// drop the leading heading — it is already the entry title
	lines := strings.Split(text, "\n")
	var keep []string
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if i == 0 && len(keep) == 0 {
			continue
		}
		keep = append(keep, l)
		if len(strings.Join(keep, " ")) > 300 {
			break
		}
	}
	out := strings.Join(keep, " ")
	if len(out) > 300 {
		out = strings.TrimSpace(out[:300]) + "…"
	}
	return out
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

// DatedName returns the YYYY-MM-DD prefix of a filename, or "".
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
