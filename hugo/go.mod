// The libcat Hugo module: a content adapter, layouts, and facet/search/
// availability UI that turn a projected catalog.json + facets.json into a
// faceted, accessible discovery site (ARCHITECTURE §6/§7, tasks/009). It is a
// separate Go module from the libcat framework so Hugo sites that import it
// never pull the Go build dependencies -- it ships only templates and assets.
module github.com/freeeve/libcat/hugo

go 1.25
