// Writes a catalog larger than one browse page, so the e2e can see the bug
// was about. fixture-catalog.json has three works; with a page size of
// 60, nothing there can distinguish "the base set is the match set" from "the
// base set is the first page of it".
//
// Shape, chosen so every number below is a different number -- a fixture where
// they coincide cannot fail informatively:
//
//   600 works in the corpus
//   400 match the query "lesbian"        (> PAGE, so the page is not the set)
//   300 match the query AND the facet    (> PAGE, so the filtered set is not a page either)
//   500 carry the facet                  (facet-alone, unaffected by the query path)
//
// The spec imports this module for SUBJECT/QUERY/EXPECT, so the file write is
// guarded: it happens only when this file is the program being run.
//
// Usage: node make-large-catalog.mjs <out.json>
import { writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

export const SUBJECT = "https://homosaurus.org/v3/homoit0000915";
export const QUERY = "lesbian";
export const EXPECT = { corpus: 600, query: 400, queryAndFacet: 300, facetOnly: 500 };

const subject = [{ id: SUBJECT, labels: { en: "LGBTQ+ people" }, scheme: "homosaurus" }];
const works = [];
for (let i = 0; i < 400; i++) {
  works.push({
    id: `wq${String(i).padStart(4, "0")}`,
    title: `Lesbian Poetry Volume ${i}`,
    languages: ["eng"],
    formats: ["book"],
    contributors: [{ name: "Poet, P." }],
    tags: ["poetry"],
    ...(i < 300 ? { subjects: subject } : {}),
  });
}
for (let i = 0; i < 200; i++) {
  works.push({
    id: `wn${String(i).padStart(4, "0")}`,
    title: `Gardening Manual ${i}`,
    languages: ["eng"],
    formats: ["book"],
    contributors: [{ name: "Gardener, G." }],
    tags: ["howto"],
    subjects: subject,
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const out = process.argv[2];
  if (!out) {
    console.error("usage: node make-large-catalog.mjs <out.json>");
    process.exit(2);
  }
  writeFileSync(out, JSON.stringify({ version: 10, works }));
  console.log(`wrote ${works.length} works to ${out}`);
}
