package openlibrary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// editionRecord is the subset of an OpenLibrary edition record this reader needs:
// the ISBNs that identify the edition and the work(s) it realizes.
type editionRecord struct {
	ISBN10 []string `json:"isbn_10"`
	ISBN13 []string `json:"isbn_13"`
	Works  []struct {
		Key string `json:"key"` // "/works/OL45804W"
	} `json:"works"`
}

// workURIPrefix turns an OpenLibrary work key ("/works/OL45804W") into a resolvable
// URI.
const workURIPrefix = "https://openlibrary.org"

// ReadEditionsDump builds an ISBN -> OpenLibrary work URI index from an OpenLibrary
// editions dump. The dump is the public bulk TSV: each line is
// type<TAB>key<TAB>revision<TAB>last_modified<TAB>JSON, and the JSON column carries
// isbn_10 / isbn_13 arrays and a works reference. The reader streams it -- the real
// dump is multi-GB -- keeping only the ISBN->work mapping.
//
// Conservatism (the enricher's precision starts here): an ISBN that the dump maps to
// more than one distinct work is dropped from the index entirely, so it can never
// contribute a false match. An edition with no works, or no ISBNs, is skipped.
func ReadEditionsDump(r io.Reader) (map[string]string, error) {
	index := map[string]string{}
	conflicted := map[string]bool{}
	sc := bufio.NewScanner(r)
	// Edition JSON lines can exceed the 64KB Scanner default; allow up to 8MB.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) < 5 {
			continue // header, blank, or a non-record line
		}
		var rec editionRecord
		if err := json.Unmarshal([]byte(cols[4]), &rec); err != nil {
			continue // a malformed row is skipped, not fatal -- the dump is huge
		}
		if len(rec.Works) == 0 || rec.Works[0].Key == "" {
			continue
		}
		work := workURIPrefix + rec.Works[0].Key
		for _, raw := range append(rec.ISBN10, rec.ISBN13...) {
			isbn := NormalizeISBN(raw)
			if isbn == "" || conflicted[isbn] {
				continue
			}
			if prev, ok := index[isbn]; ok && prev != work {
				delete(index, isbn)
				conflicted[isbn] = true
				continue
			}
			index[isbn] = work
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("openlibrary: reading editions dump at line %d: %w", line, err)
	}
	return index, nil
}
