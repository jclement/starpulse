// Package gemtext implements a small parser for text/gemini documents.
package gemtext

import "strings"

// LineType enumerates gemtext line types.
type LineType int

const (
	Text LineType = iota
	Link
	Heading1
	Heading2
	Heading3
	ListItem
	Quote
	PreToggle // ``` line; Meta holds the alt text
	PreText   // literal line inside a preformatted block
)

// Line is one parsed gemtext line.
type Line struct {
	Type LineType
	Text string // display text (or literal text for PreText)
	URL  string // Link only
	Meta string // PreToggle alt text
}

// Parse splits a gemtext document into typed lines.
func Parse(src string) []Line {
	var out []Line
	pre := false
	for _, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(raw, "```") {
			pre = !pre
			out = append(out, Line{Type: PreToggle, Meta: strings.TrimSpace(raw[3:])})
			continue
		}
		if pre {
			out = append(out, Line{Type: PreText, Text: raw})
			continue
		}
		switch {
		case strings.HasPrefix(raw, "=>"):
			rest := strings.TrimSpace(raw[2:])
			url, label, found := cutAny(rest, " \t")
			if !found {
				label = ""
			}
			label = strings.TrimSpace(label)
			if label == "" {
				label = url
			}
			out = append(out, Line{Type: Link, URL: url, Text: label})
		case strings.HasPrefix(raw, "###"):
			out = append(out, Line{Type: Heading3, Text: strings.TrimSpace(raw[3:])})
		case strings.HasPrefix(raw, "##"):
			out = append(out, Line{Type: Heading2, Text: strings.TrimSpace(raw[2:])})
		case strings.HasPrefix(raw, "#"):
			out = append(out, Line{Type: Heading1, Text: strings.TrimSpace(raw[1:])})
		case strings.HasPrefix(raw, "* "):
			out = append(out, Line{Type: ListItem, Text: strings.TrimSpace(raw[2:])})
		case strings.HasPrefix(raw, ">"):
			out = append(out, Line{Type: Quote, Text: strings.TrimSpace(raw[1:])})
		default:
			out = append(out, Line{Type: Text, Text: raw})
		}
	}
	return out
}

// cutAny splits s at the first occurrence of any rune in chars.
func cutAny(s, chars string) (before, after string, found bool) {
	if i := strings.IndexAny(s, chars); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// FirstHeading returns the first level-1 heading of a document, or "".
func FirstHeading(src string) string {
	for _, l := range Parse(src) {
		if l.Type == Heading1 {
			return l.Text
		}
	}
	return ""
}

// PlainText flattens a gemtext document to indexable text: markup stripped,
// link labels kept, URLs dropped.
func PlainText(src string) string {
	var b strings.Builder
	for _, l := range Parse(src) {
		switch l.Type {
		case PreToggle:
			continue
		default:
			if l.Text != "" {
				b.WriteString(l.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}
