package sshui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jclement/starpulse/internal/site"
	"github.com/jclement/starpulse/internal/store"
)

func TestGotoPickerFiltersOnTyping(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _ = st.SavePage("/index.gmi", []byte("# Home"), "", "t")
	_, _ = st.SavePage("/about.gmi", []byte("# About"), "", "t")
	_, _ = st.SavePage("/zebrafish-test.gmi", []byte("# Zebra"), "", "t")

	m := newProtoModel(site.New(st), st, "h", false, 100, 30, nil, "ssh")

	// press 'g'
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.mode != modeInput {
		t.Fatalf("g did not open the prompt (mode=%v)", m.mode)
	}
	t.Logf("after g: value=%q hits=%v", m.input.Value(), m.pickHits)

	// type "zeb" one rune at a time (what a human does)
	for _, r := range "zeb" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	t.Logf("after typing runes individually: value=%q hits=%v", m.input.Value(), m.pickHits)
	if m.input.Value() != "zeb" {
		t.Errorf("input value = %q, want \"zeb\"", m.input.Value())
	}
	if len(m.pickHits) != 1 || m.pickHits[0] != "/zebrafish-test" {
		t.Errorf("hits = %v, want [/zebrafish-test]", m.pickHits)
	}

	// admin sees special files too (they open the editor, not the browser)
	_, _ = st.SavePage("/posts/.header", []byte("hdr"), "", "t")
	adm := newProtoModel(site.New(st), st, "h", true, 100, 30, nil, "ssh")
	adm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	for _, r := range "header" {
		adm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	foundSpecial := false
	for _, h := range adm.pickHits {
		if h == "/posts/.header" {
			foundSpecial = true
		}
	}
	if !foundSpecial {
		t.Errorf("admin picker missing special file: %v", adm.pickHits)
	}
	// selecting it opens the editor rather than navigating
	adm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if adm.mode != modeEdit || adm.edPath != "/posts/.header" {
		t.Errorf("special file did not open the editor (mode=%v path=%q)", adm.mode, adm.edPath)
	}

	// a guest must NOT see special files
	gst := newProtoModel(site.New(st), st, "h", false, 100, 30, nil, "ssh")
	gst.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	for _, r := range "header" {
		gst.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	for _, h := range gst.pickHits {
		if h == "/posts/.header" {
			t.Error("guest picker exposed a special file")
		}
	}

	// and as a single batched KeyMsg (what a fast paste / expect send does)
	m2 := newProtoModel(site.New(st), st, "h", false, 100, 30, nil, "ssh")
	m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zeb")})
	t.Logf("after batched runes: value=%q hits=%v", m2.input.Value(), m2.pickHits)
	if m2.input.Value() != "zeb" {
		t.Errorf("batched input value = %q, want \"zeb\"", m2.input.Value())
	}
}
