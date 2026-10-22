(() => {
  let active;
  function collapse() {
    if (!active) return;
    active.audio.pause();
    active.player.hidden = true;
    active.play.setAttribute("aria-expanded", "false");
    active = undefined;
  }
  document.addEventListener("click", (event) => {
    if (active && !active.player.contains(event.target) && !active.play.contains(event.target)) collapse();
  });
  document.addEventListener("toggle", (event) => {
    if (active && !event.target.open && event.target.contains(active.player)) collapse();
  }, true);
  document.addEventListener("close", (event) => {
    if (active && event.target.contains(active.player)) collapse();
  }, true);
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") collapse();
  });
  window.addEventListener("pagehide", collapse);

  function init(root) {
    root.querySelectorAll("[data-audio-player]").forEach((player) => {
      if (player.dataset.ready) return;
      player.dataset.ready = "true";
      const audio = player.querySelector("audio");
      const error = player.querySelector("[data-audio-error]");
      function show(message) {
        error.textContent = message;
        error.hidden = false;
      }
      async function failed() {
        const source = audio.getAttribute("src");
        show(player.dataset.errorPlayback);
        if (!source?.startsWith("/attachments/") && !source?.startsWith("/private/attachments/")) return;
        try {
          const diagnosticURL = `${source}${source.includes("?") ? "&" : "?"}lang=${encodeURIComponent(document.documentElement.lang)}`;
          const response = await fetch(diagnosticURL, { headers: { Range: "bytes=0-0" } });
          if (!response.ok && response.headers.get("Content-Type")?.startsWith("text/plain")) {
            const message = (await response.text()).trim();
            if (message && message.length <= 500) show(message);
          }
        } catch {
          show(player.dataset.errorNetwork);
        }
      }
      audio.addEventListener("error", failed);
      audio.addEventListener("loadedmetadata", () => { error.hidden = true; });
      const entry = player.closest?.("[data-attachment-entry]");
      const play = entry?.querySelector("[data-audio-play]");
      play?.addEventListener("click", async () => {
        collapse();
        const playback = { player, audio, play };
        active = playback;
        player.hidden = false;
        play.setAttribute("aria-expanded", "true");
        try {
          await audio.play();
        } catch {
          if (active === playback) await failed();
        }
      });
      if (audio.error) failed();
    });
  }
  init(document);
  document.addEventListener("htmx:load", (event) => init(event.target));
})();
