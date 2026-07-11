// Dev-only internal-link checker. Walks a built Hugo site and asserts every
// root-relative link resolves to a generated file -- catching facet/term links whose
// slug does not match the page Hugo minted (e.g. a `+` in a subject/tag label that a
// CDN would 404). Not shipped: Hugo consumes only the templates and assets, never this.
//
// It cannot catch a `/` in a term key, and no version of it ever could. By
// the time a key is a path, the separator that does not belong is indistinguishable from
// the ones that do -- and the link *resolves*, because Hugo genuinely minted the nested
// page. A URL can be well-formed on disk and hostile on a host; only the first is what
// this measures. That check lives where the key still exists as a key: the content
// adapter refuses to index any taxonomy value containing `/`.
//
// It checks `src` as well as `href`, and it fails on any *document-relative* reference
// (one with no leading slash). Both gaps hid every cover rendered
// `src="covers/<id>.jpg"`, which resolves against the page's own directory, so the same
// string 404'd differently on every page -- and this file scanned only `href`, then
// skipped anything not starting with `/`. It passed clean on a build where no cover
// loaded. A relative reference is never right in this site: Hugo's relURL emits a leading
// slash, so one appearing in the output means a template forgot to call it.
//
// Usage: node link_check.cjs <built-site-dir>   (e.g. exampleSite/public)
"use strict";
const fs = require("fs");
const path = require("path");

const root = process.argv[2] || "exampleSite/public";

function walk(dir, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith(".html")) out.push(p);
  }
  return out;
}

// Decode the few HTML entities Hugo emits inside href values so a literal `+` (`&#43;`)
// or `&` (`&amp;`) is checked as the character a browser would send.
function decode(s) {
  return s
    .replace(/&#43;/g, "+")
    .replace(/&#x2b;/gi, "+")
    .replace(/&amp;/g, "&");
}

// Percent-decode a path the way a browser/server does before hitting the
// filesystem: Hugo keeps Unicode letters in a taxonomy slug (lcat-slug) and
// percent-encodes them in hrefs, so a term page minted at ".../género/" is linked
// as ".../g%C3%A9nero/". Matching the raw href against the literal-Unicode filename
// would spuriously 404. Malformed sequences fall back to the raw string.
function pctDecode(s) {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

function resolves(href) {
  const clean = decode(href).split("#")[0].split("?")[0];
  if (!clean.startsWith("/") || clean.startsWith("//")) return true; // external/protocol-relative: skip
  const rel = pctDecode(clean.slice(1));
  const target = /\.[a-z0-9]+$/i.test(clean)
    ? path.join(root, rel) // links to a file (e.g. .xml)
    : path.join(root, rel, "index.html"); // pretty dir URL
  return fs.existsSync(target);
}

const files = walk(root, []);
const broken = [];
const plusPaths = new Set();
const relative = [];
const refRe = /(?:href|src)="([^"]+)"/g;

for (const f of files) {
  const html = fs.readFileSync(f, "utf8");
  let m;
  while ((m = refRe.exec(html)) !== null) {
    const href = m[1];
    if (/^(https?:|mailto:|tel:|#|data:)/i.test(href)) continue;
    const clean = decode(href).split("#")[0].split("?")[0];
    if (clean.startsWith("//")) continue; // protocol-relative: an external host
    if (clean !== "" && !clean.startsWith("/")) {
      relative.push({ file: path.relative(root, f), href: decode(href) });
      continue;
    }
    if (!clean.startsWith("/")) continue;
    // /pagefind/ assets are emitted by the post-build `pagefind` step, not Hugo, so they
    // legitimately do not exist in a Hugo-only build -- skip them here.
    if (clean.startsWith("/pagefind/")) continue;
    // The bug this guards: an unsafe `+` left in a facet/term path (CDN 404).
    if (clean.includes("+")) plusPaths.add(clean);
    if (!resolves(href)) broken.push({ file: path.relative(root, f), href: decode(href) });
  }
}

let failed = false;
if (plusPaths.size) {
  failed = true;
  console.error(`Found ${plusPaths.size} link path(s) containing a literal '+' (CDN-unsafe):`);
  for (const p of [...plusPaths].sort()) console.error(`  ${p}`);
}
if (broken.length) {
  failed = true;
  console.error(`\n${broken.length} broken internal link(s):`);
  const seen = new Set();
  for (const b of broken) {
    const key = b.href;
    if (seen.has(key)) continue;
    seen.add(key);
    console.error(`  ${b.href}  (e.g. in ${b.file})`);
  }
}

if (relative.length) {
  failed = true;
  console.error(`\n${relative.length} document-relative reference(s) -- these resolve against the page's own directory, so they 404 differently on every page:`);
  const seen = new Set();
  for (const r of relative) {
    if (seen.has(r.href)) continue;
    seen.add(r.href);
    console.error(`  ${r.href}  (e.g. in ${r.file})`);
  }
}

if (failed) process.exit(1);
console.log(`===== ${files.length} pages checked =====`);
console.log("All internal href/src references resolve, are root-relative, and carry no CDN-unsafe '+' paths.");
