package bibframe_test

import (
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
)

// TestMergeMarkerRefusesForeignSubject covers tasks/202 at the bibframe
// layer: a marker for a subject the grain does not describe is refused
// instead of minting a phantom node.
func TestMergeMarkerRefusesForeignSubject(t *testing.T) {
	grain := []byte(`<https://github.com/freeeve/libcatalog/authority/a0d7go0nob80r8> <http://www.w3.org/2004/02/skos/core#prefLabel> "author"@en <authority:local> .` + "\n")
	derived := bibframe.LocalAuthorityIRI("a0d7go0nob80r8")

	if _, err := bibframe.AddAuthorityMergeMarker(grain, derived, "https://example.org/winner", "local"); err == nil {
		t.Fatal("marker asserted on a subject the grain does not describe")
	} else if !strings.Contains(err.Error(), "does not describe") {
		t.Fatalf("err = %v", err)
	}
	if bibframe.AuthorityGrainDescribes(grain, derived) {
		t.Fatal("Describes true for absent subject")
	}

	// The grain's REAL subject still merges fine.
	real := "https://github.com/freeeve/libcatalog/authority/a0d7go0nob80r8"
	if !bibframe.AuthorityGrainDescribes(grain, real) {
		t.Fatal("Describes false for present subject")
	}
	out, err := bibframe.AddAuthorityMergeMarker(grain, real, "https://example.org/winner", "local")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "mergedInto") || !strings.Contains(string(out), real) {
		t.Fatalf("marker missing: %s", out)
	}
}
