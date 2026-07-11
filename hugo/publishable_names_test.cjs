// Everything under hugo/assets/ is served to every visitor of every adopter's
// OPAC, comments and all -- Hugo publishes those files as-is. An internal project
// or institution name written in a source comment therefore ends up in the public
// bundle, where an adopter cannot remove it without forking the module
//.
//
// Templates under layouts/ are compiled, not served, so their `{{/* */}}`
// comments never reach a visitor and may say whatever is true. An HTML comment
// `<!-- ... -->` in a template *is* emitted verbatim, so those are checked too.
//
// This checks sources rather than a render: a render only exercises the assets a
// given site happens to load, and the name should not be written in the first
// place. Rendered pages also legitimately contain these letters inside opaque ids
// (a WorkID like w9qtvsqll3qhmo), which is why a grep over the built site cannot
// be strict and this one can.
//
// Usage: node publishable_names_test.cjs
"use strict";
const fs = require("fs");
const path = require("path");

// Names that must not appear in published source. Add sparingly, and only for
// things a reader of the public bundle should never see.
const FORBIDDEN = [
  { pattern: /\bqll\b/i, why: "an institution name; say what the mechanism does instead" },
  { pattern: /qllpoc/i, why: "an internal project name; 'an earlier POC' carries the same information" },
  { pattern: /queerbooks/i, why: "a deployment name; the module must not know its adopters" },
];

function walk(dir, out) {
  if (!fs.existsSync(dir)) return out;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

// htmlComments extracts only the `<!-- ... -->` spans of a template, which are the
// parts Hugo emits. A Go-template comment is compiled away and never served.
function htmlComments(src) {
  return (src.match(/<!--[\s\S]*?-->/g) ?? []).join("\n");
}

// assets/ ships byte-for-byte; layouts/ ships only its HTML comments.
// exampleSite is a reference site, not shipped to adopters; README.md is not
// published at all.
const targets = [
  ...walk(path.join(__dirname, "assets"), []).map((f) => ({ f, text: fs.readFileSync(f, "utf8") })),
  ...walk(path.join(__dirname, "layouts"), []).map((f) => ({ f, text: htmlComments(fs.readFileSync(f, "utf8")) })),
];
const files = targets;
const hits = [];

for (const { f, text } of targets) {
  text.split("\n").forEach((line, i) => {
    for (const { pattern, why } of FORBIDDEN) {
      if (pattern.test(line)) {
        hits.push({ file: path.relative(__dirname, f), line: i + 1, text: line.trim(), why });
      }
    }
  });
}

if (hits.length) {
  console.error(`${hits.length} internal name(s) in files Hugo publishes verbatim:\n`);
  for (const h of hits) {
    console.error(`  ${h.file}:${h.line}  ${h.why}`);
    console.error(`    ${h.text.slice(0, 100)}`);
  }
  process.exit(1);
}

console.log(`===== ${files.length} publishable files checked =====`);
console.log("No internal project or institution names in assets/ or layouts/.");
