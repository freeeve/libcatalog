/*
  Unit tests for the shared facet sidebar loader (lcat-sidebar.js, tasks/150).
  Run: node hugo/sidebar_test.cjs  (exit 0 = pass). Lives at the module root,
  not under assets/, so Hugo never publishes it. Mounts the host element in
  jsdom with a stubbed fetch and exercises fragment insertion, script
  re-activation, config passthrough, and the fetch-failure fallback.
*/
const assert = require("node:assert");
const fs = require("node:fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync(__dirname + "/assets/lcat-sidebar.js", "utf8");

let passed = 0;
const tests = [];
function test(name, fn) {
  tests.push({ name, fn });
}

const FALLBACK =
  '<ul class="lcat-facets-fallback"><li><a href="/subjects/">Subjects</a></li></ul>';

// The published fragment: sidebar nav, the negatives JSON config, an inline
// script standing in for the hydration scripts (same re-activation path), and
// a src script whose attributes must survive the re-creation.
const FRAGMENT =
  '<nav class="lcat-facets"><ul><li><a href="/tags/fiction/"><span class="lcat-facet-value">Fiction</span></a></li></ul></nav>' +
  '<script id="lcat-negatives-config" type="application/json">{"exclude":"Exclude %s"}</script>' +
  "<script>window.__lcatHydrated = (window.__lcatHydrated || 0) + 1;</script>" +
  '<script src="/lcat-facets.abc123.js" integrity="sha256-deadbeef" defer></script>';

// runScripts "dangerously" so a re-created inline script executes, proving the
// innerHTML-inert scripts really are re-activated; the loader itself is
// eval'ed, and the external src script never loads (no jsdom resource loader).
function page(opts) {
  const host = opts.host === false ? "" : `<div class="lcat-facets-shared" data-lcat-facets-src="/lcat/facets.en.abc.html">${FALLBACK}</div>`;
  const dom = new JSDOM(`<!doctype html><html><body><aside class="lcat-sidebar">${host}</aside></body></html>`, {
    url: "https://example.org/works/",
    runScripts: "dangerously",
  });
  dom.fetched = [];
  dom.window.fetch = function (url) {
    dom.fetched.push(url);
    return Promise.resolve(
      opts.status === 200
        ? { ok: true, status: 200, text: () => Promise.resolve(FRAGMENT) }
        : { ok: false, status: opts.status, text: () => Promise.resolve("") },
    );
  };
  dom.window.eval(SRC);
  return dom;
}

const settle = () => new Promise((r) => setTimeout(r, 10));

test("fetches the fragment URL from the host and replaces the fallback", async () => {
  const dom = page({ status: 200 });
  await settle();
  assert.deepEqual(dom.fetched, ["/lcat/facets.en.abc.html"]);
  const host = dom.window.document.querySelector("[data-lcat-facets-src]");
  assert.ok(host.querySelector("nav.lcat-facets a[href='/tags/fiction/']"));
  assert.equal(host.querySelector(".lcat-facets-fallback"), null);
});

test("re-creates executable scripts so hydration runs over the inserted DOM", async () => {
  const dom = page({ status: 200 });
  await settle();
  assert.equal(dom.window.__lcatHydrated, 1);
  const src = dom.window.document.querySelector("script[src]");
  assert.ok(src, "src script re-created in place");
  assert.equal(src.getAttribute("integrity"), "sha256-deadbeef");
  assert.ok(src.hasAttribute("defer"));
});

test("announces insertion via lcat:facets-loaded after scripts re-activate", async () => {
  const dom = page({ status: 200 });
  const seen = [];
  dom.window.document.addEventListener("lcat:facets-loaded", () => {
    seen.push(dom.window.__lcatHydrated);
  });
  await settle();
  // Fired exactly once, and only after the fragment's scripts were live.
  assert.deepEqual(seen, [1]);
});

test("a failed fetch never announces lcat:facets-loaded", async () => {
  const dom = page({ status: 404 });
  let fired = false;
  dom.window.document.addEventListener("lcat:facets-loaded", () => {
    fired = true;
  });
  await settle();
  assert.equal(fired, false);
});

test("JSON config scripts pass through as parsed data, untouched", async () => {
  const dom = page({ status: 200 });
  await settle();
  const cfg = dom.window.document.getElementById("lcat-negatives-config");
  assert.ok(cfg);
  assert.equal(JSON.parse(cfg.textContent).exclude, "Exclude %s");
});

test("a failed fetch keeps the fallback links", async () => {
  const dom = page({ status: 404 });
  await settle();
  const host = dom.window.document.querySelector("[data-lcat-facets-src]");
  assert.ok(host.querySelector(".lcat-facets-fallback a[href='/subjects/']"));
  assert.equal(host.querySelector("nav.lcat-facets"), null);
});

test("no host element (inline mode) means no fetch at all", async () => {
  const dom = page({ status: 200, host: false });
  await settle();
  assert.deepEqual(dom.fetched, []);
});

(async () => {
  for (const t of tests) {
    try {
      await t.fn();
      passed++;
      console.log("ok   - " + t.name);
    } catch (e) {
      console.error("FAIL - " + t.name + "\n  " + (e && e.stack ? e.stack : e));
      process.exitCode = 1;
    }
  }
  console.log("\nall " + passed + " shared-sidebar tests passed" + (process.exitCode ? " (with failures)" : ""));
})();
