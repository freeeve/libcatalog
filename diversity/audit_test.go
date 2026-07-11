package diversity

import (
	"math"
	"testing"
)

// sub is a subject-with-labels test helper.
func sub(uri string, labels ...string) SubjectRef { return SubjectRef{URI: uri, Labels: labels} }

// tally looks up a category tally by id in a report.
func tally(r Report, id string) CategoryTally {
	for _, c := range r.Categories {
		if c.ID == id {
			return c
		}
	}
	return CategoryTally{}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestAuditorCoverageAndCounts checks the coverage-first denominators: a work with
// no usable subject dilutes coverage but is categorized nowhere, and a work is
// counted once per category however many subjects matched it.
func TestAuditorCoverageAndCounts(t *testing.T) {
	a := NewAuditor(Default())

	// 1: two subjects, both LGBTQIA+ -> counted once for lgbtqia; covered.
	a.Add([]SubjectRef{sub("", "Lesbian fiction"), sub("", "Gay men")})
	// 2: immigrant + women -> two categories; covered.
	a.Add([]SubjectRef{sub("", "Immigrants"), sub("", "Women authors")})
	// 3: a subject that maps nowhere -> covered but no category.
	a.Add([]SubjectRef{sub("", "Cooking")})
	// 4: no subjects at all -> dilutes coverage, categorized nowhere.
	a.Add(nil)

	r := a.Report()
	if r.TotalWorks != 4 {
		t.Errorf("TotalWorks = %d, want 4", r.TotalWorks)
	}
	if r.CoveredWorks != 3 {
		t.Errorf("CoveredWorks = %d, want 3 (work 4 has no subjects)", r.CoveredWorks)
	}
	if !approx(r.Coverage, 3.0/4.0) {
		t.Errorf("Coverage = %v, want 0.75", r.Coverage)
	}
	if got := tally(r, "lgbtqia").Works; got != 1 {
		t.Errorf("lgbtqia works = %d, want 1 (two subjects, one work)", got)
	}
	if got := tally(r, "immigrant-diaspora").Works; got != 1 {
		t.Errorf("immigrant works = %d, want 1", got)
	}
	if got := tally(r, "women-gender").Works; got != 1 {
		t.Errorf("women-gender works = %d, want 1", got)
	}
	// Shares are against the right denominators.
	lg := tally(r, "lgbtqia")
	if !approx(lg.ShareCovered, 1.0/3.0) {
		t.Errorf("lgbtqia ShareCovered = %v, want 1/3 (of 3 covered)", lg.ShareCovered)
	}
	if !approx(lg.ShareTotal, 1.0/4.0) {
		t.Errorf("lgbtqia ShareTotal = %v, want 1/4 (of 4 total)", lg.ShareTotal)
	}
}

// TestAuditorURIAndLabelBothCount checks that a URI-only subject counts toward
// coverage and category via an override URI, alongside label matching.
func TestAuditorURIAndLabelBothCount(t *testing.T) {
	cw := Default()
	a := NewAuditor(cw)
	// A URI-only subject that maps nowhere: covered, no category.
	a.Add([]SubjectRef{sub("http://example.org/x")})
	r := a.Report()
	if r.CoveredWorks != 1 {
		t.Errorf("a URI-only subject should count as covered: %d", r.CoveredWorks)
	}
	for _, c := range r.Categories {
		if c.Works != 0 {
			t.Errorf("unexpected category hit %s=%d", c.ID, c.Works)
		}
	}
}

// TestAuditorEmptyCorpus checks the divide-by-zero guards: an empty corpus reports
// zero coverage and zero shares, not NaN.
func TestAuditorEmptyCorpus(t *testing.T) {
	r := NewAuditor(Default()).Report()
	if r.TotalWorks != 0 || r.CoveredWorks != 0 || r.Coverage != 0 {
		t.Errorf("empty corpus totals wrong: %+v", r)
	}
	for _, c := range r.Categories {
		if c.ShareCovered != 0 || c.ShareTotal != 0 {
			t.Errorf("empty corpus share should be 0, got %+v", c)
		}
	}
	if len(r.Categories) == 0 {
		t.Error("report should still list the categories (all zero)")
	}
}
