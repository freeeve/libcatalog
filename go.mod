module github.com/freeeve/libcatalog

go 1.25

require (
	github.com/RoaringBitmap/roaring/v2 v2.14.4
	github.com/freeeve/libcodex v0.7.0
	github.com/freeeve/roaringrange v0.26.3
)

require (
	github.com/bits-and-blooms/bitset v1.24.2 // indirect
	github.com/freeeve/fst-go v0.1.0 // indirect
	github.com/freeeve/go-ivfpq v0.1.0 // indirect
	github.com/freeeve/go-stemmers v0.0.0-20260606195828-3c78df9017f5 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
)

// Local replaces while libcodex/roaringrange and libcatalog are co-developed;
// drop once consuming a published tag.
replace github.com/freeeve/libcodex => ../libcodex

replace github.com/freeeve/roaringrange => ../roaringrange
