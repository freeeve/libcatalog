# 134 -- hugo module: cap term slugs; "file name too long" kills the build

Filed from queerbooks-demo (2026-07-06). Do not let a queerbooks session edit
this repo -- implement here.

## What happened

Real-corpus build (48.5k works) died at render with
`open .../contributors/a.-damico-ahmara-smith-<...300+ bytes...>/index.html:
file name too long`: anthology records carried a whole comma-separated artist
list as one contributor label, the adapter taxonomy-indexed it, and the term
page's directory name (the lcat-slug of the label) blew past the filesystem's
255-byte component limit. Hugo errors per page and the whole build fails --
in a piped CI invocation (`hugo | tail`) the failure is easy to miss because
the pipeline exit code is tail's.

queerbooks-demo fixed its provider (split the lists, cap labels at 100
chars), but the module should defend regardless: ANY adopter with messy
source data can hit this, and one bad label should not be able to fail a
97k-page build.

## Suggested shape

- lcat-slug.html (single point: facet links + adapter indexing both slug
  through it) truncates its output to a safe bound (~100 bytes?), appending a
  short stable hash of the full label on truncation so two long labels that
  share a prefix do not collide on one term page.
- Term page titles keep the full human label (only the slug truncates).
- A note in the module README's data-quality section: labels are display
  text; keys/slugs are bounded.

## Repro

Any catalog.json with a contributor/tag/subject label > ~250 bytes;
`hugo` on APFS/ext4 fails at render.
