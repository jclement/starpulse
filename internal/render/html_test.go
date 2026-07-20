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
		"&lt;script&gt;",                          // escaped
		`<a href="/page">Internal</a>`,            // .gmi stripped for web
		`class="ext"`,                             // external marker
		`class="gem"`,                             // gemini marker
		`<img src="/media/cat.png"`,               // image inlined
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

func TestLinkURLAttributeInjection(t *testing.T) {
	// a hostile URL must not break out of the href attribute
	h := GemtextToHTML(`=> https://x/"><script>alert(1)</script> click`)
	if strings.Contains(h, "<script>") {
		t.Errorf("URL broke out of attribute:\n%s", h)
	}
	if !strings.Contains(h, "&#34;") && !strings.Contains(h, "&quot;") {
		t.Errorf("quote not entity-escaped in href:\n%s", h)
	}
	// image URLs too
	h = GemtextToHTML(`=> /x.png"><script>alert(1)</script> pic`)
	if strings.Contains(h, "<script>") {
		t.Errorf("image URL broke out:\n%s", h)
	}
}

func TestUnclosedPre(t *testing.T) {
	h := GemtextToHTML("```\nline")
	if !strings.HasSuffix(strings.TrimSpace(h), "</pre>") {
		t.Errorf("unclosed pre not terminated:\n%s", h)
	}
}

func TestFencedBlockHighlighting(t *testing.T) {
	called := ""
	opts := Options{Highlight: func(lang, code string) (string, bool) {
		called = lang
		if lang == "go" {
			return "<pre class=\"chroma\">HIGHLIGHTED</pre>", true
		}
		return "", false
	}}

	// a fence with a known language is handed to the highlighter whole
	out := GemtextToHTMLOpts("```go\nfunc main() {}\nx := 1\n```", opts)
	if called != "go" {
		t.Errorf("highlighter got language %q", called)
	}
	if !strings.Contains(out, "HIGHLIGHTED") || !strings.Contains(out, `data-lang="go"`) {
		t.Errorf("highlighted block not emitted:\n%s", out)
	}

	// an unknown language falls back to plain, escaped <pre>
	out = GemtextToHTMLOpts("```nope\n<script>x</script>\n```", opts)
	if strings.Contains(out, "HIGHLIGHTED") {
		t.Error("unknown language should not be highlighted")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("fallback must still escape:\n%s", out)
	}

	// no alt text: never highlighted, content preserved verbatim
	out = GemtextToHTMLOpts("```\n  indented  \n```", opts)
	if !strings.Contains(out, "  indented  ") {
		t.Errorf("plain block mangled:\n%s", out)
	}

	// and with no highlighter at all the old behaviour is unchanged
	plain := GemtextToHTML("```go\nfunc main() {}\n```")
	if !strings.Contains(plain, "<pre") || strings.Contains(plain, "data-lang") {
		t.Errorf("default rendering changed:\n%s", plain)
	}
}
