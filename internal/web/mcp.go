package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jclement/starpulse/internal/store"
)

// The /mcp endpoint implements MCP's streamable-HTTP transport, stateless
// flavor: every request is a single JSON-RPC message answered with a single
// JSON response. Bearer token = admin password.

const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func schema(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return obj("type", "object", "properties", props, "required", required)
}

var mcpTools = []mcpTool{
	{"list_pages", "List every page and file on the site (path, title, mime, size).", schema(nil, obj())},
	{"read_page", "Read a page's raw source (gemtext for pages, base64 for binary files).", schema([]string{"path"}, obj(
		"path", obj("type", "string", "description", "Storage path, e.g. /index.gmi or /posts/.header")))},
	{"write_page", "Create or update a page with gemtext (or CSS for .theme files). Previous content is kept as a restorable version.", schema([]string{"path", "content"}, obj(
		"path", obj("type", "string", "description", "Storage path, e.g. /about.gmi, /posts/2026-07-19-hi.gmi, /.theme"),
		"content", obj("type", "string"),
		"mime", obj("type", "string", "description", "Optional mime type; inferred from the extension when omitted")))},
	{"upload_file", "Upload a binary file (image etc.) from base64 content.", schema([]string{"path", "content_base64"}, obj(
		"path", obj("type", "string", "description", "e.g. /media/photo.jpg"),
		"content_base64", obj("type", "string"),
		"mime", obj("type", "string")))},
	{"delete_page", "Delete a page (a final version snapshot is kept, so this is restorable).", schema([]string{"path"}, obj(
		"path", obj("type", "string")))},
	{"search", "Full-text search across the site.", schema([]string{"query"}, obj(
		"query", obj("type", "string")))},
	{"get_stats", "Per-page view counts broken down by protocol (http, gemini, +tor variants).", schema(nil, obj())},
	{"post_now", "Publish a short 'now' micro-post (shown on /now and via {{now}} in pages).", schema([]string{"content"}, obj(
		"content", obj("type", "string")))},
	{"list_now", "List now micro-posts, newest first.", schema(nil, obj(
		"limit", obj("type", "integer", "description", "0 or omitted = all")))},
	{"list_versions", "List saved versions of a page.", schema([]string{"path"}, obj(
		"path", obj("type", "string")))},
	{"restore_version", "Restore a page to a previous version by version id.", schema([]string{"id"}, obj(
		"id", obj("type", "integer")))},
}

// registerMCP wires up the /mcp endpoint.
func (s *Server) registerMCP(mux *http.ServeMux) {
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !s.apiAuthed(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="starpulse"`)
			jsonErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		switch r.Method {
		case http.MethodPost:
			s.mcpPost(w, r)
		case http.MethodGet:
			// no server-initiated stream in stateless mode
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (s *Server) mcpPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, s.Cfg.MaxUploadBytes*2))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad body")
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonOut(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}

	// notifications get 202 + no body
	if req.ID == nil || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = obj(
			"protocolVersion", mcpProtocolVersion,
			"capabilities", obj("tools", obj("listChanged", false)),
			"serverInfo", obj("name", "starpulse", "title", "starpulse smolweb CMS", "version", "1"),
			"instructions", "Content is gemtext. Pages live at paths like /index.gmi and /posts/2026-07-19-title.gmi. Special inherited files: .header/.footer (gemtext) and .theme (CSS) per folder. Directives: {{list dir n}}, {{include p}}, {{now n}}, {{count}}, {{random p}}.",
		)
	case "ping":
		resp.Result = obj()
	case "tools/list":
		resp.Result = obj("tools", mcpTools)
	case "tools/call":
		resp.Result = s.mcpToolCall(req.Params)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	jsonOut(w, http.StatusOK, resp)
}

// toolText wraps plain text in an MCP tool result.
func toolText(text string) map[string]any {
	return obj("content", []any{obj("type", "text", "text", text)}, "isError", false)
}

func toolErr(text string) map[string]any {
	return obj("content", []any{obj("type", "text", "text", text)}, "isError", true)
}

func toolJSON(v any) map[string]any {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolErr(err.Error())
	}
	return toolText(string(b))
}

func (s *Server) mcpToolCall(params json.RawMessage) map[string]any {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Path          string `json:"path"`
			Content       string `json:"content"`
			ContentBase64 string `json:"content_base64"`
			Mime          string `json:"mime"`
			Query         string `json:"query"`
			Limit         int    `json:"limit"`
			ID            int64  `json:"id"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolErr("bad arguments: " + err.Error())
	}
	a := p.Arguments

	switch p.Name {
	case "list_pages":
		metas, err := s.Store.ListAll()
		if err != nil {
			return toolErr(err.Error())
		}
		type row struct {
			Path    string `json:"path"`
			Title   string `json:"title,omitempty"`
			Mime    string `json:"mime"`
			Size    int64  `json:"size"`
			Updated string `json:"updated"`
		}
		rows := make([]row, 0, len(metas))
		for _, m := range metas {
			rows = append(rows, row{m.Path, m.Title, m.Mime, m.Size, m.Updated.UTC().Format("2006-01-02T15:04:05Z")})
		}
		return toolJSON(rows)

	case "read_page":
		pg, err := s.Store.GetPage(a.Path)
		if err != nil {
			return toolErr("not found: " + a.Path)
		}
		if pg.Binary {
			return toolJSON(obj("path", pg.Path, "mime", pg.Mime, "size", len(pg.Content),
				"content_base64", base64.StdEncoding.EncodeToString(pg.Content)))
		}
		return toolText(string(pg.Content))

	case "write_page":
		pg, err := s.Store.SavePage(a.Path, []byte(a.Content), a.Mime, "mcp")
		if err != nil {
			return toolErr(err.Error())
		}
		return toolText(fmt.Sprintf("saved %s (%d bytes)", pg.Path, len(pg.Content)))

	case "upload_file":
		raw, err := base64.StdEncoding.DecodeString(a.ContentBase64)
		if err != nil {
			return toolErr("invalid base64: " + err.Error())
		}
		if int64(len(raw)) > s.Cfg.MaxUploadBytes {
			return toolErr(fmt.Sprintf("file exceeds max upload size (%d bytes)", s.Cfg.MaxUploadBytes))
		}
		mime := a.Mime
		if mime == "" {
			mime = store.MimeFor(a.Path)
		}
		pg, err := s.Store.SavePage(a.Path, raw, mime, "mcp")
		if err != nil {
			return toolErr(err.Error())
		}
		return toolText(fmt.Sprintf("uploaded %s (%s, %d bytes)", pg.Path, pg.Mime, len(raw)))

	case "delete_page":
		if err := s.Store.DeletePage(a.Path, "mcp"); err != nil {
			return toolErr("not found: " + a.Path)
		}
		return toolText("deleted " + a.Path + " (restorable from versions)")

	case "search":
		hits, err := s.Store.Search(a.Query, 30)
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(hits)

	case "get_stats":
		hits, err := s.Store.Stats()
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(hits)

	case "post_now":
		post, err := s.Store.AddNow(a.Content)
		if err != nil {
			return toolErr(err.Error())
		}
		return toolText(fmt.Sprintf("posted now #%d at %s", post.ID, post.Created.Format("2006-01-02 15:04")))

	case "list_now":
		posts, err := s.Store.ListNow(a.Limit)
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(posts)

	case "list_versions":
		versions, err := s.Store.ListVersions(a.Path)
		if err != nil {
			return toolErr(err.Error())
		}
		type vj struct {
			ID      int64  `json:"id"`
			Author  string `json:"author"`
			SavedAt string `json:"saved_at"`
			Size    int64  `json:"size"`
		}
		rows := make([]vj, 0, len(versions))
		for _, v := range versions {
			rows = append(rows, vj{v.ID, v.Author, v.SavedAt.UTC().Format("2006-01-02T15:04:05Z"), v.Size})
		}
		return toolJSON(rows)

	case "restore_version":
		pg, err := s.Store.RestoreVersion(a.ID, "mcp restore")
		if err != nil {
			return toolErr("not found")
		}
		return toolText("restored " + pg.Path)

	default:
		return toolErr("unknown tool: " + p.Name)
	}
}
