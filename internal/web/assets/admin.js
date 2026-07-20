// starpulse admin enhancements. The admin works fully without JS; this adds
// a live gemtext preview (via POST /api/preview) and Ctrl/Cmd-S to save.
(function () {
  "use strict";

  var ta = document.getElementById("content");
  var form = document.querySelector('form.admin[action="/admin/save"]');

  // Ctrl/Cmd-S submits the nearest admin form
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      var f = form || document.querySelector("form.admin");
      if (f) {
        e.preventDefault();
        f.submit();
      }
    }
  });

  if (!ta || !form) return;

  // live preview pane under the editor
  var pv = document.createElement("div");
  pv.innerHTML =
    '<h3 style="margin-top:2em">preview</h3><div id="preview" style="border:1px dashed var(--line);border-radius:6px;padding:0 1.1em;min-height:4em"></div>';
  form.parentNode.insertBefore(pv, form.nextSibling);
  var out = pv.querySelector("#preview");

  var timer = null;
  function refresh() {
    fetch("/api/preview", { method: "POST", body: ta.value })
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (html) { if (html) out.innerHTML = html; })
      .catch(function () {});
  }
  ta.addEventListener("input", function () {
    clearTimeout(timer);
    timer = setTimeout(refresh, 600);
  });
  refresh();
})();
