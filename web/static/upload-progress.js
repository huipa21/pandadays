(() => {
  const messages = document.getElementById("upload-messages");
  if (!messages || !window.XMLHttpRequest || !window.FormData) return;

  document.addEventListener("submit", (event) => {
    const form = event.target;
    if (event.defaultPrevented || form.method !== "post" ||
        form.enctype !== "multipart/form-data" || form.hasAttribute("hx-post")) return;
    const action = new URL(event.submitter?.hasAttribute("formaction") ? event.submitter.formAction : form.action, window.location.href);
    if (action.origin !== window.location.origin) return;
    if (form.dataset.uploading) {
      event.preventDefault();
      return;
    }
    if (!Array.from(form.querySelectorAll('input[type="file"]')).some((input) => input.files?.length)) return;

    event.preventDefault();
    const data = new FormData(form);
    if (event.submitter?.name) data.append(event.submitter.name, event.submitter.value);
    let bar = form.querySelector("[data-upload-progress]");
    if (!bar) {
      bar = document.createElement("progress");
      bar.className = "upload-progress";
      bar.max = 100;
      bar.setAttribute("aria-label", messages.dataset.label);
      bar.dataset.uploadProgress = "";
      form.append(bar);
    }
    let status = form.querySelector("[data-upload-status]");
    if (!status) {
      status = document.createElement("p");
      status.className = "hint upload-status";
      status.dataset.uploadStatus = "";
      status.setAttribute("role", "status");
      bar.after(status);
    }
    const buttons = Array.from(form.querySelectorAll('button[type="submit"], input[type="submit"]'));
    const disabled = buttons.map((button) => button.disabled);
    const xhr = new XMLHttpRequest();
    function finish() {
      delete form.dataset.uploading;
      form.removeAttribute("aria-busy");
      buttons.forEach((button, index) => { button.disabled = disabled[index]; });
      bar.hidden = true;
    }
    function fail(message) {
      finish();
      status.textContent = message;
      status.setAttribute("role", "alert");
      form.dispatchEvent(new Event("upload:error"));
    }
    xhr.upload.addEventListener("progress", (progress) => {
      if (!progress.lengthComputable || !progress.total) {
        bar.removeAttribute("value");
        return;
      }
      bar.value = Math.floor(progress.loaded / progress.total * 100);
      status.textContent = messages.dataset.uploading + " " + bar.value + "%";
    });
    xhr.upload.addEventListener("load", () => {
      bar.removeAttribute("value");
      status.textContent = messages.dataset.processing;
    });
    xhr.addEventListener("error", () => fail(messages.dataset.network));
    xhr.addEventListener("abort", () => fail(messages.dataset.network));
    xhr.addEventListener("load", () => {
      const type = (xhr.getResponseHeader("Content-Type") || "").toLowerCase();
      if (type.startsWith("text/html") && xhr.responseText) {
        finish();
        form.dispatchEvent(new Event("upload:navigate"));
        // Render the actual POST response, including validation/import previews,
        // without resubmitting files or losing the server-rendered form errors.
        window.history.replaceState(null, "", xhr.responseURL);
        document.open();
        document.write(xhr.responseText);
        document.close();
      } else {
        const text = xhr.responseText.trim();
        fail(type.startsWith("text/plain") && text && text.length <= 500 ? text : messages.dataset.generic);
      }
    });
    xhr.open("POST", action.href);
    const csrf = data.get("csrf_token");
    if (csrf) xhr.setRequestHeader("X-CSRF-Token", csrf);
    form.dataset.uploading = "true";
    form.setAttribute("aria-busy", "true");
    buttons.forEach((button) => { button.disabled = true; });
    bar.value = 0;
    bar.hidden = false;
    status.setAttribute("role", "status");
    status.textContent = messages.dataset.uploading + " 0%";
    try {
      xhr.send(data);
    } catch (error) {
      fail(messages.dataset.network);
    }
  });
})();
