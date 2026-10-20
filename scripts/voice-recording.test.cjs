const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");
const { File } = require("node:buffer");

const script = readFileSync(join(__dirname, "..", "web", "static", "attachments.js"), "utf8");

function fixture({ supported = true, denied = false, meterSupported = true, contextFailure = false } = {}) {
  const form = new EventTarget();
  const button = new EventTarget();
  const input = new EventTarget();
  const status = {};
  const preview = { hidden: true, pause() {}, setAttribute(key, value) { this[key] = value; } };
  const player = { hidden: true };
  const name = new EventTarget();
  name.dataset = { filenameError: "Invalid filename" };
  name.setCustomValidity = message => { name.validationMessage = message; };
  const timer = { textContent: "00:00" };
  const level = { value: 0 };
  const unavailable = { hidden: false };
  const controls = {
    hidden: true,
    dataset: { recordStart: "Start", recordStop: "Stop", recording: "Recording", recordError: "Error", recordReady: "Ready" },
    closest: () => form,
    parentElement: { querySelector: () => unavailable },
    querySelector: selector => ({
      "[data-record-button]": button, "[data-record-status]": status,
      "[data-record-file]": input, "[data-record-preview]": preview,
      "[data-record-player]": player, "[data-record-name]": name,
      "[data-record-timer]": timer, "[data-record-level]": level,
    })[selector],
  };
  let recorder;
  let stopped = false;
  let closed = false;
  let disconnected = false;
  let now = 0;
  let amplitude = 0;
  let tick;
  const revoked = [];
  let nextURL = 0;
  class AudioContext {
    async resume() { if (contextFailure) throw new Error("audio context unavailable"); }
    createMediaStreamSource() {
      return { connect() {}, disconnect() { disconnected = true; } };
    }
    createAnalyser() {
      return { getFloatTimeDomainData(samples) { samples.fill(amplitude); } };
    }
    async close() { closed = true; }
  }
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
  window.AudioContext = meterSupported ? AudioContext : undefined;
  runInNewContext(script, {
    window, document: { querySelector: () => controls },
    navigator: { mediaDevices: supported ? { getUserMedia: async () => {
      if (denied) throw new Error("denied");
      return { getTracks: () => [{ stop() { stopped = true; } }] };
    } } : undefined },
    MediaRecorder: Recorder, Blob, File, Event, Date,
    performance: { now: () => now },
    setInterval: callback => { tick = callback; return 1; },
    clearInterval: () => { tick = undefined; },
    DataTransfer: class {
      constructor() {
        this.files = [];
        this.items = { add: file => this.files.push(file) };
      }
    },
    URL: { createObjectURL: () => `blob:recording-${++nextURL}`, revokeObjectURL: url => revoked.push(url) },
  });
  return { form, button, input, status, preview, player, name, timer, level, controls, unavailable, window, revoked,
    get recorder() { return recorder; }, get stopped() { return stopped; },
    get closed() { return closed; }, get disconnected() { return disconnected; },
    advance(ms, sample = amplitude) { now += ms; amplitude = sample; tick?.(); },
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
  assert.equal(f.preview.src, "blob:recording-1");
  assert.equal(f.player.hidden, false);
  assert.equal(f.name.value, f.input.files[0].name);
  assert.equal(f.name.disabled, false);
  assert.equal(f.status.textContent, "Ready");
  const ready = new Event("submit", { cancelable: true });
  f.form.dispatchEvent(ready);
  assert.equal(ready.defaultPrevented, false);
});

test("stopped recordings can be renamed without changing their content or type", async () => {
  const f = fixture();
  await f.click();
  await f.click();
  f.recorder.finish();
  const original = f.input.files[0];
  let changes = 0;
  f.input.addEventListener("change", () => changes++);
  f.name.value = "  My voice note.webm  ";
  f.name.dispatchEvent(new Event("input"));
  const renamed = f.input.files[0];
  assert.equal(renamed.name, "My voice note.webm");
  assert.equal(await renamed.text(), await original.text());
  assert.equal(renamed.type, original.type);
  assert.equal(renamed.lastModified, original.lastModified);
  assert.equal(f.preview["aria-label"], renamed.name);
  assert.equal(changes, 1);
  assert.equal(f.name.validationMessage, "");
  for (const invalid of ["", " ", ".", "..", "../voice.webm", "x\\voice.webm", "x\u0000", "x".repeat(256)]) {
    f.name.value = invalid;
    f.name.dispatchEvent(new Event("input"));
    assert.equal(f.name.validationMessage, "Invalid filename");
    assert.equal(f.input.files[0], renamed);
  }
  f.name.value = "new.webm";
  f.name.dispatchEvent(new Event("input"));
  assert.equal(f.name.validationMessage, "");
  assert.equal(f.input.files[0].name, "new.webm");
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
  assert.equal(f.closed, true);
  assert.equal(f.disconnected, true);
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

test("timer and microphone level follow elapsed time and signal strength, then freeze on stop", async () => {
  const f = fixture();
  await f.click();
  f.advance(1500, 0);
  assert.equal(f.timer.textContent, "00:01");
  assert.equal(f.level.value, 0);
  f.advance(60000, 0.01);
  assert.equal(f.timer.textContent, "01:01");
  const quiet = f.level.value;
  assert.ok(quiet > 0 && quiet < 1);
  f.advance(500, 0.5);
  assert.ok(f.level.value > quiet);
  assert.equal(f.timer.textContent, "01:02");
  await f.click();
  assert.equal(f.level.value, 0);
  assert.equal(f.closed, true);
  assert.equal(f.disconnected, true);
  f.advance(5000, 1);
  assert.equal(f.timer.textContent, "01:02");
  assert.equal(f.level.value, 0);
  f.recorder.finish();
  assert.equal(f.preview.hidden, false);
});

test("re-recording resets the timer and replaces and releases preview URLs", async () => {
  const f = fixture();
  await f.click();
  f.advance(4000, 0.1);
  await f.click();
  f.recorder.finish();
  await f.click();
  assert.equal(f.timer.textContent, "00:00");
  await f.click();
  f.recorder.finish();
  assert.equal(f.input.files.length, 1);
  assert.equal(f.preview.src, "blob:recording-2");
  assert.deepEqual(f.revoked, ["blob:recording-1"]);
  f.window.dispatchEvent(new Event("pagehide"));
  assert.deepEqual(f.revoked, ["blob:recording-1", "blob:recording-2"]);
});

test("missing audio metering retains the explicit availability message", () => {
  const f = fixture({ meterSupported: false });
  assert.equal(f.controls.hidden, true);
  assert.equal(f.unavailable.hidden, false);
});

test("back-forward caching preserves the local recording preview", async () => {
  const f = fixture();
  await f.click();
  await f.click();
  f.recorder.finish();
  const event = new Event("pagehide");
  event.persisted = true;
  f.window.dispatchEvent(event);
  assert.deepEqual(f.revoked, []);
  assert.equal(f.preview.src, "blob:recording-1");
});

test("audio context failures report an error and release resources", async () => {
  const f = fixture({ contextFailure: true });
  await f.click();
  assert.equal(f.status.textContent, "Error");
  assert.equal(f.button.disabled, false);
  assert.equal(f.closed, true);
});
