package sshui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jclement/starpulse/internal/gemtext"
)

// styles holds every lipgloss style used by the TUI. They MUST be built
// from the session's renderer (wish/bubbletea.MakeRenderer) — the global
// renderer detects the server's own stdout, which under systemd is
// colorless, and would strip all styling for every client.
type styles struct {
	h1, h2, h3       lipgloss.Style
	link, linkExt    lipgloss.Style
	linkSel, linkNum lipgloss.Style
	quote, pre       lipgloss.Style
	bullet, dim      lipgloss.Style
	bar, barDim      lipgloss.Style
	status, err      lipgloss.Style
	help             lipgloss.Style
	text             lipgloss.Style
	// bbs* render the bottom bar, old-school white-on-blue
	bbsBar, bbsKey, bbsOK, bbsErr lipgloss.Style
}

func makeStyles(r *lipgloss.Renderer) *styles {
	return &styles{
		h1:      r.NewStyle().Bold(true).Foreground(lipgloss.Color("215")),
		h2:      r.NewStyle().Bold(true).Foreground(lipgloss.Color("179")),
		h3:      r.NewStyle().Bold(true).Foreground(lipgloss.Color("230")),
		link:    r.NewStyle().Foreground(lipgloss.Color("117")).Underline(true),
		linkExt: r.NewStyle().Foreground(lipgloss.Color("108")),
		linkSel: r.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("215")).Bold(true),
		linkNum: r.NewStyle().Foreground(lipgloss.Color("245")),
		quote:   r.NewStyle().Italic(true).Foreground(lipgloss.Color("151")),
		pre:     r.NewStyle().Foreground(lipgloss.Color("115")),
		bullet:  r.NewStyle().Foreground(lipgloss.Color("215")),
		dim:     r.NewStyle().Foreground(lipgloss.Color("245")),
		bar:     r.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("215")).Bold(true),
		barDim:  r.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("179")),
		status:  r.NewStyle().Foreground(lipgloss.Color("114")),
		err:     r.NewStyle().Foreground(lipgloss.Color("203")).Bold(true),
		help:    r.NewStyle().Foreground(lipgloss.Color("245")),
		text:    r.NewStyle(),
		bbsBar:  r.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4")),
		bbsKey:  r.NewStyle().Foreground(lipgloss.Color("11")).Background(lipgloss.Color("4")).Bold(true),
		bbsOK:   r.NewStyle().Foreground(lipgloss.Color("10")).Background(lipgloss.Color("4")).Bold(true),
		bbsErr:  r.NewStyle().Foreground(lipgloss.Color("11")).Background(lipgloss.Color("4")).Bold(true).Blink(false),
	}
}

// pageLink is one followable link on a rendered page.
type pageLink struct {
	URL   string
	Label string
	Line  int // first rendered line of this link
	Lines int // how many lines it occupies, so a click can land on any of them
}

// renderDoc converts gemtext to styled terminal lines and collects links.
// selected is the index of the highlighted link (-1 for none).
func renderDoc(st *styles, src string, width int, selected int) (lines []string, links []pageLink) {
	if width < 10 {
		width = 10
	}
	wrap := func(s lipgloss.Style, text string) []string {
		if strings.TrimSpace(text) == "" {
			return []string{""}
		}
		return strings.Split(s.Width(width).Render(text), "\n")
	}
	for _, l := range gemtext.Parse(src) {
		switch l.Type {
		case gemtext.PreToggle:
			// skip the fence line itself
		case gemtext.PreText:
			// rune/width-aware truncation — never split multibyte glyphs
			lines = append(lines, st.pre.Render(ansi.Truncate(l.Text, width, "")))
		case gemtext.Heading1:
			lines = append(lines, wrap(st.h1, "# "+l.Text)...)
		case gemtext.Heading2:
			lines = append(lines, wrap(st.h2, "## "+l.Text)...)
		case gemtext.Heading3:
			lines = append(lines, wrap(st.h3, "### "+l.Text)...)
		case gemtext.ListItem:
			for i, ln := range wrap(st.text, l.Text) {
				if i == 0 {
					lines = append(lines, st.bullet.Render("• ")+ln)
				} else {
					lines = append(lines, "  "+ln)
				}
			}
		case gemtext.Quote:
			for _, ln := range wrap(st.quote, l.Text) {
				lines = append(lines, st.dim.Render("│ ")+ln)
			}
		case gemtext.Link:
			idx := len(links)
			style := st.link
			if strings.Contains(l.URL, "://") {
				style = st.linkExt
			}
			if idx == selected {
				style = st.linkSel
			}
			num := st.linkNum.Render(fmt.Sprintf("[%d] ", idx+1))
			if idx == selected {
				num = st.linkSel.Render(fmt.Sprintf("[%d] ", idx+1))
			}
			links = append(links, pageLink{URL: l.URL, Label: l.Text, Line: len(lines)})
			for i, ln := range wrap(style, l.Text) {
				if i == 0 {
					lines = append(lines, st.bullet.Render("⇒ ")+num+ln)
				} else {
					lines = append(lines, "  "+ln)
				}
			}
			links[idx].Lines = len(lines) - links[idx].Line
		default:
			lines = append(lines, wrap(st.text, l.Text)...)
		}
	}
	return lines, links
}
