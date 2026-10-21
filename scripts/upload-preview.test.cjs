const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "upload-preview.js"), "utf8");

function fixture() {
  class Element extends EventTarget {
    constructor(tag) { super(); this.tag = tag; this.children = []; }
    append(child) { this.children.push(child); }
    replaceChildren() { this.children = []; }
  }
  const input = new Element("input");
  const list = new Element("ul");
  const form = new EventTarget();
  input.files = [];
  input.form = form;
  const container = { dataset: {}, querySelector: selector => selector === "[data-media-files]" ? input : list };
  const add = new EventTarget();
  let focused = false;
  const details = { open: false, querySelector: () => ({ focus() { focused = true; } }) };
  const document = {
    querySelectorAll: () => [container],
    querySelector: () => add,
    getElementById: () => details,
    createElement: tag => new Element(tag),
  };
  const window = new EventTarget();
  let nextURL = 0;
  const revoked = [];
  const context = {
    document, window,
    URL: { createObjectURL: () => `blob:preview-${++nextURL}`, revokeObjectURL: url => revoked.push(url) },
  };
  runInNewContext(script, context);
  return {
    input, list, form, window, revoked, add, details,
    get focused() { return focused; },
    change(files) { input.files = files; input.dispatchEvent(new Event("change")); },
    initAgain() { runInNewContext(script, context); },
  };
}

test("selected files show full safe filenames and image thumbnails", () => {
  const f = fixture();
  f.change([{ name: "<img onerror=bad>.png", type: "image/png" }, { name: "notes.txt", type: "text/plain" }]);
  assert.equal(f.list.children.length, 2);
  const [imageRow, textRow] = f.list.children;
  assert.equal(imageRow.children[0].tag, "img");
  assert.equal(imageRow.children[0].src, "blob:preview-1");
  assert.equal(imageRow.children[1].textContent, "<img onerror=bad>.png");
  assert.equal(textRow.children.length, 1);
  assert.equal(textRow.children[0].textContent, "notes.txt");
  imageRow.children[0].dispatchEvent(new Event("error"));
  assert.equal(imageRow.children[0].hidden, true);
  assert.equal(imageRow.children[1].textContent, "<img onerror=bad>.png");
});

test("reselection, clearing and reset release thumbnails and remove stale filenames", () => {
  const f = fixture();
  f.change([{ name: "one.png", type: "image/png" }]);
  f.change([{ name: "two.jpg", type: "image/jpeg" }]);
  assert.deepEqual(f.revoked, ["blob:preview-1"]);
  assert.equal(f.list.children.length, 1);
  assert.equal(f.list.children[0].children[1].textContent, "two.jpg");
  f.change([]);
  assert.equal(f.list.children.length, 0);
  assert.deepEqual(f.revoked, ["blob:preview-1", "blob:preview-2"]);
  f.change([{ name: "three.png", type: "image/png" }]);
  f.form.dispatchEvent(new Event("reset"));
  assert.equal(f.list.children.length, 0);
  assert.deepEqual(f.revoked, ["blob:preview-1", "blob:preview-2", "blob:preview-3"]);
});

test("initialization is idempotent and page exit releases previews except in the back-forward cache", () => {
  const f = fixture();
  f.initAgain();
  f.change([{ name: "one.png", type: "image/png" }]);
  const cached = new Event("pagehide");
  cached.persisted = true;
  f.window.dispatchEvent(cached);
  assert.deepEqual(f.revoked, []);
  f.window.dispatchEvent(new Event("pagehide"));
  assert.deepEqual(f.revoked, ["blob:preview-1"]);
});

test("library plus button opens upload form and focuses file selection", () => {
  const f = fixture();
  f.add.dispatchEvent(new Event("click"));
  assert.equal(f.details.open, true);
  assert.equal(f.focused, true);
});
