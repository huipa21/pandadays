const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const { runInNewContext } = require("node:vm");

const script = readFileSync(join(__dirname, "..", "web", "static", "media-player.js"), "utf8");

function fixture({ status = 410, message = "Stored file missing. Restore or upload again.", type = "text/plain", networkError = false, initialError = false, lang = "en", compact = false, playFailure = false, count = 1, pendingPlay = false } = {}) {
  const entries = Array.from({ length: count }, () => {
    const audio = new EventTarget();
    audio.getAttribute = () => "/attachments/7";
    audio.error = initialError ? {} : null;
    const error = { hidden: true };
    const play = new EventTarget();
    play.setAttribute = (key, value) => { play[key] = value; };
    play.contains = target => target === play;
    let plays = 0;
    let pauses = 0;
    let rejectPlay;
    audio.pause = () => { pauses++; };
    audio.play = async () => {
      plays++;
      if (pendingPlay) return new Promise((resolve, reject) => { rejectPlay = reject; });
      if (playFailure) throw new Error("cannot play");
    };
    const player = {
      hidden: compact,
      dataset: { errorPlayback: "Cannot play this format.", errorNetwork: "Network unavailable." },
      querySelector: selector => selector === "audio" ? audio : error,
      closest: () => compact ? { querySelector: () => play } : null,
      contains: target => target === player || target === audio || target === error,
    };
    return { audio, error, play, player, container: player,
      get plays() { return plays; }, get pauses() { return pauses; },
      rejectPlay() { rejectPlay(new Error("play interrupted")); },
    };
  });
  const { audio, player } = entries[0];
  const document = new EventTarget();
  const window = new EventTarget();
  document.documentElement = { lang };
  document.querySelectorAll = () => entries.map(entry => entry.player);
  const requests = [];
  runInNewContext(script, {
    document,
    window,
    fetch: async (url, options) => {
      requests.push({ url, range: options.headers.Range });
      if (networkError) throw new Error("offline");
      return { ok: status >= 200 && status < 300, headers: { get: () => type }, text: async () => message };
    },
  });
  function dispatch(type, target = document) {
    const event = new Event(type);
    Object.defineProperty(event, "target", { value: target });
    document.dispatchEvent(event);
  }
  return { ...entries[0], requests, document, window, entries,
    get plays() { return entries[0].plays; }, get pauses() { return entries[0].pauses; },
    click(target) { dispatch("click", target); },
    toggle(target) { dispatch("toggle", target); },
    close(target) { dispatch("close", target); },
    async fail() { audio.dispatchEvent(new Event("error")); await new Promise(resolve => setImmediate(resolve)); },
    async start(index = 0) {
      entries[index].play.dispatchEvent(new Event("click"));
      dispatch("click", entries[index].play);
      await new Promise(resolve => setImmediate(resolve));
    },
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

test("switching attachments pauses and collapses the previous player", async () => {
  const f = fixture({ compact: true, count: 2 });
  await f.start();
  await f.start(1);
  assert.equal(f.container.hidden, true);
  assert.equal(f.play["aria-expanded"], "false");
  assert.equal(f.pauses, 1);
  assert.equal(f.entries[1].player.hidden, false);
  assert.equal(f.entries[1].play["aria-expanded"], "true");
  assert.equal(f.entries[1].plays, 1);
  assert.equal(f.entries[1].pauses, 0);
});

test("outside clicks, including another attachment's actions, pause and dismiss playback", async () => {
  const f = fixture({ compact: true });
  await f.start();
  f.click({});
  assert.equal(f.container.hidden, true);
  assert.equal(f.play["aria-expanded"], "false");
  assert.equal(f.pauses, 1);
  await f.start();
  assert.equal(f.container.hidden, false);
  assert.equal(f.plays, 2);
});

test("interacting with native audio controls does not collapse the player", async () => {
  const f = fixture({ compact: true });
  await f.start();
  f.click(f.audio);
  assert.equal(f.container.hidden, false);
  assert.equal(f.pauses, 0);
});

test("closing the containing attachment list pauses and collapses playback", async () => {
  const f = fixture({ compact: true });
  await f.start();
  f.toggle({ open: true, contains: () => true });
  f.toggle({ open: false, contains: () => false });
  assert.equal(f.container.hidden, false);
  f.toggle({ open: false, contains: target => target === f.player });
  assert.equal(f.container.hidden, true);
  assert.equal(f.pauses, 1);
});

test("closing the containing dialog pauses and collapses playback", async () => {
  const f = fixture({ compact: true });
  await f.start();
  f.close({ contains: () => false });
  assert.equal(f.container.hidden, false);
  f.close({ contains: target => target === f.player });
  assert.equal(f.container.hidden, true);
  assert.equal(f.pauses, 1);
});

test("Escape and leaving the page dismiss playback", async () => {
  const f = fixture({ compact: true });
  await f.start();
  const event = new Event("keydown");
  event.key = "Escape";
  f.document.dispatchEvent(event);
  assert.equal(f.container.hidden, true);
  await f.start();
  f.window.dispatchEvent(new Event("pagehide"));
  assert.equal(f.container.hidden, true);
  assert.equal(f.pauses, 2);
});

test("dismissed pending playback does not surface a false playback error", async () => {
  const f = fixture({ compact: true, pendingPlay: true });
  await f.start();
  f.click({});
  f.rejectPlay();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.error.hidden, true);
  assert.equal(f.requests.length, 0);
});

test("standalone media details players are not collapsed by outside clicks", () => {
  const f = fixture();
  f.click({});
  assert.equal(f.player.hidden, false);
  assert.equal(f.pauses, 0);
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
