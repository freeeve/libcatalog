package openlibrary

import (
	"context"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

func TestNormalizeISBN(t *testing.T) {
	cases := map[string]string{
		"978-0-553-38380-5": "9780553383805",
		"0-8044-2957-x":     "080442957X", // hyphens stripped, x check digit upper-cased
		"9780553383805":     "9780553383805",
		"":                  "",
	}
	for in, want := range cases {
		if got := NormalizeISBN(in); got != want {
			t.Errorf("NormalizeISBN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnrichMatchesByISBN(t *testing.T) {
	const olURI = "https://openlibrary.org/works/OL45804W"
	// Index keyed with a hyphenated ISBN to prove keys are normalized on load.
	e := New(map[string]string{"978-0-553-38380-5": olURI})

	got, err := e.Enrich(context.Background(), []ingest.WorkSummary{
		{WorkID: "w1", ISBNs: []string{"9780553383805"}}, // clean form, must still hit
		{WorkID: "w2", ISBNs: []string{"9999999999999"}}, // no hit -> untouched
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d enrichments, want 1 (only the matched Work)", len(got))
	}
	if got[0].WorkID != "w1" {
		t.Errorf("enriched %q, want w1", got[0].WorkID)
	}
	if len(got[0].Identities) != 1 || got[0].Identities[0].URI != olURI || got[0].Identities[0].Scheme != Scheme {
		t.Errorf("identities = %+v, want one %s -> %s", got[0].Identities, Scheme, olURI)
	}
	if got[0].Confidence != 1 {
		t.Errorf("confidence = %v, want 1 (exact ISBN match)", got[0].Confidence)
	}
}

func TestEnrichSkipsAmbiguousCluster(t *testing.T) {
	// A Work whose two ISBNs resolve to two different OpenLibrary works is an
	// ambiguous cluster: link nothing rather than guess ( conservatism).
	e := New(map[string]string{
		"9780553383805": "https://openlibrary.org/works/OL1W",
		"9780553383799": "https://openlibrary.org/works/OL2W",
	})
	got, err := e.Enrich(context.Background(), []ingest.WorkSummary{
		{WorkID: "w1", ISBNs: []string{"9780553383805", "9780553383799"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d enrichments, want 0 -- a conflicting ISBN cluster must not be linked", len(got))
	}
}

func TestEnrichAgreeingISBNsLinkOnce(t *testing.T) {
	// Two ISBNs (e.g. the ISBN-10 and ISBN-13 of one edition) both mapping to the
	// SAME work are agreement, not conflict: one identity, not skipped.
	const olURI = "https://openlibrary.org/works/OL3W"
	e := New(map[string]string{"9780553383805": olURI, "0553383809": olURI})
	got, err := e.Enrich(context.Background(), []ingest.WorkSummary{
		{WorkID: "w1", ISBNs: []string{"978-0-553-38380-5", "0553383809"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Identities) != 1 || got[0].Identities[0].URI != olURI {
		t.Fatalf("got %+v, want a single %s identity", got, olURI)
	}
}
