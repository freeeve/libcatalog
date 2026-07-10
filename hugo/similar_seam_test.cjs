// The "more like this" rail's Shares line (libcat tasks/284, tasks/296, tasks/302).
//
// The sidecar names shared concepts by authority IRI, because a label is
// language-specific and the sidecar is not. page.html used to resolve those IRIs
// against the page's *own* subjects -- but the scorer reaches a neighbour through
// the concept tree and then reports the value the *neighbour* holds verbatim, so
// `shared` routinely names a concept the page does not carry. Those printed as
// bare authority URLs to readers: 5.6% of shared IRIs on a 62.6k-work catalog.
//
// Nothing could have caught it from inside a template. This builds the site for
// real, against a catalog whose every branch is deliberate, and reads the
// rendered Shares line the way a visitor does.
//
// tasks/302 then found two things that survived. Both are language- or
// punctuation-shaped, so both need a catalog where the schemes *disagree* about
// which languages they cover, and a label that carries a comma of its own.
// Assert in `es` as hard as in `en`: English was the one place the old code was
// accidentally right.
//
// Usage: node similar_seam_test.cjs   (requires `hugo` on PATH)
"use strict";
const fs = require("fs");
const os = require("os");
const path = require("path");
const { execFileSync } = require("child_process");

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log(`ok   - ${name}`);
  } catch (e) {
    failures++;
    console.error(`FAIL - ${name}\n       ${e.message}`);
  }
}
function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

const HOMO = "https://homosaurus.org/v3/";
const FAST = "http://id.worldcat.org/fast/";
const GENDER = HOMO + "homoit0000282"; // ancestor: labeled, but no Work carries it
const TRANS = HOMO + "homoit0000669"; // carried by both Works; has an es label
const TRANS_FAST = FAST + "1735592"; // the same concept, a different scheme, en only
const UNDESCRIBED = HOMO + "homoit0009999"; // a bare URI the graph never described
const DUTCH_ONLY = HOMO + "homoit0000777"; // described, but in no site language
const COMMA = FAST + "996602"; // a subject heading whose own text contains a comma
const PERSON = "Elledge, Jim"; // free text -- a contributor, and it has a comma too

// A catalog where the ancestor GENDER is described only by the Terms sideband --
// no Work carries it -- so a page can only label it from the catalog, never from
// itself. That is the shape that broke.
const catalog = {
  version: 12,
  terms: [
    { id: GENDER, labels: { en: "Gender identity", es: "Identidad de genero" } },
    { id: TRANS, labels: { en: "Transgender people", es: "Personas trans" }, broader: [GENDER] },
    { id: DUTCH_ONLY, labels: { nl: "Transgender personen" } },
    { id: COMMA, labels: { en: "Lesbians' writings, Canadian" } },
    { id: UNDESCRIBED }, // in the sideband, but with nothing to say
  ],
  works: [
    {
      id: "wone",
      title: "One",
      subjects: [{ id: TRANS, labels: { en: "Transgender people", es: "Personas trans" }, broader: [GENDER] }],
      // tasks/310. A print book: the unmarked carrier, so its tile wears no badge.
      // One contributor, with no role: the parenthetical must not render empty.
      formats: ["book"],
      contributors: [{ name: "Young, Eris" }],
    },
    {
      id: "wtwo",
      title: "Two",
      subjects: [
        { id: TRANS, labels: { en: "Transgender people", es: "Personas trans" }, broader: [GENDER] },
        { id: TRANS_FAST, labels: { en: "Transgender people" } },
      ],
      // The fully-dressed tile: a cover, a badged carrier, two roled contributors.
      extra: { cover: "covers/wtwo.jpg" },
      formats: ["audiobook"],
      contributors: [
        { name: "Chen, Angela", role: "author" },
        { name: "Naudus, Natalie", role: "narrator" },
      ],
    },
    { id: "wthree", title: "Three" }, // no subjects at all
    { id: "wfour", title: "Four", subjects: [{ id: COMMA, labels: { en: "Lesbians' writings, Canadian" } }] },
    // Two carriers: which one would the badge name? Neither. tasks/310.
    { id: "wfive", title: "Five", formats: ["ebook", "audiobook"] },
    // A rail deeper than the page opens with, so the reveal button renders.
    { id: "wsix", title: "Six" },
  ],
};

// The site shows 2 tiles, so wsix's four-neighbour rail has two to reveal and
// every other rail here (two neighbours at most) has none. Both branches, one
// build.
const SHOWN = 2;

const similar = {
  version: 1,
  limit: 8,
  works: {
    // The reported bug: GENDER is reached through the tree and carried by neither
    // page. Only the catalog can label it.
    // wfive rides along so a multi-carrier neighbour renders somewhere; it is
    // appended, so wtwo stays neighbour [0] for every check written before it.
    wone: [
      { id: "wtwo", title: "Two", shared: [TRANS, GENDER] },
      { id: "wfive", title: "Five", shared: [COMMA] },
    ],
    // The stutter: one concept in two schemes, both resolving to one label.
    // Plus free text (a tag), which is already human and passes through.
    wtwo: [{ id: "wone", title: "One", shared: [TRANS, TRANS_FAST, "lgbtq-books"] }],
    // A page with no subjects of its own still explains itself; and an IRI the
    // catalog cannot label is dropped rather than printed raw. DUTCH_ONLY has a
    // label, just not in a site language.
    wthree: [
      { id: "wone", title: "One", shared: [GENDER, DUTCH_ONLY] },
      { id: "wtwo", title: "Two", shared: [UNDESCRIBED] },
    ],
    // tasks/302. TRANS_FAST comes FIRST, and it is the member with no es label:
    // keeping the first occurrence would print English at a Spanish reader.
    // COMMA and PERSON each contain a comma of their own.
    wfour: [{ id: "wtwo", title: "Two", shared: [TRANS_FAST, TRANS, COMMA, PERSON] }],
    // Four neighbours against a cap of two: tiles 3 and 4 are the reveal (tasks/310).
    wsix: [
      { id: "wone", title: "One", shared: [TRANS] },
      { id: "wtwo", title: "Two", shared: [TRANS] },
      { id: "wfive", title: "Five", shared: [COMMA] },
      { id: "wfour", title: "Four", shared: [COMMA] },
    ],
  },
};

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "lcat-similar-seam-"));
const siteDir = path.join(tmp, "site");
fs.cpSync(path.join(__dirname, "exampleSite"), siteDir, { recursive: true });
// The site resolves the module by a relative replace; the copy must point back.
const gomod = path.join(siteDir, "go.mod");
fs.writeFileSync(gomod, fs.readFileSync(gomod, "utf8").replace("=> ../", `=> ${__dirname}`));
const assets = path.join(siteDir, "assets");
fs.writeFileSync(path.join(assets, "catalog.json"), JSON.stringify(catalog));
fs.writeFileSync(path.join(assets, "similar.json"), JSON.stringify(similar));
// facets.json is projected from the same catalog; an empty one keeps them honest.
fs.writeFileSync(path.join(assets, "facets.json"), JSON.stringify({ version: 12 }));

// An overlay rather than an edit to the copied hugo.toml: a second [params] table
// appended to a file that already has one is a TOML redefinition error, and later
// --config files win key by key.
const overlay = path.join(siteDir, "shown.toml");
fs.writeFileSync(overlay, `[params]\nsimilarShown = ${SHOWN}\n`);

const out = path.join(tmp, "public");
execFileSync("hugo", ["--quiet", "--config", "hugo.toml,shown.toml", "--destination", out], {
  cwd: siteDir,
  stdio: ["ignore", "ignore", "inherit"],
});

const page = (work, lang) =>
  fs.readFileSync(path.join(out, ...(lang === "en" ? [] : [lang]), "works", work, "index.html"), "utf8");
const decode = (s) => s.replace(/&#39;/g, "'").replace(/&amp;/g, "&").replace(/&quot;/g, '"');

// tiles returns each rendered neighbour <li> for a work, in rail order. The split
// token stops at the class name, not at the closing `">`: a tile past the reveal
// cap carries a second class, and a token that assumed one class silently returned
// zero of them while every "it is absent" check below went green.
const tiles = (work, lang) =>
  page(work, lang)
    .split('<li class="lcat-similar-item')
    .slice(1)
    .map((chunk) => chunk.slice(0, chunk.indexOf("</li>")));

// The reveal script is fingerprinted, so it is never literally "lcat-similar.js"
// in the built page. Asserting on that name would pass whether or not the script
// was loaded.
const SCRIPT = /lcat-similar\.[0-9a-f]{16,}\.js/;

// tile returns the one tile linking to a given neighbour, so a check names the
// work it means rather than an index into the rail.
const tile = (work, lang, neighbor) => {
  const href = `href="${lang === "en" ? "" : "/" + lang}/works/${neighbor}/"`;
  const found = tiles(work, lang).filter((t) => t.includes(href));
  assert(found.length === 1, `${lang} /works/${work}/: ${found.length} tiles link to ${neighbor}`);
  return found[0];
};

// whys returns the inner HTML of each rendered Shares line for a work. It scopes
// by the neighbour <li> rather than lazily matching to a closing tag: the terms
// are themselves <span>s now, so a lazy `</span>` match eats the last one.
//
// The line is visually hidden since tasks/310 -- the covers took its room -- but
// it is still in the document, and it is still the rail's only explanation of
// itself. Everything tasks/296 and tasks/302 proved about it still has to hold.
const WHY_OPEN = '<span class="lcat-visually-hidden">';
const whys = (work, lang) =>
  tiles(work, lang)
    .filter((item) => item.includes(WHY_OPEN))
    .map((item) => item.slice(item.indexOf(WHY_OPEN) + WHY_OPEN.length, item.lastIndexOf("</span>")));

// shares reads each Shares line as a list of terms. It reads the term *elements*,
// not a split on commas: a term may contain a comma, which is the whole of
// tasks/302 #2. A reader that split on commas could not tell the fix from the bug.
const shares = (work, lang) =>
  whys(work, lang).map((line) =>
    [...line.matchAll(/<span class="lcat-similar-term">([\s\S]*?)<\/span>/g)].map((m) => decode(m[1])),
  );

const cards = (work, lang) => tiles(work, lang).length;

// ---------------------------------------------------------------------------
// tasks/296 -- the rail must never print an authority URL at a reader.
// ---------------------------------------------------------------------------

check("no rendered Shares line contains a raw authority URL", () => {
  for (const lang of ["en", "es"]) {
    for (const w of ["wone", "wtwo", "wthree", "wfour"]) {
      for (const line of shares(w, lang)) {
        const raw = line.filter((t) => t.startsWith("http://") || t.startsWith("https://"));
        assert(raw.length === 0, `${lang} /works/${w}/ shows ${JSON.stringify(raw)}`);
      }
    }
  }
});

check("a concept reached through the tree, carried by neither page, is labeled", () => {
  // wone -> wtwo shares GENDER, which no Work in this catalog carries.
  assert(
    JSON.stringify(shares("wone", "en")[0]) === JSON.stringify(["Transgender people", "Gender identity"]),
    `got ${JSON.stringify(shares("wone", "en")[0])}`,
  );
});

check("the same concept in two schemes collapses to one term", () => {
  // TRANS and TRANS_FAST both label "Transgender people". It used to print twice.
  const line = shares("wtwo", "en")[0];
  assert(JSON.stringify(line) === JSON.stringify(["Transgender people", "lgbtq-books"]), `got ${JSON.stringify(line)}`);
});

check("free text passes through unresolved", () => {
  assert(shares("wtwo", "en")[0].includes("lgbtq-books"), "the tag was dropped or rewritten");
});

check("a page with no subjects of its own still explains its rail", () => {
  // The old code built its label map from $.Params.subjectList; wthree has none,
  // so every term on this page would have rendered as a URL.
  assert(shares("wthree", "en")[0][0] === "Gender identity", `got ${JSON.stringify(shares("wthree", "en")[0])}`);
});

check("an IRI the catalog cannot label is dropped, and its card keeps no reason", () => {
  // wthree's second neighbour shares only UNDESCRIBED: card renders, Shares does not.
  assert(cards("wthree", "en") === 2, `expected 2 neighbour cards, got ${cards("wthree", "en")}`);
  assert(shares("wthree", "en").length === 1, "the unlabelable neighbour still printed a Shares line");
});

check("a label in no site language still beats printing the URL", () => {
  // DUTCH_ONLY is described, just not in en/es. The lexically-first tag wins.
  assert(shares("wthree", "en")[0].includes("Transgender personen"), `got ${JSON.stringify(shares("wthree", "en")[0])}`);
});

check("labels resolve per language", () => {
  assert(
    JSON.stringify(shares("wone", "es")[0]) === JSON.stringify(["Personas trans", "Identidad de genero"]),
    `got ${JSON.stringify(shares("wone", "es")[0])}`,
  );
});

// ---------------------------------------------------------------------------
// tasks/302 -- collapse the concept, not the string it happens to render as;
// and render one term as one thing.
// ---------------------------------------------------------------------------

check("cross-scheme synonyms collapse in every language, not just English", () => {
  // TRANS has an es label; TRANS_FAST has only en. Deduping on the *displayed*
  // label collapsed them in en (both read "Transgender people") and left both in
  // es ("Personas trans", "Transgender people") -- the same concept twice, once
  // in a language the reader did not ask for.
  for (const lang of ["en", "es"]) {
    const line = shares("wfour", lang)[0];
    const trans = line.filter((t) => t === "Transgender people" || t === "Personas trans");
    assert(trans.length === 1, `${lang}: the concept appears ${trans.length}x -- ${JSON.stringify(line)}`);
  }
});

check("the surviving member of a collapsed group speaks the page's language", () => {
  // TRANS_FAST is first in the scorer's order and has no es label, so keeping the
  // first occurrence would print "Transgender people" at a Spanish reader.
  assert(shares("wfour", "es")[0][0] === "Personas trans", `es: got ${JSON.stringify(shares("wfour", "es")[0])}`);
  assert(shares("wfour", "en")[0][0] === "Transgender people", `en: got ${JSON.stringify(shares("wfour", "en")[0])}`);
});

check("a group with no member in the page's language still renders once", () => {
  // wtwo's rail shares TRANS + TRANS_FAST plus a tag. Whatever the language, the
  // group collapses to one term and the tag survives: two terms, never three.
  for (const lang of ["en", "es"]) {
    assert(shares("wtwo", lang)[0].length === 2, `${lang}: got ${JSON.stringify(shares("wtwo", lang)[0])}`);
  }
});

check("a label containing a comma is one term, not two", () => {
  // COMMA *is* the string "Lesbians' writings, Canadian"; PERSON is one person.
  for (const lang of ["en", "es"]) {
    const line = shares("wfour", lang)[0];
    assert(line.includes("Lesbians' writings, Canadian"), `${lang}: got ${JSON.stringify(line)}`);
    assert(line.includes(PERSON), `${lang}: the contributor split -- ${JSON.stringify(line)}`);
    assert(line.length === 3, `${lang}: expected 3 terms, got ${line.length}: ${JSON.stringify(line)}`);
  }
});

check("terms are separated by markup, never by a comma in the text", () => {
  // The separator is CSS (.lcat-similar-term + .lcat-similar-term::before). Were it
  // ever to move back into the text, a comma-bearing label reads as two again.
  const SENTINEL = "@@TERM@@";
  for (const lang of ["en", "es"]) {
    for (const w of ["wone", "wtwo", "wthree", "wfour"]) {
      for (const line of whys(w, lang)) {
        assert(line.includes('<span class="lcat-similar-term">'), `${lang} /works/${w}/: no term elements`);
        // Blank each term out; whatever survives between two blanks is the
        // separator. parts[0] is the leading "Shares:" label, which is text.
        const parts = line.replace(/<span class="lcat-similar-term">[\s\S]*?<\/span>/g, SENTINEL).split(SENTINEL);
        for (const gap of parts.slice(1)) {
          const text = gap.replace(/<[^>]*>/g, "");
          assert(text.trim() === "", `${lang} /works/${w}/: ${JSON.stringify(text)} separates two terms`);
        }
      }
    }
  }
});

// ---------------------------------------------------------------------------
// tasks/310 -- the rail is a shelf. Each tile carries the neighbour's cover, its
// carrier badge and its contributors, none of which the sidecar knows: they are
// read out of the catalog by work id. Every "it is absent" check below sits next
// to one asserting the same thing is present on another tile, because an adapter
// that looked up nothing at all would satisfy the absences alone.
// ---------------------------------------------------------------------------

check("a neighbour's cover is rendered from the catalog, not from the sidecar", () => {
  // similar.json carries only id/title/shared. The cover comes from wtwo's own
  // catalog entry, reached by id.
  const t = tile("wone", "en", "wtwo");
  assert(t.includes('src="/covers/wtwo.jpg"'), `no cover img in wtwo's tile: ${t}`);
});

check("a neighbour with no cover gets a placeholder of the same size", () => {
  const t = tile("wtwo", "en", "wone");
  assert(!t.includes("<img"), `wone has no cover, but its tile rendered an img: ${t}`);
  assert(t.includes("lcat-similar-cover--none"), `no placeholder in wone's tile: ${t}`);
});

check("the placeholder is not announced to a screen reader", () => {
  // "no cover image" is not information anyone wants read aloud; the title beneath
  // already names the work.
  const t = tile("wtwo", "en", "wone");
  assert(t.includes('aria-hidden="true"'), "the empty cover box is exposed to assistive tech");
});

check("a badged carrier names itself; the unmarked one does not", () => {
  const audio = tile("wone", "en", "wtwo");
  assert(audio.includes('<span class="lcat-similar-badge">audiobook</span>'), `no badge on the audiobook: ${audio}`);
  const book = tile("wtwo", "en", "wone");
  assert(!book.includes("lcat-similar-badge"), `a print book wore a badge: ${book}`);
});

check("a neighbour with two carriers wears no badge rather than an arbitrary one", () => {
  const t = tile("wone", "en", "wfive");
  assert(!t.includes("lcat-similar-badge"), `wfive has two formats but a badge was chosen: ${t}`);
});

check("contributors render with their roles, and without empty parentheses", () => {
  const roled = tile("wone", "en", "wtwo");
  const by = roled.match(/<span class="lcat-similar-by">([\s\S]*?)<\/span>/);
  assert(by, `no contributor line on wtwo's tile: ${roled}`);
  assert(
    decode(by[1]).trim() === "Chen, Angela (author), Naudus, Natalie (narrator)",
    `got ${JSON.stringify(decode(by[1]).trim())}`,
  );
  const roleless = tile("wtwo", "en", "wone");
  const one = decode(roleless.match(/<span class="lcat-similar-by">([\s\S]*?)<\/span>/)[1]).trim();
  assert(one === "Young, Eris", `a roleless contributor rendered as ${JSON.stringify(one)}`);
});

check("a neighbour with no contributors renders no contributor line", () => {
  const t = tile("wone", "en", "wfive");
  assert(!t.includes("lcat-similar-by"), `wfive has no contributors but got a byline: ${t}`);
});

check("the tile links to the neighbour, cover and caption inside one link", () => {
  // One <a> per tile: a cover and a title that are separate links are two tab
  // stops and two announcements for one destination.
  const t = tile("wone", "en", "wtwo");
  assert((t.match(/<a /g) ?? []).length === 1, `expected one link per tile: ${t}`);
  assert(t.indexOf("lcat-similar-art") > t.indexOf("<a "), "the cover sits outside the link");
  assert(t.indexOf("lcat-similar-title") > t.indexOf("lcat-similar-art"), "the caption precedes the cover");
});

check("a rail no deeper than the page shows renders no button and no script", () => {
  // wone has two neighbours and similarShown defaults to 8. A "View more" that
  // reveals nothing is worse than no button.
  const p = page("wone", "en");
  assert(!p.includes("lcat-similar-more"), "a button was rendered for a rail with nothing to reveal");
  assert(!SCRIPT.test(p), "the reveal script was loaded with nothing to reveal");
  assert(!p.includes("data-similar-shown"), "the rail advertised a cap it does not need");
});

check("the tiles past the cap are NOT hidden in the markup", () => {
  // If the extras carried `hidden` in the HTML, a reader whose script did not run
  // would lose half the rail with no way to get it back. The script hides them;
  // the document does not. This is the one that matters, and it is invisible to a
  // browser test that always runs the script.
  const all = tiles("wsix", "en");
  assert(all.length === 4, `expected 4 tiles, got ${all.length}`);
  const extras = all.filter((t) => t.startsWith(" lcat-similar-item--extra"));
  assert(extras.length === 2, `expected 2 tiles past a cap of ${SHOWN}, got ${extras.length}`);
  for (const t of extras) {
    // The chunk starts mid-<li> tag; everything up to the first ">" is the rest
    // of that tag, which is where a `hidden` attribute would sit.
    const tag = t.slice(0, t.indexOf(">"));
    assert(!/\shidden/.test(tag), `a tile past the cap is hidden without JS: <li class="lcat-similar-item${tag}>`);
  }
});

check("a rail deeper than the page shows renders the button, hidden, with its script", () => {
  const p = page("wsix", "en");
  assert(p.includes(`data-similar-shown="${SHOWN}"`), "the rail does not name its cap");
  assert(
    /<button[^>]*class="lcat-similar-more"[^>]*hidden/.test(p),
    `the button is not hidden for no-JS: ${p.match(/<button[\s\S]{0,120}/)}`,
  );
  assert(SCRIPT.test(p), "the reveal script was not loaded");
});

check("the button is localized", () => {
  assert(page("wsix", "en").includes(">View more</button>"), "en button text");
  assert(page("wsix", "es").includes(">Ver más</button>"), "es button text");
});

check("the rail still explains itself, in the document, on every tile that can", () => {
  // The regression tasks/310 could have introduced: drop the Shares line with the
  // markup it lived in. It is hidden, not gone -- and tasks/296's whole finding
  // was that this line is the rail's only claim to being cataloging.
  const t = tile("wone", "en", "wtwo");
  assert(t.includes(WHY_OPEN), `the neighbour tile carries no explanation at all: ${t}`);
  assert(shares("wone", "en")[0].length === 2, `got ${JSON.stringify(shares("wone", "en")[0])}`);
});

fs.rmSync(tmp, { recursive: true, force: true });
console.log(failures === 0 ? "all similar-rail seam tests passed" : `${failures} failed`);
process.exit(failures === 0 ? 0 : 1);
