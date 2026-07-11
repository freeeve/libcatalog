// Retired Work ids reach the published site.
//
// `redirects.json` was emitted by every build, published nowhere, and read by
// nobody: 2710 once-public permalinks answered a bare 404. The module now does two
// things with it, and both are only visible from outside a template:
//
//   - it publishes the map to /redirects.json, so a host has something to serve
//   - it mints a meta-refresh stub for every *merged* id, and none for a tombstone
//
// The asymmetry is the design. A merged id has a successor to name, and a stub
// forwards on any host with no host configuration -- the same alias machinery Hugo
// already uses for its pagers. A tombstone has nowhere to send anyone: the honest
// answer is 410, no static host can give one, and a 200 page saying "gone" is a
// soft 404 that is worse than the 404 it replaced. `lcat serve` reads the same map
// and answers both properly (see cmd/lcat/redirects_test.go).
//
// Usage: node redirects_seam_test.cjs   (requires `hugo` on PATH)
"use strict";
const fs = require("fs");
const os = require("os");
const path = require("path");
const { execFileSync } = require("child_process");

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

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "lcat-redirects-seam-"));
const siteDir = path.join(tmp, "site");
fs.cpSync(path.join(__dirname, "exampleSite"), siteDir, { recursive: true });
const gomod = path.join(siteDir, "go.mod");
fs.writeFileSync(gomod, fs.readFileSync(gomod, "utf8").replace("=> ../", `=> ${__dirname}`));

// The exampleSite ships this fixture; assert its shape rather than trusting it, so
// a future edit to it cannot quietly empty this test.
const MERGED = "wexamplemerged";
const GONE = "wexamplegone";
const SURVIVOR = "wexampleone";
const mapPath = path.join(siteDir, "assets", "redirects.json");
const map = JSON.parse(fs.readFileSync(mapPath, "utf8"));
assert(
  map.redirects.some((r) => r.from === MERGED && r.to === SURVIVOR),
  "fixture: exampleSite's redirects.json must carry a merged id with a survivor",
);
assert(
  map.redirects.some((r) => r.from === GONE && r.to === ""),
  "fixture: exampleSite's redirects.json must carry a tombstone",
);

// An adopter extra that tries to claim the reserved param. A live Work must not be
// forwarded off its own page by one.
const catalogPath = path.join(siteDir, "assets", "catalog.json");
const catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
const victim = catalog.works.find((w) => w.id === "wexampletwo");
victim.extra = Object.assign({}, victim.extra, { lcatRetiredTo: SURVIVOR });
fs.writeFileSync(catalogPath, JSON.stringify(catalog));

const out = path.join(tmp, "public");
// No --quiet: it swallows ERROR lines.
execFileSync("hugo", ["--destination", out], { cwd: siteDir, stdio: ["ignore", "ignore", "inherit"] });

const read = (...p) => fs.readFileSync(path.join(out, ...p), "utf8");
const exists = (...p) => fs.existsSync(path.join(out, ...p));
// The status quo ante, in one regex: what a host looks for, and what the e2e probe
// greps. Hugo's own alias stubs have exactly this shape.
const REFRESH = /http-equiv="refresh"\s+content="[^"]*url=([^"]*)"/i;

check("the map reaches the published site", () => {
  assert(exists("redirects.json"), "/redirects.json was not published: a host has no map to serve");
  const served = JSON.parse(read("redirects.json"));
  assert(served.redirects.length === map.redirects.length, "the published map is not the projector's map");
});

check("the build inputs stay unpublished", () => {
  // The control that gives the check above its meaning. catalog.json, facets.json
  // and similar.json are consumed by this build and thrown away; publishing the
  // whole assets/ dir would satisfy the first check while proving nothing.
  for (const f of ["catalog.json", "facets.json", "similar.json"]) {
    assert(!exists(f), `/${f} was published; only redirects.json has a runtime consumer`);
  }
});

check("a merged id forwards to its survivor", () => {
  assert(exists("works", MERGED, "index.html"), `no page for the merged id ${MERGED}`);
  const p = read("works", MERGED, "index.html");
  const m = p.match(REFRESH);
  assert(m, "the merged id's page carries no meta refresh");
  assert(m[1].endsWith(`/works/${SURVIVOR}/`), `refreshes to ${m[1]}, not to the survivor`);
  assert(p.includes(`<link rel="canonical" href="${m[1]}">`), "canonical does not name the survivor");
  assert(p.includes('<meta name="robots" content="noindex">'), "the stub is indexable");
});

check("the stub says it in words too", () => {
  // The refresh is not guaranteed to fire. What is left has to be a page a reader
  // can act on, not a blank one.
  const p = read("works", MERGED, "index.html");
  assert(p.includes("This record has moved"), "no human-readable heading");
  assert(new RegExp(`<a class="lcat-retired-link" href="[^"]*/works/${SURVIVOR}/"`).test(p), "no clickable link to the survivor");
});

check("a tombstone gets no page", () => {
  // Not an oversight: a 200 "gone" page is a soft 404. `lcat serve` answers 410.
  assert(!exists("works", GONE), `a page was minted for the tombstoned id ${GONE}`);
});

check("a live Work keeps its own head", () => {
  // The stub replaces the whole SEO head. If that branch leaked, every Work page
  // would carry noindex and a canonical pointing somewhere else.
  const p = read("works", SURVIVOR, "index.html");
  assert(!REFRESH.test(p), "a live Work page carries a meta refresh");
  assert(!p.includes('name="robots"'), "a live Work page is noindexed");
  assert(p.includes(`<link rel="canonical" href="https://example.org/works/${SURVIVOR}/">`), "a live Work lost its self-canonical");
  assert(p.includes("application/ld+json"), "a live Work lost its Book JSON-LD");
});

check("an adopter extra cannot forward a live Work off its own page", () => {
  // `extra.lcatRetiredTo` is planted on wexampletwo above. The adapter reserves the
  // param -- empty -- on every live Work, so the extra never reaches head-seo.
  const p = read("works", "wexampletwo", "index.html");
  assert(!REFRESH.test(p), "an adopter's extra turned a live Work into a redirect stub");
  assert(!p.includes('name="robots"'), "an adopter's extra noindexed a live Work");
});

check("the stub is absent from the listings that would advertise it", () => {
  assert(!read("works", "index.html").includes(MERGED), "a retired id is listed under /works/");
  assert(!read("sitemap.xml").includes(MERGED), "a retired id is in sitemap.xml");
});

check("a merged id forwards inside the reader's language", () => {
  // absLangURL, not absURL: a Spanish reader following an old link must not be
  // dropped onto the English page.
  const p = read("es", "works", MERGED, "index.html");
  const m = p.match(REFRESH);
  assert(m, "the Spanish stub carries no meta refresh");
  assert(m[1].endsWith(`/es/works/${SURVIVOR}/`), `the Spanish stub refreshes to ${m[1]}`);
  assert(p.includes("Este registro se ha trasladado"), "the Spanish stub is not translated");
});

fs.rmSync(tmp, { recursive: true, force: true });
console.log(failures === 0 ? "all redirects seam tests passed" : `${failures} failed`);
process.exit(failures === 0 ? 0 : 1);
