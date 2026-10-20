(() => {
  const controls = document.querySelector(".voice-controls");
  if (!controls || !navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) return;

  const button = controls.querySelector("[data-record-button]");
  const status = controls.querySelector("[data-record-status]");
  const form = controls.closest("form");
  const fileInput = controls.querySelector("[data-record-file]");
  const preview = controls.querySelector("[data-record-preview]");
  const player = controls.querySelector("[data-record-player]");
  const name = controls.querySelector("[data-record-name]");
  const timer = controls.querySelector("[data-record-timer]");
  const level = controls.querySelector("[data-record-level]");
  const AudioContext = window.AudioContext || window.webkitAudioContext;
  if (!button || !status || !form || !fileInput || !preview || !player || !name || !timer || !level) return;
  if (!AudioContext) return;

  controls.hidden = false;
  controls.parentElement.querySelector("[data-record-unavailable]").hidden = true;
  let recorder;
  let stream;
  let chunks = [];
  let previewURL;
  let pending = false;
  let audioContext;
  let source;
  let monitor;
  let updateFeedback;
  function stopMonitor() {
    updateFeedback?.();
    updateFeedback = undefined;
    clearInterval(monitor);
    monitor = undefined;
    source?.disconnect();
    source = undefined;
    if (audioContext) {
      audioContext.close().catch((error) => console.error("Unable to close recording audio context", error));
      audioContext = undefined;
    }
    level.value = 0;
  }
  function release() {
    stopMonitor();
    stream?.getTracks().forEach((track) => track.stop());
    stream = undefined;
  }
  window.addEventListener("pagehide", (event) => {
    release();
    if (!event.persisted && previewURL) URL.revokeObjectURL(previewURL);
  });
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

  name.addEventListener("input", () => {
    const filename = name.value.trim();
    const valid = filename && filename !== "." && filename !== ".." &&
      [...filename].length <= 255 && !/[/\\\x00-\x1f\x7f]/.test(filename);
    name.setCustomValidity(valid ? "" : name.dataset.filenameError);
    const file = fileInput.files?.[0];
    if (!valid || !file) return;
    const data = new DataTransfer();
    data.items.add(new File([file], filename, { type: file.type, lastModified: file.lastModified }));
    fileInput.files = data.files;
    preview.setAttribute("aria-label", filename);
    fileInput.dispatchEvent(new Event("change", { bubbles: true }));
  });

  button.addEventListener("click", async () => {
    if (recorder?.state === "recording") {
      pending = true;
      recorder.stop();
      stopMonitor();
      button.disabled = true;
      return;
    }
    button.disabled = true;
    pending = true;
    try {
      preview.pause();
      audioContext = new AudioContext();
      await audioContext.resume();
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      source = audioContext.createMediaStreamSource(stream);
      const analyser = audioContext.createAnalyser();
      analyser.fftSize = 2048;
      source.connect(analyser);
      const samples = new Float32Array(analyser.fftSize);
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
          name.value = fileInput.files[0].name;
          name.disabled = false;
          name.setCustomValidity("");
          preview.setAttribute("aria-label", name.value);
          player.hidden = false;
          status.textContent = controls.dataset.recordReady;
        } catch {
          status.textContent = controls.dataset.recordError;
        }
        pending = false;
        resetButton();
      }, { once: true });
      recorder.start();
      const startedAt = performance.now();
      timer.textContent = "00:00";
      updateFeedback = () => {
        const elapsed = Math.floor((performance.now() - startedAt) / 1000);
        timer.textContent = `${String(Math.floor(elapsed / 60)).padStart(2, "0")}:${String(elapsed % 60).padStart(2, "0")}`;
        analyser.getFloatTimeDomainData(samples);
        const rms = Math.sqrt(samples.reduce((sum, sample) => sum + sample * sample, 0) / samples.length);
        // Map -60 dB to 0 dB onto the meter so quiet speech remains visible.
        level.value = rms > 0 ? Math.max(0, Math.min(1, (20 * Math.log10(rms) + 60) / 60)) : 0;
      };
      monitor = setInterval(updateFeedback, 100);
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
