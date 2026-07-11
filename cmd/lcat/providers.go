package main

import (
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/csvmap"
	"github.com/freeeve/libcat/ingest/hardcover"
	"github.com/freeeve/libcat/ingest/marc"
	"github.com/freeeve/libcat/ingest/nquads"
	"github.com/freeeve/libcat/ingest/oaipmh"
	"github.com/freeeve/libcat/ingest/overdrive"
)

// providerRegistry builds the registry of first-party ingest providers. Registration
// is explicit here (not init()) so the built-in set is auditable in one place; a
// deployment composes its own registry the same way -- its provider's package plus
// one Register call, no libcat fork (ARCHITECTURE §9a). The generic
// mapped providers (marc, nquads, csv) are the config-driven on-ramp:
// bespoke Go providers stay the exception, not the price of admission.
func providerRegistry() *ingest.Registry {
	reg := ingest.NewRegistry()
	// Register errors only on an empty name, a nil factory, or a duplicate key --
	// all build-composition bugs in this fixed built-in set, so fail loudly.
	must(reg.Register(overdrive.ProviderName, overdrive.New))
	must(reg.Register(marc.ProviderName, marc.New))
	must(reg.Register(oaipmh.ProviderName, oaipmh.New))
	must(reg.Register(hardcover.ProviderName, hardcover.New))
	must(reg.Register(nquads.ProviderName, nquads.New))
	must(reg.Register(csvmap.ProviderName, csvmap.New))
	return reg
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
