package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/hardcover"
)

// runHardcover ingests a user's Hardcover "Read" shelf into canonical BIBFRAME grains
// with minted two-tier ids via the Hardcover provider and the shared ingest.Run
// pipeline: a book's editions cluster into one Work with an Instance per format, and its
// non-BIBFRAME display fields (cover, rating, dateRead) ride through the feed graph to
// catalog.json's `extra` (tasks/026). It is a convenience alias for
// `lcat ingest --provider hardcover` that also supplies the API token and page size and
// offers a schema-introspection affordance. The token comes from --token or
// $HARDCOVER_API_TOKEN / $HARDCOVER_TOKEN and is never written to disk.
func runHardcover(args []string) error {
	fs := flag.NewFlagSet("hardcover", flag.ExitOnError)
	out := fs.String("out", "", "output directory for canonical grains and catalog.nq")
	token := fs.String("token", "", "Hardcover API token (or $HARDCOVER_API_TOKEN); never written to disk")
	limit := fs.Int("limit", 100, "GraphQL page size")
	feed := fs.String("feed", hardcover.ProviderName, "provenance graph feed:<provider> for the records")
	source := fs.String("source", "", "replay a captured user_books JSON instead of calling the API (offline)")
	introspect := fs.String("introspect", "", "dump a GraphQL type's fields and exit (e.g. user_books)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	params := map[string]string{}
	if *token != "" {
		params["token"] = *token
	}
	if *limit > 0 {
		params["limit"] = strconv.Itoa(*limit)
	}

	// --introspect: build the provider (live, so it needs a token) and dump the type.
	if *introspect != "" {
		prov, err := hardcover.New(ingest.Config{Feed: *feed, Params: params})
		if err != nil {
			return err
		}
		hp, ok := prov.(hardcover.Provider)
		if !ok {
			return fmt.Errorf("hardcover: provider does not support introspection")
		}
		data, err := hp.Introspect(context.Background(), *introspect)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	if *out == "" {
		return fmt.Errorf("--out (grains output directory) is required")
	}
	cfg := ingest.Config{Feed: *feed, Source: *source, Params: params}
	return runIngest(providerRegistry(), hardcover.ProviderName, cfg, *out, "", false)
}
