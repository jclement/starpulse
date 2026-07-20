// Package render converts gemtext to HTML for the web listener.
package render

import (
	"fmt"
	"html"
	"strings"

	"github.com/jclement/starpulse/internal/gemtext"
)

// Options tunes rendering. The zero value renders plain HTML.
type Options struct {
	// Highlight renders a fenced block for a given language, returning
	// false when it cannot (unknown language, decorative block).
	Highlight func(lang, code string) (string, bool)
}

// GemtextToHTML renders gemtext to an HTML fragment mirroring its structure.
func GemtextToHTML(src string) string { return GemtextToHTMLOpts(src, Options{}) }

// GemtextToHTMLOpts renders gemtext with options.
func GemtextToHTMLOpts(src string, opt Options) string {
	var b strings.Builder
	lines := gemtext.Parse(src)
	inPre, inList := false, false
	var preLines []string
	var preAlt string
	closeList := func() {
		if inList {
			b.WriteString("</ul>\n")
			inList = false
		}
	}
	for _, l := range lines {
		if l.Type != gemtext.ListItem && l.Type != gemtext.PreText && l.Type != gemtext.PreToggle {
			closeList()
		}
		switch l.Type {
		case gemtext.PreToggle:
			closeList()
			if inPre {
				b.WriteString(renderPre(preAlt, preLines, opt))
				preLines, preAlt = nil, ""
			} else {
				preAlt = l.Meta
			}
			inPre = !inPre
		case gemtext.PreText:
			preLines = append(preLines, l.Text)
		case gemtext.Heading1:
			fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(l.Text))
		case gemtext.Heading2:
			fmt.Fprintf(&b, "<h2>%s</h2>\n", html.EscapeString(l.Text))
		case gemtext.Heading3:
			fmt.Fprintf(&b, "<h3>%s</h3>\n", html.EscapeString(l.Text))
		case gemtext.ListItem:
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			fmt.Fprintf(&b, "<li>%s</li>\n", html.EscapeString(l.Text))
		case gemtext.Quote:
			fmt.Fprintf(&b, "<blockquote>%s</blockquote>\n", html.EscapeString(l.Text))
		case gemtext.Link:
			url := webURL(l.URL)
			// escape for HTML attribute context — %q is a Go-string quote,
			// not an HTML escape, so it would let a "> in a URL break out.
			esc := html.EscapeString(url)
			if isImage(url) {
				fmt.Fprintf(&b, "<p class=\"img\"><a href=\"%s\"><img src=\"%s\" alt=\"%s\" loading=\"lazy\"></a></p>\n",
					esc, esc, html.EscapeString(l.Text))
			} else {
				cls := ""
				if strings.HasPrefix(url, "gemini://") {
					cls = ` class="gem"`
				} else if strings.Contains(url, "://") {
					cls = ` class="ext"`
				}
				fmt.Fprintf(&b, "<p class=\"lnk\"><a%s href=\"%s\">%s</a></p>\n", cls, esc, html.EscapeString(l.Text))
			}
		default:
			if strings.TrimSpace(l.Text) == "" {
				continue
			}
			fmt.Fprintf(&b, "<p>%s</p>\n", html.EscapeString(l.Text))
		}
	}
	if inPre {
		b.WriteString(renderPre(preAlt, preLines, opt))
	}
	closeList()
	return b.String()
}

// renderPre emits one preformatted block, highlighted when possible.
func renderPre(alt string, lines []string, opt Options) string {
	code := strings.Join(lines, "\n")
	if len(lines) > 0 {
		code += "\n"
	}
	if opt.Highlight != nil && alt != "" {
		if out, ok := opt.Highlight(alt, code); ok {
			return fmt.Sprintf("<div class=\"hl\" data-lang=%q>%s</div>\n", alt, out)
		}
	}
	var b strings.Builder
	if alt != "" {
		fmt.Fprintf(&b, "<pre title=%q aria-label=%q>", alt, alt)
	} else {
		b.WriteString("<pre>")
	}
	b.WriteString(html.EscapeString(code))
	b.WriteString("</pre>\n")
	return b.String()
}

// webURL rewrites relative links ending in .gmi to their extensionless web
// form so authored gemtext cross-links work on both protocols.
func webURL(u string) string {
	if strings.Contains(u, "://") || strings.HasPrefix(u, "mailto:") {
		return u
	}
	if strings.HasSuffix(u, ".gmi") {
		return strings.TrimSuffix(u, ".gmi")
	}
	return u
}

func isImage(url string) bool {
	u := strings.ToLower(url)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"} {
		if strings.HasSuffix(u, ext) {
			return true
		}
	}
	return false
}
