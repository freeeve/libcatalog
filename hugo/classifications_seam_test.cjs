// A taxonomy key is one path segment.
//
// A Dewey number carries a prime mark -- MARC 082 `$a 813/.6` -- and the
// classification taxonomy indexed that code verbatim. A slash is structural in a
// URL path, so one term minted two segments, the second a dot-directory, leaving
// an index-less `/classifications/813/` in the middle of a public catalog with a
// sitemap entry pointing crawlers at it. Nothing caught it: the link resolved,
// because Hugo really did generate the nested page.
//
// The fix slugs the KEY and keeps the code for display, which is the split
// already built. That is only correct if three things agree on the key --
// the content adapter, the facet rail (which recomputes it from facets.json), and
// the term page's label lookup -- so this drives the built site rather than any one
// of them.
//
// Contributors are the same defect, found in the same place: real catalogues carry
// "Forry/Fino, Amanda". They are keyed on the name rather than a slug on purpose --
// slugging 50,176 real names collapses 616 groups of them, mostly punctuation
// variants of one person -- so the key loses the slash and nothing else, and the
// name is recovered for display.
//
// Usage: node classifications_seam_test.cjs   (requires `hugo` on PATH)
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

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "lcat-class-seam-"));
const siteDir = path.join(tmp, "site");
fs.cpSync(path.join(__dirname, "exampleSite"), siteDir, { recursive: true });
const gomod = path.join(siteDir, "go.mod");
fs.writeFileSync(gomod, fs.readFileSync(gomod, "utf8").replace("=> ../", `=> ${__dirname}`));

// Assert the fixture, rather than trusting it: a later edit to exampleSite that
// dropped the Dewey number would empty most of this file silently.
const catalog = JSON.parse(fs.readFileSync(path.join(siteDir, "assets", "catalog.json"), "utf8"));
const three = catalog.works.find((w) => w.id === "wexamplethree");
assert(three.classifications.some((c) => c.value === "813/.6"), "fixture: wexamplethree must carry the Dewey code 813/.6");
assert(three.contributors.some((c) => c.name === "Forry/Fino, Amanda"), "fixture: wexamplethree must carry a contributor whose name has a slash");

const out = path.join(tmp, "public");
const build = (dest) =>
  execFileSync("hugo", ["--destination", dest], { cwd: siteDir, stdio: ["ignore", "ignore", "inherit"] });
build(out);

const read = (...p) => fs.readFileSync(path.join(out, ...p), "utf8");
const exists = (...p) => fs.existsSync(path.join(out, ...p));
const h1 = (html) => (html.match(/<h1>[\s\S]*?<\/h1>/) ?? [""])[0];
const decode = (s) => s.replace(/&#43;/g, "+").replace(/&#34;/g, '"').replace(/&amp;/g, "&");

check("a code with a slash mints one segment, not two", () => {
  assert(exists("classifications", "813-6", "index.html"), "no term page at /classifications/813-6/");
  assert(!exists("classifications", "813"), "the accidental parent /classifications/813/ still exists");
});

check("no taxonomy mints a nested term page", () => {
  // The property, not the instance. A dot-directory or an index-less parent
  // anywhere under a taxonomy is the same bug wearing a different value.
  for (const tax of ["classifications", "contributors", "subjects", "tags", "formats", "languages"]) {
    if (!exists(tax)) continue;
    const nested = fs
      .readdirSync(path.join(out, tax), { withFileTypes: true })
      .filter((e) => e.isDirectory())
      .flatMap((e) =>
        fs
          .readdirSync(path.join(out, tax, e.name), { withFileTypes: true })
          .filter((k) => k.isDirectory() && k.name !== "page")
          .map((k) => `${tax}/${e.name}/${k.name}`),
      );
    assert(nested.length === 0, `nested term directories: ${nested.join(", ")}`);
  }
});

check("the term page still shows the code the cataloguer typed", () => {
  // The key can no longer spell it, and classificationList lives on Work pages.
  // Without the facets.json fallback this heading reads "813-6".
  assert(h1(read("classifications", "813-6", "index.html")).includes("813/.6"), "the Dewey term page does not show 813/.6");
});

check("a classification with a projected label still prefers the label", () => {
  // Control: the raw-code fallback must not shadow the label the graph carries.
  assert(h1(read("classifications", "fic019000", "index.html")).includes("Fiction / Literary"), "the BISAC term lost its label");
});

check("the facet rail links the slug and displays the code", () => {
  // The rail recomputes the key from facets.json, which stores the raw code. Get
  // that wrong and site.GetPage misses: the row renders, unlinked, and nothing errors.
  const p = read("works", "index.html");
  assert(p.includes('href="/classifications/813-6/"'), "the Dewey facet row is not linked to its term page");
  assert(/<a href="\/classifications\/813-6\/"><span class="lcat-facet-value">813\/\.6<\/span>/.test(p), "the linked row does not display the raw code");
  assert(!p.includes('href="/classifications/813/.6/"'), "the rail still links the nested path");
});

check("the work detail page shows the code, not the slug", () => {
  const p = read("works", "wexamplethree", "index.html");
  assert(p.includes(">813/.6<"), "the detail page lost the raw Dewey code");
  assert(!p.includes(">813-6<"), "the detail page shows the URL slug as if it were the code");
});

check("no crawler is handed a dot-segment", () => {
  // The exampleSite is bilingual, so /sitemap.xml is a sitemapindex and the term
  // URLs live in the per-language ones. Read them all -- asserting on the index
  // would pass while every listed URL was wrong.
  const maps = ["en", "es"].map((lang) => read(lang, "sitemap.xml"));
  assert(maps.length === 2, "expected a sitemap per language");
  for (const sm of maps) {
    assert(!sm.includes("813/.6"), "a sitemap still advertises the nested path");
    assert(sm.includes("/classifications/813-6/"), "a sitemap does not list the term page at all");
  }
});

check("a contributor keeps every character but the one that cannot be a path", () => {
  assert(exists("contributors", "forry-fino-amanda", "index.html"), "no term page for the slashed contributor");
  const p = read("contributors", "forry-fino-amanda", "index.html");
  assert(decode(h1(p)).includes("Forry/Fino, Amanda"), "the heading misspells her name as the key does");
  assert(decode(p).includes("<title>Forry/Fino, Amanda"), "the head <title> and the h1 disagree");
});

check("contributors are not slugged", () => {
  // The control that bounds the blast radius. Slugging contributors would fold
  // "Chesterton, G. K." into "Chesterton, G.K." and rewrite ~49k URLs. Only the
  // slash moves.
  assert(exists("contributors", "kuang-r.f.", "index.html"), "a dotted contributor URL changed; the fix is not confined to the slash");
  assert(decode(h1(read("contributors", "kuang-r.f.", "index.html"))).includes("Kuang, R.F."), "an untouched contributor lost its name");
});

check("the contributors list page spells the name too", () => {
  assert(decode(read("contributors", "index.html")).includes(">Forry/Fino, Amanda<"), "the taxonomy list page shows the key");
});

// The guard. Every dimension the adapter indexes is either slugged or drawn from a
// controlled vocabulary, so no catalogue can trip it through classifications --
// but formats and languages are passed through raw, which makes them the way to
// prove the check exists at all rather than reading it and trusting it.
check("a path separator in any taxonomy key fails the build", () => {
  const bad = JSON.parse(JSON.stringify(catalog));
  bad.works.find((w) => w.id === "wexampleone").formats = ["audio/book"];
  fs.writeFileSync(path.join(siteDir, "assets", "catalog.json"), JSON.stringify(bad));
  let stderr = "";
  try {
    execFileSync("hugo", ["--destination", path.join(tmp, "bad")], { cwd: siteDir, stdio: ["ignore", "ignore", "pipe"] });
    throw new Error("the build succeeded with a slash in a format key");
  } catch (e) {
    stderr = String(e.stderr ?? "") + String(e.message ?? "");
  } finally {
    fs.writeFileSync(path.join(siteDir, "assets", "catalog.json"), JSON.stringify(catalog));
  }
  assert(stderr.includes("path separator"), `the build failed for some other reason: ${stderr.slice(0, 300)}`);
  assert(stderr.includes("audio/book") && stderr.includes("formats"), "the error does not name the offending value and taxonomy");
});

fs.rmSync(tmp, { recursive: true, force: true });
console.log(failures === 0 ? "all classification seam tests passed" : `${failures} failed`);
process.exit(failures === 0 ? 0 : 1);
