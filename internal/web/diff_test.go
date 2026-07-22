package web

import (
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	old := "# Title\nline one\nline two\nline three\n"
	nw := "# Title\nline one\nline TWO changed\nline three\nline four\n"
	out := renderDiff(old, nw)
	if !strings.Contains(out, `class="del"`) || !strings.Contains(out, "line two") {
		t.Errorf("removed line not shown:\n%s", out)
	}
	if !strings.Contains(out, `class="add"`) || !strings.Contains(out, "line TWO changed") {
		t.Errorf("added line not shown:\n%s", out)
	}
	if !strings.Contains(out, "line four") {
		t.Errorf("appended line missing:\n%s", out)
	}
	// identical content says so
	if got := renderDiff(old, old); !strings.Contains(got, "No difference") {
		t.Errorf("identical diff: %s", got)
	}
	// a long unchanged run BETWEEN two changes collapses in the middle
	var mid strings.Builder
	mid.WriteString("top\n")
	for i := 0; i < 40; i++ {
		mid.WriteString("same\n")
	}
	mid.WriteString("bottom\n")
	a := mid.String()
	b := "TOP changed\n" + strings.Repeat("same\n", 40) + "BOTTOM changed\n"
	if got := renderDiff(a, b); !strings.Contains(got, "unchanged") {
		t.Errorf("long middle run not collapsed:\n%s", got)
	}
}
