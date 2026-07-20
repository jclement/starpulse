package web

import (
	"strings"
	"testing"
)

func TestWrapEmoji(t *testing.T) {
	cases := []struct{ in, want string }{
		{"go 🛞 wheel", `go <span class="e">🛞</span> wheel`},
		{"tor 🧅🧅 twice", `tor <span class="e">🧅🧅</span> twice`},
		{"gemini ♊ sign", `gemini <span class="e">♊</span> sign`},
		{"plain text", "plain text"},
		{"box ─── art", "box ─── art"}, // box drawing is not emoji
		{`<a href="/x">📓 log</a>`, `<a href="/x"><span class="e">📓</span> log</a>`},
		{`<img alt="🛞 wheel">after 💛`, `<img alt="🛞 wheel">after <span class="e">💛</span>`},
	}
	for _, c := range cases {
		if got := wrapEmoji(c.in); got != c.want {
			t.Errorf("wrapEmoji(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// variation selector stays inside the run
	if got := wrapEmoji("a ☀️ b"); !strings.Contains(got, "<span class=\"e\">☀️</span>") {
		t.Errorf("VS16 split from run: %q", got)
	}
}
