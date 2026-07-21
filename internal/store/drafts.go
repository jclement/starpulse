package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Drafts live in their own tables, parallel to pages and versions.
//
// That is the whole security design. Around thirty places read page content
// — the site resolver, {{list}}, feeds, search, the gemini directory index,
// the terminal browsers, backup — and none of them change, because a draft
// is not in the table they read. A draft cannot leak by omission; the worst
// a missed check can do is show the admin a stale page.
//
// Publishing is one SavePage, so the published history stays a list of
// releases rather than a transcript of everything typed on the way there.

const draftSchema = `
CREATE TABLE IF NOT EXISTS drafts (
	path    TEXT PRIMARY KEY,
	title   TEXT NOT NULL DEFAULT '',
	content BLOB NOT NULL,
	mime    TEXT NOT NULL,
	author  TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL,
	updated INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS draft_versions (
	id       INTEGER PRIMARY KEY,
	path     TEXT NOT NULL,
	content  BLOB NOT NULL,
	mime     TEXT NOT NULL,
	author   TEXT NOT NULL DEFAULT '',
	saved_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS draft_versions_path ON draft_versions(path, saved_at DESC);
`

// Draft is unpublished work on a page. The page it belongs to may not exist
// yet — a draft of something never published simply has no pages row, which
// is why it 404s everywhere without anyone checking.
type Draft struct {
	Path    string
	Title   string
	Content []byte
	Mime    string
	Binary  bool
	Author  string
	Created time.Time
	Updated time.Time
}

// SaveDraft writes unpublished content for a path, snapshotting whatever the
// draft held before so a draft is as recoverable as a published page.
func (s *Store) SaveDraft(p string, content []byte, mime, author string) (*Draft, error) {
	cp, ok := CleanPath(p)
	if !ok {
		return nil, fmt.Errorf("invalid path %q", p)
	}
	if mime == "" {
		mime = MimeFor(cp)
	}
	title := ""
	if !isBinaryMime(mime) {
		title = TitleOf(cp, content, mime)
	}
	now := time.Now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var created int64
	var prev []byte
	var prevMime string
	err = tx.QueryRow(`SELECT created, content, mime FROM drafts WHERE path = ?`, cp).
		Scan(&created, &prev, &prevMime)
	switch {
	case err == sql.ErrNoRows:
		created = now
	case err != nil:
		return nil, err
	default:
		// snapshot the outgoing draft, exactly as SavePage does for a page
		if _, err := tx.Exec(`INSERT INTO draft_versions (path, content, mime, author, saved_at)
			VALUES (?,?,?,?,?)`, cp, prev, prevMime, author, now); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(`INSERT INTO drafts (path, title, content, mime, author, created, updated)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
			title=excluded.title, content=excluded.content, mime=excluded.mime,
			author=excluded.author, updated=excluded.updated`,
		cp, title, content, mime, author, created, now); err != nil {
		return nil, err
	}
	if err := s.trimDraftVersions(tx, cp); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetDraft(cp)
}

// trimDraftVersions caps draft history the way KeepVersions caps a page's.
func (s *Store) trimDraftVersions(tx *sql.Tx, p string) error {
	if s.KeepVersions <= 0 {
		return nil
	}
	_, err := tx.Exec(`DELETE FROM draft_versions WHERE path = ? AND id NOT IN (
		SELECT id FROM draft_versions WHERE path = ? ORDER BY saved_at DESC, id DESC LIMIT ?)`,
		p, p, s.KeepVersions)
	return err
}

// GetDraft returns the unpublished content for a path, if any.
func (s *Store) GetDraft(p string) (*Draft, error) {
	cp, ok := CleanPath(p)
	if !ok {
		return nil, fmt.Errorf("invalid path %q", p)
	}
	var d Draft
	var created, updated int64
	err := s.db.QueryRow(`SELECT path, title, content, mime, author, created, updated
		FROM drafts WHERE path = ?`, cp).
		Scan(&d.Path, &d.Title, &d.Content, &d.Mime, &d.Author, &created, &updated)
	if err != nil {
		return nil, err
	}
	d.Binary = isBinaryMime(d.Mime)
	d.Created = time.Unix(created, 0)
	d.Updated = time.Unix(updated, 0)
	return &d, nil
}

// HasDraft reports whether a path has unpublished work.
func (s *Store) HasDraft(p string) bool {
	cp, ok := CleanPath(p)
	if !ok {
		return false
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM drafts WHERE path = ?`, cp).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// DraftPaths returns every path with unpublished work, for badging listings.
func (s *Store) DraftPaths() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT path FROM drafts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// ListDrafts returns draft metadata, newest first.
func (s *Store) ListDrafts() ([]Meta, error) {
	rows, err := s.db.Query(`SELECT path, title, mime, length(content), created, updated
		FROM drafts ORDER BY updated DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		var created, updated int64
		if err := rows.Scan(&m.Path, &m.Title, &m.Mime, &m.Size, &created, &updated); err != nil {
			return nil, err
		}
		m.Binary = isBinaryMime(m.Mime)
		m.Created = time.Unix(created, 0)
		m.Updated = time.Unix(updated, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DiscardDraft throws away unpublished work and its history. If the page was
// never published there is nothing left afterwards, which is what "delete"
// means for something that only ever existed as a draft.
func (s *Store) DiscardDraft(p string) error {
	cp, ok := CleanPath(p)
	if !ok {
		return fmt.Errorf("invalid path %q", p)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM drafts WHERE path = ?`, cp)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no draft for %q", cp)
	}
	if _, err := tx.Exec(`DELETE FROM draft_versions WHERE path = ?`, cp); err != nil {
		return err
	}
	return tx.Commit()
}

// PublishDraft promotes unpublished work to the live page: one SavePage, so
// however many times the draft was saved, the published history gains a
// single entry. The draft and its own history are cleared.
func (s *Store) PublishDraft(p, author string) (*Page, error) {
	d, err := s.GetDraft(p)
	if err != nil {
		return nil, err
	}
	pg, err := s.SavePage(d.Path, d.Content, d.Mime, author)
	if err != nil {
		return nil, err
	}
	if err := s.DiscardDraft(d.Path); err != nil {
		return nil, err
	}
	return pg, nil
}

// ListDraftVersions returns a draft's own history, newest first.
func (s *Store) ListDraftVersions(p string) ([]Version, error) {
	cp, ok := CleanPath(p)
	if !ok {
		return nil, fmt.Errorf("invalid path %q", p)
	}
	rows, err := s.db.Query(`SELECT id, path, mime, author, saved_at, length(content)
		FROM draft_versions WHERE path = ? ORDER BY saved_at DESC, id DESC`, cp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var savedAt int64
		if err := rows.Scan(&v.ID, &v.Path, &v.Mime, &v.Author, &savedAt, &v.Size); err != nil {
			return nil, err
		}
		v.SavedAt = time.Unix(savedAt, 0)
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetDraftVersion returns one draft snapshot, content included.
func (s *Store) GetDraftVersion(id int64) (*Version, error) {
	var v Version
	var savedAt int64
	err := s.db.QueryRow(`SELECT id, path, content, mime, author, saved_at
		FROM draft_versions WHERE id = ?`, id).
		Scan(&v.ID, &v.Path, &v.Content, &v.Mime, &v.Author, &savedAt)
	if err != nil {
		return nil, err
	}
	v.SavedAt = time.Unix(savedAt, 0)
	v.Size = int64(len(v.Content))
	return &v, nil
}

// renameDraft moves a draft and its history with the page it belongs to.
// Called from RenamePage, inside that move's transaction.
func renameDraft(tx *sql.Tx, oldPath, newPath string) error {
	if _, err := tx.Exec(`UPDATE drafts SET path = ? WHERE path = ?`, newPath, oldPath); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE draft_versions SET path = ? WHERE path = ?`, newPath, oldPath)
	return err
}

// dropDraft removes a draft when its page is deleted, inside that deletion's
// transaction — a draft of a page that no longer exists is a page nobody can
// see and nobody can reach.
func dropDraft(tx *sql.Tx, p string) error {
	if _, err := tx.Exec(`DELETE FROM drafts WHERE path = ?`, p); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM draft_versions WHERE path = ?`, p)
	return err
}
