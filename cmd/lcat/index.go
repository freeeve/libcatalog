package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/freeeve/libcatalog/project"
	"github.com/freeeve/libcatalog/search"
	"github.com/freeeve/libcatalog/storage"
)

// runIndex builds the lexical search index from a projected catalog.json: one
// roaringrange term index plus a BM25 impact sidecar per corpus language, plus a
// routing manifest -- the data the browser's WASM reader queries (ARCHITECTURE §8).
func runIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	catalogJSON := fs.String("catalog", "", "path to catalog.json (from lcat project)")
	out := fs.String("out", ".", "output directory for the search index + manifest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogJSON == "" {
		return fmt.Errorf("--catalog is required")
	}

	b, err := os.ReadFile(*catalogJSON)
	if err != nil {
		return err
	}
	var cat project.Catalog
	if err := json.Unmarshal(b, &cat); err != nil {
		return fmt.Errorf("parse catalog.json: %w", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	m, err := search.BuildIndexes(&cat, storage.Dir(*out))
	if err != nil {
		return err
	}
	langs := make([]string, len(m.Indexes))
	for i, ix := range m.Indexes {
		langs[i] = fmt.Sprintf("%s(%d)", ix.Language, ix.DocCount)
	}
	fmt.Printf("built %d language indexes to %s (schema v%d): %v\n",
		len(m.Indexes), *out, search.SchemaVersion, langs)
	return nil
}
