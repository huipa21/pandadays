const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");
const { File } = require("node:buffer");

const script = readFileSync(join(__dirname, "..", "web", "static", "attachments.js"), "utf8");

function fixture({ supported = true, denied = false } = {}) {
  const form = new EventTarget();
  const button = new EventTarget();
  const input = new EventTarget();
  const status = {};
  const preview = { hidden: true };
  const unavailable = { hidden: false };
  const controls = {
    hidden: true,
    dataset: { recordStart: "Start", recordStop: "Stop", recording: "Recording", recordError: "Error", recordReady: "Ready" },
    closest: () => form,
    parentElement: { querySelector: () => unavailable },
    querySelector: selector => ({
      "[data-record-button]": button, "[data-record-status]": status,
      "[data-record-file]": input, "[data-record-preview]": preview,
    })[selector],
  };
  let recorder;
  let stopped = false;
  class Recorder extends EventTarget {
    static isTypeSupported(type) { return type === "audio/webm"; }
    constructor() { super(); recorder = this; this.mimeType = "audio/webm"; }
    start() { this.state = "recording"; }
    stop() { this.state = "inactive"; }
    finish() {
      const data = new Event("dataavailable");
      data.data = new Blob(["voice"], { type: this.mimeType });
      this.dispatchEvent(data);
      this.dispatchEvent(new Event("stop"));
    }
  }
  const window = new EventTarget();
  window.MediaRecorder = supported ? Recorder : undefined;
  runInNewContext(script, {
    window, document: { querySelector: () => controls },
    navigator: { mediaDevices: supported ? { getUserMedia: async () => {
      if (denied) throw new Error("denied");
      return { getTracks: () => [{ stop() { stopped = true; } }] };
    } } : undefined },
    MediaRecorder: Recorder, Blob, File, Event, Date,
    DataTransfer: class {
      constructor() {
        this.files = [];
        this.items = { add: file => this.files.push(file) };
      }
    },
    URL: { createObjectURL: () => "blob:recording", revokeObjectURL() {} },
  });
  return { form, button, input, status, preview, controls, unavailable, window,
    get recorder() { return recorder; }, get stopped() { return stopped; },
    async click() { button.dispatchEvent(new Event("click")); await new Promise(resolve => setImmediate(resolve)); },
  };
}

test("recordings are queued and previewed, not immediately uploaded", async () => {
  const f = fixture();
  let changed = false;
  f.input.addEventListener("change", () => { changed = true; });
  await f.click();
  assert.equal(f.status.textContent, "Recording");
  const during = new Event("submit", { cancelable: true });
  f.form.dispatchEvent(during);
  assert.equal(during.defaultPrevented, true);
  await f.click();
  const stopping = new Event("submit", { cancelable: true });
  f.form.dispatchEvent(stopping);
  assert.equal(stopping.defaultPrevented, true);
  f.recorder.finish();
  assert.equal(f.stopped, true);
  assert.equal(changed, true);
  assert.equal(f.input.files.length, 1);
  assert.equal(await f.input.files[0].text(), "voice");
  assert.match(f.input.files[0].name, /^voice-.*\.webm$/);
  assert.equal(f.preview.hidden, false);
  assert.equal(f.status.textContent, "Ready");
  const ready = new Event("submit", { cancelable: true });
  f.form.dispatchEvent(ready);
  assert.equal(ready.defaultPrevented, false);
});

test("unsupported recording retains an explicit availability message", () => {
  const f = fixture({ supported: false });
  assert.equal(f.controls.hidden, true);
  assert.equal(f.unavailable.hidden, false);
});

test("microphone denial reports an error and allows retry", async () => {
  const f = fixture({ denied: true });
  await f.click();
  assert.equal(f.status.textContent, "Error");
  assert.equal(f.button.disabled, false);
});

test("page exit releases the microphone", async () => {
  const f = fixture();
  await f.click();
  f.window.dispatchEvent(new Event("pagehide"));
  assert.equal(f.stopped, true);
});

test("recorder errors do not queue a success-shaped recording", async () => {
  const f = fixture();
  await f.click();
  f.recorder.dispatchEvent(new Event("error"));
  f.recorder.finish();
  assert.equal(f.status.textContent, "Error");
  assert.equal(f.input.files, undefined);
  assert.equal(f.preview.hidden, true);
  assert.equal(f.stopped, true);
});
