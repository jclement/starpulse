package sshui

import "testing"

func TestFuzzyMatch(t *testing.T) {
	if _, ok := fuzzyMatch("xyz", "/about"); ok {
		t.Error("non-matching pattern matched")
	}
	if _, ok := fuzzyMatch("abt", "/about"); !ok {
		t.Error("subsequence should match")
	}
	// substring beats scattered subsequence
	sub, _ := fuzzyMatch("post", "/posts/hello")
	scat, _ := fuzzyMatch("post", "/p-o-s-t")
	if sub <= scat {
		t.Errorf("substring (%d) should outrank scattered (%d)", sub, scat)
	}
	// empty pattern matches everything
	if _, ok := fuzzyMatch("", "/anything"); !ok {
		t.Error("empty pattern should match")
	}
}

func TestFuzzyRank(t *testing.T) {
	cands := []string{"/about", "/posts/2026-07-19-hello", "/posts/2026-07-20-world", "/contact", "/projects"}
	hits := fuzzyRank("post", cands, 8)
	if len(hits) != 2 {
		t.Fatalf("expected 2 post matches, got %d: %v", len(hits), hits)
	}
	for _, h := range hits {
		if !contains(h, "/posts/") {
			t.Errorf("unexpected hit %q", h)
		}
	}
	// limit is respected
	if got := fuzzyRank("", cands, 3); len(got) != 3 {
		t.Errorf("limit ignored: %d", len(got))
	}
	// best-first: exact-ish "cont" ranks /contact first
	if got := fuzzyRank("contact", cands, 8); len(got) == 0 || got[0] != "/contact" {
		t.Errorf("ranking wrong: %v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
