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
