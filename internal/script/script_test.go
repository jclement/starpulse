package script

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store for tests.
type memStore struct {
	mu sync.Mutex
	m  map[string]map[string]string
}

func newMemStore() *memStore { return &memStore{m: map[string]map[string]string{}} }

func (s *memStore) Get(script, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[script][key]
	return v, ok
}
func (s *memStore) Set(script, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[script] == nil {
		s.m[script] = map[string]string{}
	}
	s.m[script][key] = value
	return nil
}
func (s *memStore) Delete(script, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m[script], key)
}
func (s *memStore) Keys(script string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.m[script] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func exec(t *testing.T, code string, req Request) (Result, error) {
	t.Helper()
	e := New(Options{Store: newMemStore()})
	return e.Run(context.Background(), "/s.lua", code, req)
}

func body(t *testing.T, code string, req Request) string {
	t.Helper()
	res, err := exec(t, code, req)
	if err != nil {
		t.Fatalf("script error: %v", err)
	}
	if res.NeedInput {
		t.Fatalf("unexpected input request: %q", res.Prompt)
	}
	return string(res.Body)
}

func TestOutput(t *testing.T) {
	if got := body(t, `write("# Hi\n") write("body")`, Request{}); got != "# Hi\nbody" {
		t.Errorf("write output = %q", got)
	}
	// a bare return is also output
	if got := body(t, `return "just this"`, Request{}); got != "just this" {
		t.Errorf("returned output = %q", got)
	}
	// write wins over a return when both happen
	if got := body(t, `write("written") return "returned"`, Request{}); got != "written" {
		t.Errorf("write should win: %q", got)
	}
	// numbers and the string library work
	if got := body(t, `write(string.rep("=", 3), " ", 2*21)`, Request{}); got != "=== 42" {
		t.Errorf("mixed write = %q", got)
	}
}

func TestRequestIsVisible(t *testing.T) {
	req := Request{
		Path: "/tools/dice", Proto: "gemini", Host: "owg.fyi",
		Query:        map[string]string{"n": "2", "sides": "6"},
		Identity:     "abc123", IdentityKind: "cert", Verified: true,
	}
	code := `
		write(request.path .. " " .. request.proto .. " " .. request.host .. "\n")
		write(request.query.n .. "d" .. request.query.sides .. "\n")
		write(request.identity .. " " .. request.identity_kind .. " ")
		write(tostring(request.identity_verified))
	`
	want := "/tools/dice gemini owg.fyi\n2d6\nabc123 cert true"
	if got := body(t, code, req); got != want {
		t.Errorf("request table:\n got %q\nwant %q", got, want)
	}
}

func TestInputHandshake(t *testing.T) {
	// prompt() declares a line is wanted; request.input carries the answer.
	// The board is rendered every pass, so the door can show board + prompt.
	code := `
		if request.has_input then write("hello, " .. request.input) else write("board") end
		if not request.has_input then prompt("What is your name?") end
	`

	// no input yet: the script renders and asks; body is present alongside
	res, err := exec(t, code, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedInput || res.Prompt != "What is your name?" {
		t.Fatalf("expected an input request, got %+v", res)
	}
	if string(res.Body) != "board" {
		t.Errorf("body not carried with the prompt: %q", res.Body)
	}

	// with the answer, the script completes and stops asking
	res2, err := exec(t, code, Request{Input: "jeff", HasInput: true})
	if err != nil {
		t.Fatal(err)
	}
	if res2.NeedInput {
		t.Fatal("still asking for input after it was provided")
	}
	if string(res2.Body) != "hello, jeff" {
		t.Errorf("body = %q", res2.Body)
	}

	// the sensitive flag rides along
	res3, _ := exec(t, `prompt("Password:", true)`, Request{})
	if !res3.NeedInput || !res3.Sensitive {
		t.Errorf("sensitive input not flagged: %+v", res3)
	}
}

func TestStore(t *testing.T) {
	ms := newMemStore()
	e := New(Options{Store: ms})
	r := func(code string) Result {
		res, err := e.Run(context.Background(), "/guestbook.lua", code, Request{})
		if err != nil {
			t.Fatalf("error running %q: %v", code, err)
		}
		return res
	}
	r(`store.set("visits", "1")`)
	if got := string(r(`write(store.get("visits"))`).Body); got != "1" {
		t.Errorf("store.get = %q", got)
	}
	// keys and delete
	r(`store.set("a", "1") store.set("b", "2")`)
	if got := string(r(`write(table.concat(store.keys(), ","))`).Body); got != "a,b,visits" {
		t.Errorf("store.keys = %q", got)
	}
	r(`store.delete("a")`)
	if _, ok := ms.Get("/guestbook.lua", "a"); ok {
		t.Error("store.delete did not delete")
	}
	// each script has its own namespace
	other, _ := e.Run(context.Background(), "/other.lua", `write(tostring(store.get("visits")))`, Request{})
	if string(other.Body) != "nil" {
		t.Errorf("scripts share storage: %q", other.Body)
	}
}

func TestStoreLimits(t *testing.T) {
	e := New(Options{Store: newMemStore(), MaxValueLen: 16, MaxKeys: 2})
	_, err := e.Run(context.Background(), "/s.lua", `store.set("k", string.rep("x", 100))`, Request{})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("oversize value not refused: %v", err)
	}
	_, err = e.Run(context.Background(), "/s.lua", `store.set("a","1") store.set("b","2") store.set("c","3")`, Request{})
	if err == nil || !strings.Contains(err.Error(), "full") {
		t.Errorf("key cap not enforced: %v", err)
	}
	// overwriting an existing key is fine at the cap
	if _, err := e.Run(context.Background(), "/s.lua", `store.set("a","x") store.set("b","y") store.set("a","z")`, Request{}); err != nil {
		t.Errorf("overwrite at cap refused: %v", err)
	}
}

func TestOutputIsCapped(t *testing.T) {
	e := New(Options{MaxOutput: 100})
	res, err := e.Run(context.Background(), "/s.lua", `for i=1,1000 do write("0123456789") end`, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Body) != 100 {
		t.Errorf("output not capped: %d bytes", len(res.Body))
	}
}

func TestTimeout(t *testing.T) {
	e := New(Options{Timeout: 100 * time.Millisecond})
	start := time.Now()
	_, err := e.Run(context.Background(), "/s.lua", `while true do end`, Request{})
	if err == nil || !strings.Contains(err.Error(), "time limit") {
		t.Errorf("infinite loop not stopped: %v", err)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Errorf("loop ran %v past its 100ms limit", d)
	}
}

func TestScriptErrorsAreTidy(t *testing.T) {
	_, err := exec(t, `error("boom")`, Request{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "stack traceback") {
		t.Errorf("error still carries a traceback: %q", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error lost its message: %q", err)
	}
}

// The sandbox is deny-by-default; these are the things a script must NOT be
// able to reach. If any of them stops being nil, the sandbox has a hole.
func TestSandboxDeniesEscapes(t *testing.T) {
	for _, name := range []string{
		"os", "io", "require", "package", "dofile", "loadfile",
		"load", "loadstring", "debug", "collectgarbage", "print",
		"newproxy", "module", "coroutine", "channel",
	} {
		got := body(t, `write(type(`+name+`))`, Request{})
		if got != "nil" {
			t.Errorf("%s is reachable from a script (type = %s)", name, got)
		}
	}
	// no way to read the host's environment or files even indirectly
	if _, err := exec(t, `return os.getenv("HOME")`, Request{}); err == nil {
		t.Error("os.getenv did not error")
	}
	if _, err := exec(t, `local f = io.open("/etc/passwd") return f:read("*a")`, Request{}); err == nil {
		t.Error("io.open did not error")
	}
	// string, table, math ARE available — the useful, safe subset
	for _, name := range []string{"string", "table", "math", "pairs", "ipairs", "tostring", "pcall"} {
		if got := body(t, `write(type(`+name+`))`, Request{}); got == "nil" {
			t.Errorf("%s should be available but is not", name)
		}
	}
}

// A script must not be able to keep another script's, or the host's, state
// alive between runs: each run gets a fresh interpreter.
func TestRunsAreIsolated(t *testing.T) {
	e := New(Options{Store: newMemStore()})
	_, err := e.Run(context.Background(), "/s.lua", `leaked = "x"`, Request{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Run(context.Background(), "/s.lua", `write(tostring(leaked))`, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != "nil" {
		t.Errorf("a global survived into the next run: %q", got.Body)
	}
}

// A caller's context cancellation must stop a script even before its own
// deadline — a shutting-down server should not wait on a slow script.
func TestCallerContextCancels(t *testing.T) {
	e := New(Options{Timeout: 10 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := e.Run(ctx, "/s.lua", `while true do end`, Request{})
	if err == nil {
		t.Error("cancellation did not stop the script")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("script ran %v after cancellation", d)
	}
}

// words.valid is a host-provided builtin (a dictionary the sandbox cannot
// reach on its own); it only appears when the host supplies one.

// The sp prelude is a Lua standard library over request/store/write. It adds
// no capability — but it must load before every script and its stop() must
// read as a clean finish.
func TestSpPrelude(t *testing.T) {
	ms := newMemStore()
	e := New(Options{Store: ms})
	run := func(code string, req Request) (Result, error) {
		return e.Run(context.Background(), "/s.lua", code, req)
	}

	// require_identity: returns the id when present
	res, err := run(`write(sp.require_identity())`, Request{Identity: "abc"})
	if err != nil || string(res.Body) != "abc" {
		t.Errorf("require_identity with an id: %q %v", res.Body, err)
	}
	// and stops with a standard message when absent — cleanly, not an error
	res, err = run(`write("before ") sp.require_identity("who are you?") write(" after")`,
		Request{Proto: "https"})
	if err != nil {
		t.Fatalf("require_identity stop reported an error: %v", err)
	}
	if !strings.Contains(string(res.Body), "who are you?") {
		t.Errorf("standard message missing: %q", res.Body)
	}
	if strings.Contains(string(res.Body), "after") {
		t.Errorf("script kept running past require_identity: %q", res.Body)
	}

	// the door tailors the hint
	res, _ = run(`sp.require_identity()`, Request{Proto: "gemini"})
	if !strings.Contains(string(res.Body), "certificate") {
		t.Errorf("gemini hint missing: %q", res.Body)
	}
	res, _ = run(`sp.require_identity()`, Request{Proto: "telnet"})
	if !strings.Contains(string(res.Body), "Telnet") {
		t.Errorf("telnet hint missing: %q", res.Body)
	}

	// require_strong: a verified identity passes; an unverified one is refused
	res, _ = run(`write(sp.require_strong())`, Request{Identity: "fp", Verified: true})
	if string(res.Body) != "fp" {
		t.Errorf("require_strong with a verified id: %q", res.Body)
	}
	res, err = run(`sp.require_strong() write("PASTSTOP")`, Request{Identity: "cookie", Verified: false, Proto: "https"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Body), "PASTSTOP") {
		t.Errorf("require_strong let an unverified id through: %q", res.Body)
	}
	if !strings.Contains(string(res.Body), "not verified") {
		t.Errorf("require_strong message missing: %q", res.Body)
	}
	if got, _ := run(`write(tostring(sp.verified()))`, Request{Verified: true}); string(got.Body) != "true" {
		t.Errorf("sp.verified: %q", got.Body)
	}
	if got, _ := run(`write(sp.name())`, Request{IdentityName: "Jeff"}); string(got.Body) != "Jeff" {
		t.Errorf("sp.name: %q", got.Body)
	}

	// kv helpers
	run(`sp.set("name", "jeff")`, Request{})
	res, _ = run(`write(sp.get("name", "?") .. " " .. sp.get("missing", "default"))`, Request{})
	if string(res.Body) != "jeff default" {
		t.Errorf("sp.get: %q", res.Body)
	}
	res, _ = run(`sp.inc("hits") sp.inc("hits", 5) write(sp.num("hits"))`, Request{})
	if string(res.Body) != "6" {
		t.Errorf("sp.inc/num: %q", res.Body)
	}
	res, _ = run(`sp.push("log","a") sp.push("log","b") local l=sp.list("log") write(#l..":"..l[1]..l[2]..":"..tostring(sp.has(l,"b")))`, Request{})
	if string(res.Body) != "2:ab:true" {
		t.Errorf("sp.list/push/has: %q", res.Body)
	}
}

// The leaderboard pattern Wordwell uses: only a verified identity with a name
// is recorded, and entries are read back and shown.
func TestLeaderboardPattern(t *testing.T) {
	e := New(Options{Store: newMemStore()})
	code := "<?\n" +
		"if request.identity_verified and sp.name() ~= \"\" then\n" +
		"  sp.push(\"board\", request.identity .. \"\\t\" .. sp.name() .. \"\\t3\")\n" +
		"end\n" +
		"for _, row in ipairs(sp.list(\"board\")) do\n" +
		"  local nm, n = row:match(\"^.-\\t(.-)\\t(%d+)$\")\n" +
		"  if nm then write(\"* \", nm, \" \", n, \"\\n\") end\n" +
		"end"
	run := func(req Request) string {
		res, err := e.Run(context.Background(), "/b.cgi", Compile(code), req)
		if err != nil {
			t.Fatal(err)
		}
		return string(res.Body)
	}
	// a verified, named identity is recorded and shown
	if got := run(Request{Identity: "fp1", IdentityName: "Ada", Verified: true}); !strings.Contains(got, "* Ada 3") {
		t.Errorf("verified named not recorded: %q", got)
	}
	// an unverified one (a cookie) is not
	if got := run(Request{Identity: "fp2", IdentityName: "Bob", Verified: false}); strings.Contains(got, "Bob") {
		t.Errorf("an unverified identity reached the board: %q", got)
	}
	// a verified but nameless one is not
	if got := run(Request{Identity: "fp3", Verified: true}); strings.Count(got, "*") != 1 {
		t.Errorf("a nameless identity reached the board: %q", got)
	}
}
