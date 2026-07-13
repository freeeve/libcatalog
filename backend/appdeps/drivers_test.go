package appdeps

import (
	"testing"

	"github.com/freeeve/libcat/backend/vocab"
)

// TestDriverTermsMultiLang pins the multi-language harvest driver (task
// 467): each concept yields one driver per configured language whose label
// exists, all pointing at the concept URI; a merged concept is skipped; a
// language with no label on a term is skipped for that term; identical
// labels across languages are not searched twice.
func TestDriverTermsMultiLang(t *testing.T) {
	terms := []*vocab.Term{
		{ID: "u1", Labels: map[string]string{"en": "Lesbians", "es": "Lesbianas"}},
		{ID: "u2", Labels: map[string]string{"en": "Trans people"}}, // no es label
		{ID: "u3", Labels: map[string]string{"en": "Same", "es": "Same"}},
		{ID: "u4", Labels: map[string]string{"en": "Retired", "es": "Retirado"}, MergedInto: "u1"},
	}

	// English only: one driver per non-merged term, the English label.
	en := driverTerms(terms, []string{"en"})
	if len(en) != 3 {
		t.Fatalf("en drivers = %d, want 3 (u1,u2,u3; u4 merged)", len(en))
	}

	// en+es: u1 gets both, u2 only en (no es), u3 collapses (same label).
	both := driverTerms(terms, []string{"en", "es"})
	got := map[string][]string{}
	for _, d := range both {
		got[d.URI] = append(got[d.URI], d.Query)
	}
	if q := got["u1"]; len(q) != 2 || q[0] != "Lesbians" || q[1] != "Lesbianas" {
		t.Fatalf("u1 drivers = %v, want [Lesbians Lesbianas]", q)
	}
	if q := got["u2"]; len(q) != 1 || q[0] != "Trans people" {
		t.Fatalf("u2 drivers = %v, want [Trans people] (no es label)", q)
	}
	if q := got["u3"]; len(q) != 1 {
		t.Fatalf("u3 drivers = %v, want a single entry (identical labels deduped)", q)
	}
	if _, ok := got["u4"]; ok {
		t.Fatal("u4 is merged and must not drive a search")
	}
	// Every driver carries the full label map so a match maps back in any
	// language.
	for _, d := range both {
		if d.Labels == nil {
			t.Fatalf("driver %q missing labels", d.URI)
		}
	}
}

// TestDriverTermsDefaultsToEnglish: an empty language list falls back to
// English so a misconfiguration never disables the harvest.
func TestDriverTermsDefaultsToEnglish(t *testing.T) {
	terms := []*vocab.Term{{ID: "u1", Labels: map[string]string{"en": "Lesbians", "es": "Lesbianas"}}}
	got := driverTerms(terms, nil)
	if len(got) != 1 || got[0].Query != "Lesbians" {
		t.Fatalf("default drivers = %+v, want the English label only", got)
	}
}
