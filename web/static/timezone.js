// Saves the browser timezone for users who have none yet (SPEC §4.5).
(function () {
  "use strict";
  var tz;
  try {
    tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch (e) {
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
