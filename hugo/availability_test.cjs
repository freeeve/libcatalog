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

test("overdriveRequest: direct hits Thunder, proxied hits the proxy", () => {
  const direct = A.overdriveRequest(["r1"], { slug: "queerliblib" });
  assert.equal(direct.url, "https://thunder.api.overdrive.com/v2/libraries/queerliblib/media/availability");
  assert.deepEqual(direct.body, { ids: ["r1"] });

  const proxied = A.overdriveRequest(["r1", "r2"], { transport: "proxied", proxyUrl: "https://edge.example/avail", slug: "queerliblib" });
  assert.equal(proxied.url, "https://edge.example/avail");
  assert.deepEqual(proxied.body, { provider: "overdrive", slug: "queerliblib", ids: ["r1", "r2"] });
});

test("overdriveRequest: proxied without proxyUrl errors; direct without slug errors", () => {
  assert.throws(() => A.overdriveRequest(["r1"], { transport: "proxied" }));
  assert.throws(() => A.overdriveRequest(["r1"], {}));
});

test("proxied transport yields identical normalized models to direct", async () => {
  // Same underlying availability, fetched two ways -> the models must match exactly
  // (tasks/004: "proxy fallback produces identical normalized output").
  const table = { r1: { id: "r1", ownedCopies: 3, availableCopies: 0, holdsCount: 5, estimatedWaitDays: 12, availabilityType: "normal" } };
  const directCfg = { slug: "queerliblib" };
  const proxiedCfg = { transport: "proxied", proxyUrl: "https://edge.example/avail", slug: "queerliblib" };

  const dFetch = makeFetch(table);
  const pFetch = makeFetch(table);
  const d = await A.resolve("overdrive", ["r1"], directCfg, { fetch: dFetch, now: NOW, store: A.makeStore(60000) });
  const p = await A.resolve("overdrive", ["r1"], proxiedCfg, { fetch: pFetch, now: NOW, store: A.makeStore(60000) });

  assert.deepEqual(p.r1, d.r1, "proxied model must equal direct model");
  assert.equal(d.r1.status, "holdable");
  // ...and they went to different URLs.
  assert.equal(dFetch.calls[0].url, "https://thunder.api.overdrive.com/v2/libraries/queerliblib/media/availability");
  assert.equal(pFetch.calls[0].url, "https://edge.example/avail");
});

// ---- DAIA physical-ILS adapter ------------------------------------------------

// makeDaiaFetch returns a mock DAIA endpoint: it reads the requested ids from
// the query string (direct GET) or the JSON body (proxied POST) and answers with
// the matching documents from a table, mirroring DAIA's { document: [...] }.
function makeDaiaFetch(table, opts) {
  opts = opts || {};
  const calls = [];
  const fn = async function (url, init) {
    let ids;
    if (init.body) ids = JSON.parse(init.body).ids;
    else ids = Array.from(url.matchAll(/[?&]id=([^&]+)/g)).map((m) => decodeURIComponent(m[1]));
    calls.push({ url, method: init.method, ids });
    if (opts.fail) throw new Error("network down");
    const document = ids.map((id) => table[id]).filter(Boolean);
    return { ok: true, status: 200, json: async () => ({ document }) };
  };
  fn.calls = calls;
  return fn;
}

test("daiaItemStatus: available / holdable / unavailable / unknown; service URI form", () => {
  assert.equal(A.daiaItemStatus({ available: [{ service: "loan" }] }), "available");
  assert.equal(A.daiaItemStatus({ available: [{ service: "http://purl.org/ontology/dso#Loan" }] }), "available", "full service URI");
  assert.equal(A.daiaItemStatus({ available: [{}] }), "available", "omitted service defaults to loan");
  assert.equal(A.daiaItemStatus({ unavailable: [{ service: "loan", href: "https://ils/hold/x" }] }), "holdable");
  assert.equal(A.daiaItemStatus({ unavailable: [{ service: "loan", queue: 2 }] }), "holdable", "a queue means reservable");
  assert.equal(A.daiaItemStatus({ unavailable: [{ service: "loan" }] }), "unavailable", "out with no hold path");
  assert.equal(A.daiaItemStatus({}), "unknown");
});

test("normalizeDaia: locations[], best status, physical format, action href", () => {
  const doc = {
    id: "d3",
    item: [
      { label: "A1", department: { content: "Branch A" }, available: [{ service: "loan" }] },
      { label: "B1", storage: { content: "Branch B" }, unavailable: [{ service: "loan", expected: "2026-09-01", href: "https://ils/hold/d3" }] },
    ],
  };
  const m = A.normalizeDaia(doc, {}, NOW);
  assert.equal(m.provider, "daia");
  assert.equal(m.format, "physical");
  assert.equal(m.status, "available", "best holding wins the document status");
  assert.equal(m.locations.length, 2);
  assert.deepEqual(m.locations[0], { library: "Branch A", callNumber: "A1", status: "available", dueDate: undefined });
  assert.deepEqual(m.locations[1], { library: "Branch B", callNumber: "B1", status: "holdable", dueDate: "2026-09-01" });
  assert.equal(m.actionUrl, "https://ils/hold/d3", "first reservation href becomes the action");
  assert.equal(m.fetchedAt, NOW);
});

test("statusText: physical holding shows shelf location and due date", () => {
  assert.equal(
    A.statusText({ status: "available", locations: [{ library: "Main", callNumber: "PZ7" }] }),
    "Available now · Main · PZ7"
  );
  assert.equal(
    A.statusText({ status: "holdable", locations: [{ library: "Main", callNumber: "PZ7", dueDate: "2026-08-01" }] }),
    "Place a hold · due 2026-08-01 · Main · PZ7"
  );
  assert.equal(
    A.statusText({ status: "available", locations: [{ library: "A" }, { library: "B" }, { library: "C" }] }),
    "Available now · A (+2 more)"
  );
});

test("fmtStr: substitutes {name} placeholders; leaves unknown ones verbatim", () => {
  assert.equal(A.fmtStr("Estimated wait ~{days} days", { days: 14 }), "Estimated wait ~14 days");
  assert.equal(A.fmtStr("{n} holds", { n: 3 }), "3 holds");
  assert.equal(A.fmtStr("due {date}", { date: "2026-08-01" }), "due 2026-08-01");
  assert.equal(A.fmtStr("{missing}", { n: 1 }), "{missing}", "unknown placeholder stays visible");
});

const ES = {
  availableNow: "Disponible ahora",
  notAvailable: "No disponible",
  placeHold: "Reservar",
  estimatedWait: "Espera estimada ~{days} días",
  holdsOne: "{n} reserva",
  holdsOther: "{n} reservas",
  due: "devolución {date}",
  moreLocations: "(+{n} más)",
};

test("statusText: a strings bundle localizes every word and plural", () => {
  assert.equal(A.statusText({ status: "available" }, ES), "Disponible ahora");
  assert.equal(A.statusText({ status: "unavailable" }, ES), "No disponible");
  assert.equal(A.statusText({ status: "holdable", estimatedWaitDays: 14, holdsCount: 3 }, ES), "Espera estimada ~14 días · 3 reservas");
  assert.equal(A.statusText({ status: "holdable", holdsCount: 1 }, ES), "Reservar · 1 reserva");
  assert.equal(
    A.statusText({ status: "holdable", locations: [{ library: "Central", dueDate: "2026-08-01" }, { library: "Sur" }] }, ES),
    "Reservar · devolución 2026-08-01 · Central (+1 más)"
  );
  assert.equal(A.statusText({ status: "unknown" }, ES), "");
});

test("statusText: a partial bundle falls back to English per missing key", () => {
  // Only availableNow translated; holdable detail must not print raw "{n} holds".
  const partial = { availableNow: "Disponible ahora" };
  assert.equal(A.statusText({ status: "available" }, partial), "Disponible ahora");
  assert.equal(A.statusText({ status: "holdable", holdsCount: 2 }, partial), "Place a hold · 2 holds");
});

// fakeEl is a minimal attribute-carrying element for the DOM-adjacent wireAction
// logic, so the CTA wiring is testable without a browser.
function fakeEl() {
  const attrs = {};
  return {
    attrs,
    setAttribute: (k, v) => {
      attrs[k] = String(v);
    },
    getAttribute: (k) => (k in attrs ? attrs[k] : null),
    removeAttribute: (k) => {
      delete attrs[k];
    },
    hasAttribute: (k) => k in attrs,
  };
}
function fakeEdition(cta) {
  return { querySelector: (sel) => (sel === "[data-availability-action]" ? cta : null) };
}

test("wireAction: reveals the CTA and sets href for a borrowable copy", () => {
  const cta = fakeEl();
  cta.setAttribute("hidden", "");
  A.wireAction(fakeEdition(cta), { status: "available", actionUrl: "https://lib.example/media/42" });
  assert.equal(cta.getAttribute("href"), "https://lib.example/media/42");
  assert.equal(cta.hasAttribute("hidden"), false);
});

test("wireAction: reveals the CTA for a holdable copy", () => {
  const cta = fakeEl();
  cta.setAttribute("hidden", "");
  A.wireAction(fakeEdition(cta), { status: "holdable", actionUrl: "https://lib.example/media/7" });
  assert.equal(cta.getAttribute("href"), "https://lib.example/media/7");
  assert.equal(cta.hasAttribute("hidden"), false);
});

test("wireAction: keeps the CTA hidden when unavailable, unknown, or link-less", () => {
  for (const model of [
    { status: "unavailable", actionUrl: "https://lib.example/x" },
    { status: "unknown", actionUrl: "https://lib.example/x" },
    { status: "available" }, // borrowable but no deep link
  ]) {
    const cta = fakeEl();
    cta.setAttribute("hidden", "");
    cta.setAttribute("href", "stale");
    A.wireAction(fakeEdition(cta), model);
    assert.equal(cta.hasAttribute("hidden"), true, `hidden for ${JSON.stringify(model)}`);
    assert.equal(cta.getAttribute("href"), null, `no href for ${JSON.stringify(model)}`);
  }
});

test("wireAction: no CTA element in the edition is a no-op, not a throw", () => {
  A.wireAction(fakeEdition(null), { status: "available", actionUrl: "https://lib.example/x" });
  A.wireAction(null, { status: "available", actionUrl: "https://lib.example/x" });
});

test("daiaRequest: direct GETs repeated id params, proxied POSTs the batch", () => {
  const direct = A.daiaRequest(["d1", "d2"], { baseUrl: "https://ils.example/daia" });
  assert.equal(direct.method, "GET");
  assert.equal(direct.url, "https://ils.example/daia?id=d1&id=d2&format=json");

  const proxied = A.daiaRequest(["d1"], { transport: "proxied", proxyUrl: "https://edge.example/avail" });
  assert.equal(proxied.method, "POST");
  assert.deepEqual(proxied.body, { provider: "daia", ids: ["d1"] });

  assert.throws(() => A.daiaRequest(["d1"], {}), "direct without baseUrl errors");
  assert.throws(() => A.daiaRequest(["d1"], { transport: "proxied" }), "proxied without proxyUrl errors");
});

test("resolve(daia): renders locations[], omitted id degrades to unknown", async () => {
  const table = {
    d1: { id: "d1", item: [{ label: "PZ7", department: { content: "Main" }, available: [{ service: "loan" }] }] },
  };
  const fetch = makeDaiaFetch(table);
  const out = await A.resolve("daia", ["d1", "d2"], { baseUrl: "https://ils.example/daia" }, { fetch, now: NOW, store: A.makeStore(60000) });
  assert.equal(out.d1.status, "available");
  assert.equal(out.d1.locations[0].callNumber, "PZ7");
  assert.equal(out.d2.status, "unknown", "a document the ILS omits is unknown");
  assert.equal(fetch.calls[0].method, "GET");
});

test("resolve(daia): proxied and direct yield identical models", async () => {
  const table = {
    d1: { id: "d1", item: [{ label: "QA76", storage: { content: "Annex" }, unavailable: [{ service: "loan", expected: "2026-10-01", href: "https://ils/hold/d1" }] }] },
  };
  const d = await A.resolve("daia", ["d1"], { baseUrl: "https://ils.example/daia" }, { fetch: makeDaiaFetch(table), now: NOW, store: A.makeStore(60000) });
  const p = await A.resolve("daia", ["d1"], { transport: "proxied", proxyUrl: "https://edge.example/avail" }, { fetch: makeDaiaFetch(table), now: NOW, store: A.makeStore(60000) });
  assert.deepEqual(p.d1, d.d1, "proxied physical model must equal direct");
  assert.equal(d.d1.status, "holdable");
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

// ---- Hugo lowercases param keys (tasks/287) ----
// The end-to-end proof lives in availability_seam_test.cjs, which reads the
// bytes Hugo actually emits. These pin the normalizer's contract.

test("readConfig: lowercased Hugo keys reach the adapter camelCased", () => {
  var cfg = A.readConfig(
    fakeDoc(
      JSON.stringify({
        enabled: true,
        overdrive: { slug: "examplelib", transport: "proxied", proxyurl: "https://p.example", baseurl: "https://b.example", actionurltemplate: "https://a.example/{id}", timeoutms: 900 },
      })
    )
  );
  assert.equal(cfg.overdrive.proxyUrl, "https://p.example");
  assert.equal(cfg.overdrive.baseUrl, "https://b.example");
  assert.equal(cfg.overdrive.actionUrlTemplate, "https://a.example/{id}");
  assert.equal(cfg.overdrive.timeoutMs, 900);
});

test("readConfig: camelCase still works, and unknown keys are untouched", () => {
  // A site config read from somewhere other than Hugo params must not regress,
  // and a deployment's own keys are none of the normalizer's business.
  var cfg = A.readConfig(fakeDoc(JSON.stringify({ enabled: true, daia: { baseUrl: "https://d.example", myOwnKey: { nestedKey: 1 } } })));
  assert.equal(cfg.daia.baseUrl, "https://d.example");
  assert.deepEqual(cfg.daia.myOwnKey, { nestedKey: 1 });
});

test("readConfig: canonicalization reaches into arrays and nested objects", () => {
  var cfg = A.readConfig(fakeDoc(JSON.stringify({ enabled: true, daia: [{ baseurl: "https://one.example" }, { baseurl: "https://two.example" }] })));
  assert.equal(cfg.daia[0].baseUrl, "https://one.example");
  assert.equal(cfg.daia[1].baseUrl, "https://two.example");
});

// spyConsole collects what the adapter logs. Injected via deps.console: these tests
// run concurrently with every other async test, and monkeypatching the global console
// would swallow their ok/FAIL lines and race on the restore.
function spyConsole() {
  var spy = { errors: [], warns: [] };
  spy.error = function () {
    spy.errors.push(Array.prototype.slice.call(arguments).join(" "));
  };
  spy.warn = function () {
    spy.warns.push(Array.prototype.slice.call(arguments).join(" "));
  };
  return spy;
}

test("resolve: a misconfigured provider is reported once at error level, not once per batch", async () => {
  var spy = spyConsole();
  // proxied with no proxyUrl: exactly what Hugo's lowercasing used to produce.
  var bad = { slug: "examplelib", transport: "proxied" };
  var ids = [];
  for (var i = 0; i < 60; i++) ids.push("id" + i); // 3 batches
  var adapter = A.adapters.overdrive;
  delete adapter._configErrorLogged;
  try {
    var got = await A.resolve("overdrive", ids, bad, { fetch: makeFetch({}), now: NOW, store: A.makeStore(1000), console: spy });
    assert.equal(Object.keys(got).length, 60);
    assert.equal(got.id0.status, "unknown");
  } finally {
    delete adapter._configErrorLogged;
  }
  assert.equal(spy.errors.length, 1, "a config error should be reported once, not once per batch: " + JSON.stringify(spy.errors));
  assert.match(spy.errors[0], /misconfigured/);
  assert.equal(spy.warns.length, 0, "a config error is not a transient network warning: " + JSON.stringify(spy.warns));
});

test("resolve: a network failure still warns per batch", async () => {
  var spy = spyConsole();
  var boom = function () {
    return Promise.reject(new Error("offline"));
  };
  var got = await A.resolve("overdrive", ["a", "b"], cfg, { fetch: boom, now: NOW, store: A.makeStore(1000), console: spy });
  assert.equal(got.a.status, "unknown");
  assert.equal(spy.errors.length, 0, "a transient failure is not a config error: " + JSON.stringify(spy.errors));
  assert.equal(spy.warns.length, 1, "a fetch failure should warn: " + JSON.stringify(spy.warns));
});

// Report.
process.on("exit", function () {
  if (process.exitCode) console.error("\nSOME TESTS FAILED");
  else console.log("\nall " + passed + " availability tests passed");
});
