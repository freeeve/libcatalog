package bibframe

import (
	"reflect"
	"strings"
	"testing"
)

// TestWorkRelations covers relation statements add and remove
// editorially with the describes-guard, read back sorted, and never ride a
// clone (a clone carrying its source's side would be a half-link).
func TestWorkRelations(t *testing.T) {
	grain := sampleGrain(t) // describes w1
	grain, err := SetWorkRelation(grain, "w1", PredHasPart, "w2aaa000aaa00", true)
	if err != nil {
		t.Fatal(err)
	}
	grain, err = SetWorkRelation(grain, "w1", PredHasPart, "w0bbb000bbb00", true)
	if err != nil {
		t.Fatal(err)
	}
	grain, err = SetWorkRelation(grain, "w1", PredPartOf, "w0ccc000ccc00", true)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := WorkRelationsOf(grain, "w1")
	if err != nil {
		t.Fatal(err)
	}
	want := WorkRelations{HasPart: []string{"w0bbb000bbb00", "w2aaa000aaa00"}, PartOf: []string{"w0ccc000ccc00"}}
	if !reflect.DeepEqual(rel, want) {
		t.Fatalf("relations = %+v, want %+v", rel, want)
	}

	// Removal retracts exactly one link.
	grain, err = SetWorkRelation(grain, "w1", PredHasPart, "w2aaa000aaa00", false)
	if err != nil {
		t.Fatal(err)
	}
	rel, _ = WorkRelationsOf(grain, "w1")
	if !reflect.DeepEqual(rel.HasPart, []string{"w0bbb000bbb00"}) || len(rel.PartOf) != 1 {
		t.Fatalf("after remove = %+v", rel)
	}

	// the grain refuses to hold both directions between one pair,
	// whichever side is asserted second, and removal is unaffected.
	if _, err := SetWorkRelation(grain, "w1", PredHasPart, "w0ccc000ccc00", true); err == nil {
		t.Fatal("hasPart accepted over an existing partOf to the same work")
	}
	inverted, err := SetWorkRelation(grain, "w1", PredPartOf, "w0bbb000bbb00", true)
	if err == nil {
		t.Fatalf("partOf accepted over an existing hasPart to the same work: %s", inverted)
	}
	if _, err := SetWorkRelation(grain, "w1", PredPartOf, "w0bbb000bbb00", false); err != nil {
		t.Fatalf("removing an absent inverse should stay a no-op: %v", err)
	}

	// Describes-guard and predicate check refuse.
	if _, err := SetWorkRelation(grain, "wzzz999zzz999z", PredHasPart, "w1", true); err == nil {
		t.Fatal("undescribed work accepted a relation")
	}
	if _, err := SetWorkRelation(grain, "w1", "http://example.com/related", "w1", true); err == nil {
		t.Fatal("arbitrary predicate accepted")
	}

	// A clone drops relation statements.
	cloned, _, err := CloneGrain(grain, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cloned), "hasPart") || strings.Contains(string(cloned), "partOf") {
		t.Fatalf("clone carried relation statements:\n%s", cloned)
	}
}
