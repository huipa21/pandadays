// Shows feedback for htmx requests: server/network errors (htmx does not swap
// error responses, so they would otherwise fail silently) and upload progress.
(function () {
  "use strict";

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
