const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "media-player.js"), "utf8");

function fixture({ status = 410, message = "Stored file missing. Restore or upload again.", type = "text/plain", networkError = false, initialError = false, lang = "en", compact = false, playFailure = false } = {}) {
  const audio = new EventTarget();
  audio.getAttribute = () => "/attachments/7";
  audio.error = initialError ? {} : null;
  const error = { hidden: true };
  const play = new EventTarget();
  play.setAttribute = (key, value) => { play[key] = value; };
  const container = { hidden: true };
  let plays = 0;
  audio.play = async () => { plays++; if (playFailure) throw new Error("cannot play"); };
  const player = {
    dataset: { errorPlayback: "Cannot play this format.", errorNetwork: "Network unavailable." },
    querySelector: selector => selector === "audio" ? audio : error,
    closest: () => compact ? { querySelector: selector => selector === "[data-audio-play]" ? play : container } : null,
  };
  const document = new EventTarget();
  document.documentElement = { lang };
  document.querySelectorAll = () => [player];
  const requests = [];
  runInNewContext(script, {
    document,
    fetch: async (url, options) => {
      requests.push({ url, range: options.headers.Range });
      if (networkError) throw new Error("offline");
      return { ok: status >= 200 && status < 300, headers: { get: () => type }, text: async () => message };
    },
  });
  return { error, audio, requests, document, player, play, container, get plays() { return plays; },
    async fail() { audio.dispatchEvent(new Event("error")); await new Promise(resolve => setImmediate(resolve)); },
    async start() { play.dispatchEvent(new Event("click")); await new Promise(resolve => setImmediate(resolve)); },
  };
}

test("playback errors show the server's actionable plain-text message", async () => {
  const f = fixture();
  await f.fail();
  assert.equal(f.error.hidden, false);
  assert.equal(f.error.textContent, "Stored file missing. Restore or upload again.");
  assert.deepEqual(f.requests, [{ url: "/attachments/7?lang=en", range: "bytes=0-0" }]);
});

test("compact attachment audio is revealed and started only by Play", async () => {
  const f = fixture({ compact: true });
  assert.equal(f.container.hidden, true);
  assert.equal(f.plays, 0);
  await f.start();
  assert.equal(f.container.hidden, false);
  assert.equal(f.play["aria-expanded"], "true");
  assert.equal(f.plays, 1);
});

test("rejected Play requests reveal actionable errors instead of failing silently", async () => {
  const f = fixture({ compact: true, playFailure: true });
  await f.start();
  assert.equal(f.container.hidden, false);
  assert.equal(f.error.hidden, false);
  assert.match(f.error.textContent, /Stored file missing/);
});

test("diagnostic requests preserve the displayed page language", async () => {
  const f = fixture({ lang: "zh" });
  await f.fail();
  assert.equal(f.requests[0].url, "/attachments/7?lang=zh");
});

test("unsupported or damaged media gets explicit playback guidance", async () => {
  const f = fixture({ status: 206 });
  await f.fail();
  assert.equal(f.error.textContent, "Cannot play this format.");
});

test("network errors get explicit feedback", async () => {
  const f = fixture({ networkError: true });
  await f.fail();
  assert.equal(f.error.textContent, "Network unavailable.");
});

test("HTML and oversized server errors are not displayed as file diagnostics", async () => {
  for (const options of [{ type: "text/html", message: "<html>Error</html>" }, { message: "x".repeat(501) }]) {
    const f = fixture(options);
    await f.fail();
    assert.equal(f.error.textContent, "Cannot play this format.");
  }
});

test("preload failures that precede initialization are reported", async () => {
  const f = fixture({ initialError: true });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.error.hidden, false);
  assert.match(f.error.textContent, /Stored file missing/);
});

test("successful metadata load clears the error and htmx initialization is idempotent", async () => {
  const f = fixture();
  const load = new Event("htmx:load");
  f.document.dispatchEvent(load);
  await f.fail();
  assert.equal(f.requests.length, 1);
  f.audio.dispatchEvent(new Event("loadedmetadata"));
  assert.equal(f.error.hidden, true);
});
