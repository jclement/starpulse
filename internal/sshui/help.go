package sshui

// helpDoc renders the syntax + keys cheat-sheet as gemtext (so the TUI's
// own renderer displays it, and it reads fine over telnet too).
func helpDoc(admin bool) string {
	doc := `# Help

## Browser keys

* tab / shift+tab — select next / previous link
* enter — open the selected link
* 1–9 — open the Nth link on screen
* b or backspace — go back
* g — go to a page (fuzzy autocomplete: type, ↑↓ to pick, ↵ opens)
* / — full-text search
* h — home page
* r — reload
* ↑↓ / jk / pgup / pgdn / space — scroll
* q — quit
`
	if admin {
		doc += `
## Admin keys

* e — edit the current page's source
* c — create a new page
* n — post a note into the notes folder (shown by {{now}} or {{latest}})

## Links

Internal links open here. External ones are handed to your terminal as
hyperlinks — ctrl or ⌘-click to open them on your own machine. They are never
fetched by the server.

## Mouse

The wheel scrolls, a click follows a link, and a click on a word in the
bottom bar does what that key does. While the mouse is being reported the
terminal's own selection is taken over — hold Shift while dragging to select
and copy as usual.
* x — delete the current page (confirms; restorable from web history)

In the editor: ctrl+s saves (a version is kept), ctrl+g shows this help, esc backs out (twice discards unsaved changes).

## Gemtext syntax

Lines are the unit — no inline markup.

` + "```" + `
# Heading 1        ## Heading 2       ### Heading 3
=> /path Link label
=> https://ex.example External link
* list item
> quoted text
(triple backticks toggle a preformatted block)
` + "```" + `

## Directives (expand when a page is served)

* {{list [folder] [limit] [name]}} — a folder's pages: dated first, newest
  first, same-day posts in the order written; "name" sorts alphabetically
* {{include /path}} — another page's content, inline
* {{stream [folder] [limit]}} — a folder's entries in full, newest first (0 = all)
* {{now [limit]}} — the same, for the notes folder (default 5)
* {{latest /folder [part]}} — one piece of that folder's newest entry, inline;
  part is body (default), title, date or link. "." is this page's own folder
* {{random /path}} — one random non-empty line from a file
* {{count}} — the page's view counter
* {{rev}} — the page's revision number
* {{updated}} — the page's last-edit date
* {{version}} — server build version

## Special files (inherited down folders)

* .header / .footer — gemtext included above/below every page in the folder and below
  (a page can say "footer: none", or name another file: "footer: /.footer")
* .css — CSS applied to the web rendering of that folder and below
* .feed — marks the folder as publishing a feed (title, author, limit, prefix)

## Front matter (optional, at the top of a page)

` + "```" + `
---
title: Custom title
date: 2026-07-20
header: none
footer: none
---
` + "```" + `

Dated filenames like /posts/2026-07-20-hi.gmi sort newest-first in listings and feed /feed.xml.
`
	}
	doc += "\n=> / Back home\n"
	return doc
}
