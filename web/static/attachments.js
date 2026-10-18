(() => {
  const controls = document.querySelector(".voice-controls");
  if (!controls || !navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) return;

  const button = controls.querySelector("[data-record-button]");
  const status = controls.querySelector("[data-record-status]");
  const form = document.querySelector(".attachment-upload");
  const csrf = form?.querySelector('[name="csrf_token"]')?.value;
  if (!button || !status || !form || !csrf) return;

  controls.hidden = false;
  let recorder;
  let stream;
  let chunks = [];

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
      recorder.stop();
      button.disabled = true;
      return;
    }
    button.disabled = true;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      recorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
      chunks = [];
      recorder.addEventListener("dataavailable", (event) => {
        if (event.data.size) chunks.push(event.data);
      });
      recorder.addEventListener("stop", async () => {
        stream.getTracks().forEach((track) => track.stop());
        const type = recorder.mimeType || mimeType || "audio/webm";
        const blob = new Blob(chunks, { type });
        const data = new FormData();
        data.append("file", blob, voiceFilename(type));
        status.textContent = controls.dataset.uploading;
        let serverText = "";
        try {
          const response = await fetch(form.action, {
            method: "POST",
            headers: { "X-CSRF-Token": csrf },
            body: data,
          });
          if (!response.ok) {
            if ((response.headers.get("Content-Type") || "").startsWith("text/plain")) {
              serverText = (await response.text()).trim();
            }
            throw new Error("upload");
          }
          window.location.reload();
        } catch {
          status.textContent = serverText || controls.dataset.uploadError;
          resetButton();
        }
      }, { once: true });
      recorder.start();
      status.textContent = controls.dataset.recording;
      button.textContent = controls.dataset.recordStop;
      button.disabled = false;
    } catch {
      stream?.getTracks().forEach((track) => track.stop());
      status.textContent = controls.dataset.recordError;
      resetButton();
    }
  });
})();
