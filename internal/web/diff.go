package web

import (
	"fmt"
	"html"
	"strings"
)

// A small line-level diff for the version viewer, so "what changed in r7?"
// is one glance instead of eyeballing two blocks. No dependency: pages are
// small, so a plain longest-common-subsequence table is more than fast enough.

type diffOp struct {
	kind byte // ' ' context, '-' removed, '+' added
	text string
}

func lineDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:]
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffOp{' ', a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffOp{'-', a[i]})
			i++
		default:
			out = append(out, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{'+', b[j]})
	}
	return out
}

// renderDiff builds the HTML for a diff, collapsing long unchanged runs so
// the changes are what stands out.
func renderDiff(oldText, newText string) string {
	ops := lineDiff(strings.Split(oldText, "\n"), strings.Split(newText, "\n"))
	if !hasChange(ops) {
		return `<p class="dim">No difference.</p>`
	}
	const context = 3
	var b strings.Builder
	b.WriteString(`<pre class="diff">`)
	for idx := 0; idx < len(ops); idx++ {
		op := ops[idx]
		if op.kind != ' ' {
			writeDiffLine(&b, op)
			continue
		}
		// a run of context: show a few lines near a change, hide the middle
		run := 1
		for idx+run < len(ops) && ops[idx+run].kind == ' ' {
			run++
		}
		near := idx == 0 || idx+run >= len(ops) // touching the ends
		if run <= 2*context || near {
			for k := 0; k < run; k++ {
				writeDiffLine(&b, ops[idx+k])
			}
		} else {
			for k := 0; k < context; k++ {
				writeDiffLine(&b, ops[idx+k])
			}
			fmt.Fprintf(&b, `<span class="hunk">  … %d unchanged …</span>`+"\n", run-2*context)
			for k := run - context; k < run; k++ {
				writeDiffLine(&b, ops[idx+k])
			}
		}
		idx += run - 1
	}
	b.WriteString("</pre>")
	return b.String()
}

func writeDiffLine(b *strings.Builder, op diffOp) {
	cls := "ctx"
	switch op.kind {
	case '+':
		cls = "add"
	case '-':
		cls = "del"
	}
	fmt.Fprintf(b, `<span class="%s">%c %s</span>`+"\n", cls, op.kind, html.EscapeString(op.text))
}

func hasChange(ops []diffOp) bool {
	for _, op := range ops {
		if op.kind != ' ' {
			return true
		}
	}
	return false
}
