# 270 -- barcode uniqueness is advisory: nothing rejects a duplicate typed into the item editor, imported from MARC, or already in the corpus

Opened 2026-07-09.

Split out of **269**, which closed the race that let bulk add *generate*
duplicate barcodes. Generation is now serialized and re-validated against the
grain it writes into. What is still missing is the constraint.

## What is missing

`nextBarcodes` checking its candidates against `ix.Barcodes()` is the only
uniqueness enforcement libcat has, and it guards exactly one code path. Nothing
rejects a duplicate that arrives any other way:

- **The item editor.** A cataloger can type a barcode another item already holds,
  through `PUT /v1/works/{id}` or `POST /v1/works/{id}/ops`. Nothing objects.
- **MARC import and copycat.** A holdings barcode is written straight through.
- **The corpus as it stands.** Nobody has ever checked. A deployment that ran a
  version with the 269 race may be carrying duplicates right now, and there is no
  command that would find them.

`bibframe/itemops.go:18-20` already states the project's position -- barcode is
excluded from batch-addressable item fields because "assigning one across a
selection would mint duplicates". The judgment is written down. The enforcement
is not.

## Expected

- **A report before a constraint.** `lcat items --duplicate-barcodes`, or a
  librarian-gated maintenance route, that walks the index and lists every barcode
  held by more than one item with their work and instance ids. This is cheap --
  `workindex` already builds the barcode set -- and it is what an operator needs
  first, because a constraint added on top of existing duplicates fails writes to
  records that were fine yesterday.
- **Then reject on write.** A duplicate barcode should be a 409 from the item
  write paths, not merely something the generator avoids. The check needs the
  index, because uniqueness is corpus-wide rather than per-grain, so it belongs in
  `httpapi` next to `mutateWorkGrain` rather than in `bibframe`.
- **Decide what "unique" means for a retired item.** A withdrawn copy whose
  barcode is reused on a replacement is ordinary library practice. Uniqueness
  probably means "among live items", and that decision should be written down
  before it is enforced.

## Note on scale

`ix.Barcodes()` copies the whole corpus barcode set per call. That is fine for
the bulk-add path (one call per grain-mutation attempt) and fine for a report,
but it is the wrong primitive for a per-item-write check on a large catalog. A
`Has(barcode)` that takes `ix.mu` and answers from the live map avoids the copy;
see tasks/085's sizing work before assuming the copy is free at 10M items.
