package sshui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Mouse support. A terminal that reports the mouse can do the obvious
// things: the wheel scrolls, a click on a link follows it, a click on a word
// in the bottom bar does what pressing that key does.
//
// The cost is that while mouse reporting is on, the terminal's own
// click-drag selection is taken over by the application — most terminals
// give it back if you hold Shift while dragging. That trade is why this is
// worth doing well or not at all.

// browsePairs is the bottom bar's key/action list. It is one function so the
// bar that is drawn and the bar that is clicked can never disagree about
// what is on it.
func (m *model) browsePairs() []string {
	pairs := []string{"tab", "links", "↵", "open", "b", "back", "g", "goto", "/", "search", "h", "home"}
	if m.admin {
		pairs = append(pairs, "e", "edit", "c", "new", "n", "now", "x", "del")
	}
	return append(pairs, "q", "quit")
}

// barZone is a clickable region of the bottom bar and the key it stands for.
type barZone struct {
	x0, x1 int // [x0, x1) in terminal columns
	key    string
}

// barZones lays the bar out exactly as bbsHelp draws it: a leading space,
// then "key action" pairs separated by two spaces. Styling never changes a
// string's width, so plain widths are enough to know where each pair sits.
func barZones(pairs []string) []barZone {
	var zones []barZone
	x := 1 // the leading space
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			x += 2 // the separator
		}
		start := x
		x += ansi.StringWidth(pairs[i]) + 1 + ansi.StringWidth(pairs[i+1])
		zones = append(zones, barZone{x0: start, x1: x, key: pairs[i]})
	}
	return zones
}

// updateMouse routes a mouse report. Anything it does not handle is handed
// to the viewport, which scrolls on the wheel by itself.
func (m *model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeEdit:
		// the textarea has no mouse handling of its own; the wheel moves the
		// cursor, which is what scrolls it
		if msg.Action == tea.MouseActionPress {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				return m.updateEdit(tea.KeyMsg{Type: tea.KeyUp})
			case tea.MouseButtonWheelDown:
				return m.updateEdit(tea.KeyMsg{Type: tea.KeyDown})
			}
		}
		return m, nil
	case modeEditHelp:
		var cmd tea.Cmd
		m.helpVp, cmd = m.helpVp.Update(msg)
		return m, cmd
	case modeInput, modeConfirm:
		return m, nil // a prompt is waiting on the keyboard; do not surprise it
	}

	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		if key, ok := m.clickTarget(msg.X, msg.Y); ok {
			return m.updateBrowse(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		}
		if idx, ok := m.linkAt(msg.Y); ok {
			m.sel = idx
			m.refresh(false)
			m.openLink(idx)
			return m, nil
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// clickTarget maps a click on the bottom bar to the key it stands for.
func (m *model) clickTarget(x, y int) (string, bool) {
	if y != m.height-1 || m.status != "" {
		return "", false // not the bar, or the bar is showing a status message
	}
	for _, z := range barZones(m.browsePairs()) {
		if x >= z.x0 && x < z.x1 {
			switch z.key {
			case "↵":
				return "", false // "open" needs a selection; clicking a link is the way
			case "tab":
				return "", false // cycling links by mouse is pointless
			}
			return z.key, true
		}
	}
	return "", false
}

// linkAt finds the link occupying a screen row, accounting for how far the
// page is scrolled and for labels that wrapped onto several lines.
func (m *model) linkAt(y int) (int, bool) {
	const headerRows = 1
	if y < headerRows || y >= headerRows+m.vp.Height {
		return 0, false
	}
	line := m.vp.YOffset + (y - headerRows)
	for i, l := range m.links {
		span := l.Lines
		if span < 1 {
			span = 1
		}
		if line >= l.Line && line < l.Line+span {
			return i, true
		}
	}
	return 0, false
}
