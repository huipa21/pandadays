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
  document.querySelector = () => hasForm ? form : null;
  document.querySelectorAll = () => [];
  runInNewContext(script, {
    window, document,
    FormData: class {
      entries() { return form.fields[Symbol.iterator](); }
    },
  });
  return {
    form, window,
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

test("editing after a submission or returning from page history restores protection", () => {
  for (const type of ["input", "change", "pageshow"]) {
    const f = fixture({ unsaved: true });
    f.form.dispatchEvent(new Event("submit"));
    (type === "pageshow" ? f.window : f.form).dispatchEvent(new Event(type));
    assert.equal(f.warns(), true, type);
  }
});
