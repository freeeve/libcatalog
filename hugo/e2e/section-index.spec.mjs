// Real-browser E2E for the generic section index. Before the fix,
// layouts/list.html was the only list layout, so it served /works/, the home page
// AND every other section. /lists/ -- the section index of the curated views --
// shipped as a canonical, sitemapped page reading "0 works" that linked to none of
// them, and under engine = "roaringrange" it hydrated into a second catalog search
// page as soon as a reader typed a query, because lcat-browse.js binds to
// #lcat-results wherever it finds one.
//
// Run against the roaringrange build with real browse artifacts, so "did not
// hydrate" means the reader was there and declined -- not that it failed to boot.
// Usage: node section-index.spec.mjs <base-url>
const pwMod = await import(process.env.PLAYWRIGHT_PKG || "playwright");
const chromium = (pwMod.default ?? pwMod).chromium;

const base = process.argv[2];
if (!base) {
  console.error("usage: node section-index.spec.mjs <base-url>");
  process.exit(2);
}
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push("pageerror: " + e.message));
page.on("console", (m) => {
  if (m.type() === "error") errors.push(m.text());
});

let pass = 0,
  fail = 0;
const check = (name, ok) => {
  console.log((ok ? "ok  " : "FAIL") + " - " + name);
  ok ? pass++ : fail++;
};
const count = (sel) => page.$$eval(sel, (l) => l.length);

// Type a query the way a reader does, and give the reader time to react if it
// were going to. On /works/ this same wait is enough for the cards to swap.
const typeQuery = async (q) => {
  await page.fill('.lcat-search input[name="q"]', q);
  await page.waitForTimeout(1200);
};

// 0. Control: the reader boots and browse works on /works/. Without this, every
//    "did not hydrate" assertion below would also pass on a broken build.
await page.goto(base + "/works/", { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 20000 });
await typeQuery("snow");
// The reader replaces the server-rendered cards with its own a.lcat-result rows.
const worksHits = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.textContent.trim()));
check(
  "control: /works/ hydrates and filters to Snow Country (" + worksHits.join(",") + ")",
  worksHits.length === 1 && worksHits[0].includes("Snow Country"),
);

// 1. Cold: /lists/ indexes the curated views beneath it. This is the page's whole
//    reason to exist; it used to link to nothing.
await page.goto(base + "/lists/", { waitUntil: "load" });
const links = await page.$$eval(".lcat-pagelist a", (a) => a.map((e) => e.getAttribute("href")));
check("/lists/ links to its curated pages: " + links.join(","), links.some((h) => h.endsWith("/lists/staff-picks/")));

// 2. Cold: none of the Works browse shell. Each of these is load-bearing --
//    #lcat-results is what lcat-browse.js binds to, and workCount is a category
//    error on a page that is not about works.
check("/lists/ has no #lcat-results", (await count("#lcat-results")) === 0);
check("/lists/ has no browse facet host", (await count("#lcat-browse-facets")) === 0);
check("/lists/ has no facet sidebar", (await count(".lcat-sidebar")) === 0);
check("/lists/ announces no work count", (await count(".lcat-resultcount")) === 0);

// 3. Hot: typing a query does not turn the section index into a catalog search.
await typeQuery("snow");
const hot = {
  cards: await count(".lcat-card"),
  hits: await count("a.lcat-result"),
  results: await count("#lcat-results"),
  facets: await count("#lcat-browse-facets li"),
  pagelist: await count(".lcat-pagelist li"),
};
check(
  "/lists/ does not hydrate into a browse surface on a query " + JSON.stringify(hot),
  hot.cards === 0 && hot.hits === 0 && hot.results === 0 && hot.facets === 0 && hot.pagelist === 1,
);

// 4. curated.html is unaffected: it still renders its works, in front-matter
//    order, and its <ol> carries no id so browse leaves it alone.
await page.goto(base + "/lists/staff-picks/", { waitUntil: "load" });
const curated = await page.$$eval(".lcat-card-title a", (a) => a.map((e) => e.getAttribute("href")));
check(
  "/lists/staff-picks/ renders its works in front-matter order: " + curated.join(","),
  curated.length === 2 && curated[0].endsWith("/works/wexampletwo/") && curated[1].endsWith("/works/wexampleone/"),
);
await typeQuery("snow");
const curatedAfter = await page.$$eval(".lcat-card-title a", (a) => a.length);
check("/lists/staff-picks/ does not hydrate on a query", curatedAfter === 2 && (await count("#lcat-results")) === 0);

// 5. The home page and /works/ keep the browse shell they have always had.
await page.goto(base + "/", { waitUntil: "load" });
check(
  "home is still a Works browse",
  (await count("#lcat-results")) === 1 && (await count(".lcat-resultcount")) === 1 && (await count(".lcat-sidebar")) === 1,
);

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
