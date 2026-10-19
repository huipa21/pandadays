const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "app.js"), "utf8");

class DOMEvent {
  constructor(type, options = {}) { Object.assign(this, { type }, options); }
  preventDefault() { this.defaultPrevented = true; }
}

// Only the DOM surface used by the pickers; no browser or npm dependencies.
class Element {
  constructor(tag, attributes = {}, children = []) {
    this.tag = tag;
    this.attributes = {};
    this.dataset = {};
    this.children = [];
    this.listeners = {};
    this.className = "";
    this.value = "";
    this.checked = false;
    this.hidden = false;
    this.classList = {
      add: name => this.classList.toggle(name, true),
      toggle: (name, enabled) => {
        const names = new Set(this.className.split(" ").filter(Boolean));
        if (enabled) names.add(name); else names.delete(name);
        this.className = [...names].join(" ");
      },
      contains: name => this.className.split(" ").includes(name),
    };
    for (const [name, value] of Object.entries(attributes)) this.setAttribute(name, value);
    this.append(...children);
  }
  setAttribute(name, value) {
    this.attributes[name] = String(value);
    if (name.startsWith("data-")) {
      this.dataset[name.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = String(value);
    }
    if (["name", "type", "value", "id"].includes(name)) this[name] = String(value);
  }
  getAttribute(name) { return this.attributes[name] ?? null; }
  removeAttribute(name) { delete this.attributes[name]; }
  set textContent(value) { this.children = []; this.text = value; }
  get textContent() { return (this.text || "") + this.children.map(child => child.textContent).join(""); }
  append(...children) {
    for (const child of children) {
      child.parentNode = this;
      this.children.push(child);
    }
  }
  replaceChildren(...children) { this.children = []; this.text = ""; this.append(...children); }
  matches(selector) {
    const match = selector.match(/^(\w+)?(?:\[([^=\]]+)(?:=["']?([^"'\]]+)["']?)?\])?$/);
    return !!match && (!match[1] || match[1] === this.tag) &&
      (!match[2] || (this.getAttribute(match[2]) !== null &&
        (match[3] === undefined || this.getAttribute(match[2]) === match[3])));
  }
  querySelectorAll(selector) {
    return this.children.flatMap(child => [
      ...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector),
    ]);
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  addEventListener(type, listener) { (this.listeners[type] ||= []).push(listener); }
  dispatchEvent(event) {
    event.target ||= this;
    for (const listener of this.listeners[event.type] || []) listener(event);
    if (event.bubbles && this.parentNode) this.parentNode.dispatchEvent(event);
    return !event.defaultPrevented;
  }
  contains(element) { return element === this || this.children.some(child => child.contains(element)); }
  cloneNode(deep) {
    const copy = new Element(this.tag, this.attributes);
    copy.text = this.text;
    if (deep) copy.append(...this.children.map(child => child.cloneNode(true)));
    return copy;
  }
  focus() { this.focused = true; }
  setSelectionRange(start, end) { this.selectionStart = start; this.selectionEnd = end; }
  scrollIntoView() {}
}

function fixture(enableCreate = false) {
  const el = (tag, attrs, children) => new Element(tag, attrs, children);
  const text = value => { const node = el("span"); node.textContent = value; return node; };
  const tagNames = ["Default", "Birthday", "Family trip", "工作", "<script>unsafe</script>"];
  const checkboxes = tagNames.map((name, i) => el("input", { type: "checkbox", name: "tags", value: i + 1 }));
  checkboxes[0].checked = true;
  const fallback = el("div", { "data-tag-fallback": "" }, tagNames.map((name, i) => {
    const label = el("span", { "data-tag-name": "" });
    label.textContent = name;
    return el("label", {}, [checkboxes[i], el("svg"), label]);
  }));
  const input = el("textarea");
  const chips = el("div", { "data-tag-chips": "" });
  const list = el("div", { role: "listbox" });
  const empty = el("p", { "data-tag-empty": "" });
  const editor = el("div", { "data-tag-editor": "" }, [chips, input, list, empty]);
  const tags = el("fieldset", { "data-tag-picker": "", "data-remove-label": "Remove tag" }, [fallback, editor, el("span", { "data-tag-help": "" })]);
  if (enableCreate) {
    tags.setAttribute("data-create-label", "Create tag:");
    tags.append(el("div", { "data-new-tags": "" }));
  }
  const iconInput = el("input");
  const iconOptions = el("div", { "data-icon-options": "" });
  const clear = el("button", { "data-icon-clear": "" });
  const icons = el("div", { "data-icon-picker": "" }, [iconInput, iconOptions, clear]);
  const colorInput = el("input", { type: "color", value: "#123456" });
  const noColor = el("input", { name: "no_color" });
  noColor.checked = true;
  const presets = el("div", { "data-color-presets": "" });
  const custom = el("label", { "data-custom-color": "" }, [el("span", { "data-color-label": "" }), colorInput]);
  const palette = el("div", { "aria-label": "Color" }, [presets, custom]);
  const colors = el("div", { "data-color-picker": "" }, [palette, noColor]);
  const document = el("document", {}, [tags, icons, colors]);
  document.getElementById = () => null;
  document.createElement = tag => el(tag);
  document.createElementNS = (_, tag) => el(tag);
  document.createTextNode = text;
  runInNewContext(script, { document, window: new Element("window"), Event: DOMEvent });
  return {
    document, checkboxes, input, chips, list, empty, fallback, editor,
    iconInput, iconOptions, clear, colorInput, noColor, presets, custom,
    type(value, options = {}) {
      input.value = value;
      input.setSelectionRange(value.length, value.length);
      input.dispatchEvent(new DOMEvent("input", options));
    },
    key(key, options = {}) {
      const event = new DOMEvent("keydown", { key, ...options });
      input.dispatchEvent(event);
      return event;
    },
  };
}

const click = element => element.dispatchEvent(new DOMEvent("click", { bubbles: true }));

test("tags progressively enhance checkboxes and display removable chips without hashes", () => {
  const f = fixture();
  assert.equal(f.fallback.hidden, true);
  assert.equal(f.editor.hidden, false);
  assert.equal(f.chips.children[0].textContent, "Default×");
  assert.equal(f.chips.children[0].getAttribute("aria-label"), "Remove tag Default");
  click(f.chips.children[0]);
  assert.equal(f.checkboxes[0].checked, false);
  assert.equal(f.chips.children.length, 0);
  assert.equal(f.input.focused, true);
});

test("# suggestions filter existing tags, exclude selections, and support keyboard selection", () => {
  const f = fixture();
  f.type("Birth");
  assert.equal(f.list.hidden, true);
  f.type("#");
  assert.equal(f.list.children.length, 4);
  assert.equal(f.input.getAttribute("aria-expanded"), "true");
  assert.equal(f.key("ArrowDown").defaultPrevented, true);
  assert.equal(f.input.getAttribute("aria-activedescendant"), "tag-option-3");
  assert.equal(f.key("Enter").defaultPrevented, true);
  assert.equal(f.checkboxes[2].checked, true);
  assert.equal(f.input.value, "");
  assert.equal(f.list.hidden, true);
  assert.equal(f.chips.children[1].textContent, "Family trip×");
  f.type("#BIR");
  assert.equal(f.list.children.length, 1);
  click(f.list.children[0]);
  assert.equal(f.checkboxes[1].checked, true);
  f.type("#bir");
  assert.equal(f.empty.hidden, false);
});

test("tag search supports multiword, Chinese, literal markup, Escape and IME", () => {
  const f = fixture();
  for (const [query, expected] of [["#Family tr", "Family trip"], ["#工", "工作"], ["#<script>", "<script>unsafe</script>"]]) {
    f.type(query);
    assert.equal(f.list.children.length, 1);
    assert.equal(f.list.children[0].textContent, expected);
    f.key("Escape");
    assert.equal(f.list.hidden, true);
    assert.equal(f.input.getAttribute("aria-activedescendant"), null);
  }
  f.type("#工");
  f.key("Enter", { isComposing: true });
  assert.equal(f.checkboxes[3].checked, false);
  f.key("Enter");
  assert.equal(f.checkboxes[3].checked, true);
});

test("clicking outside dismisses suggestions and selection respects the caret", () => {
  const f = fixture();
  f.type("#birth suffix");
  f.input.setSelectionRange(6, 6);
  click(f.input);
  click(f.list.children[0]);
  assert.equal(f.input.value, " suffix");
  f.type("#");
  click(f.document);
  assert.equal(f.list.hidden, true);
});

test("84 icon choices preserve custom input, synchronize selection and allow reset", () => {
  const f = fixture();
  assert.equal(f.iconOptions.children.length, 84);
  click(f.iconOptions.children[1]);
  assert.equal(f.iconInput.value, "🎂");
  assert.equal(f.iconOptions.children[1].getAttribute("aria-pressed"), "true");
  assert.equal(f.clear.disabled, false);
  f.iconInput.value = "🐉";
  f.iconInput.dispatchEvent(new DOMEvent("input"));
  assert.equal(f.iconOptions.children.some(button => button.getAttribute("aria-pressed") === "true"), false);
  click(f.clear);
  assert.equal(f.iconInput.value, "");
  assert.equal(f.clear.disabled, true);
});

test("unknown #tags can be created, removed and reselected without duplicate submissions", () => {
  const f = fixture(true);
  f.type("#Family outing");
  assert.equal(f.list.children.length, 1);
  assert.equal(f.list.children[0].textContent, "Create tag: Family outing");
  assert.equal(f.key("Enter").defaultPrevented, true);
  assert.equal(f.input.value, "");
  assert.equal(f.chips.children.at(-1).textContent, "Family outing×");
  const hidden = f.document.querySelector("[data-new-tags]").children[0];
  assert.equal(hidden.name, "new_tags");
  assert.equal(hidden.value, "Family outing");
  click(f.chips.children.at(-1));
  assert.equal(hidden.disabled, true);
  f.type("#family outing");
  click(f.list.children[0]);
  assert.equal(hidden.disabled, false);
  assert.equal(f.document.querySelector("[data-new-tags]").children.length, 1);
  f.type("#FAMILY OUTING");
  assert.equal(f.list.hidden, true);
});

test("an exact existing tag never offers duplicate creation and new names stay literal", () => {
  const f = fixture(true);
  f.type("#birthday");
  assert.equal(f.list.children.length, 1);
  assert.equal(f.list.children[0].textContent, "Birthday");
  f.type("#<img src=x>");
  click(f.list.children[0]);
  assert.equal(f.chips.children.at(-1).textContent, "<img src=x>×");
  assert.equal(f.document.querySelector("[data-new-tags]").children[0].value, "<img src=x>");
});

test("20 presets precede the custom color, enable color and reflect saved custom/none states", () => {
  const f = fixture();
  assert.equal(f.presets.children.length, 20);
  assert.equal(f.custom.classList.contains("is-selected"), false);
  assert.equal(f.custom.parentNode.children.at(-1), f.custom);
  click(f.presets.children[0]);
  assert.equal(f.colorInput.value, "#ef4444");
  assert.equal(f.noColor.checked, false);
  assert.equal(f.presets.children[0].getAttribute("aria-pressed"), "true");
  f.colorInput.value = "#123456";
  f.colorInput.dispatchEvent(new DOMEvent("input"));
  assert.equal(f.custom.classList.contains("is-selected"), true);
  assert.equal(f.presets.children[0].getAttribute("aria-pressed"), "false");
  f.noColor.checked = true;
  f.noColor.dispatchEvent(new DOMEvent("change"));
  assert.equal(f.custom.classList.contains("is-selected"), false);
});
