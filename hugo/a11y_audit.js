/*
  Accessibility audit for the libcat Hugo module (WCAG 2.1 A/AA), run with
  axe-core under jsdom over a *built* site. Dev tooling only -- Hugo never consumes
  this; it ships no runtime dependency.

  Usage:
    cd exampleSite && hugo --destination public   # or any built libcat site
    cd .. && npm install && node a11y_audit.js exampleSite/public

  It walks every .html under the given directory and reports violations, exiting
  non-zero if any are found (so it can gate CI). The color-contrast rule is disabled:
  jsdom does no layout, so contrast can't be computed here -- verify it in a real
  browser (Lighthouse / axe DevTools) as a separate check.
*/
const fs = require("fs");
const path = require("path");
const { JSDOM, VirtualConsole } = require("jsdom");

const root = process.argv[2];
if (!root) {
  console.error("usage: node a11y_audit.js <built-site-dir>");
  process.exit(2);
}

// walk collects every .html file under dir, relative to it. Fragment assets
// under /lcat/ (the shared facet sidebar) are not documents --
// they are injected into an audited page -- so they are skipped, not audited
// for document-level rules they can never satisfy.
function walk(dir, base, out) {
  for (const name of fs.readdirSync(dir)) {
    const full = path.join(dir, name);
    const st = fs.statSync(full);
    if (st.isDirectory()) walk(full, base, out);
    else if (name.endsWith(".html") && path.relative(base, dir) !== "lcat") out.push(path.relative(base, full));
  }
  return out;
}

async function auditFile(rel) {
  const html = fs.readFileSync(path.join(root, rel), "utf8");
  const vc = new VirtualConsole(); // swallow jsdom's "not implemented" noise
  const dom = new JSDOM(html, { url: "https://example.org/" + rel, pretendToBeVisual: true, virtualConsole: vc });
  global.window = dom.window;
  global.document = dom.window.document;
  global.navigator = dom.window.navigator;
  global.Node = dom.window.Node;
  global.Element = dom.window.Element;
  global.NodeList = dom.window.NodeList;
  global.getComputedStyle = dom.window.getComputedStyle;
  delete require.cache[require.resolve("axe-core")];
  const axe = require("axe-core");
  const results = await axe.run(dom.window.document, {
    resultTypes: ["violations"],
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "best-practice"] },
    rules: { "color-contrast": { enabled: false } },
  });
  dom.window.close();
  return results.violations;
}

(async () => {
  const pages = walk(root, root, []).sort();
  let total = 0;
  const byRule = {};
  for (const rel of pages) {
    let v;
    try {
      v = await auditFile(rel);
    } catch (e) {
      console.log(`### ${rel}\n  audit error: ${e.message}`);
      process.exitCode = 2;
      continue;
    }
    if (!v.length) continue;
    console.log(`\n### ${rel}`);
    for (const x of v) {
      total += x.nodes.length;
      byRule[x.id] = (byRule[x.id] || 0) + x.nodes.length;
      console.log(`  [${x.impact}] ${x.id}: ${x.help}`);
      x.nodes.slice(0, 5).forEach((n) => console.log(`      @ ${n.target.join(" ")}`));
      if (x.nodes.length > 5) console.log(`      ...+${x.nodes.length - 5} more`);
    }
  }
  console.log(`\n===== ${pages.length} pages audited =====`);
  if (!total) console.log("No WCAG 2.1 A/AA violations (color-contrast excluded -- check in a real browser).");
  else Object.entries(byRule).sort((a, b) => b[1] - a[1]).forEach(([r, n]) => console.log(`  ${n}\t${r}`));
  if (!process.exitCode) process.exitCode = total ? 1 : 0;
})();
