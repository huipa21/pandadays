const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "media-picker.js"), "utf8");

function fixture({ creation = false, selected = false } = {}) {
  const picker = new EventTarget();
  const panel = new EventTarget();
  const submit = { disabled: false };
  const library = { checked: selected };
  const upload = { files: [] };
  const recording = { files: [] };
  panel.querySelector = () => library.checked ? library : null;
  panel.querySelectorAll = () => [upload, recording];
  picker.querySelector = (selector) => selector === "[data-media-panel]" ? panel : creation ? null : submit;
  // Selection validation also works in browsers without the dialog API.
  runInNewContext(script, { document: { querySelector: () => picker, createElement: () => ({}) } });
  return {
    submit, library, upload, recording,
    change() { panel.dispatchEvent(new Event("change")); },
    send() {
      const event = new Event("submit", { cancelable: true });
      picker.dispatchEvent(event);
      return !event.defaultPrevented;
    },
  };
}

test("existing-day Upload is disabled and empty submissions are blocked", () => {
  const f = fixture();
  assert.equal(f.submit.disabled, true);
  assert.equal(f.send(), false);
});

for (const source of ["library", "upload", "recording"]) {
  test(`${source} selection enables Upload; clearing it disables Upload again`, () => {
    const f = fixture();
    if (source === "library") f.library.checked = true;
    else f[source].files = [{ name: "media" }];
    f.change();
    assert.equal(f.submit.disabled, false);
    assert.equal(f.send(), true);
    if (source === "library") f.library.checked = false;
    else f[source].files = [];
    f.change();
    assert.equal(f.submit.disabled, true);
    assert.equal(f.send(), false);
  });
}

test("clearing one source keeps Upload enabled while another is selected", () => {
  const f = fixture({ selected: true });
  assert.equal(f.submit.disabled, false);
  f.upload.files = [{}];
  f.library.checked = false;
  f.change();
  assert.equal(f.submit.disabled, false);
});

test("new-day creation still allows saving without attachments", () => {
  assert.equal(fixture({ creation: true }).send(), true);
});
