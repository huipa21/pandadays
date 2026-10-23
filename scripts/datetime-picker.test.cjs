const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

class Element {
  constructor(tag, attrs = {}, children = []) {
    this.tag = tag;
    this.attrs = attrs;
    this.children = [];
    this.dataset = {};
    this.listeners = {};
    this.value = attrs.value || "";
    this.hidden = false;
    this.classList = { add() {} };
    this.append(...children);
  }
  setAttribute(name, value) { this.attrs[name] = String(value); }
  getAttribute(name) { return this.attrs[name]; }
  append(...children) { children.forEach(child => { child.parent = this; this.children.push(child); }); }
  querySelectorAll(selector) {
    return this.children.flatMap(child => [
      ...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector),
    ]);
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  matches(selector) {
    const match = selector.match(/^\[([^=\]]+)(?:="([^"]+)")?\]$/);
    return match ? Object.hasOwn(this.attrs, match[1]) && (match[2] === undefined || this.attrs[match[1]] === match[2]) : this.tag === selector;
  }
  closest(selector) { return this.matches(selector) ? this : this.parent?.closest(selector); }
  addEventListener(name, callback) { (this.listeners[name] ||= []).push(callback); }
  dispatchEvent(event) {
    event.target ||= this;
    for (const callback of this.listeners[event.type] || []) callback(event);
    if (event.bubbles) this.parent?.dispatchEvent(event);
  }
  focus() { this.focused = true; }
  select() { this.selected = true; }
  setCustomValidity(value) { this.validationMessage = value; }
  checkValidity() { return !this.validationMessage; }
  reportValidity() { return this.checkValidity(); }
}
class DOMEvent {
  constructor(type, options = {}) { Object.assign(this, { type }, options); }
  preventDefault() { this.defaultPrevented = true; }
}
const script = readFileSync(join(__dirname, "..", "web", "static", "datetime-picker.js"), "utf8");
function fixture({ date = "2024-02-10", time = "00:00", lang = "en", lunar = false } = {}) {
  const el = (tag, attrs, children) => new Element(tag, attrs, children);
  const field = (name, value) => el("input", { name, value });
  const solar = field("date", date);
  const clock = field("time", time);
  const fields = [field("lunar_year", "2024"), field("lunar_month", "1"), field("lunar_day", "1")];
  const toggle = el("input", { "data-calendar-toggle": "" });
  toggle.checked = lunar;
  const root = el("div", { "data-datetime-picker": "" }, [solar, clock, ...fields, toggle]);
  Object.assign(root.dataset, { year: "Year", month: "Month", day: "Day", hour: "Hour", minute: "Minute", clear: "All day", invalid: "Invalid date", rangeError: "Invalid lunar date", timeError: "Invalid time" });
  const messages = JSON.parse(readFileSync(join(__dirname, "..", "locales", lang + ".json"), "utf8"));
  for (const key of ["digits", "days", "months", "year", "leap"]) {
    root.dataset[`lunar${key[0].toUpperCase()}${key.slice(1)}`] = messages[`days.picker.lunar_${key}`];
  }
  root.dataset.lunarData = JSON.stringify([
    { year: 2023, start: "2023-01-22", months: [29, 30, -29, 29, 30, 30, 29, 30, 30, 29, 30, 29, 30] },
    { year: 2024, start: "2024-02-10", months: [29, 30, 29, 29, 30, 29, 30, 30, 29, 30, 30, 29] },
  ]);
  const legend = el("legend");
  legend.textContent = "Date and time";
  const form = el("form", {}, [el("fieldset", {}, [legend, root])]);
  const document = el("document", {}, [form]);
  document.documentElement = { lang };
  document.createElement = tag => el(tag);
  runInNewContext(script, { document, Intl, Date, Event: DOMEvent });
  const editors = root.querySelectorAll("input").filter(input => input.dataset.segment);
  const group = root.children.find(child => child.attrs.role === "group");
  const weekday = group.querySelector("output");
  const leap = root.children.find(child => child.tag === "label").querySelector("input");
  const edit = (index, value, commit = true) => {
    editors[index].value = value;
    editors[index].dispatchEvent(new DOMEvent("input", { bubbles: true }));
    if (commit) editors[index].dispatchEvent(new DOMEvent("change", { bubbles: true }));
  };
  const changeCalendar = checked => {
    toggle.checked = checked;
    toggle.dispatchEvent(new DOMEvent("change", { bubbles: true }));
  };
  return { root, document, solar, clock, fields, toggle, group, editors, weekday, leap, edit, changeCalendar };
}

test("one grouped control contains exactly five editable values and no scrollers", () => {
  const f = fixture();
  assert.equal(f.group.children.filter(child => child.tag === "input").length, 5);
  assert.deepEqual(f.editors.map(input => input.value), ["2024", "02", "10", "00", "00"]);
  assert.equal(f.root.querySelectorAll("select").length, 0);
  assert.equal(f.root.querySelectorAll('[role="dialog"]').length, 0);
  assert.equal(f.weekday.textContent, "Sat");
  assert.equal(f.solar.disabled, false);
  assert.equal(f.fields[0].disabled, true);
  f.document.dispatchEvent(new DOMEvent("htmx:load"));
  assert.equal(f.root.querySelectorAll('[role="group"]').length, 1);
});

test("Gregorian dates validate full civil dates without rollover and update localized weekdays", () => {
  for (const lang of ["en", "zh"]) {
    const f = fixture({ lang });
    f.edit(2, "29");
    assert.equal(f.solar.value, "2024-02-29");
    assert.equal(f.weekday.textContent, lang === "en" ? "Thu" : "周四");
    f.edit(0, "2023");
    assert.equal(f.editors[0].checkValidity(), false);
    assert.equal(f.weekday.textContent, "");
    assert.equal(f.solar.value, "2024-02-29");
    f.edit(2, "28");
    assert.equal(f.solar.value, "2023-02-28");
    assert.equal(f.editors[0].checkValidity(), true);
    for (const invalid of ["", "text", "0", "10000", "2.5", "-1"]) {
      f.edit(0, invalid);
      assert.equal(f.editors[0].checkValidity(), false, invalid);
    }
    f.edit(0, "0001");
    assert.equal(f.solar.value, "0001-02-28");
    f.edit(0, "9999");
    assert.equal(f.solar.value, "9999-02-28");
  }
});

test("numeric lunar entry formats year month day in Chinese and converts locally", () => {
  for (const lang of ["en", "zh"]) {
    const f = fixture({ lang });
    f.changeCalendar(true);
    assert.equal(f.toggle.checked, true);
    assert.deepEqual(f.editors.slice(0, 3).map(input => input.value), ["二〇二四年", "正月", "初一"]);
    assert.equal(f.weekday.textContent, lang === "en" ? "Sat" : "周六");
    f.edit(1, "2");
    f.edit(2, "12");
    assert.equal(f.editors[1].value, "二月");
    assert.equal(f.editors[2].value, "十二");
    assert.equal(f.solar.value, "2024-03-21");
    f.changeCalendar(false);
    assert.deepEqual(f.editors.slice(0, 3).map(input => input.value), ["2024", "03", "21"]);
    assert.equal(f.solar.disabled, false);
    f.changeCalendar(true);
    assert.equal(f.solar.value, "2024-03-21");
    assert.deepEqual(f.fields.map(input => input.value), ["2024", "2", "12"]);
  }
});

test("lunar leap months and short months are validated, not silently clamped", () => {
  const f = fixture({ lunar: true });
  f.edit(0, "2023");
  f.edit(1, "闰二月");
  f.edit(2, "29");
  assert.equal(f.leap.checked, true);
  assert.equal(f.solar.value, "2023-04-19");
  assert.equal(f.fields[1].value, "-2");
  f.edit(2, "30");
  assert.equal(f.editors[2].checkValidity(), false);
  assert.equal(f.fields[2].value, "29");
  f.edit(2, "廿九");
  f.edit(0, "二〇二四年");
  assert.equal(f.editors[0].checkValidity(), false);
  f.leap.checked = false;
  f.leap.dispatchEvent(new DOMEvent("change", { bubbles: true }));
  assert.equal(f.editors[0].checkValidity(), true);
  assert.equal(f.solar.value, "2024-04-07");
});

test("an invalid leap-month toggle remains protected until corrected", () => {
  const f = fixture({ lunar: true });
  f.leap.checked = true;
  f.leap.dispatchEvent(new DOMEvent("change", { bubbles: true }));
  assert.equal(f.editors[0].checkValidity(), false);
  assert.equal(f.leap.dataset.lunarDraft, "true");
  f.leap.checked = false;
  f.leap.dispatchEvent(new DOMEvent("change", { bubbles: true }));
  assert.equal(f.editors[0].checkValidity(), true);
  assert.equal(f.leap.dataset.lunarDraft, "false");
});

test("time is 24-hour, accepts midnight, validates partial values and supports all-day", () => {
  const f = fixture();
  f.edit(3, "23");
  f.edit(4, "59");
  assert.equal(f.clock.value, "23:59");
  for (const [index, value] of [[3, "24"], [3, "-1"], [3, "text"], [4, "60"]]) {
    f.edit(index, value);
    assert.equal(f.editors[index].checkValidity(), false);
    f.edit(3, "00");
    f.edit(4, "00");
  }
  assert.equal(f.clock.value, "00:00");
  f.edit(3, "");
  assert.equal(f.editors[3].checkValidity(), false);
  f.edit(4, "");
  assert.equal(f.clock.value, "");
  assert.equal(f.editors[3].checkValidity(), true);
  f.edit(3, "8");
  f.edit(4, "5");
  assert.equal(f.clock.value, "08:05");
  f.root.children.find(child => child.tag === "button").dispatchEvent(new DOMEvent("click"));
  assert.equal(f.clock.value, "");
  assert.deepEqual(f.editors.slice(3).map(input => input.value), ["", ""]);
});

test("calendar switches preserve partial time drafts and reject unsupported dates explicitly", () => {
  const f = fixture();
  f.edit(3, "2", false);
  f.edit(4, "", false);
  f.changeCalendar(true);
  assert.equal(f.editors[3].value, "2");
  assert.equal(f.editors[4].value, "");
  assert.equal(f.editors[4].checkValidity(), false);
  f.changeCalendar(false);
  f.edit(0, "1800");
  f.changeCalendar(true);
  assert.equal(f.toggle.checked, false);
  assert.equal(f.root.children.at(-1).textContent, "Invalid lunar date");
  assert.equal(f.root.children.at(-1).hidden, false);
});

test("invalid drafts remain visible and protected, and an empty date is not defaulted", () => {
  const f = fixture({ date: "", time: "" });
  assert.deepEqual(f.editors.slice(0, 3).map(input => input.value), ["", "", ""]);
  f.edit(0, "2024");
  f.edit(1, "2");
  f.edit(2, "10");
  assert.equal(f.solar.value, "2024-02-10");
  f.edit(2, "invalid");
  assert.equal(f.editors[2].value, "invalid");
  assert.equal(f.editors[2].dataset.lunarDraft, "true");
  f.changeCalendar(true);
  assert.equal(f.toggle.checked, false);
  assert.equal(f.editors[2].value, "invalid");
});
