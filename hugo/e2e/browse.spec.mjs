// Real-browser E2E for the RoaringRange client browse path (tasks/158): boots
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
//     (tasks/173): homosaurus + fast, no raw "subject" group.
check(
  "panel groups subjects per scheme (Homosaurus + FAST)",
  fields.includes("Homosaurus") && fields.includes("FAST") && !fields.includes("subject"),
);

// 1b. The homosaurus group is a tree (tasks/174): only the root concept
//     shows, its count rolled up over the subtree (w2 direct + w1/w3 via
//     the narrower concept = 3).
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
  "homosaurus tree shows only the root with a rolled-up count of 3",
  treeRows.filter((r) => !r.nested && r.cat.includes("homosaurus")).length === 1 &&
    treeRows.some((r) => r.label === "Gender identity" && r.count === "3"),
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
//     under their forced-open ancestors (tasks/174).
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

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
