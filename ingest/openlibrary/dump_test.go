package openlibrary

import (
	"strings"
	"testing"
)

// a dump line is type<TAB>key<TAB>revision<TAB>timestamp<TAB>JSON.
func editionLine(json string) string {
	return "/type/edition\t/books/OLxM\t1\t2020-01-01T00:00:00\t" + json
}

func TestReadEditionsDump(t *testing.T) {
	dump := strings.Join([]string{
		editionLine(`{"isbn_13":["9780553383805"],"isbn_10":["0553383809"],"works":[{"key":"/works/OL1W"}]}`),
		editionLine(`{"isbn_13":["9780679761044"],"works":[{"key":"/works/OL2W"}]}`),
		editionLine(`{"isbn_13":["9781668128251"]}`),     // no works -> skipped
		editionLine(`{"works":[{"key":"/works/OL9W"}]}`), // no ISBNs -> nothing to index
		"garbage line without enough columns",            // skipped
		editionLine(`{"isbn_13":["not json`),             // malformed JSON -> skipped
	}, "\n")

	idx, err := ReadEditionsDump(strings.NewReader(dump))
	if err != nil {
		t.Fatal(err)
	}
	// Both ISBN forms of the first edition point at its work.
	if idx["9780553383805"] != "https://openlibrary.org/works/OL1W" {
		t.Errorf("isbn_13 -> %q, want the OL1W work URI", idx["9780553383805"])
	}
	if idx["0553383809"] != "https://openlibrary.org/works/OL1W" {
		t.Errorf("isbn_10 -> %q, want the OL1W work URI", idx["0553383809"])
	}
	if idx["9780679761044"] != "https://openlibrary.org/works/OL2W" {
		t.Errorf("second edition -> %q", idx["9780679761044"])
	}
	// The works-less and ISBN-less rows contributed nothing.
	if idx["9781668128251"] != "" {
		t.Errorf("an edition with no works must not be indexed, got %q", idx["9781668128251"])
	}
	if len(idx) != 3 {
		t.Errorf("index has %d entries, want 3", len(idx))
	}
}

func TestReadEditionsDumpDropsConflictingISBN(t *testing.T) {
	// The same ISBN mapped to two different works across editions is a data
	// conflict: drop it entirely so it can never mint a false match.
	dump := strings.Join([]string{
		editionLine(`{"isbn_13":["9780553383805"],"works":[{"key":"/works/OL1W"}]}`),
		editionLine(`{"isbn_13":["9780553383805"],"works":[{"key":"/works/OL2W"}]}`),
		editionLine(`{"isbn_13":["9780679761044"],"works":[{"key":"/works/OL3W"}]}`),
	}, "\n")

	idx, err := ReadEditionsDump(strings.NewReader(dump))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx["9780553383805"]; ok {
		t.Error("a conflicting ISBN must be dropped, not last-wins")
	}
	if idx["9780679761044"] != "https://openlibrary.org/works/OL3W" {
		t.Errorf("an unconflicted ISBN must survive, got %q", idx["9780679761044"])
	}
}
