// Real-browser E2E for the scope of the browse base set.
//
// lcat-browse.js built its base set with `catalog.search(q, 0, PAGE, 0, [])`,
// where PAGE = 60 is a *page length*. So every facet the reader picked was
// intersected with the first sixty ranked hits rather than with the result set:
// on a real catalog, 51 results where the truth was 8307. The rail had already
// shown 8307 -- search() computes facetCounts over every hit regardless of the
// page it returns -- and then, on the click, recomputed itself from the sixty
// and rewrote 8307 to 51. There is no state in which both numbers are on screen,
// which is why this survived: a probe that reads the rail *after* the click
// measures the number the click just wrote, and always passes.
//
// So this spec reads the rail BEFORE the click, and checks the UI against ground
// truth taken from a second RrsCatalog booted in-page from the same artifacts --
// the OPAC's own engine, so no cross-engine comparison is involved.
//
// Usage: node browse-scope.spec.mjs <base-url>
import { SUBJECT, QUERY, EXPECT } from "./make-large-catalog.mjs";

const pwMod = await import(process.env.PLAYWRIGHT_PKG || "playwright");
const chromium = (pwMod.default ?? pwMod).chromium;

const base = process.argv[2];
if (!base) {
  console.error("usage: node browse-scope.spec.mjs <base-url>");
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

await page.goto(base + "/works/", { waitUntil: "load" });
await page.waitForSelector("#lcat-browse-facets details", { timeout: 30000 });

// The static list is the exampleSite's own few works; the browse artifacts are
// the 600-work fixture. Cold state belongs to the former, browse to the latter.
const coldText = (await page.$eval(".lcat-resultcount", (e) => e.textContent)).trim();

// Ground truth, from the reader underneath the page.
const truth = await page.evaluate(async ({ b, subj, q }) => {
  const m = await import("/lcat/roaringrange.js");
  await m.default();
  const cat = await m.RrsCatalog.openAll(
    b + "/browse-index.rrs",
    b + "/browse-facets.rrsf",
    b + "/browse-records.idx",
    b + "/browse-records.bin",
  );
  const F = [["subject", subj]];
  const BIG = 1000000;
  const ids = async (query, len, f) => (await cat.search(query, 0, len, 0, f)).ids.length;
  const rail = async (len) => {
    const r = await cat.search(q, 0, len, 0, []);
    const f = r.facetCounts.find((x) => x.field === "subject");
    const c = f && f.cats.find((c) => c.name === subj);
    return c ? c.count : null;
  };
  return {
    page60: await ids(q, 60, []),
    query: await ids(q, BIG, []),
    queryAndFacet: await ids(q, BIG, F),
    railFromPage: await rail(60),
    railFromFull: await rail(BIG),
  };
}, { b: "/search", subj: SUBJECT, q: QUERY });

const total = async () => {
  const t = await page.$eval(".lcat-resultcount", (e) => e.textContent);
  const nums = t.match(/\d+/g) || [];
  return { text: t.trim(), total: Number(nums[nums.length - 1]) };
};
const cards = () => page.$$eval("#lcat-results a.lcat-result", (a) => a.length);
const railCount = () =>
  page.evaluate((s) => {
    const cb = document.querySelector(`#lcat-browse-facets input[data-cat="${s}"]`);
    const li = cb && cb.closest("li");
    return li ? Number(li.querySelector(".lcat-count").textContent) : null;
  }, SUBJECT);
const clickFacet = () =>
  page.evaluate((s) => document.querySelector(`#lcat-browse-facets input[data-cat="${s}"]`).click(), SUBJECT);
// Ask the screen, not the DOM attribute. This read `!nav.hidden` and
// so passed for two releases over a paginator that was on screen and clickable:
// `hidden` hides only via the UA `[hidden] { display: none }` rule, and lcat.css's
// `.pagination { display: flex }` outranked it. The attribute was set the whole
// time. offsetHeight is what the reader has.
const pagerVisible = () =>
  page.$$eval("ul.pagination", (els) =>
    els.some((el) => el.offsetHeight > 0 && getComputedStyle(el).display !== "none"),
  );
const type = async (q) => {
  await page.fill('.lcat-search input[name="q"]', q);
  await page.waitForTimeout(1500);
};

// 0. Controls. The fixture must actually distinguish the two hypotheses, and the
//    reader must have booted -- otherwise every assertion below is vacuous.
check(
  `fixture: corpus ${EXPECT.corpus}, query ${truth.query}, query+facet ${truth.queryAndFacet}, facet ${EXPECT.facetOnly}`,
  truth.query === EXPECT.query && truth.queryAndFacet === EXPECT.queryAndFacet,
);
check("the query matches more works than one page holds (else nothing can fail)", truth.query > 60 && truth.queryAndFacet > 60);
check("the reader booted: a 60-long page really is only a page", truth.page60 === 60);
// The engine's facetCounts ignore `len` -- this is the fact the module misread.
check(
  `facetCounts are the same whether the page is 60 or all (${truth.railFromPage} == ${truth.railFromFull})`,
  truth.railFromPage === truth.railFromFull && truth.railFromPage === EXPECT.queryAndFacet,
);
// The pager must exist cold, or check 6 below passes for the wrong reason.
const coldPager = await pagerVisible();
check("the static pager is on screen before browse takes over", coldPager);

// 1. Query only: the count is the whole match set, and exact -- no "+".
await type(QUERY);
const q1 = await total();
check(`query-only total is the match set: ${q1.text}`, q1.total === truth.query);
check("query-only renders one page of cards", (await cards()) === 60);
check("the count says the list is a page of a larger set", /first 60 of 400/.test(q1.text));
check("the count carries no '+' suffix", !q1.text.includes("+"));

// 2. The rail's promise, sampled BEFORE the click. This is the assertion with
//    teeth; sampled after, it is trivially true.
const promised = await railCount();
check(`rail promises the truth before the click (${promised})`, promised === truth.queryAndFacet);

// 3. Click it: the delivered set must be what was promised.
await clickFacet();
await page.waitForTimeout(1500);
const q2 = await total();
check(`query+facet delivers what the rail promised: ${q2.total} == ${promised}`, q2.total === promised);
check(`query+facet matches the reader: ${q2.total} == ${truth.queryAndFacet}`, q2.total === truth.queryAndFacet);
check("a filtered set larger than a page is not truncated to one", q2.total > 60);

// 4. The rail must not be silently rewritten to a smaller number by the click.
//    That rewrite is the signature of this class of bug: it destroys the evidence.
const after = await railCount();
check(`rail is not overwritten by a smaller count after the click (${promised} -> ${after})`, after === promised);

// 5. Facet alone was always correct -- its base is every doc id. Keep it honest.
await type("");
await page.waitForTimeout(1000);
const f1 = await total();
check(`facet alone: ${f1.total} == ${EXPECT.facetOnly}`, f1.total === EXPECT.facetOnly);

// 6. The static pager pages the unfiltered corpus and drops the reader's query.
//    It must not be reachable while browse owns the list -- not merely carry the
// attribute that is supposed to hide it.
const pagerState = await page.$$eval("ul.pagination", (els) =>
  els.map((el) => `hidden=${el.hasAttribute("hidden")} display=${getComputedStyle(el).display} h=${el.offsetHeight}`),
);
check(`static pager is off screen while browse owns the list [${pagerState}]`, !(await pagerVisible()));

// 7. Clearing everything restores the static list, its count, and the pager.
await clickFacet();
await page.waitForTimeout(1200);
const back = (await total()).text;
check(`clearing restores the static count (${back})`, back === coldText);
check("clearing brings the static pager back", await pagerVisible());

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
