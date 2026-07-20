package highlight

import (
	"strings"
	"testing"
)

func TestKnownLanguages(t *testing.T) {
	for _, lang := range []string{"go", "python", "csharp", "elixir", "sh", "bash", "yaml", "json"} {
		if !Known(lang) {
			t.Errorf("lexer missing for %q", lang)
		}
	}
	// decorative fences starpulse already treats specially must stay plain
	for _, lang := range []string{"banner", "table", "", "definitely-not-a-language"} {
		if Known(lang) {
			t.Errorf("%q should not be highlighted", lang)
		}
	}
	// alt text is free-form; the first word decides
	if !Known("go — an example") {
		t.Error("multi-word alt text should still resolve")
	}
}

func TestRender(t *testing.T) {
	h := New("github", "github-dark")
	out, ok := h.Render("go", "func main() {}\n")
	if !ok {
		t.Fatal("go should highlight")
	}
	if !strings.Contains(out, `class="`) || !strings.Contains(out, "func") {
		t.Errorf("expected classed spans:\n%s", out)
	}
	if _, ok := h.Render("nonsense-lang", "x"); ok {
		t.Error("unknown language should report false")
	}
}

// Both palettes must be scoped, or classes the light style defines and the
// dark one omits (punctuation) leak dark text onto a dark background.
func TestCSSScoping(t *testing.T) {
	h := New("github", "github-dark")
	css := h.CSS()
	if !strings.Contains(css, "@media (prefers-color-scheme: light)") {
		t.Error("light palette not scoped")
	}
	if !strings.Contains(css, "@media (prefers-color-scheme: dark)") {
		t.Error("dark palette not scoped")
	}
	// nothing outside a media query except the banner comment
	for _, line := range strings.Split(css, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "/*") || strings.HasPrefix(l, "@media") || l == "}" {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("unscoped rule would leak between colour schemes: %q", line)
		}
	}
}

func TestUnknownStyleFallsBack(t *testing.T) {
	h := New("no-such-style", "also-not-real")
	if h.light == nil || h.dark == nil {
		t.Fatal("unknown style names should fall back, not nil out")
	}
	if _, ok := h.Render("go", "x := 1"); !ok {
		t.Error("fallback styles should still render")
	}
}
