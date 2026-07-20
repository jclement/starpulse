package render

import (
	"strings"
	"testing"
)

func TestGemtextToHTML(t *testing.T) {
	src := `# Title
Some <script>alert(1)</script> text
=> /page.gmi Internal
=> https://ex.example External
=> gemini://ex.example Gem
=> /media/cat.png A cat
* one
* two
> wise words
` + "```\ncode <b>\n```"
	h := GemtextToHTML(src)

	for _, want := range []string{
		"<h1>Title</h1>",
		"&lt;script&gt;",                // escaped
		`<a href="/page">Internal</a>`,  // .gmi stripped for web
		`class="ext"`,                   // external marker
		`class="gem"`,                   // gemini marker
		`<img src="/media/cat.png"`,     // image inlined
		"<ul>\n<li>one</li>\n<li>two</li>\n</ul>", // list grouping
		"<blockquote>wise words</blockquote>",
		"code &lt;b&gt;",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
	if strings.Contains(h, "<script>") {
		t.Error("unescaped script tag")
	}
}

func TestUnclosedPre(t *testing.T) {
	h := GemtextToHTML("```\nline")
	if !strings.HasSuffix(strings.TrimSpace(h), "</pre>") {
		t.Errorf("unclosed pre not terminated:\n%s", h)
	}
}
