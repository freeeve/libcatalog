/*
  Unit tests for the negative facet filters (lcat-negatives.js, tasks/144).
  Run: node hugo/negatives_test.cjs  (exit 0 = pass). Lives at the module
  root, not under assets/, so Hugo never publishes it. Mounts a minimal
  sidebar + results DOM in jsdom and exercises URL-state parsing, card
  hiding, chips, link rewriting, and toggling.
*/
const assert = require("node:assert");
const fs = require("node:fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync(__dirname + "/assets/lcat-negatives.js", "utf8");

let passed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log("ok   - " + name);
  } catch (e) {
    console.error("FAIL - " + name + "\n  " + (e && e.stack ? e.stack : e));
    process.exitCode = 1;
  }
}

// The slim markup: buttons ship hidden with only their term key;
// taxonomy and label hydrate from the row anchor (the language-prefixed
// href proves segment derivation ignores leading path parts). The
// contributors row's term key differs from its URL slug on purpose.
function page(url) {
  const dom = new JSDOM(
    `<!doctype html><html><body>
    <nav class="lcat-facets"><ul>
      <li><a href="/tags/fiction/"><span class="lcat-facet-value">Fiction</span> <span class="lcat-count">2</span></a><button type="button" class="lcat-facet-not" hidden>&#x2212;</button></li>
      <li><a href="/es/languages/eng/"><span class="lcat-facet-value">Inglés</span> <span class="lcat-count">1</span></a><button type="button" class="lcat-facet-not" hidden>&#x2212;</button></li>
      <li><a href="/contributors/byron-grace/"><span class="lcat-facet-value">Byron, Grace</span></a><button type="button" class="lcat-facet-not" data-lcat-term="Byron, Grace" hidden>&#x2212;</button></li>
      <li><span class="lcat-facet-value">Unlinked</span><button type="button" class="lcat-facet-not" hidden>&#x2212;</button></li>
    </ul></nav>
    <script id="lcat-negatives-config" type="application/json">{"exclude":"Exclude %s","excluded":"Not %s","remove":"Remove exclusion of %s"}</script>
    <main>
      <ol id="lcat-results">
        <li><article class="lcat-card" data-lcat-tags="fiction|family" data-lcat-languages="eng" data-lcat-contributors="Byron, Grace"></article></li>
        <li><article class="lcat-card" data-lcat-tags="family" data-lcat-languages="spa"></article></li>
      </ol>
    </main>
    </body></html>`,
    { url: url, runScripts: "outside-only" },
  );
  dom.window.eval(SRC);
  return dom;
}

function hiddenFlags(dom) {
  return Array.from(dom.window.document.querySelectorAll("#lcat-results > li")).map((li) =>
    li.classList.contains("lcat-neg-hidden"),
  );
}

// Finds a hydrated exclude button by its row's display label.
function btn(dom, label) {
  return [...dom.window.document.querySelectorAll(".lcat-facet-not")].find(
    (b) => b.getAttribute("aria-label") === "Exclude " + label,
  );
}

test("hydration derives taxonomy/term/label from the row anchor and unhides", () => {
  const dom = page("https://example.org/works/");
  const fiction = btn(dom, "Fiction");
  assert.ok(fiction);
  assert.equal(fiction.hidden, false);
  assert.equal(fiction.getAttribute("aria-pressed"), "false");
  // Language-prefixed href: taxonomy/term are the last two segments.
  const es = btn(dom, "Inglés");
  assert.ok(es);
  assert.equal(es.hidden, false);
  // A row without a term-page link keeps its button hidden and nameless.
  const unhydrated = [...dom.window.document.querySelectorAll(".lcat-facet-not")].filter((b) => b.hidden);
  assert.equal(unhydrated.length, 1);
});

test("data-lcat-term wins over the URL slug (contributors)", () => {
  const dom = page("https://example.org/works/");
  btn(dom, "Byron, Grace").click();
  // The x-param and card match use the indexed key, not byron-grace.
  const params = new dom.window.URLSearchParams(dom.window.location.search);
  assert.equal(params.get("xcontributors"), "Byron, Grace");
  assert.deepEqual(hiddenFlags(dom), [true, false]);
});

test("x-param on load hides matching cards, chips + pressed state render", () => {
  const dom = page("https://example.org/works/?xtags=fiction");
  assert.deepEqual(hiddenFlags(dom), [true, false]);
  const chips = dom.window.document.getElementById("lcat-excluded");
  assert.ok(chips && !chips.hidden);
  assert.ok(chips.textContent.includes("Not Fiction"));
  assert.equal(btn(dom, "Fiction").getAttribute("aria-pressed"), "true");
});

test("sidebar links carry active exclusions", () => {
  const dom = page("https://example.org/works/?xtags=fiction");
  const a = dom.window.document.querySelector('a[href*="/languages/eng/"]');
  assert.ok(a.getAttribute("href").includes("xtags=fiction"));
});

test("exclude button toggles URL state and card visibility", () => {
  const dom = page("https://example.org/works/");
  const eng = btn(dom, "Inglés");
  eng.click();
  assert.ok(dom.window.location.search.includes("xlanguages=eng"));
  assert.deepEqual(hiddenFlags(dom), [true, false]);
  eng.click();
  assert.equal(dom.window.location.search, "");
  assert.deepEqual(hiddenFlags(dom), [false, false]);
});

test("chip dismiss removes the exclusion", () => {
  const dom = page("https://example.org/works/?xtags=fiction&xlanguages=eng");
  assert.deepEqual(hiddenFlags(dom), [true, false]);
  const chip = dom.window.document.querySelector("#lcat-excluded li button");
  chip.click();
  assert.equal(dom.window.location.search, "?xlanguages=eng");
  assert.deepEqual(hiddenFlags(dom), [true, false]);
  dom.window.document.querySelector("#lcat-excluded li button").click();
  assert.deepEqual(hiddenFlags(dom), [false, false]);
  assert.ok(dom.window.document.getElementById("lcat-excluded").hidden);
});

test("unknown x-params and foreign params are ignored", () => {
  const dom = page("https://example.org/works/?xfoo=bar&q=sea");
  assert.deepEqual(hiddenFlags(dom), [false, false]);
  const chips = dom.window.document.getElementById("lcat-excluded");
  assert.ok(!chips || chips.hidden);
  const a = dom.window.document.querySelector('a[href*="/tags/fiction/"]');
  assert.ok(!a.getAttribute("href").includes("xfoo"));
});

console.log("\nall " + passed + " negative-filter tests passed" + (process.exitCode ? " (with failures)" : ""));
