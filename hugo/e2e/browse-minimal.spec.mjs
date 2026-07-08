// Real-browser E2E for the minimal-profile browse path (tasks/170): with
// taxonomy/term kinds disabled and the shared sidebar on, the sidebar's
// unlinked facet rows must hydrate into checkbox toggles driving the WASM
// reader, and the duplicate #lcat-browse-facets panel must stay empty. Usage:
// node browse-minimal.spec.mjs <base-url>
const pwMod = await import(process.env.PLAYWRIGHT_PKG || "playwright");
const chromium = (pwMod.default ?? pwMod).chromium;

const base = process.argv[2];
if (!base) {
  console.error("usage: node browse-minimal.spec.mjs <base-url>");
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

// 1. The shared fragment arrives and its unlinked rows hydrate into toggles
//    (covers the lcat:facets-loaded handoff between loader and reader).
await page.waitForSelector('.lcat-facets li[data-lcat-field] input[data-field]', { timeout: 20000 });
const rows = await page.$$eval(".lcat-facets li[data-lcat-field]", (lis) =>
  lis.map((li) => ({
    hydrated: !!li.querySelector("input[data-field]"),
    linked: !!li.querySelector("a[href]"),
  })),
);
check(
  "every unlinked sidebar row hydrates, none carries a link (" + rows.length + " rows)",
  rows.length > 0 && rows.every((r) => r.hydrated && !r.linked),
);

// 2. The duplicate panel never renders: the sidebar is the facet UI.
const panel = await page.$eval("#lcat-browse-facets", (el) => ({ hidden: el.hidden, empty: el.innerHTML === "" }));
check("#lcat-browse-facets stays hidden and empty", panel.hidden && panel.empty);

// 3. Facet-only browse through a sidebar toggle: format=ebook -> the
//    fixture's one ebook work.
await page.click('.lcat-facets li[data-lcat-field="format"][data-lcat-cat="ebook"] input');
await page.waitForSelector('#lcat-results a.lcat-result[href*="wexampletwo"]', { timeout: 10000 });
let hrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check("sidebar toggle ebook -> exactly wexampletwo", hrefs.length === 1 && hrefs[0].includes("wexampletwo"));

// 4. A query intersects with the sidebar toggle.
await page.fill('.lcat-search input[name="q"]', "Herculine");
await page.waitForTimeout(700);
hrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check("sidebar toggle filters out non-ebook query hit", !hrefs.some((h) => h.includes("wexampleone")));

// 5. Clearing query + toggle restores the server-rendered list.
await page.fill('.lcat-search input[name="q"]', "");
await page.click('.lcat-facets li[data-lcat-field="format"][data-lcat-cat="ebook"] input');
await page.waitForTimeout(500);
const staticBack = await page.$$eval("#lcat-results li", (lis) => lis.length);
check("clearing restores static list (" + staticLis + " lis)", staticBack === staticLis);

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
