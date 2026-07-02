/*
  Unit tests for the client-side availability core (lcat-availability.js, tasks/004).
  Run: node hugo/availability_test.cjs  (exit 0 = pass). Lives at the module root, not
  under assets/, so Hugo never publishes it. Exercises the OverDrive/Thunder mapping,
  batching, cache, in-flight de-dup, and error degradation with an injected fetch --
  no network, no DOM.
*/
const assert = require("node:assert");
const A = require("./assets/lcat-availability.js");

const NOW = 1_700_000_000_000;
let passed = 0;
function test(name, fn) {
  try {
    const r = fn();
    if (r && typeof r.then === "function") return r.then(() => ok(name), (e) => fail(name, e));
    ok(name);
  } catch (e) {
    fail(name, e);
  }
}
function ok(name) {
  passed++;
  console.log("ok   - " + name);
}
function fail(name, e) {
  console.error("FAIL - " + name + "\n  " + (e && e.stack ? e.stack : e));
  process.exitCode = 1;
}

// makeFetch returns a mock fetch resolving availability for known ids from a table;
// ids absent from the table are omitted from items (the source-omits case). It records
// every call so tests can assert batching and de-dup.
function makeFetch(table, opts) {
  opts = opts || {};
  const calls = [];
  const fn = async function (url, init) {
    calls.push({ url: url, body: JSON.parse(init.body) });
    if (opts.fail) throw new Error("network down");
    const ids = JSON.parse(init.body).ids;
    if (opts.status && opts.status >= 400) {
      return { ok: false, status: opts.status, json: async () => ({}) };
    }
    const items = ids.map((id) => table[id]).filter(Boolean);
    return { ok: true, status: 200, json: async () => ({ items: items }) };
  };
  fn.calls = calls;
  return fn;
}

const cfg = { slug: "queerliblib" };

test("overdriveStatus: available / holdable / unavailable / always", () => {
  assert.equal(A.overdriveStatus({ ownedCopies: 3, availableCopies: 2 }), "available");
  assert.equal(A.overdriveStatus({ ownedCopies: 3, availableCopies: 0 }), "holdable");
  assert.equal(A.overdriveStatus({ ownedCopies: 0, availableCopies: 0 }), "unavailable");
  assert.equal(A.overdriveStatus({ availabilityType: "always", ownedCopies: 0 }), "available");
});

test("normalizeOverdrive: field mapping + holdsPlaced fallback + wait null", () => {
  const m = A.normalizeOverdrive(
    { id: "r1", ownedCopies: 5, availableCopies: 0, holdsPlaced: 4, estimatedWaitDays: null, availabilityType: "normal" },
    cfg,
    NOW
  );
  assert.equal(m.provider, "overdrive");
  assert.equal(m.status, "holdable");
  assert.equal(m.copiesOwned, 5);
  assert.equal(m.copiesAvailable, 0);
  assert.equal(m.holdsCount, 4, "holdsCount falls back to holdsPlaced");
  assert.equal(m.estimatedWaitDays, undefined, "null wait -> undefined");
  assert.equal(m.actionUrl, "https://queerliblib.overdrive.com/media/r1");
  assert.equal(m.fetchedAt, NOW);
});

test("statusText renders wait + holds for holdable", () => {
  assert.equal(A.statusText({ status: "available" }), "Available now");
  assert.equal(A.statusText({ status: "holdable", estimatedWaitDays: 14, holdsCount: 3 }), "Estimated wait ~14 days · 3 holds");
  assert.equal(A.statusText({ status: "holdable", holdsCount: 1 }), "Place a hold · 1 hold");
  assert.equal(A.statusText({ status: "unavailable" }), "Not available");
  assert.equal(A.statusText({ status: "unknown" }), "");
});

test("chunk splits to batch size", () => {
  const ids = Array.from({ length: 30 }, (_, i) => "r" + i);
  const c = A.chunk(ids, 25);
  assert.equal(c.length, 2);
  assert.equal(c[0].length, 25);
  assert.equal(c[1].length, 5);
});

test("resolve: maps ids, omitted id degrades to unknown", async () => {
  const table = { r1: { id: "r1", ownedCopies: 2, availableCopies: 1 } };
  const fetch = makeFetch(table);
  const store = A.makeStore(60000);
  const out = await A.resolve("overdrive", ["r1", "r2"], cfg, { fetch: fetch, now: NOW, store: store });
  assert.equal(out.r1.status, "available");
  assert.equal(out.r2.status, "unknown", "an id the source omits is unknown, not missing");
  assert.equal(fetch.calls.length, 1);
});

test("resolve: cache hit serves without a second fetch", async () => {
  const fetch = makeFetch({ r1: { id: "r1", ownedCopies: 1, availableCopies: 1 } });
  const store = A.makeStore(60000);
  await A.resolve("overdrive", ["r1"], cfg, { fetch: fetch, now: NOW, store: store });
  await A.resolve("overdrive", ["r1"], cfg, { fetch: fetch, now: NOW + 1000, store: store });
  assert.equal(fetch.calls.length, 1, "second resolve is cache-served");
});

test("resolve: concurrent calls de-dup to one fetch", async () => {
  const fetch = makeFetch({ r1: { id: "r1", ownedCopies: 1, availableCopies: 0 } });
  const store = A.makeStore(60000);
  const [a, b] = await Promise.all([
    A.resolve("overdrive", ["r1"], cfg, { fetch: fetch, now: NOW, store: store }),
    A.resolve("overdrive", ["r1"], cfg, { fetch: fetch, now: NOW, store: store }),
  ]);
  assert.equal(a.r1.status, "holdable");
  assert.equal(b.r1.status, "holdable");
  assert.equal(fetch.calls.length, 1, "in-flight de-dup: one request for the shared id");
});

test("resolve: >25 ids fan out into multiple batches", async () => {
  const table = {};
  const ids = [];
  for (let i = 0; i < 30; i++) {
    ids.push("r" + i);
    table["r" + i] = { id: "r" + i, ownedCopies: 1, availableCopies: 1 };
  }
  const fetch = makeFetch(table);
  const out = await A.resolve("overdrive", ids, cfg, { fetch: fetch, now: NOW, store: A.makeStore(60000) });
  assert.equal(Object.keys(out).length, 30);
  assert.equal(fetch.calls.length, 2, "30 ids -> two <=25 batches");
});

test("resolve: failed fetch degrades all ids to unknown", async () => {
  const fetch = makeFetch({}, { fail: true });
  const out = await A.resolve("overdrive", ["r1", "r2"], cfg, { fetch: fetch, now: NOW, store: A.makeStore(60000) });
  assert.equal(out.r1.status, "unknown");
  assert.equal(out.r2.status, "unknown");
});

test("resolve: non-2xx degrades to unknown", async () => {
  const fetch = makeFetch({}, { status: 503 });
  const out = await A.resolve("overdrive", ["r1"], cfg, { fetch: fetch, now: NOW, store: A.makeStore(60000) });
  assert.equal(out.r1.status, "unknown");
});

test("fetchOverdriveBatch: POSTs ids to the library availability endpoint", async () => {
  const fetch = makeFetch({ r9: { id: "r9", ownedCopies: 4, availableCopies: 0, holdsCount: 7, estimatedWaitDays: 21 } });
  const map = await A.fetchOverdriveBatch(["r9"], cfg, { fetch: fetch, now: NOW });
  assert.equal(fetch.calls[0].url, "https://thunder.api.overdrive.com/v2/libraries/queerliblib/media/availability");
  assert.deepEqual(fetch.calls[0].body, { ids: ["r9"] });
  assert.equal(map.r9.status, "holdable");
  assert.equal(map.r9.holdsCount, 7);
  assert.equal(map.r9.estimatedWaitDays, 21);
});

// fakeDoc returns a minimal document exposing one config script by id.
function fakeDoc(textContent) {
  return {
    getElementById: function (id) {
      return id === "lcat-availability-config" ? { textContent: textContent } : null;
    },
  };
}

test("readConfig: plain object (standalone Hugo)", () => {
  var cfg = A.readConfig(fakeDoc('{"enabled":true,"overdrive":{"slug":"queerliblib"}}'));
  assert.ok(cfg);
  assert.equal(cfg.enabled, true);
  assert.equal(cfg.overdrive.slug, "queerliblib");
});

test("readConfig: double-encoded string (module-imported Hugo) unwraps", () => {
  // What Hugo emits when the module is imported: jsonify of a JSON string.
  var doubled = JSON.stringify('{"enabled":true,"overdrive":{"slug":"queerliblib"}}');
  var cfg = A.readConfig(fakeDoc(doubled));
  assert.ok(cfg, "double-encoded config still parses");
  assert.equal(cfg.enabled, true);
  assert.equal(cfg.overdrive.slug, "queerliblib");
});

test("readConfig: disabled and absent return null", () => {
  assert.equal(A.readConfig(fakeDoc('{"enabled":false}')), null);
  assert.equal(A.readConfig({ getElementById: () => null }), null);
  assert.equal(A.readConfig(fakeDoc("not json")), null);
});

// Report.
process.on("exit", function () {
  if (process.exitCode) console.error("\nSOME TESTS FAILED");
  else console.log("\nall " + passed + " availability tests passed");
});
