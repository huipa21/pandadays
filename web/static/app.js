// Shows feedback for htmx requests: server/network errors (htmx does not swap
// error responses, so they would otherwise fail silently) and upload progress.
(function () {
  "use strict";

  var themeToggle = document.getElementById("theme-toggle");
  function syncThemeToggle() {
    if (themeToggle) themeToggle.setAttribute("aria-pressed", String(document.documentElement.dataset.theme === "dark"));
  }
  syncThemeToggle();
  if (themeToggle) {
    themeToggle.addEventListener("click", function () {
      var theme = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
      document.documentElement.dataset.theme = theme;
      document.cookie = "panda_theme=" + theme + "; Path=/; Max-Age=31536000; SameSite=Lax" +
        (window.location.protocol === "https:" ? "; Secure" : "");
      syncThemeToggle();
    });
  }

  function flash() {
    return document.getElementById("flash");
  }

  function showError(message) {
    var el = flash();
    if (!el) return;
    el.textContent = message;
    el.hidden = false;
    el.scrollIntoView({ block: "nearest" });
  }

  function serverMessage(xhr) {
    var type = (xhr.getResponseHeader("Content-Type") || "").toLowerCase();
    var text = (xhr.responseText || "").trim();
    if (type.indexOf("text/plain") === 0 && text && text.length <= 500) return text;
    return flash() ? flash().dataset.errorGeneric : "";
  }

  function progressFor(elt) {
    return elt && elt.querySelector ? elt.querySelector("[data-upload-progress]") : null;
  }

  document.addEventListener("submit", function (event) {
    var message = event.target.dataset.confirm;
    if (message && !window.confirm(message)) event.preventDefault();
  });

  document.addEventListener("click", function (event) {
    var cancel = event.target.closest ? event.target.closest("dialog [data-close-dialog]") : null;
    if (!cancel) return;
    event.preventDefault();
    cancel.closest("dialog").close();
  });

  document.addEventListener("htmx:beforeRequest", function (event) {
    var menu = event.target.closest ? event.target.closest(".dropdown[open]") : null;
    if (menu) menu.open = false;
  });

  document.addEventListener("htmx:afterSwap", function (event) {
    var dialog = event.detail.target;
    if (!dialog || dialog.id !== "app-dialog") return;
    localizeTimes(dialog);
    if (dialog.showModal && !dialog.open) dialog.showModal();
    dialog.scrollTop = 0;
  });

  // Users without a saved timezone see server times re-rendered in the browser's timezone.
  function pad(n) {
    return (n < 10 ? "0" : "") + n;
  }
  function localizeTimes(root) {
    root.querySelectorAll("time[data-local-time]").forEach(function (el) {
      var d = new Date(el.getAttribute("datetime"));
      if (isNaN(d.getTime())) return;
      var zone = "";
      try {
        var part = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(d)
          .find(function (p) { return p.type === "timeZoneName"; });
        if (part) zone = " " + part.value;
      } catch (e) {}
      el.textContent = d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " +
        pad(d.getHours()) + ":" + pad(d.getMinutes()) + zone;
    });
  }
  localizeTimes(document);

  document.addEventListener("click", function (event) {
    document.querySelectorAll(".dropdown[open]").forEach(function (menu) {
      if (!menu.contains(event.target)) menu.open = false;
    });
  });

  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") return;
    document.querySelectorAll(".dropdown[open]").forEach(function (menu) {
      menu.open = false;
      menu.querySelector("summary").focus();
    });
  });

  document.addEventListener("htmx:beforeRequest", function (event) {
    var el = flash();
    if (el) el.hidden = true;
    var bar = progressFor(event.target);
    if (bar) bar.hidden = false;
  });

  document.addEventListener("htmx:responseError", function (event) {
    showError(serverMessage(event.detail.xhr));
  });

  document.addEventListener("htmx:sendError", function () {
    var el = flash();
    if (el) showError(el.dataset.errorNetwork);
  });

  document.addEventListener("htmx:xhr:progress", function (event) {
    var bar = progressFor(event.target);
    if (!bar || !event.detail.lengthComputable || !event.detail.total) return;
    bar.hidden = false;
    bar.value = Math.round((event.detail.loaded / event.detail.total) * 100);
  });

  document.addEventListener("htmx:afterRequest", function (event) {
    var bar = progressFor(event.target);
    if (!bar) return;
    bar.hidden = true;
    bar.removeAttribute("value");
  });
})();
