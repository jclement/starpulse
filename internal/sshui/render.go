package sshui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jclement/starpulse/internal/gemtext"
)

var (
	stH1     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("215"))
	stH2     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("222"))
	stH3     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230"))
	stLink   = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	stLinkExt = lipgloss.NewStyle().Foreground(lipgloss.Color("108"))
	stLinkSel = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("215")).Bold(true)
	stQuote  = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("245"))
	stPre    = lipgloss.NewStyle().Foreground(lipgloss.Color("150"))
	stBullet = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	stDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// pageLink is one followable link on a rendered page.
type pageLink struct {
	URL   string
	Label string
	Line  int // first rendered line of this link
}

// renderDoc converts gemtext to styled terminal lines and collects links.
// selected is the index of the highlighted link (-1 for none).
func renderDoc(src string, width int, selected int) (lines []string, links []pageLink) {
	if width < 10 {
		width = 10
	}
	wrap := func(st lipgloss.Style, text string) []string {
		if strings.TrimSpace(text) == "" {
			return []string{""}
		}
		return strings.Split(st.Width(width).Render(text), "\n")
	}
	for _, l := range gemtext.Parse(src) {
		switch l.Type {
		case gemtext.PreToggle:
			// skip the fence line itself
		case gemtext.PreText:
			t := l.Text
			if len(t) > width {
				t = t[:width]
			}
			lines = append(lines, stPre.Render(t))
		case gemtext.Heading1:
			lines = append(lines, wrap(stH1, l.Text)...)
		case gemtext.Heading2:
			lines = append(lines, wrap(stH2, "▸ "+l.Text)...)
		case gemtext.Heading3:
			lines = append(lines, wrap(stH3, "· "+l.Text)...)
		case gemtext.ListItem:
			for i, ln := range wrap(lipgloss.NewStyle(), l.Text) {
				if i == 0 {
					lines = append(lines, stBullet.Render("• ")+ln)
				} else {
					lines = append(lines, "  "+ln)
				}
			}
		case gemtext.Quote:
			lines = append(lines, wrap(stQuote, "│ "+l.Text)...)
		case gemtext.Link:
			idx := len(links)
			style := stLink
			if strings.Contains(l.URL, "://") {
				style = stLinkExt
			}
			if idx == selected {
				style = stLinkSel
			}
			label := fmt.Sprintf("[%d] %s", idx+1, l.Text)
			links = append(links, pageLink{URL: l.URL, Label: l.Text, Line: len(lines)})
			for i, ln := range wrap(style, label) {
				if i == 0 {
					lines = append(lines, stDim.Render("⇒ ")+ln)
				} else {
					lines = append(lines, "  "+ln)
				}
			}
		default:
			lines = append(lines, wrap(lipgloss.NewStyle(), l.Text)...)
		}
	}
	return lines, links
}
