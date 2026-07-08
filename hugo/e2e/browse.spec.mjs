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
//     and localized labels, like the static rail (tasks/173): the fixture
//     carries homosaurus + fast concepts sharing the label "Fiction".
check(
  "panel groups subjects per scheme (Homosaurus + FAST)",
  fields.includes("Homosaurus") && fields.includes("FAST") && !fields.includes("subject"),
);
const subjectLabels = await page.$$eval(
  '#lcat-browse-facets input[data-field="subject"]',
  (ins) => ins.map((i) => ({ cat: i.getAttribute("data-cat"), text: i.parentElement.textContent.trim() })),
);
check(
  "panel subject rows show labels, not raw ids",
  subjectLabels.length === 3 && subjectLabels.every((s) => s.text.startsWith("Fiction") || s.text.startsWith("Memoirs")),
);

// 1b. A scheme-grouped subject toggle still filters by the raw id.
await page.$$eval("#lcat-browse-facets details", (ds) => ds.forEach((d) => (d.open = true)));
await page.click('#lcat-browse-facets input[data-field="subject"][data-cat="f:fiction"]');
await page.waitForSelector('#lcat-results a.lcat-result[href*="wexampletwo"]', { timeout: 10000 });
let subjHrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check("fast Fiction toggle -> exactly wexampletwo", subjHrefs.length === 1 && subjHrefs[0].includes("wexampletwo"));
await page.click('#lcat-browse-facets input[data-field="subject"][data-cat="f:fiction"]');
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
