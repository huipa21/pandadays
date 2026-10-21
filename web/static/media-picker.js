// Without JavaScript all media sections stay inline.
(() => {
  const picker = document.querySelector("[data-media-picker]");
  const panel = picker?.querySelector("[data-media-panel]");
  if (!panel) return;
  const submit = picker.querySelector("[data-media-submit]");
  function hasSelection() {
    return !!panel.querySelector('input[name="library"]:checked') ||
      Array.from(panel.querySelectorAll('input[type="file"]')).some((input) => input.files?.length);
  }
  if (submit) {
    const updateSubmit = () => {
      submit.disabled = !hasSelection();
      const uploads = Array.from(panel.querySelectorAll('input[type="file"]')).some((input) => input.files?.length);
      const label = uploads ? submit.dataset?.uploadLabel : submit.dataset?.selectLabel;
      if (label) submit.textContent = label;
    };
    panel.addEventListener("change", updateSubmit);
    picker.addEventListener("submit", (event) => {
      if (!hasSelection()) event.preventDefault();
    });
    updateSubmit();
  }
  const dialog = document.createElement("dialog");
  if (!dialog.showModal) return;

  const bar = picker.querySelector("[data-media-bar]");
  const status = picker.querySelector("[data-media-status]");
  const openButton = picker.querySelector("[data-media-open]");
  const done = panel.querySelector("[data-media-done]");
  const tablist = panel.querySelector("[data-media-tabs]");
  const tabs = Array.from(tablist.querySelectorAll('[role="tab"]'));
  const files = panel.querySelector("[data-media-files]");
  const library = () => panel.querySelectorAll('input[name="library"]:checked').length;

  const inline = picker.hasAttribute("data-media-inline");
  if (!inline) {
    dialog.className = "card confirm-dialog media-dialog";
    dialog.setAttribute("aria-labelledby", "media-panel-heading");
    panel.before(dialog);
    dialog.append(panel);
  }
  picker.classList.add("media-picker-enhanced");
  if (bar) bar.hidden = false;
  done.hidden = inline;
  tablist.hidden = false;

  function select(tab, focus) {
    tabs.forEach((t) => {
      const on = t === tab;
      t.setAttribute("aria-selected", String(on));
      t.tabIndex = on ? 0 : -1;
      document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
    });
    if (focus) tab.focus();
  }

  function count(template, n) {
    return template.replace("%d", String(n));
  }

  function update() {
    if (!status) return;
    const parts = [];
    const picked = library();
    const uploads = (files?.files?.length || 0) + (panel.querySelector("[data-record-file]")?.files?.length || 0);
    if (picked) parts.push(count(status.dataset.libraryCount, picked));
    if (uploads) parts.push(count(status.dataset.fileCount, uploads));
    status.textContent = parts.length ? parts.join(" · ") : status.dataset.none;
  }

  tabs.forEach((tab, i) => {
    tab.addEventListener("click", () => select(tab, false));
    tab.addEventListener("keydown", (event) => {
      let next;
      if (event.key === "ArrowRight") next = tabs[(i + 1) % tabs.length];
      else if (event.key === "ArrowLeft") next = tabs[(i - 1 + tabs.length) % tabs.length];
      else if (event.key === "Home") next = tabs[0];
      else if (event.key === "End") next = tabs[tabs.length - 1];
      if (!next) return;
      event.preventDefault();
      select(next, true);
    });
  });

  openButton?.addEventListener("click", () => dialog.showModal());
  done.addEventListener("click", () => dialog.close());
  panel.addEventListener("change", update);

  const hasLibrary = panel.querySelector('input[name="library"]');
  select(hasLibrary ? tabs[0] : tabs[1], false);
  update();
})();
