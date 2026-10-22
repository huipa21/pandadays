const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "app.js"), "utf8");

function fixture(fetch) {
  const events = {};
  const error = { hidden: true, textContent: "" };
  const submit = { disabled: false };
  const password = { selected: false, select() { this.selected = true; } };
  const dialog = { addEventListener(name, listener) { events[name] = listener; } };
  let resets = 0;
  let reloads = 0;
  let redirect = "";
  const form = {
    action: "/vault/unlock",
    closest() { return dialog; },
    reset() { resets++; },
    querySelector(selector) {
      return selector === "[data-vault-error]" ? error :
        selector === 'button[type="submit"]' ? submit : password;
    },
    addEventListener(name, listener) { events[name] = listener; },
  };
  const flash = { dataset: { errorGeneric: "Server error", errorNetwork: "Network error" } };
  runInNewContext(script, {
    document: {
      querySelector(selector) { return selector === "[data-vault-unlock]" ? form : null; },
      querySelectorAll() { return []; },
      getElementById(id) { return id === "flash" ? flash : null; },
      addEventListener() {},
    },
    window: {
      addEventListener() {},
      location: { reload() { reloads++; }, assign(url) { redirect = url; } },
    },
    fetch, URLSearchParams,
    FormData: class {
      *[Symbol.iterator]() {
        yield ["password", "test-password"];
        yield ["csrf_token", "test-csrf"];
      }
    },
  });
  return {
    events, error, submit, password,
    resets: () => resets, reloads: () => reloads, redirect: () => redirect,
    async send() {
      let prevented = false;
      await events.submit({ preventDefault() { prevented = true; } });
      assert.equal(prevented, true);
    },
  };
}

function response(status, body, type = "application/json") {
  return { ok: status === 200, redirected: false, headers: { get: () => type }, json: async () => body };
}

test("Vault confirmation posts the password and CSRF token and reloads only after verification", async () => {
  let request;
  const f = fixture(async (url, options) => {
    request = { url, options };
    assert.equal(f.submit.disabled, true);
    return response(200, {});
  });
  await f.send();
  assert.equal(request.url, "/vault/unlock");
  assert.equal(request.options.method, "POST");
  assert.equal(request.options.headers.Accept, "application/json");
  assert.equal(request.options.body.get("password"), "test-password");
  assert.equal(request.options.body.get("csrf_token"), "test-csrf");
  assert.equal(f.reloads(), 1);
  assert.equal(f.submit.disabled, false);
});

test("Vault password and rate-limit errors stay in the dialog without enabling visibility", async () => {
  for (const status of [422, 429]) {
    const f = fixture(async () => response(status, { error: "Verification failed" }));
    await f.send();
    assert.equal(f.error.textContent, "Verification failed");
    assert.equal(f.error.hidden, false);
    assert.equal(f.password.selected, true);
    assert.equal(f.reloads(), 0);
    assert.equal(f.submit.disabled, false);
  }
});

test("Vault server and network failures display explicit feedback and permit retry", async () => {
  for (const [fetch, message] of [
    [async () => response(500, null, "text/plain"), "Server error"],
    [async () => { throw new Error("offline"); }, "Network error"],
  ]) {
    const f = fixture(fetch);
    await f.send();
    assert.equal(f.error.textContent, message);
    assert.equal(f.error.hidden, false);
    assert.equal(f.submit.disabled, false);
    assert.equal(f.reloads(), 0);
  }
});

test("Closing the Vault dialog clears the password and errors without unlocking", () => {
  let requests = 0;
  const f = fixture(async () => { requests++; });
  f.error.hidden = false;
  f.events.close();
  assert.equal(f.resets(), 1);
  assert.equal(f.error.hidden, true);
  assert.equal(requests, 0);
});

test("Expired Vault sessions follow the server's login redirect", async () => {
  const f = fixture(async () => ({ redirected: true, url: "/login" }));
  await f.send();
  assert.equal(f.redirect(), "/login");
  assert.equal(f.reloads(), 0);
});
