(() => {
  document.querySelectorAll("[data-upload-files]").forEach((container) => {
    if (container.dataset.ready) return;
    container.dataset.ready = "true";
    const input = container.querySelector("[data-media-files]");
    const list = container.querySelector("[data-upload-list]");
    let urls = [];
    function release() {
      urls.forEach((url) => URL.revokeObjectURL(url));
      urls = [];
    }
    function update() {
      release();
      list.replaceChildren();
      Array.from(input.files || []).forEach((file) => {
        const row = document.createElement("li");
        if (file.type.startsWith("image/")) {
          const image = document.createElement("img");
          const url = URL.createObjectURL(file);
          urls.push(url);
          image.src = url;
          image.alt = "";
          image.addEventListener("error", () => { image.hidden = true; });
          row.append(image);
        }
        const name = document.createElement("span");
        name.textContent = file.name;
        row.append(name);
        list.append(row);
      });
    }
    input.addEventListener("change", update);
    input.form?.addEventListener("reset", () => {
      release();
      list.replaceChildren();
    });
    window.addEventListener("pagehide", (event) => {
      if (!event.persisted) release();
    });
    update();
  });
  document.querySelector(".media-add")?.addEventListener("click", () => {
    const form = document.getElementById("media-upload-form");
    form.open = true;
    form.querySelector('input[type="file"]').focus();
  });
})();
