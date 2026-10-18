// Detects the setup timezone or saves it for users who have none yet (SPEC §4.5).
(function () {
  "use strict";
  var tz;
  try {
    tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch (e) {
    console.warn("Browser timezone detection failed", e);
    return;
  }
  var input = document.querySelector("[data-browser-timezone]");
  if (input) {
    if (!input.value && tz) input.value = tz;
    return;
  }
  var meta = document.querySelector('meta[name="csrf-token"]');
  if (!tz || !meta || !window.fetch) return;
  fetch("/settings/timezone", {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-CSRF-Token": meta.content },
    body: new URLSearchParams({ timezone: tz })
  }).catch(function () {});
})();
