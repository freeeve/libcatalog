package overdrive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// TestProviderCleansTitles pins that the provider normalizes HTML character
// references and markup in transcribed titles at the source, so both the
// BIBFRAME titles and the identity clustering key see clean text.
func TestProviderCleansTitles(t *testing.T) {
	it := sampleItem()
	it.Title = "Incredible LEGO&#174; Creations"
	it.Subtitle = "Emperor Xuanzong of Qing&#8212;Min Ning"
	it.Series = "The <b>Big</b> Series"

	dir := t.TempDir()
	page := struct {
		Items []Item `json:"items"`
	}{Items: []Item{it}}
	data, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page-0001.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	prov, err := New(ingest.Config{Source: dir})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := prov.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	work := recs[0].Work()
	if got, want := work.Titles[0].MainTitle, "Incredible LEGO® Creations"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	if got, want := work.Titles[0].Subtitle, "Emperor Xuanzong of Qing—Min Ning"; got != want {
		t.Errorf("subtitle = %q, want %q", got, want)
	}
	// The identity clustering title is cleaned too.
	if got, want := recs[0].Identity().Title, "Incredible LEGO® Creations"; got != want {
		t.Errorf("identity title = %q, want %q", got, want)
	}
}
