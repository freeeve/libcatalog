# Diversity audit

libcat can report on representation in a collection along two axes:

- **Content**: what the works are *about*, derived from their subject headings
  and tags. Fully local -- no network, no personal data; it describes book
  topics, not people.
- **Creators**: who *made* the works, derived by resolving creators through
  cataloged identifiers to Wikidata and copying the demographic claims
  Wikidata states outright. Opt-in, rate-limited, cached with provenance.

Both are **coverage-first**: every figure is reported against an explicit
denominator, because the honest headline of most audits is how much of the
collection they can speak for at all.

## Running the content audit

```sh
# full corpus (includes works the public projection suppresses)
lcat diversity-audit --graph catalog.nq

# the public view (what the OPAC shows)
lcat diversity-audit --catalog catalog.json

# scope to a sub-collection by a work extra, e.g. one institution's holdings
lcat diversity-audit --graph catalog.nq --filter inQll=true
lcat diversity-audit --graph catalog.nq --source qll

# json for dashboards; the report names its input mode and scope
lcat diversity-audit --graph catalog.nq --format json
```

The cataloging backend serves the same report live at
`GET /v1/audit/diversity` (librarian role; `?filter=k=v` and `?source=`), and
the admin UI renders it on the **Diversity** screen. The live report covers
the cataloging corpus: suppressed works included (they are held, just not
published), tombstoned works excluded (they are retired).

## How a work is categorized

A work counts toward a category when **any** of its subjects or tags matches
the category's crosswalk entry, by any of three dimensions:

- **URI**: an exact subject-authority URI (LCSH, FAST, Homosaurus...).
- **Scheme**: the subject's whole vocabulary, for vocabularies that are
  category-relevant wholesale -- the seed maps every `homosaurus`-scheme
  subject to LGBTQIA+.
- **Keyword**: a case-insensitive whole-word/phrase match against the heading
  text, tolerating plural inflections one-directionally ("Lesbians" matches
  the keyword `lesbian`; write keywords in the singular). Derived noun forms
  are separate entries ("Homosexuality" does not match `homosexual`).

Uncontrolled **tags count as aboutness signal** alongside controlled
subjects. An ILS feed carries a genre heading as a bare string, a
direct-BIBFRAME feed as a tag, a well-cataloged record as a FAST URI -- the
audit treats these as the same statement about the work so results are
comparable across cataloging depth.

A work is **covered** when it carries at least one subject or tag; only
covered works can be categorized. Each category reports its share of covered
works (the meaningful ratio) and of the whole collection (the dilution).
Improving cataloging depth moves these numbers as much as collection
development does -- that is a feature of an honest audit, not a flaw.

## The taxonomy is an editorial choice

The shipped seed (`diversity/crosswalk.toml`) is conservative and
US-collection-oriented: women & gender, BIPOC, Indigenous peoples, LGBTQIA+,
disability & neurodiversity, religion & faith communities, immigrants & diaspora,
economic hardship. It is a starting point, not a truth. Operators tune it
with an override file:

```toml
# my-crosswalk.toml -- merged over the seed by category id:
# keywords/uris/schemes union in; a non-empty label replaces; new ids append.
[[category]]
id = "lgbtqia"
keywords = ["achillean"]                # add local vocabulary
uris = ["https://homosaurus.org/v5/homoit0000123"]

[[category]]
id = "veterans"                          # add a category the seed lacks
label = "Veterans & military families"
keywords = ["veterans", "military families"]
```

```sh
lcat diversity-audit --graph catalog.nq --crosswalk my-crosswalk.toml
```

For a vocabulary the deployment has loaded, a category can follow the
hierarchy instead of enumerating URIs by hand:

```toml
[[category]]
id = "lgbtqia-lesbian"
label = "Lesbian"
roots = ["https://homosaurus.org/v5/homoit0000556",   # Lesbians
         "https://homosaurus.org/v5/homoit0002277"]   # Sapphics
keywords = ["lesbian", "sapphic", "butch"]
```

`roots` is sugar for "these URIs plus everything skos:narrower beneath
them", expanded at audit time from the loaded scheme -- so the facet
self-maintains as the vocabulary (or a peer-harvest) introduces new
descendant terms. Make roots a small CURATED set, not one URI: closure only
descends, so siblings and high parents each need their own root. And keep
keywords: concept/-ism terms with no broader edge ("Lesbianism") are
unreachable by closure and match by label only. Roots, uris, and keywords
union. The CLI's `--graph` mode expands from the graph's own skos:broader
edges; `--catalog` mode has no hierarchy, so roots there match only
themselves.

A zero in a category can mean a genuine collection gap *or* vocabulary the
crosswalk does not know. When a community vocabulary is central to your
collection, prefer scheme- or URI-level entries over keywords; the
queerbooks deployment's override is a worked example of splitting the flat
LGBTQIA+ umbrella into finer identity facets.

### Configuring the crosswalk from the admin UI

The same override persists server-side: the **Diversity setup** screen
(linked from the Diversity screen, `#/diversity/config`) edits it, and
`GET /v1/audit/diversity` merges it over the seed on every report, so the
on-screen audit renders the operator's categories without a redeploy. The
stored document is the identical TOML dialect `--crosswalk` reads
(`PUT/GET/DELETE /v1/audit/diversity/crosswalk`, librarian role), so a file
maintained for the CLI pastes straight in and the server's copy pastes
straight out.

Rather than hand-typing authority URIs, the screen's facet builder surfaces
the corpus's actual subject terms as a work-count histogram
(`GET /v1/audit/terms`: controlled URIs, heading labels, and tags, each with
its count in the current scope). Check the terms that belong together, send
them to a category -- URIs become exact matches, labels become keywords --
and preview the resulting counts (`POST /v1/audit/diversity/preview`)
before saving. The counts make the editorial call concrete: a term carried
by 80 works may deserve its own facet; one carried by 7 probably doesn't.

Override semantics are the same as the file's: extend or relabel a seed
category, or append new ones. Seed matching is never removable, and facet
categories overlap by design -- they are lenses over the collection, not a
partition of it.

## The creator axis

Off by default. Enable on the cataloging backend with:

```sh
LCATD_ENRICH_WIKIDATA=direct   # the only mode; see below
```

and trigger a run with `POST /v1/enrich/wikidata/run` (admin role; `GET
/v1/enrich` lists the configured sources). A corpus-scale run can hold that
request open for a long time; the async alternative is
`POST /v1/enrich/wikidata/jobs` (same `?filter`/`?source` scoping), which
returns a job id immediately -- a background worker executes it, and
`GET /v1/enrich/jobs/{id}` polls the record with the run's **live batch
counters** while it is in flight (`GET /v1/enrich/jobs` lists recent jobs;
records expire after a week). For each work the run
resolves creators **through cataloged identifiers only**: a Wikidata entity
URI on the agent binds directly (no hop at all), author authority ids hop
through their properties (VIAF P214, LCNAF P244, ISNI P213, ORCID P496),
and only works those passes leave unresolved fall back to ISBN -> Wikidata
edition (P212/P957) -> work (P629) -> author (P50). It then copies the claims
Wikidata states explicitly -- P21 (sex or gender), P27 (country of
citizenship), P91 (sexual orientation), P172 (ethnic group) -- into the
`enrichment:wikidata` graph with the entity QID, the identifier that matched,
and the retrieval date. Re-running refreshes; a claim retracted upstream
disappears here too.

The rules, in order:

1. **No name inference, ever.** A creator's name is never matched against
   anything, and no demographic is ever guessed from a name, photo, or title.
   A work without a resolvable identifier yields nothing.
2. **Explicit claims only, with provenance.** Only statements Wikidata makes
   outright are copied, each traceable to its entity and retrieval date.
   Birth and death dates are deliberately not fetched.
3. **Aggregate use.** The data exists for collection-level distributions.
   The projector does not surface it on work pages, and there is no
   moderation-queue mode because these are entity statements, not subject
   candidates -- hence `direct` is the only mode.

Once claims are cached, the **Diversity** screen (and the
`/v1/audit/diversity` response's `creators` block) reports the aggregate:
match rate first, then each property's value distribution over distinct
resolved creators with the not-stated remainder alongside. No person is ever
named in the report.

**Why Wikidata and not the name authorities:** LCNAF/NACO records carry
names, dates, places, occupations, and languages -- identifiers, not
demographics. The one demographic field it had (MARC 375, gender) was
formally retired by the PCC in April 2022 -- do not record, delete on edit --
for the same privacy and misgendering reasons this feature's rules encode;
ethnicity and orientation were never systematically recorded (LCDGT terms in
bib-level 385/386 are sparse and describe records, not persons). Name
authorities remain excellent resolution identifiers; they are not a claims
source.

Expect low match rates and read them first. Wikidata's book coverage is
thin and skewed: most editions have no ISBN item at all, ~82% of humans with
a gender claim are "male", and non-Western and Indigenous creators are
underrepresented. An unknown is an unknown; the audit must never backfill it.

## Benchmarks: comparison points, not targets

There is no standard target percentage for any category; the audit
literature deliberately avoids quotas. Three benchmark families are used in
practice:

1. **Service-area demographics** (census/ACS) -- what collectionHQ's EDI
   analysis and most public-library audits compare against.
2. **Publishing output** (the CCBC's annual diversity statistics) --
   separates a collection gap from an industry gap: you cannot buy what is
   not published. (Lee & Low's Diversity Baseline Survey measures the
   publishing *workforce*, context for the skew rather than a collection
   benchmark.)
3. **The collection's own baseline over time** -- audit, set local goals in
   the collection-development policy, re-audit; the goal is the trend. For a
   special collection with no meaningful community comparator, this is
   usually the only honest benchmark.

Cross-cutting: "mirrors and windows" (Rudine Sims Bishop) -- parity is not
the goal, and windows argue for representation *above* local share for
smaller groups.

Accordingly, libcat ships **no built-in targets**. A category may carry an
operator-supplied `benchmark` (a share in `[0,1]`) with a required
`benchmarkSource` naming where the number came from:

```toml
[[category]]
id = "bipoc"
benchmark = 0.34
benchmarkSource = "ACS 2024 service area"
```

The report passes both through (`benchmark`/`benchmarkSource` on the
category tally), and the Diversity screen draws a neutral dashed marker on
the share bar plus a labeled column -- never a red/green grade. A share is
only as meaningful as the coverage above it, and the delta is the
librarian's to interpret. The Diversity setup screen edits benchmarks
alongside the rest of the crosswalk.

## Limitations, measured

- A subject audit measures **aboutness**, not the identity of characters or
  creators. The two axes are deliberately separate.
- Keyword matching is English-first; headings in other languages match only
  by URI or scheme. Labels are read with an `en`-then-`mul` preference.
- Coverage varies wildly by source: a vendor ILS load may subject 15% of
  works; a curated corpus 90%. Compare categories only alongside coverage.
- Wikidata resolution costs seconds per SPARQL batch (the ISBN match scans a
  property path); a large corpus is an hours-long, resumable batch job, not
  an interactive call.

## Sources

The design follows the library diversity-audit literature (subject-heading
audits and their limits; the case against opaque automated audits) and the
documented skews of Wikidata/VIAF demographic data. See the research notes in
the feature ledger for citations.

Benchmark-practice sources (verified 2026-07-11):

- Service-area demographics: [collectionHQ DEI analysis](https://www.collectionhq.com/creating-community/)
  ([Library Journal review](https://www.libraryjournal.com/story/Diversity-Equity-and-Inclusion-Analysis-Powered-CollectionHQ-Reference-eReview));
  [CT State Library collection-audit guide](https://libguides.ctstatelibrary.org/collection-management/collection-audit).
- Publishing output: [CCBC diversity statistics](https://ccbc.education.wisc.edu/literature-resources/ccbc-diversity-statistics/)
  (annual since 1994); [Lee & Low Diversity Baseline Survey](https://www.leeandlow.com/about/diversity-baseline-survey/)
  (publishing workforce, not output).
- Baseline-over-time practice: [SLJ "Diversity Auditing 101"](https://www.slj.com/story/diversity-auditing-101-how-to-evaluate-collection);
  [In the Library with the Lead Pipe on conducting audits](https://www.inthelibrarywiththeleadpipe.org/2024/conducting-a-diversity-audit/);
  [Computers in Libraries on using audit data](https://www.infotoday.com/cilmag/nov22/Gates-McGaughey-Mulder-Voels--Using-Data-From-Collection-Diversity-Audits.shtml).
