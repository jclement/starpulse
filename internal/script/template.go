package script

import "strings"

// Executable pages are templates, PHP-style: text is emitted as-is, and code
// lives in <? … ?> islands. This compiles a template to the Lua the engine
// runs — literal text becomes write() calls, <?= expr ?> writes an
// expression, <? code ?> is code verbatim. A page that is all program simply
// opens with <? and never closes, exactly as a code-only PHP file does.
//
// Output lines track input lines: a literal is emitted one write() per source
// line and code is copied verbatim, so a runtime error's line number still
// points into the page.
func Compile(tmpl string) string {
	var b strings.Builder
	i := 0
	for i < len(tmpl) {
		open := strings.Index(tmpl[i:], "<?")
		if open < 0 {
			emitLiteral(&b, tmpl[i:])
			break
		}
		emitLiteral(&b, tmpl[i:i+open])
		i += open + 2

		echo := i < len(tmpl) && tmpl[i] == '='
		if echo {
			i++
		}
		var code string
		if close := strings.Index(tmpl[i:], "?>"); close < 0 {
			code, i = tmpl[i:], len(tmpl) // no close: the rest is code
		} else {
			code, i = tmpl[i:i+close], i+close+2
		}
		if echo {
			b.WriteString(" write(__tostr(" + strings.TrimSpace(code) + ")) ")
		} else {
			b.WriteString(code)
		}
	}
	return b.String()
}

// emitLiteral writes literal text as write() calls, one per line so the line
// count is preserved.
func emitLiteral(b *strings.Builder, text string) {
	if text == "" {
		return
	}
	for _, line := range splitAfterNewline(text) {
		if line == "" {
			continue
		}
		b.WriteString("write(" + luaQuote(line) + ") ")
		if strings.HasSuffix(line, "\n") {
			b.WriteByte('\n')
		}
	}
}

func splitAfterNewline(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			if s != "" {
				out = append(out, s)
			}
			return out
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
}

// luaQuote renders a string as a Lua double-quoted literal.
func luaQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				const hex = "0123456789abcdef"
				b.WriteString(`\x`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
