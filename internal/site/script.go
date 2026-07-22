package site

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jclement/starpulse/internal/script"
	"github.com/jclement/starpulse/internal/store"
)

// Executable pages. A stored page whose name ends .lua is a program, not a
// document: /wordle.gmi.lua is served at /wordle, run in the sandbox, and its
// output rendered as gemtext (the .gmi middle extension). /status.txt.lua
// would be served at /status as plain text. The engine and its limits live
// in internal/script; this file is only the glue between a URL and a run.

// scriptStore adapts *store.Store to the engine's per-script key/value space.
type scriptStore struct{ st *store.Store }

func (s scriptStore) Get(script, key string) (string, bool) { return s.st.ScriptKVGet(script, key) }
func (s scriptStore) Set(script, key, value string) error   { return s.st.ScriptKVSet(script, key, value) }
func (s scriptStore) Delete(script, key string)             { s.st.ScriptKVDelete(script, key) }
func (s scriptStore) Keys(script string) []string           { return s.st.ScriptKVKeys(script) }

var engineOnce sync.Once
var engine *script.Engine

func (s *Site) engine() *script.Engine {
	engineOnce.Do(func() {
		engine = script.New(script.Options{Store: scriptStore{st: s.Store}})
	})
	return engine
}

// ScriptResult is what running an executable page produced. Body is gemtext
// when Gemtext is true, otherwise plain text. When NeedInput is set the
// script is asking the reader for a line; Body then holds whatever it wrote
// before asking.
type ScriptResult struct {
	SourcePath string
	Body       string
	Gemtext    bool
	NeedInput  bool
	Prompt     string
	Sensitive  bool
}

// ScriptFor reports the stored path of the executable page that answers a
// URL, and whether its output is gemtext. A URL /wordle is answered by the
// first of /wordle.gmi.lua, /wordle.txt.lua or /wordle.lua that exists.
func (s *Site) ScriptFor(urlPath string) (storePath string, gemtext bool, ok bool) {
	cleaned, valid := CleanURL(urlPath)
	if !valid {
		return "", false, false
	}
	cleaned = strings.TrimSuffix(cleaned, "/")
	if cleaned == "" {
		return "", false, false
	}
	for _, c := range []struct {
		suffix  string
		gemtext bool
	}{
		{".gmi.lua", true},
		{".txt.lua", false},
		{".lua", true}, // bare .lua defaults to gemtext, the native format
	} {
		p := cleaned + c.suffix
		if s.Store.PageExists(p) {
			return p, c.gemtext, true
		}
	}
	return "", false, false
}

// RunScript executes the page at storePath and returns its result. req.Path
// is filled in from the served URL; the caller supplies query, proto,
// identity and any input.
func (s *Site) RunScript(ctx context.Context, storePath, urlPath string, req script.Request) (ScriptResult, error) {
	_, gemtext, ok := s.ScriptFor(urlPath)
	pg, err := s.Store.GetPage(storePath)
	if err != nil || !ok {
		return ScriptResult{}, err
	}
	req.Path = strings.TrimSuffix(urlPath, "/")
	if req.Now.IsZero() {
		req.Now = time.Now().In(s.loc())
	}
	res, err := s.engine().Run(ctx, storePath, string(pg.Content), req)
	if err != nil {
		return ScriptResult{SourcePath: storePath, Gemtext: gemtext}, err
	}
	return ScriptResult{
		SourcePath: storePath,
		Body:       string(res.Body),
		Gemtext:    gemtext,
		NeedInput:  res.NeedInput,
		Prompt:     res.Prompt,
		Sensitive:  res.Sensitive,
	}, nil
}
