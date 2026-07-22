// Package store persists all site state in a single SQLite database:
// pages (text and binary), page versions, hit counters, "now" micro-posts,
// settings, and an FTS5 full-text index over text pages.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jclement/starpulse/internal/gemtext"
)

// ErrNotFound is returned when a page or version does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
	// KeepVersions caps retained history per page (0 = unlimited).
	KeepVersions int
	// Loc is the timezone a creation timestamp is read in when it has to
	// become a date. A post written at 6pm belongs to that evening's date in
	// the author's zone, not to tomorrow because the server runs on UTC.
	// Nil means the server's local time.
	Loc *time.Location
}

func (s *Store) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

// Page is one stored page or file.
type Page struct {
	ID    int64
	Path  string // canonical: "/index.gmi", "/posts/.header", "/media/cat.png"
	Title string
	// Date is the publication date (YYYY-MM-DD) when this page is a post,
	// otherwise "". See PageDate for how it is derived.
	Date    string
	Content []byte
	Mime    string
	Binary  bool
	Created time.Time
	Updated time.Time
}

// Meta is a Page without its content (for listings).
type Meta struct {
	Path    string
	Title   string
	Date    string
	Created time.Time
	Mime    string
	Binary  bool
	Size    int64
	Updated time.Time
}

// Version is one historical snapshot of a page.
type Version struct {
	ID      int64
	Path    string
	Mime    string
	Author  string
	SavedAt time.Time
	Size    int64
	Content []byte // only populated by GetVersion
}

// Hit is a per-page, per-protocol view counter row.
type Hit struct {
	Path  string
	Proto string
	Count int64
}

// SearchHit is one full-text search result.
type SearchHit struct {
	Path    string
	Title   string
	Snippet string
}

const schema = `
CREATE TABLE IF NOT EXISTS pages (
	id       INTEGER PRIMARY KEY,
	path     TEXT NOT NULL UNIQUE,
	title    TEXT NOT NULL DEFAULT '',
	content  BLOB NOT NULL,
	mime     TEXT NOT NULL,
	binary   INTEGER NOT NULL DEFAULT 0,
	date     TEXT NOT NULL DEFAULT '',
	created  INTEGER NOT NULL,
	updated  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS versions (
	id       INTEGER PRIMARY KEY,
	path     TEXT NOT NULL,
	content  BLOB NOT NULL,
	mime     TEXT NOT NULL,
	author   TEXT NOT NULL DEFAULT '',
	saved_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS versions_path ON versions(path, saved_at DESC);
CREATE TABLE IF NOT EXISTS hits (
	path  TEXT NOT NULL,
	proto TEXT NOT NULL,
	count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (path, proto)
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS pages_fts USING fts5(title, body, path UNINDEXED);
`

// Open opens (creating if needed) the database at dbPath. Use ":memory:"
// for tests.
func Open(dbPath string) (*Store, error) {
	dsn := dbPath
	if dbPath != ":memory:" {
		dsn = "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc sqlite is happiest with one writer connection
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + draftSchema + scriptKVSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}
	// migrate databases created before pages carried a date
	if _, err := db.Exec(`ALTER TABLE pages ADD COLUMN date TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrating pages.date: %w", err)
	}
	s := &Store{db: db, KeepVersions: 25}
	if err := s.backfillDates(); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfilling dates: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// ---- paths --------------------------------------------------------------

// CleanPath normalizes a page path; ok=false means reject. Paths are
// slash-separated, absolute, with no ".." or empty segments. Dot-prefixed
// segment names are allowed only as the final segment (special files like
// ".header"); dot-prefixed directories are rejected.
func CleanPath(p string) (string, bool) {
	if p == "" || strings.Contains(p, "\x00") {
		return "", false
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// reject traversal before cleaning — nothing legitimate uses ".."
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "/" || strings.Contains(cleaned, "..") {
		return "", false
	}
	segs := strings.Split(cleaned[1:], "/")
	for i, seg := range segs {
		if seg == "" {
			return "", false
		}
		if strings.HasPrefix(seg, ".") && i != len(segs)-1 {
			return "", false
		}
	}
	return cleaned, true
}

// DefaultExt gives a path a .gmi extension when its filename has none, so
// "/about" becomes "/about.gmi". Special files (".header", ".css") and
// paths that already carry an extension are left alone. Without this it is
// far too easy to create a page that stores as an unviewable binary blob.
func DefaultExt(p string) string {
	base := path.Base(p)
	if strings.HasPrefix(base, ".") {
		return p // .header / .footer / .css / .feed
	}
	if strings.Contains(base, ".") {
		return p // already has an extension
	}
	return p + ".gmi"
}

// TextMime coerces a mime type to a text one. The editors only ever produce
// text, so a page must never be stored as an opaque binary because its path
// had an unfamiliar extension.
func TextMime(mime string) string {
	if isBinaryMime(mime) {
		return "text/plain; charset=utf-8"
	}
	return mime
}

var fmDateRe = regexp.MustCompile(`(?m)^date\s*[:=]\s*["\']?(\d{4}-\d{2}-\d{2})`)
var nameDateRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[-_]`)

// PageDate resolves a page's *explicit* publication date, in priority order:
//
//  1. a YYYY-MM-DD prefix on the filename — visible, portable, sorts itself
//  2. a "date:" in front matter — for when you want a clean filename
//
// Returns "" when neither is present; callers inside a log folder then fall
// back to the row's creation timestamp (see Store.EffectiveDate), which is
// why a folder must be *marked* for that to happen — otherwise every page on
// the site would look like a post.
func PageDate(p string, content []byte) string {
	if m := nameDateRe.FindStringSubmatch(path.Base(p)); m != nil {
		return m[1]
	}
	if i := strings.Index(string(content), "\n---"); i >= 0 && strings.HasPrefix(string(content), "---") {
		if m := fmDateRe.FindSubmatch(content[:i]); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// ---- log folders --------------------------------------------------------

// FeedMarker is the special file that makes a folder publish a feed: a
// gemlog, a project journal, release notes. Its presence means "every page
// in here is a post", so posts can have plain names and take their date
// from the database. It also holds that feed's settings.
const FeedMarker = ".feed"

// FeedFolders returns the folders that publish a feed — that is, the ones
// carrying a .feed marker. Publishing is always an explicit choice: a folder
// never starts producing a feed just because something in it looks dated.
func (s *Store) FeedFolders() map[string]bool {
	out := map[string]bool{}
	metas, err := s.ListAll()
	if err != nil {
		return out
	}
	for _, m := range metas {
		if path.Base(m.Path) == FeedMarker {
			out[folderOf(m.Path)] = true
		}
	}
	return out
}

// IsFeedFolder reports whether a folder publishes a feed.
func (s *Store) IsFeedFolder(folder string) bool {
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	return s.PageExists(folder + FeedMarker)
}

// FeedSettings are the per-folder knobs held in a .feed marker file.
type FeedSettings struct {
	Title    string
	Subtitle string
	Author   string
	Limit    int
	// Prefix decides what a new page in this folder is called before you
	// type anything: "none", "date" (2026-07-20-) or "datetime"
	// (2026-07-20-1423, complete, for notes you never name). A feed folder
	// defaults to "date" because entries want to sort chronologically.
	Prefix string
}

// ParseFeedMarker reads a .feed file: plain "key: value" lines, with # for
// comments. Unknown keys are ignored so the format can grow.
func ParseFeedMarker(content []byte) FeedSettings {
	var fs FeedSettings
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "title":
			fs.Title = v
		case "subtitle":
			fs.Subtitle = v
		case "author":
			fs.Author = v
		case "prefix":
			switch v {
			case "none", "date", "datetime":
				fs.Prefix = v
			}
		case "limit":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				fs.Limit = n
			}
		}
	}
	return fs
}

// FeedInfo reads a folder's .feed settings.
func (s *Store) FeedInfo(folder string) FeedSettings {
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	pg, err := s.GetPage(folder + FeedMarker)
	if err != nil {
		return FeedSettings{}
	}
	return ParseFeedMarker(pg.Content)
}

// DefaultFeedMarker renders a starting .feed file for a folder, prefilled
// with the values in effect so they can simply be edited.
func DefaultFeedMarker(title, author string, limit int) []byte {
	return []byte(fmt.Sprintf(`# Feed settings for this folder. Delete this file to stop publishing.
# index.gmi and dot-files are never entries.
title: %s
subtitle:
author: %s
limit: %d
# prefix: what a new page here is called before you type — none, date, or
# datetime (a complete name, for short notes you never title)
prefix: date
`, title, author, limit))
}

// NamePrefix reports how new pages in a folder are named. Folders that
// publish default to dated names; everywhere else you name the file.
func (s *Store) NamePrefix(folder string) string {
	if !s.IsFeedFolder(folder) {
		return "none"
	}
	if p := s.FeedInfo(folder).Prefix; p != "" {
		return p
	}
	return "date"
}

// NewPagePath is the filename offered when you create a page in a folder.
// It is only ever a starting point — the editor shows it in an editable
// field, so nothing here is irreversible or magic.
func (s *Store) NewPagePath(folder string, now time.Time) string {
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	switch s.NamePrefix(folder) {
	case "date":
		return folder + now.Format("2006-01-02") + "-"
	case "datetime":
		return s.NewStreamPath(folder, now)
	}
	return folder
}

// StreamPages returns a folder's pages newest-first, for rendering a stream
// of notes or picking the latest one.
func (s *Store) StreamPages(folder string, limit int) []Page {
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	metas, err := s.ListPrefix(folder)
	if err != nil {
		return nil
	}
	type dated struct {
		m    Meta
		date string
	}
	var items []dated
	for _, m := range metas {
		if m.Binary || Hidden(m.Path) || !strings.HasSuffix(m.Path, ".gmi") {
			continue
		}
		if strings.HasSuffix(m.Path, "/index.gmi") {
			continue
		}
		if strings.Contains(strings.TrimPrefix(m.Path, folder), "/") {
			continue // direct children only
		}
		items = append(items, dated{m, s.EffectiveDate(m, true)})
	}
	// newest first, breaking ties on creation time so same-day notes order
	sort.Slice(items, func(i, j int) bool {
		if items[i].date != items[j].date {
			return items[i].date > items[j].date
		}
		return items[i].m.Created.After(items[j].m.Created)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make([]Page, 0, len(items))
	for _, it := range items {
		if pg, err := s.GetPage(it.m.Path); err == nil {
			out = append(out, *pg)
		}
	}
	return out
}

// NewStreamPath invents a complete, unique, naturally-sorting filename for
// a note posted without one — over titan, ssh, the API or MCP.
func (s *Store) NewStreamPath(folder string, now time.Time) string {
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	base := folder + now.Format("2006-01-02-1504")
	p := base + ".gmi"
	for i := 2; s.PageExists(p); i++ {
		p = fmt.Sprintf("%s-%d.gmi", base, i)
	}
	return p
}

// EffectiveDate is when a page happened, in priority order:
//
//  1. a YYYY-MM-DD prefix on the filename — authoritative, portable, visible
//  2. a "date:" in front matter — for clean filenames
//  3. the day the page was created, from the database
//
// useCreated gates that last step. Feeds and feed-folder listings pass true
// (every page in a feed folder is a post, so it needs a date); ordinary
// listings pass false, so an undated page stays undated and sorts by name
// rather than silently becoming chronological.
func (s *Store) EffectiveDate(m Meta, useCreated bool) string {
	if m.Date != "" {
		return m.Date
	}
	if useCreated {
		return m.Created.In(s.loc()).Format("2006-01-02")
	}
	return ""
}

func folderOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return "/"
}

// backfillDates fills the date column for rows written before it existed.
func (s *Store) backfillDates() error {
	rows, err := s.db.Query(`SELECT id, path, content FROM pages WHERE date = '' AND binary = 0`)
	if err != nil {
		return err
	}
	type row struct {
		id   int64
		date string
	}
	var todo []row
	for rows.Next() {
		var id int64
		var p string
		var content []byte
		if err := rows.Scan(&id, &p, &content); err != nil {
			rows.Close()
			return err
		}
		if d := PageDate(p, content); d != "" {
			todo = append(todo, row{id, d})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range todo {
		if _, err := s.db.Exec(`UPDATE pages SET date = ? WHERE id = ?`, t.date, t.id); err != nil {
			return err
		}
	}
	return nil
}

// Hidden reports whether a path is a special file (final segment starts
// with ".") that must never be served or listed directly.
func Hidden(p string) bool {
	return strings.HasPrefix(path.Base(p), ".")
}

// ---- pages --------------------------------------------------------------

// TitleOf extracts a display title from page content.
func TitleOf(p string, content []byte, mime string) string {
	if strings.HasPrefix(mime, "text/gemini") {
		if h := gemtext.FirstHeading(string(content)); h != "" {
			return h
		}
	}
	base := path.Base(p)
	return strings.TrimSuffix(base, path.Ext(base))
}

// SavePage creates or updates a page, snapshotting the previous content as
// a version and keeping the FTS index in sync.
func (s *Store) SavePage(p string, content []byte, mime string, author string) (*Page, error) {
	cp, ok := CleanPath(p)
	if !ok {
		return nil, fmt.Errorf("invalid path %q", p)
	}
	if mime == "" {
		mime = MimeFor(cp)
	}
	binary := isBinaryMime(mime)
	title, date := "", ""
	if !binary {
		title = TitleOf(cp, content, mime)
		date = PageDate(cp, content)
	}
	now := time.Now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var id int64
	var created int64
	err = tx.QueryRow(`SELECT id, created FROM pages WHERE path = ?`, cp).Scan(&id, &created)
	switch {
	case err == sql.ErrNoRows:
		res, err := tx.Exec(`INSERT INTO pages (path, title, content, mime, binary, date, created, updated) VALUES (?,?,?,?,?,?,?,?)`,
			cp, title, content, mime, boolInt(binary), date, now, now)
		if err != nil {
			return nil, err
		}
		id, _ = res.LastInsertId()
		created = now
	case err != nil:
		return nil, err
	default:
		// snapshot the outgoing content before overwriting
		if _, err := tx.Exec(`INSERT INTO versions (path, content, mime, author, saved_at)
			SELECT path, content, mime, ?, ? FROM pages WHERE id = ?`, author, now, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE pages SET title=?, content=?, mime=?, binary=?, date=?, updated=? WHERE id=?`,
			title, content, mime, boolInt(binary), date, now, id); err != nil {
			return nil, err
		}
		if err := s.pruneVersions(tx, cp); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(`DELETE FROM pages_fts WHERE rowid = ?`, id); err != nil {
		return nil, err
	}
	if !binary && !Hidden(cp) {
		body := string(content)
		if strings.HasPrefix(mime, "text/gemini") {
			body = gemtext.PlainText(body)
		}
		if _, err := tx.Exec(`INSERT INTO pages_fts (rowid, title, body, path) VALUES (?,?,?,?)`,
			id, title, body, cp); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Page{ID: id, Path: cp, Title: title, Date: date, Content: content, Mime: mime, Binary: binary,
		Created: time.Unix(created, 0), Updated: time.Unix(now, 0)}, nil
}

func (s *Store) pruneVersions(tx *sql.Tx, p string) error {
	if s.KeepVersions <= 0 {
		return nil
	}
	_, err := tx.Exec(`DELETE FROM versions WHERE path = ? AND id NOT IN
		(SELECT id FROM versions WHERE path = ? ORDER BY saved_at DESC, id DESC LIMIT ?)`,
		p, p, s.KeepVersions)
	return err
}

// GetPage fetches a page by exact path.
func (s *Store) GetPage(p string) (*Page, error) {
	cp, ok := CleanPath(p)
	if !ok {
		return nil, ErrNotFound
	}
	var pg Page
	var bin int
	var created, updated int64
	err := s.db.QueryRow(`SELECT id, path, title, content, mime, binary, date, created, updated FROM pages WHERE path = ?`, cp).
		Scan(&pg.ID, &pg.Path, &pg.Title, &pg.Content, &pg.Mime, &bin, &pg.Date, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	pg.Binary = bin != 0
	pg.Created = time.Unix(created, 0)
	pg.Updated = time.Unix(updated, 0)
	return &pg, nil
}

// PageExists reports whether an exact path exists.
func (s *Store) PageExists(p string) bool {
	_, err := s.GetPage(p)
	return err == nil
}

// DeletePage removes a page (its content is snapshotted as a final version
// first, so deletion is recoverable).
func (s *Store) DeletePage(p string, author string) error {
	cp, ok := CleanPath(p)
	if !ok {
		return ErrNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`SELECT id FROM pages WHERE path = ?`, cp).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(`INSERT INTO versions (path, content, mime, author, saved_at)
		SELECT path, content, mime, ?, ? FROM pages WHERE id = ?`, author, time.Now().Unix(), id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM pages WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM pages_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	// a deleted page leaves no hit counters behind — phantom rows would
	// otherwise resurface as stray stats, and steal a later page's counts
	// when a rename lands on the same key
	if _, err := tx.Exec(`DELETE FROM hits WHERE path = ?`, strings.TrimSuffix(cp, ".gmi")); err != nil {
		return err
	}
	if err := dropDraft(tx, cp); err != nil {
		return err
	}
	if err := dropScriptKV(tx, cp); err != nil {
		return err
	}
	return tx.Commit()
}

// ListAll returns metadata for every page, ordered by path.
func (s *Store) ListAll() ([]Meta, error) {
	return s.listWhere(`1=1`, nil)
}

// ListPrefix returns metadata for pages whose path starts with prefix.
func (s *Store) ListPrefix(prefix string) ([]Meta, error) {
	return s.listWhere(`path LIKE ? ESCAPE '\'`, []any{likeEscape(prefix) + "%"})
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (s *Store) listWhere(where string, args []any) ([]Meta, error) {
	rows, err := s.db.Query(`SELECT path, title, mime, binary, date, length(content), created, updated FROM pages WHERE `+where+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		var bin int
		var created, updated int64
		if err := rows.Scan(&m.Path, &m.Title, &m.Mime, &bin, &m.Date, &m.Size, &created, &updated); err != nil {
			return nil, err
		}
		m.Binary = bin != 0
		m.Created = time.Unix(created, 0)
		m.Updated = time.Unix(updated, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// RenamePage moves a page to a new path, carrying its version history with
// it. It fails if the destination already exists.
func (s *Store) RenamePage(oldPath, newPath, author string) (*Page, error) {
	op, ok := CleanPath(oldPath)
	if !ok {
		return nil, fmt.Errorf("invalid source path %q", oldPath)
	}
	np, ok := CleanPath(newPath)
	if !ok {
		return nil, fmt.Errorf("invalid destination path %q", newPath)
	}
	if op == np {
		return s.GetPage(op)
	}
	if s.PageExists(np) {
		return nil, fmt.Errorf("%s already exists", np)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var id int64
	if err := tx.QueryRow(`SELECT id FROM pages WHERE path = ?`, op).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	now := time.Now().Unix()
	// snapshot under the OLD path so the move itself is undoable
	if _, err := tx.Exec(`INSERT INTO versions (path, content, mime, author, saved_at)
		SELECT path, content, mime, ?, ? FROM pages WHERE id = ?`,
		author+" (renamed to "+np+")", now, id); err != nil {
		return nil, err
	}

	// The derived metadata belongs to the NAME, not just the row: a page's
	// title can come from its filename, and its date from a YYYY-MM-DD prefix
	// on it. Moving the row without recomputing these left a page renamed to
	// a dated name still undated, and a filename-titled page showing its old
	// name. Recompute from the content under the new path, exactly as
	// SavePage would.
	var content []byte
	var mime string
	if err := tx.QueryRow(`SELECT content, mime FROM pages WHERE id = ?`, id).Scan(&content, &mime); err != nil {
		return nil, err
	}
	binary := isBinaryMime(mime)
	title, date := "", ""
	if !binary {
		title = TitleOf(np, content, mime)
		date = PageDate(np, content)
	}
	if _, err := tx.Exec(`UPDATE pages SET path = ?, title = ?, date = ?, updated = ? WHERE id = ?`,
		np, title, date, now, id); err != nil {
		return nil, err
	}
	// history follows the page
	if _, err := tx.Exec(`UPDATE versions SET path = ? WHERE path = ?`, np, op); err != nil {
		return nil, err
	}
	// stats follow it too, summed into any counts already at the destination
	// key rather than dropped by OR IGNORE
	if _, err := tx.Exec(`INSERT INTO hits (path, proto, count)
		SELECT ?, proto, count FROM hits WHERE path = ?
		ON CONFLICT(path, proto) DO UPDATE SET count = count + excluded.count`,
		strings.TrimSuffix(np, ".gmi"), strings.TrimSuffix(op, ".gmi")); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM hits WHERE path = ?`, strings.TrimSuffix(op, ".gmi")); err != nil {
		return nil, err
	}
	// rebuild the search row from scratch: a plain UPDATE could not fix the
	// two boundary cases — a page moved into hiding kept its searchable row,
	// and a page moved out of hiding never got one. Delete, then insert only
	// when the new name is a visible text page, as SavePage does.
	if _, err := tx.Exec(`DELETE FROM pages_fts WHERE rowid = ?`, id); err != nil {
		return nil, err
	}
	if !binary && !Hidden(np) {
		body := string(content)
		if strings.HasPrefix(mime, "text/gemini") {
			body = gemtext.PlainText(body)
		}
		if _, err := tx.Exec(`INSERT INTO pages_fts (rowid, title, body, path) VALUES (?,?,?,?)`,
			id, title, body, np); err != nil {
			return nil, err
		}
	}
	// unpublished work follows the page it belongs to
	if err := renameDraft(tx, op, np); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetPage(np)
}

// ---- versions -----------------------------------------------------------

// ListVersions returns version metadata for a path, newest first.
func (s *Store) ListVersions(p string) ([]Version, error) {
	cp, ok := CleanPath(p)
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(`SELECT id, path, mime, author, saved_at, length(content)
		FROM versions WHERE path = ? ORDER BY saved_at DESC, id DESC`, cp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var at int64
		if err := rows.Scan(&v.ID, &v.Path, &v.Mime, &v.Author, &at, &v.Size); err != nil {
			return nil, err
		}
		v.SavedAt = time.Unix(at, 0)
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion fetches one version with content.
func (s *Store) GetVersion(id int64) (*Version, error) {
	var v Version
	var at int64
	err := s.db.QueryRow(`SELECT id, path, content, mime, author, saved_at FROM versions WHERE id = ?`, id).
		Scan(&v.ID, &v.Path, &v.Content, &v.Mime, &v.Author, &at)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.SavedAt = time.Unix(at, 0)
	v.Size = int64(len(v.Content))
	return &v, nil
}

// RestoreVersion re-saves a historical version as the current page content.
func (s *Store) RestoreVersion(id int64, author string) (*Page, error) {
	v, err := s.GetVersion(id)
	if err != nil {
		return nil, err
	}
	return s.SavePage(v.Path, v.Content, v.Mime, author)
}

// ---- hits ---------------------------------------------------------------

// Bump increments the (path, proto) view counter and returns the page's
// total across all protocols.
func (s *Store) Bump(p, proto string) int64 {
	_, _ = s.db.Exec(`INSERT INTO hits (path, proto, count) VALUES (?,?,1)
		ON CONFLICT(path, proto) DO UPDATE SET count = count + 1`, p, proto)
	return s.Count(p)
}

// Count returns a page's total view count across protocols.
func (s *Store) Count(p string) int64 {
	var n sql.NullInt64
	_ = s.db.QueryRow(`SELECT SUM(count) FROM hits WHERE path = ?`, p).Scan(&n)
	return n.Int64
}

// Stats returns all hit counters, highest first.
func (s *Store) Stats() ([]Hit, error) {
	rows, err := s.db.Query(`SELECT path, proto, count FROM hits ORDER BY count DESC, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Path, &h.Proto, &h.Count); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Totals returns overall counts for status displays: pages, stored
// versions, and how many of those pages are dated (i.e. posts).
func (s *Store) Totals() (pages, versions, posts int64) {
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&pages)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM versions`).Scan(&versions)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE date != ''`).Scan(&posts)
	return
}

// CountVersions returns how many historical versions a page has.
func (s *Store) CountVersions(p string) int64 {
	cp, ok := CleanPath(p)
	if !ok {
		return 0
	}
	var n int64
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM versions WHERE path = ?`, cp).Scan(&n)
	return n
}

// ---- settings -----------------------------------------------------------

// GetSetting returns a settings value ("" when unset).
func (s *Store) GetSetting(key string) string {
	var v string
	_ = s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

// SetSetting stores a settings value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- search -------------------------------------------------------------

// Search runs a full-text query over text pages.
func (s *Store) Search(query string, limit int) ([]SearchHit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT path, title, snippet(pages_fts, 1, '', '', '…', 14)
		FROM pages_fts WHERE pages_fts MATCH ? ORDER BY bm25(pages_fts, 5.0, 1.0) LIMIT ?`, match, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.Path, &h.Title, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ftsQuery converts free text into a safe FTS5 query: each word becomes a
// quoted prefix term, ANDed together.
func ftsQuery(q string) string {
	var terms []string
	for _, f := range strings.Fields(q) {
		f = strings.Trim(f, `"'.,;:!?()[]{}`)
		f = strings.ReplaceAll(f, `"`, ``)
		if f == "" {
			continue
		}
		terms = append(terms, `"`+f+`"*`)
		if len(terms) == 8 {
			break
		}
	}
	return strings.Join(terms, " ")
}

// ---- misc ---------------------------------------------------------------

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// MimeFor guesses a mime type from a path's extension.
func MimeFor(p string) string {
	base := path.Base(p)
	// special files are gemtext except .css
	if base == ".css" {
		return "text/css; charset=utf-8"
	}
	if strings.HasPrefix(base, ".") {
		return "text/gemini; charset=utf-8"
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".gmi", ".gemini":
		return "text/gemini; charset=utf-8"
	case ".txt", ".asc", ".key", ".text":
		return "text/plain; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".mp3":
		return "audio/mpeg"
	case ".mp4":
		return "video/mp4"
	case ".woff2":
		return "font/woff2"
	case ".zip":
		return "application/zip"
	case ".gz", ".tgz":
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}

func isBinaryMime(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return false
	}
	switch {
	case strings.HasPrefix(mime, "application/json"),
		strings.HasPrefix(mime, "application/xml"),
		strings.HasPrefix(mime, "application/atom+xml"):
		return false
	}
	return true
}
