package gemtext

import "testing"

func TestParse(t *testing.T) {
	src := "# Title\n## Sub\n### Deep\n=> /link Label\n=> /bare\n* item\n> quoted\n```alt\nliteral # not heading\n```\nplain"
	lines := Parse(src)
	want := []struct {
		typ  LineType
		text string
	}{
		{Heading1, "Title"},
		{Heading2, "Sub"},
		{Heading3, "Deep"},
		{Link, "Label"},
		{Link, "/bare"},
		{ListItem, "item"},
		{Quote, "quoted"},
		{PreToggle, ""},
		{PreText, "literal # not heading"},
		{PreToggle, ""},
		{Text, "plain"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(want), lines)
	}
	for i, w := range want {
		if lines[i].Type != w.typ || lines[i].Text != w.text {
			t.Errorf("line %d = %+v, want type=%v text=%q", i, lines[i], w.typ, w.text)
		}
	}
	if lines[3].URL != "/link" {
		t.Errorf("link url = %q", lines[3].URL)
	}
	if lines[7].Meta != "alt" {
		t.Errorf("pre alt = %q", lines[7].Meta)
	}
}

func TestParseTabSeparatedLink(t *testing.T) {
	lines := Parse("=>\t/x\tthe label")
	if lines[0].URL != "/x" || lines[0].Text != "the label" {
		t.Errorf("tab link: %+v", lines[0])
	}
}

func TestFirstHeading(t *testing.T) {
	if h := FirstHeading("text\n# The One\n# Second"); h != "The One" {
		t.Errorf("FirstHeading = %q", h)
	}
	if h := FirstHeading("```\n# in pre\n```\nno headings"); h != "" {
		t.Errorf("heading found in pre: %q", h)
	}
}

func TestPlainText(t *testing.T) {
	p := PlainText("# Head\n=> /url Label\n```\ncode line\n```\nbody")
	for _, want := range []string{"Head", "Label", "code line", "body"} {
		if !contains(p, want) {
			t.Errorf("PlainText missing %q in %q", want, p)
		}
	}
	if contains(p, "/url") {
		t.Errorf("PlainText contains URL: %q", p)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && (stringIndex(s, sub) >= 0))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
