// starpulse admin enhancements — embedded, no CDN, no framework.
// The admin works without JS (plain form posts); this layers on:
//   * full-screen editor: fetch-based save (Ctrl/Cmd-S), dirty tracking,
//     toggleable preview (split pane when wide, overlay when narrow)
//   * other admin pages: Ctrl/Cmd-S submits the main form
(function () {
  "use strict";

  var form = document.getElementById("ed");

  // ---- non-editor admin pages: just Ctrl-S ----
  if (!form) {
    document.addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && e.key === "s") {
        var f = document.querySelector("form.admin");
        if (f) {
          e.preventDefault();
          f.submit();
        }
      }
    });
    return;
  }

  // ---- full-screen editor ----
  var ta = document.getElementById("content");
  var pathInput = document.getElementById("path");
  var status = document.getElementById("ed-status");
  var toggle = document.getElementById("pv-toggle");
  var pane = document.getElementById("pv-pane");
  var preview = document.getElementById("preview");

  var dirty = false;
  var previewTimer = null;

  // A local copy of whatever is in the box, kept in this browser. It exists
  // because the editor once reported "saved" over work the server had
  // discarded: whatever goes wrong next, the words should still be here.
  var stash = (function () {
    var key = "starpulse:draft:" + location.search;
    var ok = true;
    try { localStorage.setItem(key + ":probe", "1"); localStorage.removeItem(key + ":probe"); }
    catch (e) { ok = false; }
    return {
      keep: function (path, text) {
        if (!ok) return;
        try { localStorage.setItem(key, JSON.stringify({ path: path, text: text, at: Date.now() })); }
        catch (e) { /* full or private: the server is still the real answer */ }
      },
      read: function () {
        if (!ok) return null;
        try { return JSON.parse(localStorage.getItem(key) || "null"); } catch (e) { return null; }
      },
      clear: function () { if (ok) { try { localStorage.removeItem(key); } catch (e) {} } },
    };
  })();

  function setStatus(msg, isErr) {
    status.textContent = msg;
    status.style.color = isErr ? "#b3402a" : "";
  }

  function markDirty() {
    if (!dirty) {
      dirty = true;
      setStatus("unsaved changes");
    }
    if (!pane.hidden) {
      clearTimeout(previewTimer);
      previewTimer = setTimeout(renderPreview, 500);
    }
  }
  ta.addEventListener("input", markDirty);
  pathInput.addEventListener("input", markDirty);
  ta.addEventListener("input", function () { stash.keep(pathInput.value, ta.value); });

  // an unsaved copy from last time means something went wrong: offer it back
  // rather than quietly overwriting it with what the server has
  (function () {
    var s = stash.read();
    if (!s || s.text === ta.value) return;
    var bar = document.createElement("div");
    bar.className = "recover";
    bar.innerHTML = "<span>An unsaved copy of this page is in this browser " +
      "(" + new Date(s.at).toLocaleString() + "). The editor is showing the server's version.</span> ";
    var use = document.createElement("button");
    use.type = "button";
    use.textContent = "restore it";
    use.addEventListener("click", function () {
      ta.value = s.text;
      markDirty();
      bar.remove();
      if (typeof paintHook === "function") paintHook();
    });
    var drop = document.createElement("button");
    drop.type = "button";
    drop.className = "quiet";
    drop.textContent = "discard it";
    drop.addEventListener("click", function () { stash.clear(); bar.remove(); });
    bar.appendChild(use);
    bar.appendChild(drop);
    form.insertBefore(bar, form.firstChild);
  })();

  window.addEventListener("beforeunload", function (e) {
    if (dirty) {
      e.preventDefault();
      e.returnValue = "";
    }
  });

  function save(publish) {
    var path = pathInput.value.trim();
    if (!path) {
      setStatus("path required", true);
      pathInput.focus();
      return;
    }
    setStatus(publish ? "publishing…" : "saving…");
    // FormData does not include the button that submitted the form, and the
    // difference between these two buttons is the difference between the
    // world seeing this and not
    var body = new FormData(form);
    body.set("publish", publish ? "1" : "0");
    fetch("/admin/save", { method: "POST", body: body, headers: { Accept: "application/json" } })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) throw new Error(j.error || "HTTP " + r.status);
          return j;
        });
      })
      .then(function (j) {
        if (j.msg) setStatus(j.msg, true);
        dirty = false;
        stash.clear();
        if (publish) {
          // the draft is gone and the badge with it: reload so the page
          // says what is actually true now
          window.location = "/admin/edit?path=" + encodeURIComponent(path);
          return;
        }
        setStatus("saved draft " + new Date().toLocaleTimeString());
        // a rename (or first save) becomes the new baseline
        var old = form.querySelector('input[name="oldpath"]');
        if (!old) {
          old = document.createElement("input");
          old.type = "hidden";
          old.name = "oldpath";
          form.appendChild(old);
        }
        old.value = path;
      })
      .catch(function (err) {
        setStatus((publish ? "publish" : "save") + " failed: " + err.message, true);
      });
  }

  form.addEventListener("submit", function (e) {
    // discard posts to its own action: let the browser do it and follow the
    // redirect, rather than swallowing it as a save
    var to = e.submitter && e.submitter.getAttribute("formaction");
    if (to) {
      dirty = false; // the draft is going away; do not warn about leaving
      return;
    }
    e.preventDefault();
    save(e.submitter && e.submitter.value === "1");
  });
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      // ctrl-s is the safe one: it saves a draft, never publishes
      e.preventDefault();
      save(false);
    }
    if (e.key === "Escape" && !pane.hidden) {
      setPreview(false);
    }
  });

  function renderPreview() {
    fetch("/api/preview?path=" + encodeURIComponent(pathInput.value.trim()), { method: "POST", body: ta.value })
      .then(function (r) { return r.ok ? r.text() : Promise.reject(new Error("HTTP " + r.status)); })
      .then(function (html) { preview.innerHTML = html; })
      .catch(function () {});
  }

  function setPreview(on) {
    pane.hidden = !on;
    toggle.classList.toggle("on", on);
    if (on) renderPreview();
  }

  toggle.hidden = false;
  toggle.addEventListener("click", function () {
    setPreview(pane.hidden);
  });

  // tab inserts spaces instead of leaving the textarea
  ta.addEventListener("keydown", function (e) {
    if (e.key === "Tab" && !e.shiftKey) {
      e.preventDefault();
      var s = ta.selectionStart;
      ta.setRangeText("    ", s, ta.selectionEnd, "end");
      markDirty();
    }
  });
})();

// "close" returns to wherever you opened the editor from — usually the
// folder you were browsing, not the root. The href stays a real link so it
// works without JS, and is used as the fallback when there is nothing to go
// back to (a bookmarked or newly-opened editor tab).
(function () {
  var close = document.getElementById("ed-close");
  if (!close || !document.referrer) return;
  var from;
  try { from = new URL(document.referrer); } catch (e) { return; }
  if (from.origin !== location.origin || from.pathname.indexOf("/admin") !== 0) return;
  if (from.href === location.href) return; // a save that reloaded the editor
  close.addEventListener("click", function (e) {
    e.preventDefault();
    history.back();
  });
})();

// syntax help popover: close on Escape or outside click (CSS-only otherwise)
(function () {
  var help = document.getElementById("syntax-help");
  if (!help) return;
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && help.open) help.open = false;
  });
  document.addEventListener("click", function (e) {
    if (help.open && !help.contains(e.target)) help.open = false;
  });
})();

// admin search: the folder browser shows one folder, so the filter has to
// reach past it. It runs against an inline index of every page and swaps the
// browser for a flat result list. With JS off the same input is a GET form
// and the server renders the identical list.
(function () {
  var input = document.getElementById("page-filter");
  var browse = document.getElementById("browse");
  var out = document.getElementById("search-results");
  var blob = document.getElementById("page-index");
  if (!input || !browse || !out || !blob) return;
  var count = document.getElementById("filter-count");
  var pages;
  try { pages = JSON.parse(blob.textContent); } catch (e) { return; }
  // the form would reload the page on Enter; we already have the answer
  var form = input.form;
  if (form) form.addEventListener("submit", function (e) { e.preventDefault(); });

  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function row(p) {
    var view = p.v ? '<a href="' + esc(p.v) + '">view</a> <span class="dim">·</span> ' : "";
    return '<tr class="page-row"><td><a href="/admin/edit?path=' + encodeURIComponent(p.p) +
      '" title="' + esc(p.t ? p.p + " — " + p.t : p.p) + '">' + esc(p.p) + "</a></td>" +
      '<td class="dim num"></td><td class="acts dim">' + view + "</td></tr>";
  }

  function apply() {
    var q = input.value.trim().toLowerCase();
    if (!q) {
      out.hidden = true;
      out.innerHTML = "";
      browse.hidden = false;
      count.hidden = true;
      return;
    }
    var terms = q.split(/\s+/);
    var hits = pages.filter(function (p) {
      var key = (p.p + " " + (p.t || "")).toLowerCase();
      return terms.every(function (t) { return key.indexOf(t) !== -1; });
    });
    out.innerHTML = '<table class="admin browse">' +
      (hits.length ? hits.map(row).join("") : '<tr><td colspan="3" class="dim">nothing matches</td></tr>') +
      "</table>";
    out.hidden = false;
    browse.hidden = true;
    count.hidden = false;
    count.textContent = hits.length + " of " + pages.length + " pages";
  }
  input.addEventListener("input", apply);
  if (input.value.trim()) apply();
  // '/' focuses the search from anywhere on the page
  document.addEventListener("keydown", function (e) {
    if (e.key === "/" && document.activeElement !== input) {
      e.preventDefault();
      input.focus();
    }
    if (e.key === "Escape" && document.activeElement === input && input.value) {
      input.value = "";
      apply();
    }
  });
})();

// discarding a draft of a page that was never published removes it outright,
// and unlike a delete there is no history to recover it from
(function () {
  var btn = document.getElementById("ed-discard");
  if (!btn) return;
  btn.addEventListener("click", function (e) {
    var p = btn.getAttribute("data-path") || "this page";
    var msg = btn.getAttribute("data-published") === "true"
      ? "Discard the draft of " + p + "?\n\nThe published version stays as it is."
      : "Discard " + p + "?\n\nIt was never published, so this removes it entirely.";
    if (!confirm(msg)) e.preventDefault();
  });
})();

// confirm row deletes (deletes are recoverable from history, but still)
(function () {
  document.querySelectorAll("form.del").forEach(function (f) {
    f.addEventListener("submit", function (e) {
      var p = f.querySelector("button").getAttribute("data-path") || "this page";
      if (!confirm("Delete " + p + "?\n\nIt stays recoverable from its history.")) {
        e.preventDefault();
      }
    });
  });
})();

// ---- editor syntax highlighting -------------------------------------
// A transparent textarea sitting over a highlighted <pre>. No dependencies:
// gemtext is line-oriented, so a per-line pass is all it takes; CSS and the
// key:value special files get a light touch too.
(function () {
  var ta = document.getElementById("content");
  var path = document.getElementById("path");
  if (!ta || !window.getComputedStyle) return;

  // The layer is absolutely positioned over the textarea, so it needs a
  // wrapper of exactly the textarea's size. Positioning it against .ed-main
  // made it span the preview column too and paint its background over the
  // rendered page — the preview looked empty apart from the emoji, which
  // have a filter and so paint in a layer of their own.
  var main = ta.parentNode;
  var src = document.createElement("div");
  src.className = "ed-src";
  main.insertBefore(src, ta);
  src.appendChild(ta);
  var layer = document.createElement("pre");
  layer.className = "ed-hl";
  layer.setAttribute("aria-hidden", "true");
  var code = document.createElement("code");
  layer.appendChild(code);
  src.insertBefore(layer, ta);
  main.classList.add("ed-highlighted");

  function esc(s) {
    return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  function span(cls, s) { return '<span class="' + cls + '">' + esc(s) + "</span>"; }

  // {{directives}} are highlighted inside ordinary text
  function withDirectives(s) {
    var out = "", last = 0, re = /\{\{[^}]*\}\}/g, m;
    while ((m = re.exec(s))) {
      out += esc(s.slice(last, m.index)) + span("g-dir", m[0]);
      last = m.index + m[0].length;
    }
    return out + esc(s.slice(last));
  }

  function gemtext(src) {
    var out = [], pre = false;
    src.split("\n").forEach(function (line) {
      if (line.indexOf("```") === 0) {
        pre = !pre;
        out.push(span("g-fence", line));
        return;
      }
      if (pre) { out.push(span("g-pre", line)); return; }
      if (/^###/.test(line)) { out.push(span("g-h3", line)); return; }
      if (/^##/.test(line))  { out.push(span("g-h2", line)); return; }
      if (/^#/.test(line))   { out.push(span("g-h1", line)); return; }
      if (/^=>/.test(line)) {
        var m = line.match(/^(=>\s*)(\S+)(\s*)([\s\S]*)$/);
        out.push(m
          ? span("g-arrow", m[1]) + span("g-url", m[2]) + esc(m[3]) + span("g-label", m[4])
          : span("g-arrow", line));
        return;
      }
      if (/^\*\s/.test(line)) { out.push(span("g-list", "* ") + withDirectives(line.slice(2))); return; }
      if (/^>/.test(line))    { out.push(span("g-quote", line)); return; }
      if (/^---\s*$/.test(line)) { out.push(span("g-fm", line)); return; }
      out.push(withDirectives(line));
    });
    return out.join("\n");
  }

  function css(src) {
    return esc(src)
      .replace(/(\/\*[\s\S]*?\*\/)/g, '<span class="c-comment">$1</span>')
      .replace(/(--[\w-]+)(\s*:)/g, '<span class="c-var">$1</span>$2')
      .replace(/^(\s*)([\w.#:\[\]@*-][^{};\n]*)(\{)/gm, '$1<span class="c-sel">$2</span>$3');
  }

  function keyvals(src) {
    return esc(src)
      .replace(/^(#.*)$/gm, '<span class="c-comment">$1</span>')
      .replace(/^([\w-]+)(\s*:)/gm, '<span class="c-var">$1</span>$2');
  }

  // Lua for executable pages (name ends .lua). A light touch, in the same
  // spirit as the others: comments, strings, numbers, and the keywords —
  // enough to read the shape of a script, not a full grammar.
  var luaKw = /\b(and|break|do|else|elseif|end|false|for|function|if|in|local|nil|not|or|repeat|return|then|true|until|while)\b/g;
  function lua(src) {
    var out = "", i = 0;
    // walk the source token by token so a keyword inside a string or comment
    // is not coloured as a keyword
    var re = /--\[\[[\s\S]*?\]\]|--[^\n]*|\[\[[\s\S]*?\]\]|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|\b\d[\w.]*\b/g, m;
    while ((m = re.exec(src))) {
      out += esc(src.slice(i, m.index)).replace(luaKw, '<span class="l-kw">$1</span>');
      var tok = m[0], cls = "l-str";
      if (tok.indexOf("--") === 0) cls = "c-comment";
      else if (/^\d/.test(tok)) cls = "l-num";
      out += '<span class="' + cls + '">' + esc(tok) + "</span>";
      i = m.index + tok.length;
    }
    out += esc(src.slice(i)).replace(luaKw, '<span class="l-kw">$1</span>');
    return out;
  }

  function pick() {
    var p = (path && path.value) || "";
    if (/\.lua$/.test(p)) return lua;
    if (/\.feed$/.test(p)) return keyvals;
    if (/\.(css)$/.test(p)) return css;
    if (/\.(gmi|gemini)$/.test(p) || p === "" || !/\.[a-z0-9]+$/i.test(p)) return gemtext;
    return esc; // unknown type: no highlighting, just escaped text
  }

  function paint() {
    // trailing newline keeps the last line's height in the layer
    code.innerHTML = pick()(ta.value) + "\n";
    sync();
  }
  function sync() {
    layer.scrollTop = ta.scrollTop;
    layer.scrollLeft = ta.scrollLeft;
  }

  window.paintHook = paint; // so a restored copy repaints the highlight layer
  ta.addEventListener("input", paint);
  ta.addEventListener("scroll", sync);
  if (path) path.addEventListener("input", paint);
  window.addEventListener("resize", sync);
  paint();
})();
