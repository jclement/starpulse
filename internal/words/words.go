// Package words is a small embedded English word list, so an executable page
// can tell a real word from five random letters without reaching outside the
// sandbox for a dictionary. It is the system word list (Webster's) filtered
// to five-letter lowercase words — enough for a word game, a few tens of KB.
package words

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed words5.txt
var raw string

var (
	once sync.Once
	set  map[string]struct{}
)

func load() {
	once.Do(func() {
		lines := strings.Split(strings.TrimSpace(raw), "\n")
		set = make(map[string]struct{}, len(lines))
		for _, w := range lines {
			set[w] = struct{}{}
		}
	})
}

// Valid reports whether w is a known five-letter English word. Case and
// surrounding space are ignored.
func Valid(w string) bool {
	load()
	_, ok := set[strings.ToLower(strings.TrimSpace(w))]
	return ok
}

// Count is how many words the list holds.
func Count() int {
	load()
	return len(set)
}
