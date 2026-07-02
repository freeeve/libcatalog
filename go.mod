module github.com/freeeve/libcatalog

go 1.25

require github.com/freeeve/libcodex v0.6.0

// Local replace while libcodex and libcatalog are co-developed; drop once
// consuming a published tag.
replace github.com/freeeve/libcodex => ../libcodex
