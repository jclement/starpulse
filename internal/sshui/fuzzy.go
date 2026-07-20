package sshui

import "strings"

// fuzzyMatch scores how well pattern fuzzy-matches target (case-insensitive
// subsequence). Returns (score, true) on a match; higher is better. Contiguous
// runs, matches at word/segment boundaries, and shorter targets score higher.
func fuzzyMatch(pattern, target string) (int, bool) {
	if pattern == "" {
		return 1, true
	}
	p := strings.ToLower(pattern)
	t := strings.ToLower(target)

	// fast path: exact substring is always the strongest signal
	if idx := strings.Index(t, p); idx >= 0 {
		score := 1000 - idx // earlier is better
		if idx == 0 || isBoundary(t[idx-1]) {
			score += 200
		}
		score -= len(t) // prefer shorter targets
		return score, true
	}

	// subsequence match
	score := 0
	ti := 0
	prevMatch := -2
	for pi := 0; pi < len(p); pi++ {
		c := p[pi]
		found := -1
		for ; ti < len(t); ti++ {
			if t[ti] == c {
				found = ti
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		score += 10
		if found == prevMatch+1 {
			score += 15 // contiguous
		}
		if found == 0 || isBoundary(t[found-1]) {
			score += 20 // boundary
		}
		prevMatch = found
		ti = found + 1
	}
	score -= len(t)
	return score, true
}

func isBoundary(c byte) bool {
	return c == '/' || c == '-' || c == '_' || c == '.' || c == ' '
}

// fuzzyRank returns candidates matching pattern, best first, capped at limit.
func fuzzyRank(pattern string, candidates []string, limit int) []string {
	type scored struct {
		s int
		v string
	}
	var hits []scored
	for _, c := range candidates {
		if sc, ok := fuzzyMatch(pattern, c); ok {
			hits = append(hits, scored{sc, c})
		}
	}
	// simple insertion sort (lists are small)
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].s > hits[j-1].s; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	out := make([]string, 0, limit)
	for i, h := range hits {
		if i >= limit {
			break
		}
		out = append(out, h.v)
	}
	return out
}
