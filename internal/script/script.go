// Package script runs a page written in Lua and returns what it produced.
//
// A script is an ordinary stored page whose name ends .lua — /tools/dice.gmi.lua
// is served at /tools/dice, and the middle extension (.gmi, .txt) tells the
// caller how to treat the output. The engine here does not care about any of
// that; it takes code and a Request and returns a Result, and every door
// renders that Result its own way.
//
// The sandbox is deny-by-default. Lua starts with no standard libraries at
// all, and only base/string/table/math are opened — with the dangerous parts
// of base (load, dofile, require, the debug hooks) stripped back out. A
// script cannot reach the filesystem, the network, the clock beyond what it
// is handed, or any Go value it was not given. What it can do:
//
//	write(...)                 append to the output
//	input(prompt [, secret])   ask the reader for a line (gemini status 10)
//	request.{path,query,...}   what was asked, and by whom
//	store.get/set/delete/keys  a small per-script key/value space
//
// CPU is bounded by a deadline enforced between VM instructions. Output is
// capped. What is NOT bounded is memory: gopher-lua has no allocation cap and
// checks the deadline only between instructions, so a single huge string
// operation can overshoot the timeout and grab a lot of heap. That is a
// footgun for the author, not a door for a stranger — only the author can
// write a script — but callers should still run scripts under a short
// deadline and refuse to run one URL many times in parallel.
package script

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// Request is everything a script is told about the call. It is plain data —
// no methods to probe, nothing that reaches back into the host.
type Request struct {
	Path  string            // "/tools/dice" (no .lua, no output extension)
	Query map[string]string // parsed query parameters
	Proto string            // "https" | "http" | "gemini" | "ssh" | "telnet" | "http+tor" | ...
	Host  string
	Now   time.Time

	// Identity of the caller, or "" when anonymous. Kind is how it was
	// established; Verified is true only when it is cryptographically bound
	// (a client certificate, an ssh key) rather than a bearer token (a
	// cookie) — a distinction a script must be able to see before trusting it.
	Identity     string
	IdentityKind string // "cert" | "sshkey" | "cookie" | ""
	Verified     bool

	// Input is a line the caller has already collected for this run — a
	// resubmit carrying ?input=, a gemini input line, a TUI prompt answer.
	// HasInput distinguishes "" the answer from no answer yet.
	Input    string
	HasInput bool
}

// Result is either output or a request for input — the two shapes gemini's
// 10/20 status codes already model, so every door can render it.
type Result struct {
	Body      []byte
	NeedInput bool
	Prompt    string
	Sensitive bool
}

// Store is the per-script key/value space. Keys are namespaced by script path
// by the implementation; the script only ever sees its own.
type Store interface {
	Get(script, key string) (string, bool)
	Set(script, key, value string) error
	Delete(script, key string)
	Keys(script string) []string
}

// Options bound what a script may do. Zero values get sane defaults in New.
type Options struct {
	Timeout     time.Duration // wall-clock ceiling per run
	MaxOutput   int           // bytes a script may write
	MaxValueLen int           // bytes a single store value may hold
	MaxKeys     int           // keys a script may keep
	Store       Store         // nil disables the store table entirely
}

// Engine runs scripts under a fixed set of limits.
type Engine struct{ opts Options }

// New returns an engine, filling in defaults for any unset limit.
func New(opts Options) *Engine {
	if opts.Timeout <= 0 {
		opts.Timeout = 250 * time.Millisecond
	}
	if opts.MaxOutput <= 0 {
		opts.MaxOutput = 256 << 10
	}
	if opts.MaxValueLen <= 0 {
		opts.MaxValueLen = 64 << 10
	}
	if opts.MaxKeys <= 0 {
		opts.MaxKeys = 256
	}
	return &Engine{opts: opts}
}

// run is the mutable state of a single invocation.
type run struct {
	eng       *Engine
	script    string
	req       Request
	out       strings.Builder
	overflow  bool
	needInput bool
	prompt    string
	sensitive bool
}

// Run executes code as the script at scriptPath and returns what it made.
// A returned error is a fault in the script (a Lua error, or a timeout); a
// Result with NeedInput set is the script asking for a line, not a failure.
func (e *Engine) Run(ctx context.Context, scriptPath, code string, req Request) (Result, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true, RegistrySize: 1024 * 8, CallStackSize: 120})
	defer L.Close()
	openSafeLibs(L)

	r := &run{eng: e, script: scriptPath, req: req}
	L.SetGlobal("write", L.NewFunction(r.write))
	L.SetGlobal("prompt", L.NewFunction(r.prompt_))
	L.SetGlobal("request", r.requestTable(L))
	if e.opts.Store != nil {
		L.SetGlobal("store", r.storeTable(L))
	}

	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	L.SetContext(ctx)

	// the standard helpers load first, as their own chunk, so a user
	// script's error line numbers stay its own
	if err := L.DoString(prelude); err != nil {
		return Result{}, fmt.Errorf("prelude failed to load: %w", err)
	}

	err := L.DoString(code)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("script exceeded its %s time limit", e.opts.Timeout)
		}
		// sp.stop() is a clean early finish, not a fault
		if !strings.Contains(err.Error(), spStop) {
			return Result{}, scriptError(err)
		}
	}

	// a script may write(), or simply return a string, or both — write wins
	body := r.out.String()
	if body == "" {
		if s, ok := returnedString(L); ok {
			body = s
		}
	}
	// prompt() sets needInput but does not abort, so the whole body (a game
	// board, say) is here as well as the request for the next line: the web
	// and TUI show both, gemini sends status 10 with just the prompt.
	if r.needInput {
		return Result{Body: []byte(body), NeedInput: true, Prompt: r.prompt, Sensitive: r.sensitive}, nil
	}
	return Result{Body: []byte(body)}, nil
}

// write appends its arguments to the output, stopping at the cap.
func (r *run) write(L *lua.LState) int {
	for i := 1; i <= L.GetTop() && !r.overflow; i++ {
		s := L.Get(i).String()
		if room := r.eng.opts.MaxOutput - r.out.Len(); len(s) > room {
			if room > 0 {
				r.out.WriteString(s[:room])
			}
			r.overflow = true
			return 0
		}
		r.out.WriteString(s)
	}
	return 0
}

// prompt declares that the script wants a line of input from the reader,
// without stopping — the script goes on to render its full output, and the
// door decides how to ask (a web form, the TUI prompt, gemini status 10/11).
// The submitted line, when there is one, is request.input.
func (r *run) prompt_(L *lua.LState) int {
	r.prompt = L.OptString(1, "")
	r.sensitive = L.OptBool(2, false)
	r.needInput = true
	return 0
}

func (r *run) requestTable(L *lua.LState) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("path", lua.LString(r.req.Path))
	t.RawSetString("proto", lua.LString(r.req.Proto))
	t.RawSetString("host", lua.LString(r.req.Host))
	t.RawSetString("now", lua.LNumber(r.req.Now.Unix()))
	t.RawSetString("identity", lua.LString(r.req.Identity))
	t.RawSetString("identity_kind", lua.LString(r.req.IdentityKind))
	t.RawSetString("identity_verified", lua.LBool(r.req.Verified))
	t.RawSetString("input", lua.LString(r.req.Input))
	t.RawSetString("has_input", lua.LBool(r.req.HasInput))
	q := L.NewTable()
	for k, v := range r.req.Query {
		q.RawSetString(k, lua.LString(v))
	}
	t.RawSetString("query", q)
	return t
}

func (r *run) storeTable(L *lua.LState) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("get", L.NewFunction(r.storeGet))
	t.RawSetString("set", L.NewFunction(r.storeSet))
	t.RawSetString("delete", L.NewFunction(r.storeDelete))
	t.RawSetString("keys", L.NewFunction(r.storeKeys))
	return t
}

func (r *run) storeGet(L *lua.LState) int {
	if v, ok := r.eng.opts.Store.Get(r.script, L.CheckString(1)); ok {
		L.Push(lua.LString(v))
	} else {
		L.Push(lua.LNil)
	}
	return 1
}

func (r *run) storeSet(L *lua.LState) int {
	key := L.CheckString(1)
	val := L.CheckString(2)
	if len(val) > r.eng.opts.MaxValueLen {
		L.RaiseError("store value too large (max %d bytes)", r.eng.opts.MaxValueLen)
		return 0
	}
	// a new key counts against the cap; overwriting an existing one does not
	if _, exists := r.eng.opts.Store.Get(r.script, key); !exists {
		if len(r.eng.opts.Store.Keys(r.script)) >= r.eng.opts.MaxKeys {
			L.RaiseError("store is full (max %d keys)", r.eng.opts.MaxKeys)
			return 0
		}
	}
	if err := r.eng.opts.Store.Set(r.script, key, val); err != nil {
		L.RaiseError("store write failed: %s", err.Error())
	}
	return 0
}

func (r *run) storeDelete(L *lua.LState) int {
	r.eng.opts.Store.Delete(r.script, L.CheckString(1))
	return 0
}

func (r *run) storeKeys(L *lua.LState) int {
	keys := r.eng.opts.Store.Keys(r.script)
	t := L.NewTable()
	for _, k := range keys {
		t.Append(lua.LString(k))
	}
	L.Push(t)
	return 1
}

// returnedString reports a string left on the stack by `return "..."`.
func returnedString(L *lua.LState) (string, bool) {
	if L.GetTop() == 0 {
		return "", false
	}
	if s, ok := L.Get(-1).(lua.LString); ok {
		return string(s), true
	}
	return "", false
}

// scriptError trims gopher-lua's stack traceback to the first line, which is
// the part an author can act on.
func scriptError(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, "\nstack traceback:"); i >= 0 {
		msg = msg[:i]
	}
	return errors.New(strings.TrimSpace(msg))
}

// openSafeLibs opens only the libraries a page script needs, then removes the
// members of the base library that would let a script load more code or
// escape — none of them can reach os/io (never opened), but they are removed
// so the surface a script sees is exactly what is intended.
func openSafeLibs(L *lua.LState) {
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.StringLibName, lua.OpenString},
		{lua.TabLibName, lua.OpenTable},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(lib.open))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
	for _, gone := range []string{
		"dofile", "loadfile", "load", "loadstring", "require", "module",
		"collectgarbage", "print", "newproxy",
	} {
		L.SetGlobal(gone, lua.LNil)
	}
}
