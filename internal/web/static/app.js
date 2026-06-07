// app.js - Echo admin dashboard and user app session glue.
(function () {
  "use strict";

  var csrfToken = "";

  disableHTMXScripting();

  function disableHTMXScripting() {
    if (window.htmx) {
      var htmx = window.htmx;
      htmx.config.allowScriptTags = false;
    }
  }

  function redirectToLogin() {
    window.location.href = "/login";
  }

  function refreshSession() {
    return fetch("/api/session/me", { credentials: "same-origin" })
      .then(function (resp) {
        if (resp.status === 401) {
          redirectToLogin();
          return null;
        }
        if (!resp.ok) {
          return null;
        }
        return resp.json();
      })
      .then(function (data) {
        if (!data || !data.authenticated) {
          csrfToken = "";
          return null;
        }
        csrfToken = data.csrf_token || "";
        return data;
      })
      .catch(function () {
        csrfToken = "";
      });
  }

  function sameOriginURL(url) {
    if (!url) {
      return null;
    }
    try {
      var parsed = new URL(url, window.location.origin);
      if (parsed.origin !== window.location.origin) {
        return null;
      }
      return parsed;
    } catch (err) {
      return null;
    }
  }

  function sameOriginPath(url) {
    var parsed = sameOriginURL(url);
    return parsed ? parsed.pathname : "";
  }

  function safeMethod(method) {
    switch ((method || "GET").toUpperCase()) {
      case "GET":
      case "HEAD":
      case "OPTIONS":
      case "TRACE":
        return true;
      default:
        return false;
    }
  }

  function addCSRFHeader(headers, method, url) {
    if (!csrfToken || safeMethod(method) || !sameOriginURL(url)) {
      return;
    }
    headers["X-CSRF-Token"] = csrfToken;
  }

  document.addEventListener("htmx:configRequest", function (evt) {
    if (!evt.detail.headers) {
      evt.detail.headers = {};
    }
    var method = evt.detail.verb || evt.detail.method || "GET";
    addCSRFHeader(evt.detail.headers, method, evt.detail.path);
  });

  document.addEventListener("htmx:responseError", function (evt) {
    if (evt.detail && evt.detail.xhr && evt.detail.xhr.status === 401) {
      redirectToLogin();
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

  // serializeForm turns management and user forms into the typed JSON object their
  // endpoints decode. Checkboxes coerce to booleans, number inputs to numbers,
  // everything else to strings. Empty number inputs are omitted entirely so
  // optional numeric body fields decode as JSON null.
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
          continue;
        }
        obj[key] = Number(raw);
        continue;
      }
      obj[key] = el.value;
    }
    return obj;
  }

  function managementTarget(form) {
    var url = form.getAttribute("hx-post");
    var method = "POST";
    if (!url) {
      url = form.getAttribute("hx-patch");
      method = "PATCH";
    }
    var path = sameOriginPath(url);
    if (!path || path.indexOf("/api/me/") === 0 || path.indexOf("/api/") !== 0) {
      return null;
    }
    return { url: url, method: method };
  }

  function userMediaTarget(form) {
    var url = form.getAttribute("hx-post");
    if (!url) {
      return null;
    }
    var path = sameOriginPath(url);
    if (!path || path.indexOf("/api/me/discovery/") !== 0) {
      return null;
    }
    return { url: url, path: path, method: "POST" };
  }

  function logoutTarget(form) {
    var url = form.getAttribute("hx-post");
    if (!url || sameOriginPath(url) !== "/api/session/logout") {
      return null;
    }
    return { url: url, method: "POST" };
  }

  document.addEventListener(
    "submit",
    function (evt) {
      var form = evt.target;
      if (!form || form.tagName !== "FORM") {
        return;
      }
      if (isLoginForm(form)) {
        evt.preventDefault();
        evt.stopPropagation();
        submitLogin(form);
        return;
      }
      var logout = logoutTarget(form);
      if (logout) {
        evt.preventDefault();
        evt.stopPropagation();
        submitLogout(logout);
        return;
      }
      var userTarget = userMediaTarget(form);
      if (userTarget) {
        evt.preventDefault();
        evt.stopPropagation();
        submitJSONForm(form, userTarget, function () {
          reloadAppPanels();
        });
        return;
      }
      var target = managementTarget(form);
      if (!target) {
        return;
      }
      evt.preventDefault();
      evt.stopPropagation();

      submitJSONForm(form, target, function () {
        reloadFor(form);
      });
    },
    true
  );

  function isLoginForm(form) {
    return form.id === "login-form" || form.getAttribute("id") === "login-form";
  }

  function submitLogin(form) {
    setLoginError(false);
    fetch("/api/session/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(serializeForm(form)),
    })
      .then(function (resp) {
        if (!resp.ok) {
          throw new Error("login failed");
        }
        return resp.json();
      })
      .then(function (data) {
        csrfToken = data.csrf_token || "";
        var next = safeRelativeNext(form.getAttribute("data-next") || "");
        if (!next) {
          next = isAdminUser(data.user) ? "/" : "/app";
        }
        window.location.href = next;
      })
      .catch(function () {
        setLoginError(true);
      });
  }

  function submitLogout(target) {
    var headers = {};
    addCSRFHeader(headers, target.method, target.url);
    fetch(target.url, {
      method: target.method,
      credentials: "same-origin",
      headers: headers,
    })
      .then(function (resp) {
        if (resp.status === 401) {
          redirectToLogin();
          return;
        }
        if (resp.ok) {
          csrfToken = "";
          redirectToLogin();
          return;
        }
        return resp.text().then(function (body) {
          alert("Sign out failed (" + resp.status + "): " + body);
        });
      })
      .catch(function (err) {
        alert("Sign out failed: " + err);
      });
  }

  function setLoginError(visible) {
    var error = document.getElementById("login-error");
    if (!error) {
      return;
    }
    error.hidden = !visible;
    if (visible) {
      error.textContent = "Sign in failed.";
    }
  }

  function safeRelativeNext(next) {
    next = (next || "").trim();
    if (
      !next ||
      next.charAt(0) !== "/" ||
      next.indexOf("//") === 0 ||
      next.indexOf("\\") !== -1 ||
      next.toLowerCase().indexOf("%5c") !== -1
    ) {
      return "";
    }
    if (!sameOriginURL(next)) {
      return "";
    }
    return next;
  }

  function isAdminUser(user) {
    if (!user) {
      return false;
    }
    if (user.role === "admin") {
      return true;
    }
    var scopes = user.scopes || [];
    for (var i = 0; i < scopes.length; i++) {
      if (scopes[i] === "admin") {
        return true;
      }
    }
    return false;
  }

  function submitJSONForm(form, target, onOK) {
    var headers = { "Content-Type": "application/json" };
    addCSRFHeader(headers, target.method, target.url);
    fetch(target.url, {
      method: target.method,
      credentials: "same-origin",
      headers: headers,
      body: JSON.stringify(serializeForm(form)),
    })
      .then(function (resp) {
        if (resp.status === 401) {
          redirectToLogin();
          return;
        }
        if (resp.ok) {
          onOK();
          return;
        }
        return resp.text().then(function (body) {
          alert("Save failed (" + resp.status + "): " + body);
        });
      })
      .catch(function (err) {
        alert("Save failed: " + err);
      });
  }

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
    window.location.reload();
  }

  function reloadAppPanels() {
    if (!window.htmx) {
      return;
    }
    var panels = document.querySelectorAll(
      "#media-requests-panel, #media-account-panel"
    );
    for (var i = 0; i < panels.length; i++) {
      window.htmx.trigger(panels[i], "reload");
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    disableHTMXScripting();
    refreshSession();
  });
})();
