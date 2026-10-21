const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "app.js"), "utf8");

function fixture({ unsaved = false, hasForm = true } = {}) {
  const form = new EventTarget();
  form.dataset = { unsaved: String(unsaved) };
  form.fields = [
    ["csrf_token", "token"],
    ["title", ""],
    ["date", "2026-10-19"],
    ["time", ""],
    ["repeat", "none"],
    ["collection", "1"],
    ["tags", "1"],
    ["tag_query", ""],
    ["comment", ""],
    ["color", "#123456"],
    ["no_color", "1"],
    ["icon", ""],
  ];
  const window = new EventTarget();
  const document = new EventTarget();
  document.getElementById = () => null;
  document.querySelector = selector => selector === "[data-day-form]" && hasForm ? form : null;
  document.querySelectorAll = () => [];
  runInNewContext(script, {
    window, document,
    FormData: class {
      entries() { return form.fields[Symbol.iterator](); }
    },
  });
  return {
    form, window, document,
    set(name, value) {
      form.fields.find(entry => entry[0] === name)[1] = value;
      form.dispatchEvent(new Event("input"));
    },
    warns() {
      const event = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(event);
      return event.defaultPrevented;
    },
  };
}

test("unchanged forms and pages without a day form do not warn", () => {
  assert.equal(fixture().warns(), false);
  assert.equal(fixture({ hasForm: false }).warns(), false);
});

test("every editable day field is protected, and reverting changes clears the warning", () => {
  for (const name of ["title", "date", "time", "repeat", "collection", "tags", "tag_query", "comment", "color", "no_color", "icon"]) {
    const f = fixture();
    const original = f.form.fields.find(entry => entry[0] === name)[1];
    f.set(name, "changed");
    assert.equal(f.warns(), true, name);
    assert.equal(f.warns(), true, "canceling exit must keep protecting " + name);
    f.set(name, original);
    assert.equal(f.warns(), false, name);
  }
});

test("checkbox removal, multiple tags, and changes without input events are protected", () => {
  const f = fixture();
  f.form.fields = f.form.fields.filter(entry => entry[0] !== "tags");
  assert.equal(f.warns(), true);
  f.form.fields.push(["tags", "1"], ["tags", "2"]);
  assert.equal(f.warns(), true);
});

test("CSRF token changes are not unsaved user data", () => {
  const f = fixture();
  f.set("csrf_token", "new-token");
  assert.equal(f.warns(), false);
});

test("unselected upload and recording inputs do not trigger exit warnings", () => {
  const f = fixture();
  f.form.fields.push(["file", { name: "", size: 0, lastModified: 1, type: "application/octet-stream" }]);
  assert.equal(f.warns(), false);
  f.form.fields[f.form.fields.length - 1][1].lastModified = 2;
  assert.equal(f.warns(), false);
});

test("server validation errors remain protected without further editing", () => {
  assert.equal(fixture({ unsaved: true }).warns(), true);
});

test("saving bypasses the warning even when the server response is delayed", async () => {
  const f = fixture({ unsaved: true });
  f.form.dispatchEvent(new Event("submit", { cancelable: true }));
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(f.warns(), false);
});

test("canceled submissions do not bypass protection", () => {
  const f = fixture({ unsaved: true });
  const event = new Event("submit", { cancelable: true });
  f.form.dispatchEvent(event);
  event.preventDefault();
  assert.equal(f.warns(), true);
});

test("successful upload responses can navigate but failed uploads remain protected", () => {
  const f = fixture({ unsaved: true });
  const event = new Event("submit", { cancelable: true });
  f.form.dispatchEvent(event);
  event.preventDefault();
  f.form.dispatchEvent(new Event("upload:error"));
  assert.equal(f.warns(), true);
  f.form.dispatchEvent(new Event("upload:navigate"));
  assert.equal(f.warns(), false);
  f.window.dispatchEvent(new Event("pageshow"));
  assert.equal(f.warns(), true);
});

test("editing after a submission or returning from page history restores protection", () => {
  for (const type of ["input", "change", "pageshow"]) {
    const f = fixture({ unsaved: true });
    f.form.dispatchEvent(new Event("submit"));
    (type === "pageshow" ? f.window : f.form).dispatchEvent(new Event(type));
    assert.equal(f.warns(), true, type);
  }
});

test("day menu links open modal dialogs instead of inline sections and close the menu", () => {
  for (const id of ["attachments", "day-sharing"]) {
    const f = fixture({ hasForm: false });
    const dialog = { open: false, showModal() { this.open = true; } };
    const section = { tagName: "SECTION", closest: selector => selector === "dialog" ? dialog : null };
    const menu = { open: true };
    f.document.getElementById = target => target === id ? section : null;
    const link = { dataset: { expandSection: id }, closest: selector => selector === ".dropdown" ? menu : null };
    const event = new Event("click", { cancelable: true });
    Object.defineProperty(event, "target", {
      value: { closest: selector => selector === "[data-expand-section]" ? link : null },
    });
    f.document.dispatchEvent(event);
    assert.equal(dialog.open, true);
    assert.equal(menu.open, false);
    assert.equal(event.defaultPrevented, true);
  }
});

test("selected files are included in unsaved-change protection", () => {
  const f = fixture();
  f.form.fields.push(["file", { name: "photo.png", size: 123, lastModified: 42, type: "image/png" }]);
  assert.equal(f.warns(), true);
  f.form.fields.pop();
  assert.equal(f.warns(), false);
});

test("Escape closes action dialogs, including group management", () => {
  const f = fixture({ hasForm: false });
  const dialog = { open: true, close() { this.open = false; } };
  f.document.querySelector = selector => selector === "dialog[open]" ? dialog : null;
  const event = new Event("keydown", { cancelable: true });
  Object.defineProperty(event, "key", { value: "Escape" });
  f.document.dispatchEvent(event);
  assert.equal(dialog.open, false);
  assert.equal(event.defaultPrevented, true);
});

test("day deletion requires confirmation and cancellation prevents submission", () => {
  const f = fixture({ hasForm: false });
  for (const accepted of [false, true]) {
    let prompted = 0;
    f.window.confirm = message => { prompted++; assert.equal(message, "Delete this day?"); return accepted; };
    const event = new Event("submit", { cancelable: true });
    Object.defineProperty(event, "target", { value: { dataset: { confirm: "Delete this day?" } } });
    f.document.dispatchEvent(event);
    assert.equal(prompted, 1);
    assert.equal(event.defaultPrevented, !accepted);
  }
});
