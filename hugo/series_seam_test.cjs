// The Work-level series line, and the param name it does NOT take.
//
// Schema v12 moved series from the instance to the work and made them objects.
// The adapter passes them as `seriesList`, not `series`, and that is not a
// stylistic choice: `series` is a name adopters already use for their own
// `lcat:extra/series` string, a reserved param always beats an extra of the same
// name, and most records carry no 490. Claiming `series` would have overwritten
// every adopter's series line with an empty slice -- silently, on every Work --
// and where a Work had both, this module's own layout would `range` over their
// string and fail the build.
//
// Both of those were observed before the rename. Neither is visible from inside a
// template, so this builds the site with an adopter extra in place and reads what
// a visitor gets.
//
// Usage: node series_seam_test.cjs   (requires `hugo` on PATH)
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

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "lcat-series-seam-"));
const siteDir = path.join(tmp, "site");
fs.cpSync(path.join(__dirname, "exampleSite"), siteDir, { recursive: true });
const gomod = path.join(siteDir, "go.mod");
fs.writeFileSync(gomod, fs.readFileSync(gomod, "utf8").replace("=> ../", `=> ${__dirname}`));

// Plant an adopter extra named `series` on both works: one that has projected
// series (wexampleone) and one that has none (wexampletwo). The second is the case
// that failed silently.
const catalogPath = path.join(siteDir, "assets", "catalog.json");
const catalog = JSON.parse(fs.readFileSync(catalogPath, "utf8"));
const ADOPTER = "Adopter Series Name";
for (const w of catalog.works) {
  if (w.id === "wexampleone" || w.id === "wexampletwo") {
    w.extra = Object.assign({}, w.extra, { series: ADOPTER });
  }
}
const withSeries = catalog.works.find((w) => w.id === "wexampleone");
assert(withSeries.series && withSeries.series.length === 2, "fixture: wexampleone must carry 2 projected series");
const withoutSeries = catalog.works.find((w) => w.id === "wexampletwo");
assert(!withoutSeries.series, "fixture: wexampletwo must carry no projected series");
fs.writeFileSync(catalogPath, JSON.stringify(catalog));

// The adopter's own work-extra.html, reading .Params.series the way a real one does.
const partials = path.join(siteDir, "layouts", "_partials");
fs.mkdirSync(partials, { recursive: true });
fs.writeFileSync(
  path.join(partials, "work-extra.html"),
  `{{- with .Params.series }}<p class="adopter">EXTRA:{{ printf "%T" . }}:{{ . }}</p>{{ end }}\n`,
);

const out = path.join(tmp, "public");
// No --quiet: it swallows ERROR lines, and a template that cannot range over a
// string is exactly the failure this test exists to catch.
execFileSync("hugo", ["--destination", out], { cwd: siteDir, stdio: ["ignore", "ignore", "inherit"] });

const page = (work) => fs.readFileSync(path.join(out, "works", work, "index.html"), "utf8");

check("the adopter's `series` extra survives on a Work that has projected series", () => {
  const p = page("wexampleone");
  assert(p.includes(`EXTRA:string:${ADOPTER}`), `adopter extra was shadowed: ${p.match(/EXTRA:[^<]*/)}`);
});

check("the adopter's `series` extra survives on a Work that has none", () => {
  // The silent one: an empty reserved param overwrote the extra, and `with` made
  // the adopter's whole series row disappear. Nothing errored.
  const p = page("wexampletwo");
  assert(p.includes(`EXTRA:string:${ADOPTER}`), "adopter extra was emptied by a Work with no 490");
});

check("the module renders its own series from seriesList", () => {
  const p = page("wexampleone");
  assert(p.includes("<dt>Series</dt>"), "no series row rendered");
  assert(p.includes("Example Audio Series, v. 2"), "the statement and its enumeration are not paired");
  assert(p.includes("Example Untraced Series"), "the second series is missing");
});

check("each series keeps its own enumeration", () => {
  // The whole point of the reshape: the flat shape paired statement to enumeration
  // by list position, so the untraced series would have inherited "v. 2".
  const p = page("wexampleone");
  assert(!p.includes("Example Untraced Series, v. 2"), "an enumeration leaked onto the wrong series");
});

check("the ISSN rides the markup, unrendered", () => {
  const p = page("wexampleone");
  assert(p.includes('data-issn="0075-2118"'), "the series ISSN is not exposed to adopters");
  assert(!p.includes(">0075-2118<"), "the ISSN is rendered as reader-facing text");
});

check("a Work with no projected series renders no series row", () => {
  // The control: without it, a layout that printed the row unconditionally would
  // satisfy every assertion above.
  assert(!page("wexampletwo").includes("<dt>Series</dt>"), "a Work with no series got a series row");
});

check("series are a Work fact, not an edition fact", () => {
  // They used to render inside each <li class="lcat-edition">. A 490 is transcribed
  // on every printing; borrowing the audiobook does not change what series it is in.
  const p = page("wexampleone");
  const editions = p.slice(p.indexOf('<ul class="lcat-editions">'), p.indexOf("</ul>", p.indexOf('<ul class="lcat-editions">')));
  assert(!editions.includes("Example Audio Series"), "the series is still rendered inside an edition");
});

fs.rmSync(tmp, { recursive: true, force: true });
console.log(failures === 0 ? "all series seam tests passed" : `${failures} failed`);
process.exit(failures === 0 ? 0 : 1);
