package ingest

import (
	"html"
	"regexp"
	"strings"
)

// htmlTag matches an opening or closing markup tag; a bare "<" followed by a
// non-letter (prose like "a < b") never matches.
var htmlTag = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

// CleanText normalizes a vendor free-text field: it resolves HTML character
// references, drops markup tags, and collapses the whitespace runs that
// stripping leaves. Text carrying neither "&" nor "<" passes through untouched,
// so a clean value (and a bare "<" as prose, e.g. "2 < 3") is byte-identical.
//
// References decode to a bounded fixpoint (three passes): vendor feeds
// double-escape (&amp;#8212;), so a single pass would only peel one layer. The
// providers apply this to prose and transcribed titles at ingest, so grains
// store clean text and every downstream projection and export inherits it
// (tasks/081, tasks/089). Headings and identifiers are deliberately left
// untouched.
func CleanText(s string) string {
	if !strings.ContainsAny(s, "&<") {
		return s
	}
	for range 3 {
		u := html.UnescapeString(s)
		if u == s {
			break
		}
		s = u
	}
	s = htmlTag.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}
