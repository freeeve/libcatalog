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

A zero in a category can mean a genuine collection gap *or* vocabulary the
crosswalk does not know. When a community vocabulary is central to your
collection, prefer scheme- or URI-level entries over keywords; the
queerbooks deployment's override is a worked example of splitting the flat
LGBTQIA+ umbrella into finer identity facets.

## The creator axis

Off by default. Enable on the cataloging backend with:

```sh
LCATD_ENRICH_WIKIDATA=direct   # the only mode; see below
```

and trigger a run with `POST /v1/enrich/wikidata/run` (admin role; `GET
/v1/enrich` lists the configured sources). For each work it
resolves creators **through cataloged identifiers only**: ISBN -> Wikidata
edition (P212/P957) -> work (P629) -> author (P50), then copies the claims
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
