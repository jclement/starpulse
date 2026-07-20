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

  window.addEventListener("beforeunload", function (e) {
    if (dirty) {
      e.preventDefault();
      e.returnValue = "";
    }
  });

  function save() {
    var path = pathInput.value.trim();
    if (!path) {
      setStatus("path required", true);
      pathInput.focus();
      return;
    }
    setStatus("saving…");
    fetch("/admin/save", { method: "POST", body: new FormData(form) })
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        dirty = false;
        setStatus("saved " + new Date().toLocaleTimeString());
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
        setStatus("save failed: " + err.message, true);
      });
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    save();
  });
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      save();
    }
    if (e.key === "Escape" && !pane.hidden) {
      setPreview(false);
    }
  });

  function renderPreview() {
    fetch("/api/preview", { method: "POST", body: ta.value })
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

// admin page-list filter: live substring match on path/title, keeps folder
// headers visible only when they have matches. Works fully client-side.
(function () {
  var input = document.getElementById("page-filter");
  var table = document.getElementById("pages-table");
  if (!input || !table) return;
  var count = document.getElementById("filter-count");
  var rows = Array.prototype.slice.call(table.querySelectorAll("tr.page-row"));
  var groups = Array.prototype.slice.call(table.querySelectorAll("tbody.folder-group"));

  function apply() {
    var q = input.value.trim().toLowerCase();
    var terms = q.split(/\s+/).filter(Boolean);
    var shown = 0;
    rows.forEach(function (r) {
      var key = r.getAttribute("data-key");
      var ok = terms.every(function (t) { return key.indexOf(t) !== -1; });
      r.hidden = !ok;
      if (ok) shown++;
    });
    // hide a folder group whose rows are all filtered out
    groups.forEach(function (g) {
      var anyVisible = g.querySelector("tr.page-row:not([hidden])") !== null;
      g.querySelector("tr.folder-row").hidden = !anyVisible;
    });
    if (q) {
      count.hidden = false;
      count.textContent = "showing " + shown + " of " + rows.length;
    } else {
      count.hidden = true;
    }
  }
  input.addEventListener("input", apply);
  // '/' focuses the filter from anywhere on the page
  document.addEventListener("keydown", function (e) {
    if (e.key === "/" && document.activeElement !== input) {
      e.preventDefault();
      input.focus();
    }
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

  var main = ta.parentNode;
  var layer = document.createElement("pre");
  layer.className = "ed-hl";
  layer.setAttribute("aria-hidden", "true");
  var code = document.createElement("code");
  layer.appendChild(code);
  main.insertBefore(layer, ta);
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

  function pick() {
    var p = (path && path.value) || "";
    if (/\.theme$/.test(p)) return css;
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

  ta.addEventListener("input", paint);
  ta.addEventListener("scroll", sync);
  if (path) path.addEventListener("input", paint);
  window.addEventListener("resize", sync);
  paint();
})();
