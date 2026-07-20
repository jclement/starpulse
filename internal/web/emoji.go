package web

import "strings"

// wrapEmoji wraps runs of emoji in <span class="e"> so CSS can render them
// monochrome (grayscale filter). Only text between tags is touched — never
// tag names or attribute values.
func wrapEmoji(html string) string {
	var b strings.Builder
	b.Grow(len(html) + 64)
	inTag := false
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		b.WriteString(`<span class="e">`)
		b.WriteString(string(run))
		b.WriteString(`</span>`)
		run = run[:0]
	}
	for _, r := range html {
		if inTag {
			b.WriteRune(r)
			if r == '>' {
				inTag = false
			}
			continue
		}
		if r == '<' {
			flush()
			inTag = true
			b.WriteRune(r)
			continue
		}
		if isEmojiRune(r) || (len(run) > 0 && isEmojiJoiner(r)) {
			run = append(run, r)
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF: // pictographs, transport, supplemental
		return true
	case r >= 0x2600 && r <= 0x27BF: // misc symbols + dingbats (♊ ☀ ✅ …)
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // stars, arrows (⭐ …)
		return true
	}
	return false
}

// joiners/selectors continue an emoji run but never start one.
func isEmojiJoiner(r rune) bool {
	return r == 0x200D || r == 0xFE0F || r == 0xFE0E || (r >= 0x1F3FB && r <= 0x1F3FF)
}
