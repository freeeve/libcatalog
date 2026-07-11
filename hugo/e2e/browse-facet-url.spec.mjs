// Real-browser E2E for facet selections in the URL -- the deferred
// half. A faceted browse page must be a shareable, back/forward-able
// deep link: the selection encodes into ?f=/?x= and reconstructs into the facet UI
// on a cold load, together with ?q= and ?page=, so the reader who shares or
// bookmarks a subject page lands on exactly that filtered set.
//
// Same 600-work fixture as browse-scope/browse-pager: query "lesbian" matches 400,
// the SUBJECT facet matches 500, query+facet 300. SUBJECT is a homosaurus root, so
// its tree row renders without expansion -- reconstruction only needs to check it.
//
// Usage: node browse-facet-url.spec.mjs <base-url>
import { SUBJECT, QUERY, EXPECT } from "./make-large-catalog.mjs";

const PAGE = 60;
const pwMod = await import(process.env.PLAYWRIGHT_PKG || "playwright");
const chromium = (pwMod.default ?? pwMod).chromium;

const base = process.argv[2];
if (!base) {
  console.error("usage: node browse-facet-url.spec.mjs <base-url>");
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

const fparam = "subject:" + SUBJECT;
const countText = () => page.$eval(".lcat-resultcount", (e) => e.textContent.trim());
const totalNum = async () => {
  const nums = (await countText()).match(/\d+/g) || [];
  return Number(nums[nums.length - 1]);
};
const cards = () => page.$$eval("#lcat-results a.lcat-result", (a) => a.length);
const facetChecked = () =>
  page.evaluate((s) => {
    const cb = document.querySelector(`#lcat-browse-facets input[data-cat="${s}"]`);
    return !!(cb && cb.checked);
  }, SUBJECT);
const activePage = () =>
  page.$eval('.lcat-browse-pager a[aria-current="page"]', (a) => Number(a.textContent)).catch(() => null);
const clickFacet = () =>
  page.evaluate((s) => document.querySelector(`#lcat-browse-facets input[data-cat="${s}"]`).click(), SUBJECT);
const waitTotal = (total) =>
  page.waitForFunction((t) => {
    const el = document.querySelector(".lcat-resultcount");
    if (!el) return false;
    const nums = (el.textContent.match(/\d+/g) || []).map(Number);
    return nums[nums.length - 1] === t;
  }, total, { timeout: 15000 });

// 1. Cold deep link to a facet-only selection: the facet reconstructs, the whole
//    filtered set is delivered (not the static list, not capped at 60).
await page.goto(base + "/works/?f=" + encodeURIComponent(fparam), { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 30000 });
await waitTotal(EXPECT.facetOnly);
check("a cold ?f= deep link reconstructs the facet (checkbox checked)", await facetChecked());
check(`the deep-linked facet delivers its full set (${EXPECT.facetOnly})`, (await totalNum()) === EXPECT.facetOnly);
check("the facet-only deep link is not capped at one page", (await cards()) === PAGE);

// 2. Cold deep link to a facet AND a page: lands on that page of the filtered set.
await page.goto(base + "/works/?f=" + encodeURIComponent(fparam) + "&page=2", { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 30000 });
await waitTotal(EXPECT.facetOnly);
await page.waitForTimeout(400);
check("a facet + ?page=2 deep link applies the facet", await facetChecked());
check("a facet + ?page=2 deep link lands on page 2", (await activePage()) === 2);
check("facet + page count reads a later-page range", /showing 61[–-]120 of 500/.test(await countText()));

// 3. Cold deep link to query AND facet: the intersection (300), both applied.
await page.goto(base + "/works/?q=" + encodeURIComponent(QUERY) + "&f=" + encodeURIComponent(fparam), { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 30000 });
await waitTotal(EXPECT.queryAndFacet);
check("a q + f deep link applies the facet", await facetChecked());
check(`a q + f deep link delivers the intersection (${EXPECT.queryAndFacet})`, (await totalNum()) === EXPECT.queryAndFacet);
check("the query box is populated from the deep link", (await page.$eval('.lcat-search input[name="q"]', (e) => e.value)) === QUERY);

// 4. In-session: clicking a facet writes ?f= into the URL, and a pager link
//    carries it so paging never drops the facet.
await page.goto(base + "/works/", { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 30000 });
await clickFacet();
await waitTotal(EXPECT.facetOnly);
check("clicking a facet encodes it into the URL as ?f=", /[?&]f=/.test(page.url()));
const nextHref = await page.$eval('.lcat-browse-pager a[rel="next"]', (a) => a.getAttribute("href"));
check("a pager link carries the facet so paging keeps it", /[?&]f=/.test(nextHref));

// 5. Back/forward across pages keeps the facet applied.
await page.click('.lcat-browse-pager a[rel="next"]');
await page.waitForTimeout(600);
check("paging within a facet advances to page 2 with the facet still applied", (await activePage()) === 2 && (await facetChecked()));
await page.goBack();
await page.waitForTimeout(700);
check("back returns to page 1 with the facet still applied", (await activePage()) === 1 && (await facetChecked()));
check("still the facet's full set after back", (await totalNum()) === EXPECT.facetOnly);

// 6. Clearing the facet drops ?f= from the URL and restores the static list.
await clickFacet();
await page.waitForTimeout(900);
check("clearing the facet removes ?f= from the URL", !/[?&]f=/.test(page.url()));
check("clearing the facet unchecks it", !(await facetChecked()));

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
