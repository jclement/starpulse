package script

import (
	"context"
	"strings"
	"testing"
)

func TestTemplateCompile(t *testing.T) {
	e := New(Options{})
	out := func(tmpl string, req Request) string {
		res, err := e.Run(context.Background(), "/t.cgi", Compile(tmpl), req)
		if err != nil {
			t.Fatalf("run %q: %v", tmpl, err)
		}
		return string(res.Body)
	}

	// plain text passes through verbatim
	if got := out("# Hi\n\nhello world\n", Request{}); got != "# Hi\n\nhello world\n" {
		t.Errorf("literal passthrough: %q", got)
	}
	// <?= expr ?> writes an expression; nil becomes empty
	if got := out("Hello <?= request.host ?>!", Request{Host: "owg.fyi"}); got != "Hello owg.fyi!" {
		t.Errorf("echo: %q", got)
	}
	if got := out("x<?= nil ?>y", Request{}); got != "xy" {
		t.Errorf("nil echo should be empty: %q", got)
	}
	// <? code ?> runs; a loop over literals repeats the literal
	got := out("<? for i=1,3 do ?>* item <?= i ?>\n<? end ?>", Request{})
	if got != "* item 1\n* item 2\n* item 3\n" {
		t.Errorf("loop: %q", got)
	}
	// a page that is all code (opens <? , never closes) behaves like a script
	if got := out("<?\nwrite('# Program\\n')\nwrite('body')", Request{}); got != "# Program\nbody" {
		t.Errorf("code-only page: %q", got)
	}
	// text with quotes and brackets survives the literal quoting
	if got := out(`a "quote" and ]] a bracket`, Request{}); got != `a "quote" and ]] a bracket` {
		t.Errorf("tricky literal: %q", got)
	}
	// a runtime error's line number points into the page, not the compiled Lua
	_, err := e.Run(context.Background(), "/t.cgi", Compile("line one\nline two\n<? error('boom') ?>\n"), Request{})
	if err == nil || !strings.Contains(err.Error(), ":3") {
		t.Errorf("error line should be 3 (the code line): %v", err)
	}
}
