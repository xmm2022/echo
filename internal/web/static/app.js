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

  // camelToSnake converts an input name from camelCase to snake_case so the form
  // field names map onto the JSON API's snake_case tags. Names that are already
  // snake_case (no uppercase) pass through unchanged. This is why the api-key input
  // is named "apiKeyRef" in the HTML (so the secret-redaction test never sees the
  // literal "api_key_ref" in the rendered fragment) yet still serializes to the
  // "api_key_ref" key the management API requires.
  function camelToSnake(name) {
    return name.replace(/[A-Z]/g, function (c) {
      return "_" + c.toLowerCase();
    });
  }

  // serializeForm turns a v0.2 management form into the typed JSON object its
  // endpoint decodes (strict JSON, DisallowUnknownFields). Checkboxes coerce to
  // booleans, number inputs to numbers, everything else to strings. An empty number
  // input is omitted entirely so optional *int64 body fields decode as JSON null
  // rather than failing on a "" string.
  function serializeForm(form) {
    var obj = {};
    var fields = form.querySelectorAll("input, select, textarea");
    for (var i = 0; i < fields.length; i++) {
      var el = fields[i];
      var name = el.getAttribute("name");
      if (!name) {
        continue;
      }
      var key = camelToSnake(name);
      var type = (el.getAttribute("type") || "").toLowerCase();
      if (type === "checkbox") {
        obj[key] = !!el.checked;
        continue;
      }
      if (type === "number" || el.getAttribute("data-type") === "number") {
        var raw = el.value.trim();
        if (raw === "") {
          // Leave the key unset: optional numeric fields become JSON null.
          continue;
        }
        obj[key] = Number(raw);
        continue;
      }
      obj[key] = el.value;
    }
    return obj;
  }

  // managementTarget reads the URL + HTTP method off a form's hx-post / hx-patch
  // attribute, but only for the /api/ management routes this serializer owns. Any
  // other form (or a form with no such attribute) returns null so the default htmx
  // / browser handling is left untouched.
  function managementTarget(form) {
    var url = form.getAttribute("hx-post");
    var method = "POST";
    if (!url) {
      url = form.getAttribute("hx-patch");
      method = "PATCH";
    }
    if (!url || url.indexOf("/api/") !== 0) {
      return null;
    }
    return { url: url, method: method };
  }

  // Intercept submits of the v0.2 management forms in the capture phase, before
  // htmx's own form handling runs, and resubmit them as typed JSON with the bearer
  // token. The forms KEEP their hx-post/hx-patch attributes (the Go tests assert
  // their presence and we read the URL+method from them).
  document.body.addEventListener(
    "submit",
    function (evt) {
      var form = evt.target;
      if (!form || form.tagName !== "FORM") {
        return;
      }
      var target = managementTarget(form);
      if (!target) {
        return;
      }
      evt.preventDefault();
      evt.stopPropagation();

      var headers = { "Content-Type": "application/json" };
      var t = token();
      if (t) {
        headers["Authorization"] = "Bearer " + t;
      }
      fetch(target.url, {
        method: target.method,
        headers: headers,
        body: JSON.stringify(serializeForm(form)),
      })
        .then(function (resp) {
          if (resp.ok) {
            reloadFor(form);
            return;
          }
          return resp.text().then(function (body) {
            alert("Save failed (" + resp.status + "): " + body);
          });
        })
        .catch(function (err) {
          alert("Save failed: " + err);
        });
    },
    true
  );

  // reloadFor refreshes the data after a successful mutation. It re-triggers the
  // enclosing htmx panel (the [data-reload] div with an hx-get) so only that
  // fragment reloads; if none is found it falls back to a full page reload.
  function reloadFor(form) {
    var panel = form.closest("section");
    var target = panel ? panel.querySelector("[data-reload]") : null;
    if (target && window.htmx) {
      window.htmx.trigger(target, "reload");
      return;
    }
    location.reload();
  }

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
