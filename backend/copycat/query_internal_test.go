package copycat

import "testing"

// TestSRUQuery pins the CQL the fielded search sends: dc-set indexes, AND
// composition, and the Bath-profile identifier indexes (isbn/issn/lccn).
func TestSRUQuery(t *testing.T) {
	cases := []struct {
		name  string
		terms []FieldTerm
		want  string
	}{
		{"any", []FieldTerm{{Index: "any", Term: "dutch house"}}, `"dutch house"`},
		{"isbn-bath", []FieldTerm{{Index: "isbn", Term: "9780062963673"}}, `bath.isbn = "9780062963673"`},
		{"issn-bath", []FieldTerm{{Index: "issn", Term: "0028-0836"}}, `bath.issn = "0028-0836"`},
		{"lccn-bath", []FieldTerm{{Index: "lccn", Term: "2019005498"}}, `bath.lccn = "2019005498"`},
		{
			"anded",
			[]FieldTerm{{Index: "title", Term: "dutch house"}, {Index: "author", Term: "patchett"}},
			`(dc.title = "dutch house") and (dc.author = "patchett")`,
		},
	}
	for _, tc := range cases {
		if got := sruQuery(tc.terms).String(); got != tc.want {
			t.Errorf("%s: cql = %s, want %s", tc.name, got, tc.want)
		}
	}
}
