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

  button.addEventListener("click", async () => {
    if (recorder?.state === "recording") {
      recorder.stop();
      button.disabled = true;
      return;
    }
    button.disabled = true;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      recorder = new MediaRecorder(stream);
      chunks = [];
      recorder.addEventListener("dataavailable", (event) => {
        if (event.data.size) chunks.push(event.data);
      });
      recorder.addEventListener("stop", async () => {
        stream.getTracks().forEach((track) => track.stop());
        const blob = new Blob(chunks, { type: recorder.mimeType || "audio/webm" });
        const data = new FormData();
        data.append("file", blob, "voice.webm");
        try {
          const response = await fetch(form.action, {
            method: "POST",
            headers: { "X-CSRF-Token": csrf },
            body: data,
          });
          if (!response.ok) throw new Error("upload");
          window.location.reload();
        } catch {
          status.textContent = controls.dataset.uploadError;
          button.disabled = false;
          button.textContent = controls.dataset.recordStart;
        }
      }, { once: true });
      recorder.start();
      status.textContent = controls.dataset.recording;
      button.textContent = controls.dataset.recordStop;
      button.disabled = false;
    } catch {
      status.textContent = controls.dataset.recordError;
      button.disabled = false;
    }
  });
})();
