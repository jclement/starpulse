package web

import (
	"html"
	"net/http"
	"strings"

	"github.com/jclement/starpulse/internal/script"
)

// visitorCookie names the anonymous, per-browser identity handed to
// executable pages. It is not the admin session (that is starpulse_session)
// and grants nothing — it is only a stable string so a script can tell one
// returning visitor from another. It is set only when a script actually
// runs, so ordinary pages set no cookie.
const visitorCookie = "sp_visitor"

// serveScript runs an executable page and writes its output, or false if the
// URL is not a script. It is called before ordinary page resolution.
func (s *Server) serveScript(w http.ResponseWriter, r *http.Request) bool {
	storePath, _, ok := s.Site.ScriptFor(r.URL.Path)
	if !ok {
		return false
	}

	// a stable identity for this browser: a cookie we set the first time a
	// script needs one. Bearer, not proof — the script is told so.
	id, err := r.Cookie(visitorCookie)
	identity := ""
	if err == nil {
		identity = id.Value
	} else {
		identity = randToken(18)
		http.SetCookie(w, &http.Cookie{
			Name: visitorCookie, Value: identity, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
			MaxAge: 365 * 24 * 3600,
		})
	}

	q := r.URL.Query()
	query := map[string]string{}
	for k, vs := range q {
		if len(vs) > 0 {
			query[k] = vs[0]
		}
	}
	req := script.Request{
		Query:        query,
		Proto:        s.proto(r),
		Host:         s.Cfg.Hostname,
		Identity:     identity,
		IdentityKind: "cookie",
		Verified:     false,
	}
	if v, has := query["input"]; has {
		req.Input, req.HasInput = v, true
	}

	res, runErr := s.Site.RunScript(r.Context(), storePath, r.URL.Path, req)
	if runErr != nil {
		body := `<h1>Script error</h1><pre>` + html.EscapeString(runErr.Error()) + `</pre>`
		if s.loggedIn(r) {
			body += `<p class="lnk"><a href="/admin/edit?path=` + html.EscapeString(storePath) + `">edit</a></p>`
		}
		s.render(w, r, http.StatusInternalServerError, s.Cfg.Hostname, s.Cfg.Hostname, "", "", body)
		return true
	}

	var body string
	if res.Gemtext {
		body = s.gemtextBody(res.Body)
	} else {
		body = `<pre>` + html.EscapeString(res.Body) + `</pre>`
	}
	if res.NeedInput {
		body += scriptInputForm(r.URL.Path, q, res.Prompt, res.Sensitive)
	}
	// scripts change on every visit, and set a cookie — never cache them
	noStore(w)
	s.render(w, r, http.StatusOK, s.Cfg.Hostname, s.Cfg.Hostname, "", "", body)
	return true
}

// scriptInputForm is the web form that answers a script's input() request —
// a GET back to the same URL carrying the answer as ?input=, which is how a
// resubmit re-enters the script with the line filled in.
func scriptInputForm(path string, q map[string][]string, prompt string, sensitive bool) string {
	typ := "text"
	if sensitive {
		typ = "password"
	}
	var b strings.Builder
	b.WriteString(`<form class="script-input" method="get" action="` + html.EscapeString(path) + `">`)
	// preserve any other query parameters, so a script that reads them keeps
	// working across the input round-trip
	for k, vs := range q {
		if k == "input" || len(vs) == 0 {
			continue
		}
		b.WriteString(`<input type="hidden" name="` + html.EscapeString(k) + `" value="` + html.EscapeString(vs[0]) + `">`)
	}
	if prompt != "" {
		b.WriteString(`<label for="script-input">` + html.EscapeString(prompt) + `</label>`)
	}
	b.WriteString(`<input id="script-input" type="` + typ + `" name="input" autofocus autocomplete="off">`)
	b.WriteString(`<button type="submit">send</button></form>`)
	return b.String()
}
