// Real-browser E2E for the browse result pager.
//
// Before this, browse rendered the first PAGE (60) of a match set and the reader
// had no way to reach result 61: a facet on a public catalog could hold 21,792
// works and surface 60. This spec drives the same 600-work fixture browse-scope
// uses and checks that the whole result set is reachable, one page at a time --
// on both the query path and the facet-only path a reader actually takes -- that
// the pages partition the set (no id repeats, last page holds the remainder),
// that a page is deep-linkable and back/forward works, and that clearing still
// restores the static list and the static (corpus) pager.
//
// Usage: node browse-pager.spec.mjs <base-url>
import { SUBJECT, QUERY, EXPECT } from "./make-large-catalog.mjs";

const PAGE = 60;
const pwMod = await import(process.env.PLAYWRIGHT_PKG || "playwright");
const chromium = (pwMod.default ?? pwMod).chromium;

const base = process.argv[2];
if (!base) {
  console.error("usage: node browse-pager.spec.mjs <base-url>");
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

// ---- helpers ----
const countText = () => page.$eval(".lcat-resultcount", (e) => e.textContent.trim());
const cardIds = () => page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
const pagerVisible = () =>
  page
    .$$eval("ul.lcat-browse-pager", (els) => els.some((el) => el.offsetHeight > 0 && getComputedStyle(el).display !== "none"))
    .catch(() => false);
const staticPagerVisible = () =>
  page.$$eval("ul.pagination", (els) => els.some((el) => el.offsetHeight > 0 && getComputedStyle(el).display !== "none"));
// The active page's number carries aria-current; read it as the 1-based page.
const activePage = () =>
  page.$eval('.lcat-browse-pager a[aria-current="page"]', (a) => Number(a.textContent)).catch(() => null);
const clickRel = (rel) => page.click(`.lcat-browse-pager a[rel="${rel}"]`);
// prev/next are the first/last controls; a disabled one carries aria-disabled
// but no rel (it is not a real prev/next link), so read it by position.
const prevDisabled = () => page.$eval(".lcat-browse-pager li:first-child a", (a) => a.getAttribute("aria-disabled") === "true");
const nextDisabled = () => page.$eval(".lcat-browse-pager li:last-child a", (a) => a.getAttribute("aria-disabled") === "true");
const type = async (q) => {
  await page.fill('.lcat-search input[name="q"]', q);
  await page.waitForTimeout(1200);
};
// Wait until the count line settles on a given total, so an assertion never
// races the async page render.
const waitTotal = (total) =>
  page.waitForFunction((t) => {
    const el = document.querySelector(".lcat-resultcount");
    if (!el) return false;
    const nums = (el.textContent.match(/\d+/g) || []).map(Number);
    return nums[nums.length - 1] === t;
  }, total, { timeout: 15000 });

await page.goto(base + "/works/", { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 30000 });

// 0. Control: the static (corpus) pager is on screen before browse takes over,
//    and no browse pager exists yet.
check("static pager is on screen cold", await staticPagerVisible());
check("no browse pager cold", !(await pagerVisible()));

// ---- Query path: 400 matches over ceil(400/60) = 7 pages ----
const qPages = Math.ceil(EXPECT.query / PAGE);
const lastRemainder = EXPECT.query - (qPages - 1) * PAGE;

await type(QUERY);
await waitTotal(EXPECT.query);
check(`query "${QUERY}" matches ${EXPECT.query} over more than one page`, EXPECT.query > PAGE);
check("the browse pager appears once the result set exceeds a page", await pagerVisible());
check("the static corpus pager is hidden while browse owns the list", !(await staticPagerVisible()));
check("page one renders a full page of cards", (await cardIds()).length === PAGE);
check("page one count reads 'the first 60 of 400'", /first 60 of 400/.test(await countText()));
check("page one has no active previous control", await prevDisabled());

// Walk every page, collecting ids to prove the pages partition the set.
const seen = new Set();
let repeats = 0;
let walkedRemainder = null;
for (let p = 1; p <= qPages; p++) {
  const cur = await activePage();
  if (cur !== p) check(`walk: expected to be on page ${p}, am on ${cur}`, false);
  const ids = await cardIds();
  ids.forEach((id) => {
    if (seen.has(id)) repeats++;
    seen.add(id);
  });
  if (p === qPages) walkedRemainder = ids.length;
  if (p < qPages) {
    await clickRel("next");
    await page.waitForTimeout(600);
  }
}
check(`every one of the ${EXPECT.query} results is reachable across ${qPages} pages`, seen.size === EXPECT.query);
check("no work appears on two pages", repeats === 0);
check(`the last page holds the remainder (${lastRemainder})`, walkedRemainder === lastRemainder);
check("on the last page the next control is disabled", await nextDisabled());
check("the last page count reads a range, not 'the first'", /showing 361[–-]400 of 400/.test(await countText()));
check("the last page URL carries ?page=", /[?&]page=7\b/.test(page.url()));

// ---- Deep link: land straight on page 3 of the query result set ----
await page.goto(base + "/works/?q=" + encodeURIComponent(QUERY) + "&page=3", { waitUntil: "load" });
await waitTotal(EXPECT.query);
await page.waitForTimeout(400);
check("a ?page=3 deep link lands on page 3", (await activePage()) === 3);
check("the deep-linked page count is the right range", /showing 121[–-]180 of 400/.test(await countText()));
const p3ids = new Set(await cardIds());
check("the deep-linked page shows page-3 works, not page-1", ![...p3ids].some((id) => id.includes("wq0000")) && p3ids.size === PAGE);

// ---- Back/forward: page 3 -> click next -> back returns to page 3 ----
await clickRel("next");
await page.waitForTimeout(600);
check("clicking next from a deep link advances to page 4", (await activePage()) === 4);
await page.goBack();
await page.waitForTimeout(700);
check("the back button returns to page 3, not page 1", (await activePage()) === 3);

// ---- Facet-only path (no query): 500 matches paginate too ----
await page.goto(base + "/works/", { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 30000 });
await page.evaluate((s) => document.querySelector(`#lcat-browse-facets input[data-cat="${s}"]`).click(), SUBJECT);
await waitTotal(EXPECT.facetOnly);
check(`facet-only matches ${EXPECT.facetOnly}, more than one page`, EXPECT.facetOnly > PAGE);
check("the facet-only result set is paged, not capped at 60", await pagerVisible());
await clickRel("next");
await page.waitForTimeout(600);
const facetPage2 = await cardIds();
check("the facet-only path reaches results past the first page", facetPage2.length === PAGE && (await activePage()) === 2);

// ---- Clearing restores the static list AND the static pager ----
await page.evaluate((s) => document.querySelector(`#lcat-browse-facets input[data-cat="${s}"]`).click(), SUBJECT);
await page.waitForTimeout(900);
check("clearing tears the browse pager down", !(await pagerVisible()));
check("clearing brings the static corpus pager back", await staticPagerVisible());

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
