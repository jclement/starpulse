// Package store persists all site state in a single SQLite database:
// pages (text and binary), page versions, hit counters, "now" micro-posts,
// settings, and an FTS5 full-text index over text pages.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path"
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
}

// Page is one stored page or file.
type Page struct {
	ID      int64
	Path    string // canonical: "/index.gmi", "/posts/.header", "/media/cat.png"
	Title   string
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

// NowPost is a short timestamped micro-post.
type NowPost struct {
	ID      int64
	Content string
	Created time.Time
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
CREATE TABLE IF NOT EXISTS now_posts (
	id      INTEGER PRIMARY KEY,
	content TEXT NOT NULL,
	created INTEGER NOT NULL
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}
	return &Store{db: db, KeepVersions: 25}, nil
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
	title := ""
	if !binary {
		title = TitleOf(cp, content, mime)
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
		res, err := tx.Exec(`INSERT INTO pages (path, title, content, mime, binary, created, updated) VALUES (?,?,?,?,?,?,?)`,
			cp, title, content, mime, boolInt(binary), now, now)
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
		if _, err := tx.Exec(`UPDATE pages SET title=?, content=?, mime=?, binary=?, updated=? WHERE id=?`,
			title, content, mime, boolInt(binary), now, id); err != nil {
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
	return &Page{ID: id, Path: cp, Title: title, Content: content, Mime: mime, Binary: binary,
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
	err := s.db.QueryRow(`SELECT id, path, title, content, mime, binary, created, updated FROM pages WHERE path = ?`, cp).
		Scan(&pg.ID, &pg.Path, &pg.Title, &pg.Content, &pg.Mime, &bin, &created, &updated)
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
	rows, err := s.db.Query(`SELECT path, title, mime, binary, length(content), updated FROM pages WHERE `+where+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		var bin int
		var updated int64
		if err := rows.Scan(&m.Path, &m.Title, &m.Mime, &bin, &m.Size, &updated); err != nil {
			return nil, err
		}
		m.Binary = bin != 0
		m.Updated = time.Unix(updated, 0)
		out = append(out, m)
	}
	return out, rows.Err()
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

// ---- now posts ----------------------------------------------------------

// AddNow appends a now micro-post.
func (s *Store) AddNow(content string) (*NowPost, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("empty now post")
	}
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO now_posts (content, created) VALUES (?,?)`, content, now.Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &NowPost{ID: id, Content: content, Created: now}, nil
}

// ListNow returns now posts, newest first (limit 0 = all).
func (s *Store) ListNow(limit int) ([]NowPost, error) {
	q := `SELECT id, content, created FROM now_posts ORDER BY created DESC, id DESC`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.db.Query(q+` LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NowPost
	for rows.Next() {
		var n NowPost
		var at int64
		if err := rows.Scan(&n.ID, &n.Content, &at); err != nil {
			return nil, err
		}
		n.Created = time.Unix(at, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteNow removes a now post.
func (s *Store) DeleteNow(id int64) error {
	res, err := s.db.Exec(`DELETE FROM now_posts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountVersions returns how many historical versions a path has.
func (s *Store) CountVersions(p string) int64 {
	var n int64
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM versions WHERE path = ?`, p).Scan(&n)
	return n
}

// Totals returns overall row counts for status displays.
func (s *Store) Totals() (pages, versions, nows int64) {
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&pages)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM versions`).Scan(&versions)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM now_posts`).Scan(&nows)
	return
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
	// special files are gemtext except .theme (CSS)
	if base == ".theme" {
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
