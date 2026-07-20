package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jclement/starpulse/internal/auth"
	"github.com/jclement/starpulse/internal/render"
	"github.com/jclement/starpulse/internal/store"
)

// apiAuthed reports whether the request may use the API: a bearer token
// matching the admin password, or a valid admin session cookie (lets the
// admin UI's JS call the same endpoints).
func (s *Server) apiAuthed(r *http.Request) bool {
	if s.loggedIn(r) {
		return true
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(h, "Bearer ")
	// an OAuth access token (signed, expiring) — no throttle needed, it is
	// unguessable and self-validating
	if s.Sessions.Valid(token) {
		return true
	}
	ip := clientIP(r)
	if s.authGate().blocked(ip, time.Now()) {
		return false
	}
	if s.Cfg.AdminPassword != "" && auth.CheckPassword(s.Cfg.AdminPassword, token) {
		s.authGate().succeed(ip)
		return true
	}
	s.authGate().fail(ip, time.Now())
	return false
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	jsonOut(w, status, map[string]string{"error": msg})
}

// registerAPI wires up the /api REST endpoints (bearer token = admin password).
func (s *Server) registerAPI(mux *http.ServeMux) {
	guard := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.apiAuthed(r) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="starpulse"`)
				jsonErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("/api/pages", guard(s.apiPages))
	mux.HandleFunc("/api/pages/", guard(s.apiPage))
	mux.HandleFunc("/api/search", guard(s.apiSearch))
	mux.HandleFunc("/api/stats", guard(s.apiStats))
	mux.HandleFunc("/api/now", guard(s.apiNow))
	mux.HandleFunc("/api/versions", guard(s.apiVersions))
	mux.HandleFunc("/api/restore", guard(s.apiRestore))
	mux.HandleFunc("/api/preview", guard(s.apiPreview))
}

type pageJSON struct {
	Path    string `json:"path"`
	Title   string `json:"title,omitempty"`
	Mime    string `json:"mime"`
	Binary  bool   `json:"binary"`
	Size    int64  `json:"size"`
	Updated string `json:"updated"`
	Content string `json:"content,omitempty"` // omitted in listings; base64 for binary
}

// apiPages handles GET /api/pages — list all pages.
func (s *Server) apiPages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	metas, err := s.Store.ListAll()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]pageJSON, 0, len(metas))
	for _, m := range metas {
		out = append(out, pageJSON{
			Path: m.Path, Title: m.Title, Mime: m.Mime, Binary: m.Binary,
			Size: m.Size, Updated: m.Updated.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	jsonOut(w, http.StatusOK, map[string]any{"pages": out})
}

// apiPage handles GET/PUT/DELETE /api/pages/<path>.
func (s *Server) apiPage(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/pages")
	// GET/DELETE address an existing page exactly; writes get .gmi by default
	raw, ok := store.CleanPath(p)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	cp := raw
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		if !s.Store.PageExists(raw) {
			cp, _ = store.CleanPath(store.DefaultExt(raw))
		}
	}
	switch r.Method {
	case http.MethodGet:
		pg, err := s.Store.GetPage(cp)
		if err != nil {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		pj := pageJSON{
			Path: pg.Path, Title: pg.Title, Mime: pg.Mime, Binary: pg.Binary,
			Size: int64(len(pg.Content)), Updated: pg.Updated.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if pg.Binary {
			pj.Content = base64Std(pg.Content)
		} else {
			pj.Content = string(pg.Content)
		}
		jsonOut(w, http.StatusOK, pj)
	case http.MethodPut, http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, s.Cfg.MaxUploadBytes+1))
		if err != nil || int64(len(body)) > s.Cfg.MaxUploadBytes {
			jsonErr(w, http.StatusRequestEntityTooLarge, "body exceeds max upload size")
			return
		}
		mime := r.Header.Get("Content-Type")
		if i := strings.Index(mime, ";"); i > 0 && !strings.HasPrefix(mime, "text/gemini") {
			mime = strings.TrimSpace(mime[:i])
		}
		if mime == "" || mime == "application/octet-stream" || mime == "application/x-www-form-urlencoded" {
			mime = store.MimeFor(cp)
		}
		pg, err := s.Store.SavePage(cp, body, mime, "api")
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"saved": pg.Path, "size": len(pg.Content)})
	case http.MethodDelete:
		if err := s.Store.DeletePage(cp, "api"); err != nil {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"deleted": cp})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	hits, err := s.Store.Search(r.URL.Query().Get("q"), 50)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"hits": orEmpty(hits)})
}

func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	hits, err := s.Store.Stats()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"hits": orEmpty(hits)})
}

// apiNow handles GET (list) and POST (create) /api/now.
func (s *Server) apiNow(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		pages := s.Store.StreamPages(s.nowFolder(), limit)
		out := make([]map[string]any, 0, len(pages))
		for _, p := range pages {
			out = append(out, map[string]any{
				"path": p.Path, "content": string(p.Content),
				"date": p.Date, "created": p.Created.UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		jsonOut(w, http.StatusOK, map[string]any{"posts": out})
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			jsonErr(w, http.StatusBadRequest, "bad body")
			return
		}
		text := string(body)
		// accept {"content": "..."} or raw text
		var obj struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(body, &obj) == nil && obj.Content != "" {
			text = obj.Content
		}
		text = strings.TrimSpace(text)
		if text == "" {
			jsonErr(w, http.StatusBadRequest, "empty note")
			return
		}
		path := s.Store.NewStreamPath(s.nowFolder(), time.Now().In(s.loc()))
		pg, err := s.Store.SavePage(path, []byte(text+"\n"), "", "api note")
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"path": pg.Path, "content": string(pg.Content)})
	case http.MethodDelete:
		p := r.URL.Query().Get("path")
		if err := s.Store.DeletePage(p, "api"); err != nil {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"deleted": p})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) apiVersions(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if id := r.URL.Query().Get("id"); id != "" {
		vid, _ := strconv.ParseInt(id, 10, 64)
		v, err := s.Store.GetVersion(vid)
		if err != nil {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{
			"id": v.ID, "path": v.Path, "mime": v.Mime, "author": v.Author,
			"saved_at": v.SavedAt.UTC().Format("2006-01-02T15:04:05Z"),
			"content":  string(v.Content),
		})
		return
	}
	versions, err := s.Store.ListVersions(p)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad path")
		return
	}
	type vj struct {
		ID      int64  `json:"id"`
		Author  string `json:"author"`
		SavedAt string `json:"saved_at"`
		Size    int64  `json:"size"`
	}
	out := make([]vj, 0, len(versions))
	for _, v := range versions {
		out = append(out, vj{ID: v.ID, Author: v.Author, SavedAt: v.SavedAt.UTC().Format("2006-01-02T15:04:05Z"), Size: v.Size})
	}
	jsonOut(w, http.StatusOK, map[string]any{"path": p, "versions": out})
}

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	pg, err := s.Store.RestoreVersion(id, "api restore")
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not found")
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"restored": pg.Path})
}

// apiPreview renders posted gemtext to an HTML fragment (used by the admin
// editor's live preview).
func (s *Server) apiPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.Cfg.MaxUploadBytes))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad body")
		return
	}
	// the editor posts the path too, so the preview can resolve the same
	// .header/.footer and relative directives the saved page would
	gmi := s.Site.Preview(r.URL.Query().Get("path"), string(body))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, wrapEmoji(render.GemtextToHTMLOpts(gmi, s.renderOpts())))
}

func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func base64Std(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
