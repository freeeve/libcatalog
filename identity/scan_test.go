package identity

import (
	"sort"
	"testing"
)

// TestScanGrain recovers the Instance identity from a grain with distinct Work
// and Instance ids and two typed identifiers.
func TestScanGrain(t *testing.T) {
	grain := []byte(`<#i1Instance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .
<#i1Instance> <http://id.loc.gov/ontologies/bibframe/instanceOf> <#w1Work> <feed:overdrive> .
<#i1Instance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:a <feed:overdrive> .
<#i1Instance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:b <feed:overdrive> .
_:a <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Isbn> <feed:overdrive> .
_:a <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "9780000000001" <feed:overdrive> .
_:b <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Identifier> <feed:overdrive> .
_:b <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "od-42" <feed:overdrive> .
`)

	ids, err := ScanGrain(grain)
	if err != nil {
		t.Fatalf("ScanGrain: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("got %d identities, want 1", len(ids))
	}
	got := ids[0]
	if got.InstanceID != "i1" {
		t.Errorf("InstanceID = %q, want i1", got.InstanceID)
	}
	if got.WorkID != "w1" {
		t.Errorf("WorkID = %q, want w1", got.WorkID)
	}
	want := []string{"id:od-42", "isbn:9780000000001"}
	keys := append([]string(nil), got.ProviderKeys...)
	sort.Strings(keys)
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("ProviderKeys = %v, want %v", got.ProviderKeys, want)
	}
}
