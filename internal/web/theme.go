package web

import "strings"

// defaultThemeCSS returns a starting point for a .css file: the site's own
// colour variables, lifted from the embedded stylesheet so they always match
// what is actually shipping. A .css is injected after the stylesheet, so
// redefining these is all most themes need.
func defaultThemeCSS() string {
	css, err := assets.ReadFile("assets/style.css")
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("/* Theme for this folder and everything below it.\n")
	b.WriteString("   These are the site's current values — adjust to taste.\n")
	b.WriteString("   Any CSS works here; it is applied after the main stylesheet. */\n\n")
	for _, block := range []string{":root {", "@media (prefers-color-scheme: dark) {"} {
		if s := extractBlock(string(css), block); s != "" {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// extractBlock returns the brace-balanced CSS block starting at marker.
func extractBlock(css, marker string) string {
	i := strings.Index(css, marker)
	if i < 0 {
		return ""
	}
	depth := 0
	for j := i; j < len(css); j++ {
		switch css[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[i : j+1]
			}
		}
	}
	return ""
}
