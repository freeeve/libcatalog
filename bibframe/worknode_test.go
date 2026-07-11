package bibframe

import "testing"

// WorkIDFromIRI decides which subjects a batch patch may rebind, so
// it has to match exactly the node WorkIRI mints -- never a skolem child that
// merely starts the same way, and never an Instance node.
func TestWorkIDFromIRI(t *testing.T) {
	cases := []struct {
		iri  string
		want string
		ok   bool
	}{
		{WorkIRI("wabc123def456"), "wabc123def456", true},
		{"#wabc123def456Work", "wabc123def456", true},
		// A skolem child of the Work node is not the Work node.
		{"#wabc123def456Work-ed-title", "", false},
		{"#wabc123def456Workish", "", false},
		{InstanceIRI("iabc123def456"), "", false},
		{"#iabc123def456Instance", "", false},
		// Absolute IRIs are not grain-local at all.
		{"https://example.org/wabc123def456Work", "", false},
		{"wabc123def456Work", "", false},
		// Shape of the id itself is checked: it must look like a work id.
		{"#Work", "", false},
		{"#xabc123def456Work", "", false},
		{"#wABC123DEF456Work", "", false},
		{"#wabcWork", "", false},
		{"", "", false},
		{"#", "", false},
		// A work id is at most 20 chars after the w.
		{"#w" + "abcdefghij0123456789" + "Work", "w" + "abcdefghij0123456789", true},
		{"#w" + "abcdefghij01234567890" + "Work", "", false},
	}
	for _, tc := range cases {
		got, ok := WorkIDFromIRI(tc.iri)
		if ok != tc.ok || got != tc.want {
			t.Errorf("WorkIDFromIRI(%q) = (%q, %v), want (%q, %v)", tc.iri, got, ok, tc.want, tc.ok)
		}
	}
}

// Round-trip: every id WorkIRI accepts comes back out.
func FuzzWorkIDFromIRI(f *testing.F) {
	f.Add("wabc123def456")
	f.Add("w000000")
	f.Fuzz(func(t *testing.T, id string) {
		got, ok := WorkIDFromIRI(WorkIRI(id))
		if ok && got != id {
			t.Fatalf("WorkIDFromIRI(WorkIRI(%q)) = %q", id, got)
		}
		// An id that round-trips must also be one the Work IRI can name
		// unambiguously: no second "Work" suffix hiding a different id.
		if ok && WorkIRI(got) != WorkIRI(id) {
			t.Fatalf("ambiguous: %q and %q share an IRI", got, id)
		}
	})
}
