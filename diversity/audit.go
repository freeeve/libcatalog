package diversity

import "strings"

// SubjectRef is one of a work's subjects as the audit sees it: an authority URI
// (may be empty for a bare-string ILS heading) and its heading labels (may be
// empty for a URI-only reference). Either dimension feeds the crosswalk.
type SubjectRef struct {
	URI    string
	Labels []string
}

// Report is a coverage-first content-diversity audit of a corpus. It reports what
// the works are *about*, never who created them. Every category share is stated
// against an explicit denominator so undercounting is visible rather than hidden:
// CoveredWorks (works that carry any subject at all) is the honest base for
// representation, while ShareTotal shows the dilution across the whole corpus,
// including works no one has subjected yet.
type Report struct {
	// TotalWorks is every work the audit saw.
	TotalWorks int `json:"totalWorks"`
	// CoveredWorks is the works carrying at least one subject (URI or label) --
	// the only works that can be categorized, and the honest denominator.
	CoveredWorks int `json:"coveredWorks"`
	// Coverage is CoveredWorks/TotalWorks in [0,1]. A low value means the audit
	// speaks for only part of the collection; read every category share with it.
	Coverage float64 `json:"coverage"`
	// Categories are the diversity categories in the crosswalk's reporting order.
	Categories []CategoryTally `json:"categories"`
}

// CategoryTally is one category's representation. Works counts each work once,
// however many of its subjects matched. ShareCovered is Works/CoveredWorks (the
// share among works that could be categorized at all); ShareTotal is
// Works/TotalWorks (the share of the whole corpus).
type CategoryTally struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Works        int     `json:"works"`
	ShareCovered float64 `json:"shareCovered"`
	ShareTotal   float64 `json:"shareTotal"`
}

// Auditor streams works into a coverage-first content-diversity tally. Build it
// once from a crosswalk, Add each work's subjects, then read Report. It is not safe
// for concurrent Add.
type Auditor struct {
	cw      *Crosswalk
	total   int
	covered int
	perCat  map[string]int
}

// NewAuditor returns an Auditor over the given crosswalk.
func NewAuditor(cw *Crosswalk) *Auditor {
	return &Auditor{cw: cw, perCat: map[string]int{}}
}

// Add folds one work's subjects into the tally. A work counts toward CoveredWorks
// when it carries at least one subject with a URI or a non-empty label, and toward
// a category once when any of its subjects maps there. A work with no usable
// subjects contributes only to TotalWorks -- it dilutes coverage, which is the
// point of reporting coverage.
func (a *Auditor) Add(subjects []SubjectRef) {
	a.total++
	covered := false
	cats := map[string]bool{}
	for _, s := range subjects {
		if s.URI != "" {
			covered = true
			for _, id := range a.cw.Categorize(s.URI, "") {
				cats[id] = true
			}
		}
		for _, l := range s.Labels {
			if strings.TrimSpace(l) == "" {
				continue
			}
			covered = true
			for _, id := range a.cw.Categorize("", l) {
				cats[id] = true
			}
		}
	}
	if covered {
		a.covered++
	}
	for id := range cats {
		a.perCat[id]++
	}
}

// Report snapshots the tally as a coverage-first Report, with categories in the
// crosswalk's stable reporting order. Shares are 0 when their denominator is 0.
func (a *Auditor) Report() Report {
	r := Report{TotalWorks: a.total, CoveredWorks: a.covered}
	if a.total > 0 {
		r.Coverage = float64(a.covered) / float64(a.total)
	}
	for _, c := range a.cw.Categories() {
		t := CategoryTally{ID: c.ID, Label: c.Label, Works: a.perCat[c.ID]}
		if a.covered > 0 {
			t.ShareCovered = float64(t.Works) / float64(a.covered)
		}
		if a.total > 0 {
			t.ShareTotal = float64(t.Works) / float64(a.total)
		}
		r.Categories = append(r.Categories, t)
	}
	return r
}
