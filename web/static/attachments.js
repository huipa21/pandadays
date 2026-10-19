(() => {
  const controls = document.querySelector(".voice-controls");
  if (!controls || !navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) return;

  const button = controls.querySelector("[data-record-button]");
  const status = controls.querySelector("[data-record-status]");
  const form = controls.closest("form");
  const fileInput = controls.querySelector("[data-record-file]");
  const preview = controls.querySelector("[data-record-preview]");
  if (!button || !status || !form || !fileInput) return;

  controls.hidden = false;
  controls.parentElement.querySelector("[data-record-unavailable]").hidden = true;
  let recorder;
  let stream;
  let chunks = [];
  let previewURL;
  let pending = false;
  function release() {
    stream?.getTracks().forEach((track) => track.stop());
  }
  window.addEventListener("pagehide", release);
  form.addEventListener("submit", (event) => {
    if (pending || recorder?.state === "recording") {
      event.preventDefault();
      status.textContent = controls.dataset.recording;
    }
  });

  // Browsers support different recorder formats: Chrome/Firefox WebM or Ogg
  // Opus, Safari MP4/AAC. The server sniffs the content to classify it.
  const preferredTypes = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4", "audio/ogg;codecs=opus", "audio/ogg"];
  const mimeType = preferredTypes.find((type) => MediaRecorder.isTypeSupported?.(type));
  const extensions = { "audio/webm": "webm", "video/webm": "webm", "audio/mp4": "m4a", "video/mp4": "m4a", "audio/ogg": "ogg", "audio/aac": "aac" };

  function voiceFilename(type) {
    const ext = extensions[(type || "").split(";")[0].trim().toLowerCase()] || "webm";
    const stamp = new Date().toISOString().replace(/\.\d+Z$/, "").replace(/[-:]/g, "").replace("T", "-");
    return `voice-${stamp}.${ext}`;
  }

  function resetButton() {
    button.disabled = false;
    button.textContent = controls.dataset.recordStart;
  }

  button.addEventListener("click", async () => {
    if (recorder?.state === "recording") {
      pending = true;
      recorder.stop();
      button.disabled = true;
      return;
    }
    button.disabled = true;
    pending = true;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      recorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
      chunks = [];
      let failed = false;
      recorder.addEventListener("dataavailable", (event) => {
        if (event.data.size) chunks.push(event.data);
      });
      recorder.addEventListener("error", () => {
        failed = true;
        release();
        pending = false;
        status.textContent = controls.dataset.recordError;
        resetButton();
      });
      recorder.addEventListener("stop", () => {
        release();
        if (failed) return;
        const type = recorder.mimeType || mimeType || "audio/webm";
        const blob = new Blob(chunks, { type });
        try {
          if (!blob.size) throw new Error("empty recording");
          const data = new DataTransfer();
          data.items.add(new File([blob], voiceFilename(type), { type }));
          fileInput.files = data.files;
          fileInput.dispatchEvent(new Event("change", { bubbles: true }));
          if (previewURL) URL.revokeObjectURL(previewURL);
          previewURL = URL.createObjectURL(blob);
          preview.src = previewURL;
          preview.hidden = false;
          status.textContent = controls.dataset.recordReady;
        } catch {
          status.textContent = controls.dataset.recordError;
        }
        pending = false;
        resetButton();
      }, { once: true });
      recorder.start();
      pending = false;
      status.textContent = controls.dataset.recording;
      button.textContent = controls.dataset.recordStop;
      button.disabled = false;
    } catch {
      release();
      pending = false;
      status.textContent = controls.dataset.recordError;
      resetButton();
    }
  });
})();
