package suggest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The note this replaces was json.Marshal(results)[:512]. Past a handful of
// works the cut landed mid-token, so the note stopped being parseable, and a
// slice at a byte boundary can split a UTF-8 rune. A RunNote truncates the
// *list* and says how many it dropped.
func TestRunNoteStaysParseableAndSaysWhatItDropped(t *testing.T) {
	var works []string
	for i := 0; i < maxNotedWorks+37; i++ {
		works = append(works, fmt.Sprintf("wtrunc%08d", i))
	}
	note := RunNote{Selection: "all", Matched: len(works), Applied: len(works), Rewritten: len(works), Works: works}.String()

	if !utf8.ValidString(note) {
		t.Fatal("note is not valid UTF-8")
	}
	var got RunNote
	if err := json.Unmarshal([]byte(note), &got); err != nil {
		t.Fatalf("note is not parseable JSON: %v\n%s", err, note)
	}
	if len(got.Works) != maxNotedWorks {
		t.Fatalf("carried %d ids, want the cap of %d", len(got.Works), maxNotedWorks)
	}
	// No silent caps: the count of what was left out is in the note.
	if got.More != 37 {
		t.Fatalf("more = %d, want 37", got.More)
	}
	if got.Rewritten != len(works) {
		t.Fatalf("rewritten = %d, want the true total %d", got.Rewritten, len(works))
	}
}

func TestRunNoteNamesItsWorks(t *testing.T) {
	note := RunNote{Selection: "ids", Matched: 2, Applied: 2, Rewritten: 2, Added: 2, Works: []string{"wone000000001", "wtwo000000002"}}.String()
	for _, id := range []string{"wone000000001", "wtwo000000002"} {
		if !strings.Contains(note, id) {
			t.Fatalf("note does not name %s: %s", id, note)
		}
	}
	var got RunNote
	if err := json.Unmarshal([]byte(note), &got); err != nil {
		t.Fatal(err)
	}
	if got.More != 0 {
		t.Fatalf("more = %d on an untruncated note", got.More)
	}
}

// A run that rewrote nothing still produces a parseable note with an empty
// list, not a null the reader has to special-case.
func TestRunNoteWithNoWorks(t *testing.T) {
	note := RunNote{Selection: "search", Matched: 5, Applied: 5}.String()
	var got RunNote
	if err := json.Unmarshal([]byte(note), &got); err != nil {
		t.Fatalf("%v: %s", err, note)
	}
	if got.Works == nil || len(got.Works) != 0 {
		t.Fatalf("works = %v, want an empty list", got.Works)
	}
	if !strings.Contains(note, `"works":[]`) {
		t.Fatalf("note = %s", note)
	}
}

// A work id carrying multi-byte characters must survive: the old byte-slice
// truncation could cut one in half.
func TestRunNoteDoesNotSplitRunes(t *testing.T) {
	var works []string
	for i := 0; i < maxNotedWorks+5; i++ {
		works = append(works, fmt.Sprintf("w%dλ文字", i))
	}
	note := RunNote{Works: works}.String()
	if !utf8.ValidString(note) {
		t.Fatal("note is not valid UTF-8")
	}
	var got RunNote
	if err := json.Unmarshal([]byte(note), &got); err != nil {
		t.Fatalf("note is not parseable: %v", err)
	}
	for i, w := range got.Works {
		if !utf8.ValidString(w) {
			t.Fatalf("work %d is not valid UTF-8: %q", i, w)
		}
	}
}
