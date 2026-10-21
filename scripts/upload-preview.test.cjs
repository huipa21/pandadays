const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "upload-preview.js"), "utf8");

function fixture({ multiple = true, wrapped = true } = {}) {
  class Element extends EventTarget {
    constructor(tag) {
      super();
      this.tag = tag;
      this.children = [];
      this.dataset = {};
      this.attributes = {};
      this.classes = new Set();
      this._textContent = "";
      this.classList = { add: name => this.classes.add(name), remove: name => this.classes.delete(name) };
    }
    append(child) {
      if (child.parentElement) {
        child.parentElement.children = child.parentElement.children.filter(item => item !== child);
      }
      this.children.push(child);
      child.parentElement = this;
    }
    before(child) {
      this.parentElement.children.splice(this.parentElement.children.indexOf(this), 0, child);
      child.parentElement = this.parentElement;
    }
    replaceChildren(...children) { this.children = children; }
    setAttribute(name, value) { this.attributes[name] = value; }
    set textContent(value) { this._textContent = value; }
    get textContent() { return this._textContent || this.children.map(child => child.textContent).join(""); }
    focus() { this.focused = true; }
    get lastElementChild() { return this.children.at(-1); }
    querySelector(selector) {
      if (selector === "button") return this.children.find(item => item.tag === "button");
      if (selector === "[data-upload-dropzone]") return this.children.find(item => item.tag === "zone");
      if (selector === ".hint") return this.children.find(item => item.tag === "hint");
      if (selector === "[data-upload-list]") return this.children.find(item => item.tag === "ul");
    }
    closest(selector) {
      if (selector === "label") return this.tag === "label" ? this : this.parentElement?.closest(selector);
      if (selector === "[data-upload-files]") return this === container && wrapped ? this : this.parentElement?.closest(selector);
      return null;
    }
  }
  const container = new Element("div");
  const label = new Element("label");
  const input = new Element("input");
  const list = new Element("ul");
  const form = new EventTarget();
  container.append(label);
  label.append(input);
  if (wrapped) container.append(list);
  input.files = [];
  input.multiple = multiple;
  input.form = form;
  const template = {
    dataset: { removeLabel: "Remove %s", selectedLabel: "Selected files", singleHint: "Select one file." },
    content: {
      cloneNode() {
        const zone = new Element("zone");
        zone.append(new Element("hint"));
        return zone;
      },
    },
  };
  const document = new EventTarget();
  const mediaIcon = new Element("span");
  mediaIcon.className = "media-thumb-icon";
  mediaIcon.dataset.filename = "budget.xlsx";
  mediaIcon.dataset.mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";
  const root = new Element("root");
  root.append(container);
  root.append(mediaIcon);
  document.querySelectorAll = selector => selector.startsWith("input[type") ? [input] : [mediaIcon];
  document.querySelector = () => null;
  document.getElementById = id => id === "upload-dropzone-template" ? template : null;
  document.createElement = tag => new Element(tag);
  document.createElementNS = (_, tag) => new Element(tag);
  const window = new EventTarget();
  let nextURL = 0;
  const revoked = [];
  const context = {
    document, window, Event,
    DataTransfer: class {
      constructor() { this.files = []; this.items = { add: file => this.files.push(file) }; }
    },
    URL: { createObjectURL: () => `blob:preview-${++nextURL}`, revokeObjectURL: url => revoked.push(url) },
  };
  runInNewContext(script, context);
  return {
    input, label, form, window, revoked, mediaIcon,
    get list() { return input.closest("[data-upload-files]")?.querySelector("[data-upload-list]") || input.parentElement.parentElement.querySelector("[data-upload-list]"); },
    change(files) { input.files = files; input.dispatchEvent(new Event("change")); },
    remove(index) { this.list.children[index].querySelector("button").dispatchEvent(new Event("click")); },
    drop(files) {
      const event = new Event("drop", { cancelable: true });
      event.dataTransfer = { files };
      label.querySelector("[data-upload-dropzone]").dispatchEvent(event);
      assert.equal(event.defaultPrevented, true);
    },
    initAgain() { runInNewContext(script, context); },
    load() {
      const event = new Event("htmx:load");
      event.detail = { elt: document };
      document.dispatchEvent(event);
    },
  };
}

const file = (name, type = "text/plain") => ({ name, type });

test("selected files show safe filenames, thumbnails and accessible SVG remove buttons", () => {
  const f = fixture();
  f.change([file("<img onerror=bad>.png", "image/png"), file("notes.txt")]);
  const [imageRow, textRow] = f.list.children;
  assert.equal(imageRow.children[0].tag, "img");
  assert.equal(imageRow.children[0].src, "blob:preview-1");
  assert.equal(imageRow.children[1].textContent, "<img onerror=bad>.png");
  assert.equal(textRow.children[0].tag, "svg");
  assert.equal(textRow.children[1].textContent, "notes.txt");
  assert.equal(textRow.children[2].attributes["aria-label"], "Remove notes.txt");
  assert.equal(textRow.children[2].children[0].tag, "svg");
  imageRow.children[0].dispatchEvent(new Event("error"));
  assert.equal(imageRow.children[0].hidden, true);
});

test("file type cards use format labels and distinct pictograms; library thumbnails match", () => {
  const f = fixture();
  f.change([
    file("report.pdf", "application/pdf"),
    file("budget.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"),
    file("clip.mp4", "video/mp4"),
    file("archive.zip", "application/zip"),
    file("unknown.customformat", "application/octet-stream"),
  ]);
  const icons = f.list.children.map(row => row.children[0]);
  assert.deepEqual(icons.map(icon => icon.attributes["aria-label"]), ["PDF", "XLSX", "MP4", "ZIP", "CUST"]);
  assert.deepEqual(icons.map(icon => icon.dataset.category), ["pdf", "sheet", "video", "archive", "file"]);
  assert.ok(icons.every(icon => icon.tag === "svg" && icon.children.some(child => child.tag === "path")));
  assert.equal(f.mediaIcon.children[0].tag, "svg");
  assert.equal(f.mediaIcon.children[0].attributes["aria-label"], "XLSX");
  assert.equal(f.mediaIcon.children[0].dataset.category, "sheet");
  f.load();
  assert.equal(f.mediaIcon.children.length, 1, "media icon enhancement is idempotent");
});

test("repeated selections append files and chooser cancellation preserves the queue", () => {
  const f = fixture();
  const first = file("folder-one.png", "image/png");
  const second = file("folder-two.jpg", "image/jpeg");
  f.change([first]);
  f.change([second, file("third.txt")]);
  assert.deepEqual(Array.from(f.input.files, entry => entry.name), [first.name, second.name, "third.txt"]);
  f.change([]);
  // A native cancelled chooser does not replace files; an empty change is also harmless.
  assert.equal(f.list.children.length, 3);
  assert.equal(f.input.files.length, 3);
  f.drop([file("fourth.txt")]);
  assert.equal(f.input.files.length, 4);
});

test("remove labels preserve filenames containing replacement-pattern characters", () => {
  const f = fixture();
  f.change([file("$&-$`-$'.txt")]);
  assert.equal(f.list.children[0].querySelector("button").attributes["aria-label"], "Remove $&-$`-$'.txt");
});

test("remove updates submitted files, notifies dependents, focuses a control, and allows re-adding", () => {
  const f = fixture();
  const image = file("one.png", "image/png");
  f.change([image, file("two.txt")]);
  let changes = 0;
  f.input.addEventListener("change", () => changes++);
  f.remove(0);
  assert.deepEqual(Array.from(f.input.files, entry => entry.name), ["two.txt"]);
  assert.deepEqual(f.revoked, ["blob:preview-1"]);
  assert.equal(f.list.children[0].querySelector("button").focused, true);
  assert.equal(changes, 1);
  f.remove(0);
  assert.equal(f.input.files.length, 0);
  assert.equal(f.input.focused, true);
  f.change([image]);
  assert.equal(f.input.files.length, 1);
});

test("drop appends, prevents native navigation and notifies form observers once", () => {
  const f = fixture();
  f.change([file("one.txt")]);
  let changes = 0;
  f.input.addEventListener("change", () => changes++);
  f.drop([file("two.txt"), file("three.txt")]);
  assert.deepEqual(Array.from(f.input.files, entry => entry.name), ["one.txt", "two.txt", "three.txt"]);
  assert.equal(changes, 1);
});

test("single-file import, restore and avatar inputs replace instead of appending", () => {
  const f = fixture({ multiple: false, wrapped: false });
  f.change([file("one.csv")]);
  f.change([file("two.csv")]);
  assert.deepEqual(Array.from(f.input.files, entry => entry.name), ["two.csv"]);
  f.drop([file("three.csv"), file("four.csv")]);
  assert.deepEqual(Array.from(f.input.files, entry => entry.name), ["three.csv"]);
  assert.equal(f.label.querySelector("[data-upload-dropzone]").querySelector(".hint").textContent, "Select one file.");
});

test("reset clears the queue and previews without retaining previously selected files", () => {
  const f = fixture();
  f.change([file("one.png", "image/png")]);
  f.form.dispatchEvent(new Event("reset"));
  assert.equal(f.list.children.length, 0);
  assert.equal(f.input.value, "");
  assert.deepEqual(f.revoked, ["blob:preview-1"]);
  f.change([file("two.txt")]);
  assert.equal(f.input.files.length, 1);
});

test("initialization and htmx loads are idempotent and page exit releases previews except in bfcache", () => {
  const f = fixture();
  f.initAgain();
  f.load();
  f.change([file("one.png", "image/png")]);
  assert.equal(f.input.files.length, 1);
  assert.equal(f.label.children.filter(item => item.tag === "zone").length, 1);
  const cached = new Event("pagehide");
  cached.persisted = true;
  f.window.dispatchEvent(cached);
  assert.deepEqual(f.revoked, []);
  f.window.dispatchEvent(new Event("pagehide"));
  assert.deepEqual(f.revoked, ["blob:preview-1"]);
});
