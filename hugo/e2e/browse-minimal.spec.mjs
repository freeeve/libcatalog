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

// 3a. Live counts paint the hydrated rows too (tasks/177): the ebook
//     survivor set is {House of the Spirits} (spa), so jpn greys to 0 and
//     spa recounts to 1 -- overriding the fragment's static numbers.
await page.waitForFunction(
  () => {
    const li = document.querySelector('.lcat-facets li[data-lcat-field="language"][data-lcat-cat="jpn"]');
    return li && li.querySelector(".lcat-count").textContent === "0";
  },
  { timeout: 10000 },
);
const liveRows = await page.evaluate(() => {
  const g = (c) => {
    const li = document.querySelector(`.lcat-facets li[data-lcat-field="language"][data-lcat-cat="${c}"]`);
    return li && { n: li.querySelector(".lcat-count").textContent, zero: li.classList.contains("lcat-count-zero") };
  };
  return { jpn: g("jpn"), spa: g("spa") };
});
check(
  "sidebar live counts: jpn 0 greyed, spa 1",
  liveRows.jpn && liveRows.jpn.n === "0" && liveRows.jpn.zero && liveRows.spa && liveRows.spa.n === "1" && !liveRows.spa.zero,
);

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
// 5a. Clearing restores the fragment's cold count and ungreys (tasks/177).
const coldRow = await page.evaluate(() => {
  const li = document.querySelector('.lcat-facets li[data-lcat-field="language"][data-lcat-cat="jpn"]');
  return li && { n: li.querySelector(".lcat-count").textContent, zero: li.classList.contains("lcat-count-zero") };
});
check("clearing restores the cold jpn count, ungreyed", coldRow && coldRow.n === "1" && !coldRow.zero);

// 6. Negative filter (tasks/173): hydration unhides the row's exclude toggle;
//    pressing it subtracts the category (ebook -> wexampletwo drops).
const notBtn = '.lcat-facets li[data-lcat-field="format"][data-lcat-cat="ebook"] .lcat-facet-not';
const notState = await page.$eval(notBtn, (b) => ({
  hidden: b.hidden,
  pressed: b.getAttribute("aria-pressed"),
  named: (b.getAttribute("aria-label") || "").length > 0,
}));
check(
  "hydrated row unhides its exclude toggle (labeled, unpressed)",
  !notState.hidden && notState.pressed === "false" && notState.named,
);
await page.click(notBtn);
await page.waitForSelector("#lcat-results a.lcat-result", { timeout: 10000 });
hrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check(
  "excluding ebook drops wexampletwo, keeps the other two",
  hrefs.length === 2 && !hrefs.some((h) => h.includes("wexampletwo")),
);

// 7. Include and exclude on one row are mutually exclusive: checking the
//    include toggle clears the exclusion and wins.
await page.click('.lcat-facets li[data-lcat-field="format"][data-lcat-cat="ebook"] input');
await page.waitForSelector('#lcat-results a.lcat-result[href*="wexampletwo"]', { timeout: 10000 });
const cleared = await page.$eval(notBtn, (b) => b.getAttribute("aria-pressed"));
hrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check(
  "checking the row clears its exclusion and includes ebook",
  cleared === "false" && hrefs.length === 1 && hrefs[0].includes("wexampletwo"),
);

// 8. Clearing the include restores the static list again.
await page.click('.lcat-facets li[data-lcat-field="format"][data-lcat-cat="ebook"] input');
await page.waitForTimeout(500);
const staticAgain = await page.$$eval("#lcat-results li", (lis) => lis.length);
check("clearing after negatives restores static list", staticAgain === staticLis);

// 9. The hydrated homosaurus group upgrades to a vocabulary tree (tasks/174):
//    only the root concept renders, count rolled up over the subtree.
await page.waitForSelector(".lcat-facets .lcat-facet-caret", { timeout: 10000 });
const sidebarTree = await page.$$eval('.lcat-facets li[data-lcat-cat*="homosaurus.org"]', (lis) =>
  lis.map((li) => ({
    label: li.querySelector(".lcat-facet-value").textContent,
    count: li.querySelector(".lcat-count").textContent,
    nested: !!li.closest("ul.lcat-facet-children"),
  })),
);
check(
  "sidebar homosaurus group treeifies to the root (Gender identity, 3)",
  sidebarTree.length === 1 && !sidebarTree[0].nested && sidebarTree[0].label === "Gender identity" && sidebarTree[0].count === "3",
);
// The root's unlabeled minted ancestor (homoit9999901, fixture) must not have
// rendered above it as a raw-URI row (tasks/176) -- covered by length === 1
// above; assert the id is absent anywhere in the sidebar for clarity.
const mintedRows = await page.$$eval('.lcat-facets li[data-lcat-cat$="homoit9999901"]', (lis) => lis.length);
check("unlabeled minted ancestor never renders in the sidebar", mintedRows === 0);

// 10. Excluding the root subtracts the whole subtree: every fixture work
//     carries a homosaurus concept at or under it, so nothing survives.
await page.click('.lcat-facets li[data-lcat-cat$="homoit0000282"] .lcat-facet-not');
await page.waitForSelector("#lcat-results .lcat-noresults", { timeout: 10000 });
check("excluding the root excludes descendant-tagged works too", true);
await page.click('.lcat-facets li[data-lcat-cat$="homoit0000282"] .lcat-facet-not');
await page.waitForTimeout(500);

// 11. Expanding the root reveals the narrower concept; selecting it filters,
//     and the selection survives a filter-input rebuild of the tree.
await page.click('.lcat-facets li[data-lcat-cat$="homoit0000282"] > .lcat-facet-caret');
await page.click('.lcat-facets li[data-lcat-cat$="homoit0000669"] input[data-cat]');
await page.waitForSelector('#lcat-results a.lcat-result[href*="wexampleone"]', { timeout: 10000 });
const sidebarFilter = '.lcat-facets details:has(.lcat-facet-caret) .lcat-facet-filter';
await page.fill(sidebarFilter, "gender");
await page.waitForTimeout(300);
const stillChecked = await page.$eval('.lcat-facets li[data-lcat-cat$="homoit0000669"] input[data-cat]', (cb) => cb.checked);
let treeHrefs = await page.$$eval("#lcat-results a.lcat-result", (as) => as.map((a) => a.getAttribute("href")));
check(
  "narrower selection filters results and survives a tree rebuild",
  stillChecked && treeHrefs.length === 2 && !treeHrefs.some((h) => h.includes("wexampletwo")),
);
await page.fill(sidebarFilter, "");
await page.waitForTimeout(300);
await page.click('.lcat-facets li[data-lcat-cat$="homoit0000669"] input[data-cat]');
await page.waitForTimeout(500);
const staticFinal = await page.$$eval("#lcat-results li", (lis) => lis.length);
check("clearing the tree selection restores the static list", staticFinal === staticLis);

if (errors.length) console.log("CONSOLE_ERRORS:", errors.slice(0, 5).join(" | "));
console.log(pass + " passed, " + fail + " failed");
await browser.close();
process.exit(fail === 0 && errors.length === 0 ? 0 : 1);
