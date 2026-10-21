const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "upload-progress.js"), "utf8");

function fixture({ files = true, html = true } = {}) {
  class Element extends EventTarget {
    constructor() { super(); this.dataset = {}; this.attributes = {}; }
    setAttribute(key, value) { this.attributes[key] = value; }
    removeAttribute(key) { delete this.attributes[key]; }
    hasAttribute(key) { return key in this.attributes; }
    append(node) { this.bar = node; }
    after(node) { form.status = node; }
  }
  const form = new Element();
  form.method = "post";
  form.enctype = "multipart/form-data";
  form.action = "http://localhost/media";
  const button = new Element();
  button.disabled = false;
  button.formAction = "http://localhost/incorrect-default";
  form.querySelector = selector => selector === "[data-upload-progress]" ? form.bar : form.status;
  form.querySelectorAll = selector => selector === 'input[type="file"]' ? [{ files: files ? [{}] : [] }] : [button];
  const document = new EventTarget();
  document.getElementById = () => ({
    dataset: { label: "Upload progress", uploading: "Uploading...", processing: "Processing...", network: "Network error", generic: "Server error" },
  });
  document.createElement = () => new Element();
  const rendered = [];
  document.open = () => rendered.push("open");
  document.write = text => rendered.push(text);
  document.close = () => rendered.push("close");
  let xhr;
  class XHR extends EventTarget {
    constructor() { super(); this.upload = new EventTarget(); this.headers = {}; xhr = this; }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(key, value) { this.headers[key] = value; }
    send(data) { this.data = data; }
    getResponseHeader() { return html ? "text/html; charset=utf-8" : "text/plain"; }
  }
  const locations = [];
  const window = {
    XMLHttpRequest: XHR, FormData: true,
    location: { origin: "http://localhost", href: "http://localhost/days/new" },
    history: { replaceState: (_, __, url) => locations.push(url) },
  };
  runInNewContext(script, {
    document, window, URL, Event, XMLHttpRequest: XHR,
    FormData: class {
      get() { return "csrf"; }
      append(name, value) { this.submitter = [name, value]; }
    },
  });
  function submit(canceled = false) {
    const event = new Event("submit", { cancelable: true });
    Object.defineProperties(event, { target: { value: form }, submitter: { value: button } });
    if (canceled) event.preventDefault();
    document.dispatchEvent(event);
    return event;
  }
  return {
    form, button, rendered, locations, submit, get xhr() { return xhr; },
    progress(loaded, total, lengthComputable = true) {
      const event = new Event("progress");
      Object.assign(event, { loaded, total, lengthComputable });
      xhr.upload.dispatchEvent(event);
    },
    response(text, url = "http://localhost/media", status = 200) {
      xhr.responseText = text;
      xhr.responseURL = url;
      xhr.status = status;
      xhr.dispatchEvent(new Event("load"));
    },
  };
}

test("multipart uploads show actual byte progress and processing until the response", () => {
  const f = fixture();
  assert.equal(f.submit().defaultPrevented, true);
  assert.equal(f.xhr.url, "http://localhost/media");
  assert.equal(f.xhr.headers["X-CSRF-Token"], "csrf");
  assert.equal(f.form.bar.value, 0);
  assert.equal(f.form.bar.hidden, false);
  assert.equal(f.button.disabled, true);
  assert.equal(f.form.attributes["aria-busy"], "true");
  f.progress(62, 100);
  assert.equal(f.form.bar.value, 62);
  assert.match(f.form.status.textContent, /62%/);
  f.xhr.upload.dispatchEvent(new Event("load"));
  assert.equal(f.form.status.textContent, "Processing...");
  assert.equal(f.form.bar.hidden, false);
  f.response("<html>Saved</html>", "http://localhost/days/1");
  assert.deepEqual(f.rendered, ["open", "<html>Saved</html>", "close"]);
  assert.deepEqual(f.locations, ["http://localhost/days/1"]);
});

test("unknown transfer size remains indeterminate and duplicate submits are blocked", () => {
  const f = fixture();
  f.submit();
  const xhr = f.xhr;
  f.progress(1, 0, false);
  assert.equal(f.form.bar.attributes.value, undefined);
  assert.equal(f.submit().defaultPrevented, true);
  assert.equal(f.xhr, xhr);
});

test("canceled forms, non-upload forms, and htmx submissions retain existing behavior", () => {
  const empty = fixture({ files: false });
  assert.equal(empty.submit().defaultPrevented, false);
  assert.equal(empty.xhr, undefined);
  const canceled = fixture();
  canceled.submit(true);
  assert.equal(canceled.xhr, undefined);
  const htmx = fixture();
  htmx.form.setAttribute("hx-post", "/media");
  assert.equal(htmx.submit().defaultPrevented, false);
  assert.equal(htmx.xhr, undefined);
});

test("network and abort failures are explicit and allow a fresh retry", () => {
  for (const type of ["error", "abort"]) {
    const f = fixture();
    f.submit();
    const xhr = f.xhr;
    xhr.dispatchEvent(new Event(type));
    assert.equal(f.form.status.textContent, "Network error");
    assert.equal(f.form.status.attributes.role, "alert");
    assert.equal(f.button.disabled, false);
    assert.equal(f.form.dataset.uploading, undefined);
    f.submit();
    assert.notEqual(f.xhr, xhr);
    assert.equal(f.form.bar.value, 0);
  }
});

test("server validation pages and previews render without repeating the upload", () => {
  for (const status of [200, 400, 413, 422, 500]) {
    const f = fixture();
    let navigating = false;
    f.form.addEventListener("upload:navigate", () => { navigating = true; });
    f.submit();
    f.response("<html>Validation or preview</html>", "http://localhost/settings/import", status);
    assert.equal(navigating, true);
    assert.deepEqual(f.rendered, ["open", "<html>Validation or preview</html>", "close"]);
  }
});

test("plain-text server errors are shown as text, never interpreted as HTML", () => {
  const f = fixture({ html: false });
  f.submit();
  f.response("Quota exceeded", undefined, 413);
  assert.equal(f.form.status.textContent, "Quota exceeded");
  assert.deepEqual(f.rendered, []);
});
