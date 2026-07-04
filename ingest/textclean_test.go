package ingest

import "testing"

// TestCleanText pins the shared free-text normalization used by every ingest
// provider: character references decode to a fixpoint, markup strips, clean text
// (and a bare "<" as prose) passes through unchanged.
func TestCleanText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"clean passthrough", "Hamlet", "Hamlet"},
		{"bare lt as prose", "plain prose, 2 < 3, no markup", "plain prose, 2 < 3, no markup"},
		{"literal ampersand", "Biography & Autobiography", "Biography & Autobiography"},
		{"numeric emdash", "friendship&#8212;a Newbery Honor Book", "friendship—a Newbery Honor Book"},
		{"hex emdash", "life&#x2014;death", "life—death"},
		{"named entity", "Etiquette &amp; Espionage", "Etiquette & Espionage"},
		{"trademark ref", "LEGO&#174; Creations", "LEGO® Creations"},
		{"double-escaped fixpoint", "friendship&amp;#8212;a Newbery Honor Book", "friendship—a Newbery Honor Book"},
		{"markup strip", "a <b>bold</b> title<br/>", "a bold title"},
		{"markup and entity", "Hamlet&#39;s <i>soliloquy</i>", "Hamlet's soliloquy"},
	}
	for _, c := range cases {
		if got := CleanText(c.in); got != c.want {
			t.Errorf("%s: CleanText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
