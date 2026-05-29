// app.js — Echo admin dashboard glue.
//
// The v0.1 API is guarded by a single static admin bearer token (spec §6). A
// browser cannot send that header on its own, so the dashboard shell at "/" is a
// data-free public page with a token box: the admin pastes the token once, it is
// kept in localStorage, and this script attaches it to every htmx request. The
// data endpoints (/ui/*, /api/*) stay behind the auth middleware.
(function () {
  "use strict";
  var KEY = "echo_admin_token";

  function token() {
    return (localStorage.getItem(KEY) || "").trim();
  }

  // Attach the bearer token to every htmx-issued request.
  document.body.addEventListener("htmx:configRequest", function (evt) {
    var t = token();
    if (t) {
      evt.detail.headers["Authorization"] = "Bearer " + t;
    }
  });

  document.addEventListener("DOMContentLoaded", function () {
    var input = document.getElementById("admin-token");
    if (!input) {
      return;
    }
    input.value = token();
    input.addEventListener("change", function () {
      localStorage.setItem(KEY, input.value.trim());
      reloadPanels();
    });
  });

  function reloadPanels() {
    if (!window.htmx) {
      return;
    }
    var panels = document.querySelectorAll("[data-reload]");
    for (var i = 0; i < panels.length; i++) {
      window.htmx.trigger(panels[i], "reload");
    }
  }
})();
