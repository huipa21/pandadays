(() => {
  const template = document.getElementById("upload-dropzone-template");
  const svgNS = "http://www.w3.org/2000/svg";
  const types = {
    pdf: ["pdf", "pdf"], txt: ["text", "text"], md: ["text", "text"],
    xls: ["sheet", "sheet"], xlsx: ["sheet", "sheet"], csv: ["sheet", "sheet"],
    jpg: ["image", "image"], jpeg: ["image", "image"], png: ["image", "image"],
    gif: ["image", "image"], webp: ["image", "image"], svg: ["code", "code"],
    mp3: ["audio", "audio"], wav: ["audio", "audio"], m4a: ["audio", "audio"],
    ogg: ["audio", "audio"], aac: ["audio", "audio"],
    mp4: ["video", "video"], mov: ["video", "video"], avi: ["video", "video"],
    webm: ["video", "video"], zip: ["archive", "archive"], rar: ["archive", "archive"],
    "7z": ["archive", "archive"], gz: ["archive", "archive"], tar: ["archive", "archive"],
    doc: ["word", "text"], docx: ["word", "text"], rtf: ["word", "text"],
    html: ["code", "code"], htm: ["code", "code"], css: ["code", "code"],
    js: ["code", "code"], ts: ["code", "code"], json: ["code", "code"],
    xml: ["code", "code"], py: ["code", "code"], go: ["code", "code"],
    ppt: ["slides", "slides"], pptx: ["slides", "slides"],
    psd: ["design", "design"], ai: ["design", "design"],
    exe: ["app", "app"], dll: ["app", "app"],
  };
  const mimeTypes = [
    [/^image\//, "image"], [/^audio\//, "audio"], [/^video\//, "video"],
    [/spreadsheet|excel|csv/, "sheet"], [/presentation|powerpoint/, "slides"],
    [/pdf/, "pdf"], [/zip|compressed|archive/, "archive"],
    [/word|document|text\//, "text"], [/javascript|json|xml|html|css/, "code"],
  ];
  function fileType(file) {
    const extension = file.name?.match(/\.([^.]+)$/)?.[1]?.toLowerCase() || "";
    const [category, glyph] = types[extension] || [
      mimeTypes.find(([pattern]) => pattern.test(file.type || ""))?.[1] || "file",
      "file",
    ];
    const label = extension ? extension.slice(0, 4).toUpperCase() :
      ({ image: "IMG", audio: "AUD", video: "VID" }[category] || "FILE");
    return { category, glyph, label };
  }
  function fileTypeIcon(file, extraClass) {
    const type = fileType(file);
    const svg = document.createElementNS(svgNS, "svg");
    svg.setAttribute("class", `file-type-icon${extraClass ? ` ${extraClass}` : ""}`);
    svg.setAttribute("viewBox", "0 0 48 56");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", type.label);
    svg.dataset.category = type.category;
    const add = (tag, attributes, text) => {
      const node = document.createElementNS(svgNS, tag);
      Object.entries(attributes).forEach(([key, value]) => node.setAttribute(key, value));
      if (text) node.textContent = text;
      svg.append(node);
    };
    add("path", { class: "file-type-sheet", d: "M5 1h25l13 13v40H5z" });
    add("path", { class: "file-type-fold", d: "M30 1v13h13" });
    add("rect", { class: "file-type-label", x: 1, y: 5, width: 32, height: 13, rx: 1 });
    add("text", { class: "file-type-extension", x: 17, y: 14, "text-anchor": "middle" }, type.label);
    const shapes = {
      image: [
        ["circle", { cx: 30, cy: 27, r: 2.5 }],
        ["path", { d: "m13 43 8-10 5 6 4-5 8 9z" }],
      ],
      audio: [
        ["path", { d: "M13 34h3m2-5v10m4-13v16m4-11v6m4-13v20m4-15v10m4-7v4" }],
      ],
      video: [
        ["rect", { x: 12, y: 28, width: 26, height: 17, rx: 2 }],
        ["path", { d: "m23 32 9 5-9 5z" }],
      ],
      sheet: [
        ["rect", { x: 12, y: 27, width: 27, height: 18, rx: 1 }],
        ["path", { d: "M21 27v18m9-18v18M12 33h27m-27 6h27" }],
      ],
      code: [
        ["path", { d: "m21 30-7 7 7 7m9-14 7 7-7 7" }],
      ],
      archive: [
        ["path", { d: "M26 25v20m-3-17h6m-6 6h6m-6 6h6" }],
        ["path", { d: "M21 25h10m-8 20h6" }],
      ],
      slides: [
        ["circle", { cx: 26, cy: 36, r: 10 }],
        ["path", { d: "M26 26v10h10a10 10 0 0 0-10-10zm0 10-7 7" }],
      ],
      design: [
        ["path", { d: "m13 31 13-7 13 7-13 7zm0 5 13 7 13-7m-26 5 13 7 13-7" }],
      ],
      pdf: [
        ["path", { d: "m15 44 9-18 9 18m-14-7h10m-20 12h24" }],
      ],
      text: [
        ["path", { d: "M14 29h24m-24 5h24m-24 5h24m-24 5h17" }],
      ],
      app: [
        ["path", { d: "M26 25v4m0 14v4m-11-13h4m14 0h4m-19-7 3 3m18 8 3 3m0-14-3 3m-18 8-3 3" }],
        ["circle", { cx: 26, cy: 36, r: 7 }],
        ["circle", { cx: 26, cy: 36, r: 2 }],
      ],
      file: [
        ["path", { d: "M14 30h24m-24 5h24m-24 5h18" }],
      ],
    };
    for (const [tag, attributes] of shapes[type.glyph]) add(tag, { class: "file-type-glyph", ...attributes });
    return svg;
  }
  function enhanceMediaThumbs(root) {
    root.querySelectorAll(".media-thumb-icon[data-filename]").forEach((icon) => {
      if (icon.dataset.fileTypeReady) return;
      icon.dataset.fileTypeReady = "true";
      icon.replaceChildren(fileTypeIcon({ name: icon.dataset.filename, type: icon.dataset.mime }, "media-file-type-icon"));
    });
  }
  function enhance(input) {
    if (input.dataset.uploadReady) return;
    input.dataset.uploadReady = "true";
    let container = input.closest("[data-upload-files]");
    if (!container) {
      container = document.createElement("div");
      input.parentElement.before(container);
      container.append(input.parentElement);
    }
    let list = container.querySelector("[data-upload-list]");
    if (!list) {
      list = document.createElement("ul");
      list.className = "upload-file-list";
      list.setAttribute("aria-live", "polite");
      list.setAttribute("aria-label", template.dataset.selectedLabel);
      container.append(list);
    }
    const label = input.closest("label");
    label.classList.add("upload-field");
    label.append(template.content.cloneNode(true));
    input.classList.add("visually-hidden");
    const zone = label.querySelector("[data-upload-dropzone]");
    if (!input.multiple) zone.querySelector(".hint").textContent = template.dataset.singleHint;
    let selected = Array.from(input.files || []);
    let notifying = false;
    let urls = [];
    function release() {
      urls.forEach((url) => URL.revokeObjectURL(url));
      urls = [];
    }
    function sync(notify = false) {
      const data = new DataTransfer();
      selected.forEach((file) => data.items.add(file));
      input.files = data.files;
      render();
      if (notify) {
        notifying = true;
        input.dispatchEvent(new Event("change", { bubbles: true }));
        notifying = false;
      }
    }
    function render() {
      release();
      list.replaceChildren();
      selected.forEach((file, index) => {
        const row = document.createElement("li");
        if (file.type.startsWith("image/")) {
          const image = document.createElement("img");
          const url = URL.createObjectURL(file);
          urls.push(url);
          image.src = url;
          image.alt = "";
          image.addEventListener("error", () => { image.hidden = true; });
          row.append(image);
        } else {
          row.append(fileTypeIcon(file, "upload-file-icon"));
        }
        const name = document.createElement("span");
        name.textContent = file.name;
        name.className = "upload-file-name";
        row.append(name);
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "icon-button upload-remove";
        remove.setAttribute("aria-label", template.dataset.removeLabel.replace("%s", () => file.name));
        const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
        icon.setAttribute("class", "icon");
        icon.setAttribute("viewBox", "0 0 24 24");
        icon.setAttribute("aria-hidden", "true");
        const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
        path.setAttribute("d", "m6 6 12 12M18 6 6 18");
        icon.append(path);
        remove.append(icon);
        remove.addEventListener("click", () => {
          selected.splice(index, 1);
          sync(true);
          (list.children[index]?.querySelector("button") || list.lastElementChild?.querySelector("button") || input).focus();
        });
        row.append(remove);
        list.append(row);
      });
    }
    function append(files) {
      if (!files.length) { sync(); return; }
      selected = input.multiple ? selected.concat(files) : files.slice(0, 1);
      sync();
    }
    input.addEventListener("change", () => {
      if (!notifying) append(Array.from(input.files || []));
    });
    input.addEventListener("upload:clear", () => {
      selected = [];
      sync();
    });
    zone.addEventListener("dragover", (event) => {
      event.preventDefault();
      zone.classList.add("is-dragging");
    });
    zone.addEventListener("dragleave", () => zone.classList.remove("is-dragging"));
    zone.addEventListener("drop", (event) => {
      event.preventDefault();
      zone.classList.remove("is-dragging");
      append(Array.from(event.dataTransfer.files));
      notifying = true;
      input.dispatchEvent(new Event("change", { bubbles: true }));
      notifying = false;
    });
    input.form?.addEventListener("reset", () => {
      selected = [];
      input.value = "";
      release();
      list.replaceChildren();
    });
    window.addEventListener("pagehide", (event) => {
      if (!event.persisted) release();
    });
    render();
  }
  function initialize(root) {
    root.querySelectorAll('input[type="file"]:not([data-record-file])').forEach(enhance);
    enhanceMediaThumbs(root);
  }
  initialize(document);
  document.addEventListener("htmx:load", (event) => initialize(event.detail.elt));
  const panel = document.getElementById("media-upload-form");
  const opener = document.querySelector(".media-add");
  if (panel && opener && !panel.closest("dialog")) {
    const dialog = document.createElement("dialog");
    if (!dialog.showModal) return;
    dialog.className = "card confirm-dialog upload-dialog";
    dialog.setAttribute("aria-labelledby", "media-upload-title");
    panel.before(dialog);
    dialog.append(panel);
    panel.querySelectorAll("[data-close-dialog]").forEach((button) => { button.hidden = false; });
    opener.addEventListener("click", (event) => {
      event.preventDefault();
      dialog.showModal();
    });
  }
})();
