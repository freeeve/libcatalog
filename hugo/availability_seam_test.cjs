// The seams nothing crossed.
//
// availability_test.cjs is deep and every test hands the adapter a hand-written
// JavaScript object -- so it can never discover that the real producers spell
// things differently. There are two producers:
//
//   config:    TOML -> Hugo (lowercases param keys) -> jsonify -> readConfig -> adapter
//   edition:   catalog.json -> page.html -> DOM attribute -> collect() -> adapter
//
// broke the first. broke the second: no template emitted
// data-daia-id, so the DAIA adapter could never run on any page libcat builds.
//
// This test builds hugo/exampleSite for real and asserts on what the browser
// receives. Nothing here is hand-written but the TOML.
//
// Usage: node availability_seam_test.cjs   (requires `hugo` on PATH)
"use strict";
const fs = require("fs");
const os = require("os");
const path = require("path");
const { execFileSync } = require("child_process");

const A = require("./assets/lcat-availability.js");

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log(`ok   - ${name}`);
  } catch (e) {
    failures++;
    console.error(`FAIL - ${name}\n       ${e.message}`);
  }
}
function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "lcat-availability-seam-"));
const siteDir = path.join(__dirname, "exampleSite");

// build renders exampleSite with an extra config overlay and returns its output dir.
function build(name, toml) {
  const overlay = path.join(tmp, name + ".toml");
  fs.writeFileSync(overlay, toml);
  const out = path.join(tmp, name);
  execFileSync("hugo", ["--quiet", "--config", `hugo.toml,${overlay}`, "--destination", out], {
    cwd: siteDir,
    stdio: ["ignore", "ignore", "inherit"],
  });
  return out;
}
const page = (out, work) => fs.readFileSync(path.join(out, "works", work, "index.html"), "utf8");
const editions = (html) => html.match(/<li class="lcat-edition"[^>]*>/g) ?? [];
const edition = (html, id) => editions(html).find((e) => e.includes(`data-instance="${id}"`));

// ---------------------------------------------------------------------------
// -- Hugo lowercases param keys, so the config the browser receives
// spells proxyUrl as proxyurl.
// ---------------------------------------------------------------------------

const PROXY = "https://proxy.example/av";
const ACTION = "https://borrow.example/go/{id}";
const BASE = "https://thunder.example/v2";

// The README's own spelling, verbatim (hugo/README.md).
const configured = build(
  "configured",
  `
[params.availability]
  enabled = true
  [params.availability.overdrive]
    slug = "examplelib"
    transport = "proxied"
    proxyUrl = "${PROXY}"
    baseUrl = "${BASE}"
    actionUrlTemplate = "${ACTION}"
    timeoutMs = 4321
`
);

const m = page(configured, "wexampleone").match(/<script id="lcat-availability-config"[^>]*>([\s\S]*?)<\/script>/);
if (!m) {
  console.error("FAIL - no lcat-availability-config script in the rendered page");
  process.exit(1);
}
const emitted = m[1];
const doc = { getElementById: (id) => (id === "lcat-availability-config" ? { textContent: emitted } : null) };

// The bug, stated as a fact about Hugo rather than an assumption. If Hugo ever
// stops lowercasing, this fails and the normalizer can go.
check("287: Hugo emits the param keys lowercased", () => {
  let raw = JSON.parse(emitted);
  if (typeof raw === "string") raw = JSON.parse(raw);
  const keys = Object.keys(raw.overdrive ?? {});
  assert(keys.includes("proxyurl"), `emitted keys are ${JSON.stringify(keys)}; expected the lowercased "proxyurl"`);
  assert(!keys.includes("proxyUrl"), "Hugo preserved camelCase; the normalizer is now dead code");
});

check("287: readConfig hands the adapter the spelling it reads", () => {
  const od = A.readConfig(doc).overdrive;
  assert(od.proxyUrl === PROXY, `proxyUrl = ${JSON.stringify(od.proxyUrl)}`);
  assert(od.baseUrl === BASE, `baseUrl = ${JSON.stringify(od.baseUrl)}`);
  assert(od.actionUrlTemplate === ACTION, `actionUrlTemplate = ${JSON.stringify(od.actionUrlTemplate)}`);
  assert(od.timeoutMs === 4321, `timeoutMs = ${JSON.stringify(od.timeoutMs)}`);
  assert(od.transport === "proxied" && od.slug === "examplelib", "the already-lowercase keys regressed");
});

// The failure that matters: proxied transport configured per the README, and not
// one request ever issued.
check("287: a proxied transport built from real config posts to the proxy", () => {
  const req = A.overdriveRequest(["abc"], A.readConfig(doc).overdrive);
  assert(req.url === PROXY, `request went to ${JSON.stringify(req.url)}, not the configured proxy`);
  assert(req.body.slug === "examplelib", "the proxy body lost the slug");
});

check("287: unknown keys survive canonicalization", () => {
  const cfg = A.readConfig({
    getElementById: () => ({ textContent: JSON.stringify({ enabled: true, overdrive: { proxyurl: PROXY, myOwnKey: 7 } }) }),
  });
  assert(cfg.overdrive.proxyUrl === PROXY, "known key not canonicalized");
  assert(cfg.overdrive.myOwnKey === 7, "a deployment's own key was renamed or dropped");
});

check("287: a disabled config is still null", () => {
  const cfg = A.readConfig({ getElementById: () => ({ textContent: JSON.stringify({ enabled: false, overdrive: {} }) }) });
  assert(cfg === null, "a disabled availability block produced a config");
});

// ---------------------------------------------------------------------------
// -- page.html emits one DOM attribute per adapter, from the
// scheme -> attribute table in data/lcat/availabilityAttrs.toml.
// ---------------------------------------------------------------------------

// Every adapter's domAttr must be reachable, or the adapter is dead code. This
// asks the adapter registry rather than a hardcoded list, so a new adapter that
// nobody wired into the table fails here.
check("288: every registered adapter's domAttr appears on a real edition", () => {
  const html = ["wexampleone", "wexampletwo", "wexamplethree"].map((w) => page(configured, w)).join("\n");
  const missing = Object.keys(A.adapters).filter((k) => !html.includes(A.adapters[k].domAttr + "="));
  assert(
    missing.length === 0,
    `adapters ${JSON.stringify(missing)} collect on attributes no template emits, so they can never run ` +
      `(add a row to data/lcat/availabilityAttrs.toml and a providerId to exampleSite's catalog.json)`
  );
});

check("288: the physical edition carries the DAIA document id", () => {
  const li = edition(page(configured, "wexampletwo"), "iextwoprint");
  assert(li, "exampleSite lost its print edition; the physical path has no fixture again");
  assert(/data-daia-id="ppn:example-daia-1"/.test(li), `no data-daia-id on the print edition: ${li}`);
});

// A providerId whose scheme has no table row is projected into catalog.json but must
// not reach the DOM. wexampleone's audio instance carries OverDrive's "overdrive"
// title id alongside its "overdrive-reserve" Reserve ID; only the latter resolves
// availability. This is a real fixture, not a constructed one.
check("288: a scheme with no table row emits no attribute", () => {
  const li = edition(page(configured, "wexampleone"), "iexoneaudio");
  assert(/data-overdrive-reserve="24760f5d-/.test(li), `the Reserve ID should be emitted: ${li}`);
  const attrs = [...li.matchAll(/\s([a-zA-Z0-9-]+)=/g)].map((x) => x[1]).sort();
  assert(
    JSON.stringify(attrs) === JSON.stringify(["class", "data-format", "data-instance", "data-overdrive-reserve"]),
    `unexpected attributes on the audio edition: ${JSON.stringify(attrs)} -- the "overdrive" title id must not be emitted`
  );
});

// html/template refuses to compute an attribute *name*: it emits the ZgotmplZ
// sentinel instead of erroring. That is the same silent-failure class as the bug
// being fixed, so page.html builds the attribute with safeHTMLAttr -- and a stray
// ZgotmplZ anywhere means that went wrong.
check("288: no ZgotmplZ sentinel in any rendered work page", () => {
  for (const w of ["wexampleone", "wexampletwo", "wexamplethree"]) {
    assert(!page(configured, w).includes("ZgotmplZ"), `${w} rendered a ZgotmplZ attribute`);
  }
});

// safeHTMLAttr means the attribute name is injected unescaped, so page.html gates it
// on ^data-[a-z0-9-]+$. A site can override the table via its own data/ file, so the
// gate has to hold against a hostile row, not just a typo. Site data merges over
// module data per key, which this also pins.
check("288: a hostile attribute name in the table is dropped, not emitted", () => {
  const dataDir = path.join(tmp, "hostile-data", "lcat");
  fs.mkdirSync(dataDir, { recursive: true });
  fs.writeFileSync(path.join(dataDir, "availabilityAttrs.toml"), `"overdrive-reserve" = "onload=alert(1)"\n`);
  const out = build("hostile", `[[module.mounts]]\n  source = "${path.join(tmp, "hostile-data")}"\n  target = "data"\n`);

  const one = page(out, "wexampleone");
  assert(!one.includes("onload"), "a hostile attribute name from the data table reached the rendered page");
  assert(!edition(one, "iexoneaudio").includes("data-overdrive-reserve"), "the overridden row should not emit its old attribute");
  // The module's own rows still merge in, so the build is otherwise healthy.
  assert(/data-daia-id="ppn:example-daia-1"/.test(page(out, "wexampletwo")), "site data replaced the module table wholesale");
});

fs.rmSync(tmp, { recursive: true, force: true });

if (failures) {
  console.error(`\n${failures} seam test(s) failed`);
  process.exit(1);
}
console.log("\nall availability seam tests passed");
