// Package render converts gemtext to HTML for the web listener.
package render

import (
	"fmt"
	"html"
	"strings"

	"github.com/jclement/starpulse/internal/gemtext"
)

// GemtextToHTML renders gemtext to an HTML fragment mirroring its structure.
func GemtextToHTML(src string) string {
	var b strings.Builder
	lines := gemtext.Parse(src)
	inPre, inList := false, false
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
				b.WriteString("</pre>\n")
			} else {
				if l.Meta != "" {
					fmt.Fprintf(&b, "<pre title=%q aria-label=%q>", l.Meta, l.Meta)
				} else {
					b.WriteString("<pre>")
				}
			}
			inPre = !inPre
		case gemtext.PreText:
			b.WriteString(html.EscapeString(l.Text))
			b.WriteString("\n")
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
			if isImage(url) {
				fmt.Fprintf(&b, "<p class=\"img\"><a href=%q><img src=%q alt=%q loading=\"lazy\"></a></p>\n",
					url, url, html.EscapeString(l.Text))
			} else {
				cls := ""
				if strings.HasPrefix(url, "gemini://") {
					cls = ` class="gem"`
				} else if strings.Contains(url, "://") {
					cls = ` class="ext"`
				}
				fmt.Fprintf(&b, "<p class=\"lnk\"><a%s href=%q>%s</a></p>\n", cls, url, html.EscapeString(l.Text))
			}
		default:
			if strings.TrimSpace(l.Text) == "" {
				continue
			}
			fmt.Fprintf(&b, "<p>%s</p>\n", html.EscapeString(l.Text))
		}
	}
	if inPre {
		b.WriteString("</pre>\n")
	}
	closeList()
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
