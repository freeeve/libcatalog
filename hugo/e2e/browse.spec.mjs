// Real-browser E2E for the RoaringRange client browse path: boots
// the WASM reader in Chromium and drives search, facet-only browse, query+facet
// intersection, and the static-list restore. jsdom cannot run ES modules/WASM,
// so this is the only automated coverage of the reader path -- see README.md
// for the runner. Usage: node browse.spec.mjs <base-url>
//
// Playwright resolves from PLAYWRIGHT_PKG when set (e.g. the npx cache:
// ~/.npm/_npx/<hash>/node_modules/playwright/index.js), else the bare package.
const pwMod = await import(process.env.PLAYWRIGHT_PKG || "playwright");
const chromium = (pwMod.default ?? pwMod).chromium;

const base = process.argv[2];
if (!base) {
  console.error("usage: node browse.spec.mjs <base-url>");
  process.exit(2);
}
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push("pageerror: " + e.message));
page.on("console", (m) => {
  if (m.type() === "error") errors.push(m.text());
});

await page.goto(base + "/works/", { waitUntil: "load" });
const staticLis = await page.$$eval("#lcat-results li", (l) => l.length);
let pass = 0,
  fail = 0;
const check = (name, ok) => {
  console.log((ok ? "ok  " : "FAIL") + " - " + name);
  ok ? pass++ : fail++;
};

// 1. Facet panel renders from the sidecar (host present triggers eager boot).
await page.waitForSelector("#lcat-browse-facets details", { timeout: 20000 });
const fields = await page.$$eval("#lcat-browse-facets details summary", (els) => els.map((e) => e.textContent.trim()));
check("facet panel renders fields: " + fields.join(","), fields.includes("format") && fields.includes("language"));

// 1a. Subjects group by vocabulary scheme with the configured display names
//: homosaurus + fast, no raw "subject" group.
check(
  "panel groups subjects per scheme (Homosaurus + FAST)",
  fields.includes("Homosaurus") && fields.includes("FAST") && !fields.includes("subject"),
);

// 1b. The homosaurus group is a tree: only root concepts show,
//     counts rolled up over their subtrees -- "Gender identity" (w2 direct +
//     w1/w3 via the narrower concept = 3) and "Trans community" (a concept
//     no work carries, minted from the catalog's terms sideband with a real
// label; w1/w3 via the narrower concept = 2).
await page.$$eval("#lcat-browse-facets details", (ds) => ds.forEach((d) => (d.open = true)));
await page.waitForSelector("#lcat-browse-facets .lcat-facet-caret", { timeout: 10000 });
const treeRows = await page.$$eval('#lcat-browse-facets li[data-lcat-field="subject"]', (lis) =>
  lis.map((li) => ({
    cat: li.getAttribute("data-lcat-cat"),
    label: li.querySelector(".lcat-facet-value").textContent,
    count: li.querySelector(".lcat-count").textContent,
    nested: !!li.closest("ul.lcat-facet-children"),
  })),
);
check(
  "homosaurus tree shows the two roots with rolled-up counts",
  treeRows.filter((r) => !r.nested && r.cat.includes("homosaurus")).length === 2 &&
    treeRows.some((r) => r.label === "Gender identity" && r.count === "3"),
);

// 1b'. A minted ancestor with a sideband label renders as a real tree node
// -- here as a root, since no work carries it directly.
check(
  "sideband-labeled minted ancestor renders as a root (Trans community, 2)",
  treeRows.some((r) => !r.nested && r.cat.endsWith("homoit9999902") && r.label === "Trans community" && r.count === "2"),
);

// 1b''. The fixture also gives "Gender identity" an unlabeled broader
//       ancestor (homoit9999901) absent from the sideband: the build mints
//       it for rolled-up postings, but it must never render -- a label-less
//       concept would show as a raw authority URI at the top of the tree
//.
check(
  "unlabeled minted ancestor never renders (no URI rows)",
  !treeRows.some((r) => r.cat.includes("homoit9999901") || r.label.includes("homosaurus.org")),
);

// 1c. Expanding the root reveals the narrower concept with its own count.
await page.click('#lcat-browse-facets li[data-lcat-cat$="homoit0000282"] > .lcat-facet-caret');
await page.waitForSelector('#lcat-browse-facets li[data-lcat-cat$="homoit0000669"]', { timeout: 10000 });
const child = await page.$eval('#lcat-browse-facets li[data-lcat-cat$="homoit0000669"]', (li) => ({
  label: li.querySelector(".lcat-facet-value").textContent,
  count: li.querySelector(".lcat-count").textContent,
}));
check("expanding reveals Transgender people (2)", child.label === "Transgender people" && child.count === "2");

// 1d. Selecting the narrower concept filters to its works.
await page.click('#lcat-browse-facets li[data-lcat-cat$="homoit0000669"] input[data-cat]');
await page.waitForSelector('#lcat-results a.lcat-result[href*="wexampleone"]', { timeout: 10000 });
let subjHrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check(
  "narrower toggle -> Herculine + Snow Country",
  subjHrefs.length === 2 && !subjHrefs.some((h) => h.includes("wexampletwo")),
);
await page.click('#lcat-browse-facets li[data-lcat-cat$="homoit0000669"] input[data-cat]');
await page.waitForTimeout(400);

// 1e. The per-group filter searches the full vocabulary and renders matches
// under their forced-open ancestors.
const treeFilter = '#lcat-browse-facets details:has(.lcat-facet-caret) .lcat-facet-filter';
await page.fill(treeFilter, "transgender");
await page.waitForTimeout(300);
const filtered = await page.$$eval(
  '#lcat-browse-facets details:has(.lcat-facet-caret) li[data-lcat-field="subject"]',
  (lis) => lis.map((li) => li.querySelector(".lcat-facet-value").textContent),
);
check(
  "tree filter finds the narrower concept with its ancestor for context",
  filtered.includes("Transgender people") && filtered.includes("Gender identity"),
);
await page.fill(treeFilter, "");
await page.waitForTimeout(300);

// 1f. The fast group stays flat and its toggle filters by raw id.
await page.click('#lcat-browse-facets input[data-cat$="fast/1735592"]');
await page.waitForSelector('#lcat-results a.lcat-result[href*="wexampleone"]', { timeout: 10000 });
subjHrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check("fast toggle -> exactly wexampleone", subjHrefs.length === 1 && subjHrefs[0].includes("wexampleone"));
await page.click('#lcat-browse-facets input[data-cat$="fast/1735592"]');
await page.waitForTimeout(400);

// 2. Facet-only browse: format=ebook -> exactly the fixture's one ebook work.
await page.$$eval("#lcat-browse-facets details", (ds) => ds.forEach((d) => (d.open = true)));
await page.click('#lcat-browse-facets input[data-field="format"][data-cat="ebook"]');
await page.waitForSelector('#lcat-results a.lcat-result[href*="wexampletwo"]', { timeout: 10000 });
let hrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check("facet-only ebook -> exactly wexampletwo", hrefs.length === 1 && hrefs[0].includes("wexampletwo"));

// 2a. Live facet counts: with ebook active, inactive fields
//     intersect the survivors (fre drops to 0 and greys, spa keeps 1), the
//     active format field recounts with its own selection removed so its
//     other values stay addable (book keeps 2), and the homosaurus root's
//     rollup re-derives over the survivors (1).
await page.waitForFunction(
  () => {
    const cb = document.querySelector('#lcat-browse-facets input[data-field="language"][data-cat="fre"]');
    const li = cb && cb.closest("li");
    return li && li.querySelector(".lcat-count").textContent === "0";
  },
  { timeout: 10000 },
);
const live = await page.evaluate(() => {
  const g = (f, c) => {
    const cb = document.querySelector(`#lcat-browse-facets input[data-field="${f}"][data-cat="${c}"]`);
    const li = cb && cb.closest("li");
    return li && { n: li.querySelector(".lcat-count").textContent, zero: li.classList.contains("lcat-count-zero") };
  };
  const root = document.querySelector('#lcat-browse-facets li[data-lcat-cat$="homoit0000282"]');
  return { fre: g("language", "fre"), spa: g("language", "spa"), book: g("format", "book"), root: root && root.querySelector(".lcat-count").textContent };
});
check(
  "live counts: fre greys to 0, spa keeps 1, active-field book stays addable at 2",
  live.fre && live.fre.n === "0" && live.fre.zero && live.spa && live.spa.n === "1" && !live.spa.zero && live.book && live.book.n === "2" && !live.book.zero,
);
check("live counts: homosaurus root rollup recounts to 1", live.root === "1");

// 3. A query on top of the facet intersects.
await page.fill('.lcat-search input[name="q"]', "Spirits");
await page.waitForTimeout(700);
hrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check("query+facet -> wexampletwo", hrefs.length >= 1 && hrefs.every((h) => h.includes("wexampletwo")));

// 4. The facet excludes a text hit that does not carry it.
await page.fill('.lcat-search input[name="q"]', "Herculine");
await page.waitForTimeout(700);
hrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check("facet filters out non-ebook query hit", !hrefs.some((h) => h.includes("wexampleone")));

// 5. Clearing query + facets restores the server-rendered list.
await page.fill('.lcat-search input[name="q"]', "");
await page.click('#lcat-browse-facets input[data-field="format"][data-cat="ebook"]');
await page.waitForTimeout(500);
const staticBack = await page.$$eval("#lcat-results li", (lis) => lis.length);
check("clearing restores static list (" + staticLis + " lis)", staticBack === staticLis);

// 5a. Clearing also restores the cold full-corpus counts.
const coldBack = await page.evaluate(() => {
  const cb = document.querySelector('#lcat-browse-facets input[data-field="language"][data-cat="fre"]');
  const li = cb && cb.closest("li");
  return li && { n: li.querySelector(".lcat-count").textContent, zero: li.classList.contains("lcat-count-zero") };
});
check("clearing restores cold counts (fre 1, ungreyed)", coldBack && coldBack.n === "1" && !coldBack.zero);

// 6. Query-only live counts: the search pass's own facet counts drive the
//    rail (spa greys out of the Herculine result, fre keeps its 1).
await page.fill('.lcat-search input[name="q"]', "Herculine");
await page.waitForFunction(
  () => {
    const cb = document.querySelector('#lcat-browse-facets input[data-field="language"][data-cat="spa"]');
    const li = cb && cb.closest("li");
    return li && li.querySelector(".lcat-count").textContent === "0";
  },
  { timeout: 10000 },
);
const qLive = await page.evaluate(() => {
  const g = (c) => {
    const cb = document.querySelector(`#lcat-browse-facets input[data-field="language"][data-cat="${c}"]`);
    const li = cb && cb.closest("li");
    return li && { n: li.querySelector(".lcat-count").textContent, zero: li.classList.contains("lcat-count-zero") };
  };
  return { fre: g("fre"), spa: g("spa") };
});
check(
  "query-only live counts: spa 0 greyed, fre 1",
  qLive.spa && qLive.spa.n === "0" && qLive.spa.zero && qLive.fre && qLive.fre.n === "1" && !qLive.fre.zero,
);
await page.fill('.lcat-search input[name="q"]', "");
await page.waitForTimeout(500);

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
