package sshui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

type mode int

const (
	modeBrowse mode = iota
	modeEdit
	modeInput // goto / search / new-page path prompt
	modeConfirm
)

type inputKind int

const (
	inputGoto inputKind = iota
	inputSearch
	inputNewPath
)

var (
	stBar    = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("215")).Bold(true)
	stBarDim = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("179"))
	stStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	stErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// model is the SSH TUI: a gemini browser, plus editing when admin.
type model struct {
	site     *site.Site
	store    *store.Store
	hostname string
	admin    bool

	width, height int
	mode          mode

	// browser
	url        string
	title      string
	sourcePath string // editable source of the page ("" = synthetic)
	gmi        string
	links      []pageLink
	sel        int
	vp         viewport.Model
	history    []string
	status     string
	statusErr  bool

	// input prompt
	input      textinput.Model
	inputKind  inputKind
	inputLabel string

	// editor
	ed      textarea.Model
	edPath  string
	edDirty bool
	edNow   bool // editing a now-post instead of a page
	confirm string
}

func newModel(sy *site.Site, st *store.Store, hostname string, admin bool, w, h int) *model {
	m := &model{
		site:     sy,
		store:    st,
		hostname: hostname,
		admin:    admin,
		width:    w,
		height:   h,
		vp:       viewport.New(w, max(1, h-3)),
	}
	m.navigate("/", false)
	return m
}

func (m *model) Init() tea.Cmd { return nil }

// ---- navigation ---------------------------------------------------------

func (m *model) navigate(url string, pushHistory bool) {
	res := m.site.Resolve(url, "ssh")
	switch res.Type {
	case site.RedirectResult:
		res = m.site.Resolve(res.Location, "")
		url = strings.TrimSuffix(res.Location, "")
	}
	switch res.Type {
	case site.PageResult:
		if pushHistory && m.url != "" && m.url != url {
			m.history = append(m.history, m.url)
			if len(m.history) > 100 {
				m.history = m.history[1:]
			}
		}
		m.url = res.Page.URLPath
		m.title = res.Page.Title
		m.sourcePath = res.Page.SourcePath
		m.gmi = res.Page.Gemtext
		m.sel = -1
		m.setStatus("", false)
		m.refresh(true)
	case site.FileResult:
		m.setStatus(fmt.Sprintf("%s is a file (%s, %d bytes) — can't display", url, res.File.Mime, len(res.File.Content)), true)
	default:
		m.setStatus("not found: "+url, true)
	}
}

func (m *model) showDoc(url, title, gmi string) {
	if m.url != "" && m.url != url {
		m.history = append(m.history, m.url)
	}
	m.url = url
	m.title = title
	m.sourcePath = ""
	m.gmi = gmi
	m.sel = -1
	m.refresh(true)
}

// refresh re-renders the current document into the viewport.
func (m *model) refresh(toTop bool) {
	lines, links := renderDoc(m.gmi, min(m.width-2, 100), m.sel)
	m.links = links
	m.vp.SetContent(strings.Join(lines, "\n"))
	if toTop {
		m.vp.GotoTop()
	}
}

func (m *model) setStatus(msg string, isErr bool) {
	m.status = msg
	m.statusErr = isErr
}

// scrollToLink keeps the selected link visible.
func (m *model) scrollToLink() {
	if m.sel < 0 || m.sel >= len(m.links) {
		return
	}
	line := m.links[m.sel].Line
	if line < m.vp.YOffset {
		m.vp.SetYOffset(line)
	} else if line >= m.vp.YOffset+m.vp.Height {
		m.vp.SetYOffset(line - m.vp.Height + 1)
	}
}

func (m *model) openLink(i int) {
	if i < 0 || i >= len(m.links) {
		return
	}
	u := m.links[i].URL
	if strings.Contains(u, "://") || strings.HasPrefix(u, "mailto:") {
		m.setStatus("external link: "+u, false)
		return
	}
	u = strings.TrimSuffix(u, ".gmi")
	if !strings.HasPrefix(u, "/") {
		base := m.url
		if !strings.HasSuffix(base, "/") {
			if i := strings.LastIndexByte(base, '/'); i >= 0 {
				base = base[:i+1]
			}
		}
		u = base + u
	}
	m.navigate(u, true)
}

// ---- update -------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// headless PTYs can report 0x0 — keep the previous (default) size
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.vp.Width = m.width
		m.vp.Height = max(1, m.height-3)
		if m.mode == modeEdit {
			m.sizeEditor()
		}
		m.refresh(false)
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case modeEdit:
			return m.updateEdit(msg)
		case modeInput:
			return m.updateInput(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		default:
			return m.updateBrowse(msg)
		}
	}
	return m, nil
}

func (m *model) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.vp.ScrollUp(1)
	case "down", "j":
		m.vp.ScrollDown(1)
	case "pgup":
		m.vp.PageUp()
	case "pgdown", " ":
		m.vp.PageDown()
	case "home":
		m.vp.GotoTop()
	case "end":
		m.vp.GotoBottom()
	case "tab":
		if len(m.links) > 0 {
			m.sel = (m.sel + 1) % len(m.links)
			m.refresh(false)
			m.scrollToLink()
		}
	case "shift+tab":
		if len(m.links) > 0 {
			m.sel--
			if m.sel < 0 {
				m.sel = len(m.links) - 1
			}
			m.refresh(false)
			m.scrollToLink()
		}
	case "enter":
		m.openLink(m.sel)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.openLink(int(msg.String()[0]-'1') + m.vpFirstVisibleLink())
	case "b", "backspace":
		if n := len(m.history); n > 0 {
			prev := m.history[n-1]
			m.history = m.history[:n-1]
			cur := m.url
			m.url = ""
			m.navigate(prev, false)
			_ = cur
		}
	case "h":
		m.navigate("/", true)
	case "r":
		m.navigate(m.url, false)
		m.setStatus("reloaded", false)
	case "g":
		m.prompt(inputGoto, "go to path", "/")
	case "/":
		m.prompt(inputSearch, "search", "")
	case "e":
		if !m.admin {
			break
		}
		if m.sourcePath == "" {
			m.setStatus("this page has no editable source", true)
			break
		}
		m.startEdit(m.sourcePath, false)
	case "c":
		if m.admin {
			m.prompt(inputNewPath, "new page path", "/")
		}
	case "n":
		if m.admin {
			m.startNowPost()
		}
	case "x":
		if !m.admin {
			break
		}
		if m.sourcePath == "" {
			m.setStatus("this page has no deletable source", true)
			break
		}
		m.mode = modeConfirm
		m.confirm = m.sourcePath
	}
	return m, nil
}

// vpFirstVisibleLink returns the index of the first link at/after the top of
// the viewport, so number keys map to what's on screen.
func (m *model) vpFirstVisibleLink() int {
	for i, l := range m.links {
		if l.Line >= m.vp.YOffset {
			return i
		}
	}
	return 0
}

func (m *model) prompt(kind inputKind, label, value string) {
	m.inputKind = kind
	m.inputLabel = label
	in := textinput.New()
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	in.Width = max(20, m.width-10)
	m.input = in
	m.mode = modeInput
}

func (m *model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeBrowse
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.input.Value())
		m.mode = modeBrowse
		if val == "" {
			return m, nil
		}
		switch m.inputKind {
		case inputGoto:
			m.navigate(val, true)
		case inputSearch:
			m.runSearch(val)
		case inputNewPath:
			cp, ok := store.CleanPath(ensureGmi(val))
			if !ok {
				m.setStatus("invalid path: "+val, true)
				return m, nil
			}
			m.startEdit(cp, true)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func ensureGmi(p string) string {
	base := p[strings.LastIndexByte(p, '/')+1:]
	if !strings.Contains(base, ".") {
		return p + ".gmi"
	}
	return p
}

func (m *model) runSearch(q string) {
	hits, err := m.store.Search(q, 20)
	var b strings.Builder
	fmt.Fprintf(&b, "# Search: %s\n\n", q)
	if err != nil || len(hits) == 0 {
		b.WriteString("Nothing found. Try fewer or different words.\n")
	} else {
		fmt.Fprintf(&b, "%d result(s):\n\n", len(hits))
		for _, h := range hits {
			title := h.Title
			if title == "" {
				title = h.Path
			}
			fmt.Fprintf(&b, "=> %s %s\n", strings.TrimSuffix(h.Path, ".gmi"), title)
			if h.Snippet != "" {
				fmt.Fprintf(&b, "> …%s…\n", h.Snippet)
			}
		}
	}
	m.showDoc("/search", "search", b.String())
}

func (m *model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		if err := m.store.DeletePage(m.confirm, "ssh"); err != nil {
			m.setStatus("delete failed: "+err.Error(), true)
		} else {
			m.navigate("/", false)
			m.setStatus("deleted "+m.confirm+" (restorable from web admin history)", false)
		}
	default:
		m.setStatus("delete cancelled", false)
	}
	m.mode = modeBrowse
	return m, nil
}

// ---- editor -------------------------------------------------------------

func (m *model) startEdit(path string, isNew bool) {
	content := ""
	if !isNew {
		if pg, err := m.store.GetPage(path); err == nil {
			if pg.Binary {
				m.setStatus("binary file — edit via the web admin", true)
				return
			}
			content = string(pg.Content)
		}
	}
	m.edPath = path
	m.edNow = false
	m.initEditor(content)
}

func (m *model) startNowPost() {
	m.edPath = ""
	m.edNow = true
	m.initEditor("")
}

func (m *model) initEditor(content string) {
	ed := textarea.New()
	ed.CharLimit = 0
	ed.MaxHeight = 0
	ed.SetValue(content)
	ed.Focus()
	m.ed = ed
	m.edDirty = false
	m.mode = modeEdit
	m.sizeEditor()
	m.setStatus("", false)
}

func (m *model) sizeEditor() {
	m.ed.SetWidth(m.width)
	m.ed.SetHeight(max(3, m.height-3))
}

func (m *model) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		return m.saveEdit()
	case "ctrl+q", "esc":
		if m.edDirty {
			m.edDirty = false
			m.setStatus("unsaved changes — press again to discard, ctrl+s to save", true)
			return m, nil
		}
		m.mode = modeBrowse
		m.setStatus("", false)
		m.navigate(m.url, false)
		return m, nil
	}
	before := m.ed.Value()
	var cmd tea.Cmd
	m.ed, cmd = m.ed.Update(msg)
	if m.ed.Value() != before {
		m.edDirty = true
	}
	return m, cmd
}

func (m *model) saveEdit() (tea.Model, tea.Cmd) {
	content := m.ed.Value()
	if m.edNow {
		if strings.TrimSpace(content) == "" {
			m.setStatus("empty now post", true)
			return m, nil
		}
		if _, err := m.store.AddNow(content); err != nil {
			m.setStatus("post failed: "+err.Error(), true)
			return m, nil
		}
		m.mode = modeBrowse
		m.navigate("/now", true)
		m.setStatus("now post published ✓", false)
		return m, nil
	}
	pg, err := m.store.SavePage(m.edPath, []byte(content), "", "ssh")
	if err != nil {
		m.setStatus("save failed: "+err.Error(), true)
		return m, nil
	}
	m.edDirty = false
	m.setStatus(fmt.Sprintf("saved %s (r%d) ✓", pg.Path, m.store.CountVersions(pg.Path)+1), false)
	return m, nil
}

// ---- view ---------------------------------------------------------------

func (m *model) View() string {
	switch m.mode {
	case modeEdit:
		return m.viewEdit()
	default:
		return m.viewBrowse()
	}
}

func (m *model) header(label string) string {
	brand := " ✨ " + m.hostname + " · starpulse "
	who := "guest"
	if m.admin {
		who = "admin"
	}
	right := " " + who + " · " + label + " "
	gap := m.width - lipgloss.Width(brand) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return stBar.Render(brand) + strings.Repeat(" ", gap) + stBarDim.Render(right)
}

func (m *model) statusLine() string {
	if m.status == "" {
		return ""
	}
	if m.statusErr {
		return stErr.Render(m.status)
	}
	return stStatus.Render(m.status)
}

func (m *model) viewBrowse() string {
	var footer string
	switch m.mode {
	case modeInput:
		footer = stHelp.Render(m.inputLabel+": ") + m.input.View()
	case modeConfirm:
		footer = stErr.Render("delete " + m.confirm + "? [y/N]")
	default:
		help := "tab links · ↵ open · b back · g goto · / search · h home"
		if m.admin {
			help += " · e edit · c new · n now · x del"
		}
		help += " · q quit"
		footer = m.statusLine()
		if footer == "" {
			footer = stHelp.Render(help)
		}
	}
	return m.header(m.url) + "\n" + m.vp.View() + "\n" + footer
}

func (m *model) viewEdit() string {
	label := "editing " + m.edPath
	if m.edNow {
		label = "new now post"
	}
	foot := m.statusLine()
	if foot == "" {
		foot = stHelp.Render("ctrl+s save · esc/ctrl+q back")
	}
	return m.header(label) + "\n" + m.ed.View() + "\n" + foot
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
