package profilesvc

import (
	"encoding/json"
	"testing"

	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/profiles"
)

// loadOne returns a shipped default profile to mutate into an override.
func loadOne(t *testing.T, id string) *profiles.Profile {
	t.Helper()
	set, err := profiles.LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	p := set[id]
	if p == nil {
		t.Fatalf("default %q not found", id)
	}
	return p
}

func marshal(t *testing.T, p *profiles.Profile) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newSvc(t *testing.T) *Service {
	t.Helper()
	svc := New(blob.NewMem(), "data/profiles/", nil)
	if err := svc.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestLoadHasDefaults(t *testing.T) {
	svc := newSvc(t)
	if svc.Set()["work-monograph"] == nil {
		t.Fatal("expected work-monograph default in the set")
	}
	if svc.Overridden("work-monograph") {
		t.Fatal("a fresh service has no overrides")
	}
}

func TestPutOverridesAndMapperReflects(t *testing.T) {
	svc := newSvc(t)
	p := loadOne(t, "work-monograph")
	p.Fields[0].Label = "Uniform Title"

	etag, err := svc.Put(t.Context(), "work-monograph", marshal(t, p), "")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if etag == "" {
		t.Fatal("Put returned an empty etag")
	}
	if !svc.Overridden("work-monograph") {
		t.Fatal("expected work-monograph to be overridden")
	}
	if got := svc.Set()["work-monograph"].Fields[0].Label; got != "Uniform Title" {
		t.Fatalf("set label = %q, want the override", got)
	}
	// The mapper the record/batch paths read must follow the edit.
	if got := svc.Mapper().WorkProfile.Fields[0].Label; got != "Uniform Title" {
		t.Fatalf("mapper label = %q, want the override", got)
	}
}

func TestPutRejectsInvalidAndKeepsSet(t *testing.T) {
	svc := newSvc(t)
	bad := marshal(t, &profiles.Profile{ID: "work-monograph", Label: "x", ResourceType: profiles.ResourceWork}) // no fields
	if _, err := svc.Put(t.Context(), "work-monograph", bad, ""); err == nil {
		t.Fatal("expected an error for a fieldless profile")
	}
	// The rejected save must not have touched the live set.
	if svc.Overridden("work-monograph") {
		t.Fatal("a rejected Put must not persist an override")
	}
	if n := len(svc.Set()["work-monograph"].Fields); n == 0 {
		t.Fatal("the shipped default's fields were clobbered")
	}
}

func TestPutIDMismatch(t *testing.T) {
	svc := newSvc(t)
	p := loadOne(t, "work-monograph")
	if _, err := svc.Put(t.Context(), "some-other-id", marshal(t, p), ""); err != ErrIDMismatch {
		t.Fatalf("err = %v, want ErrIDMismatch", err)
	}
}

func TestPutETagConflict(t *testing.T) {
	svc := newSvc(t)
	p := loadOne(t, "work-monograph")
	if _, err := svc.Put(t.Context(), "work-monograph", marshal(t, p), ""); err != nil {
		t.Fatal(err)
	}
	// A second create-only Put (stale empty ifMatch) loses the race.
	if _, err := svc.Put(t.Context(), "work-monograph", marshal(t, p), ""); err != ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestDeleteOverrideReverts(t *testing.T) {
	svc := newSvc(t)
	p := loadOne(t, "work-monograph")
	p.Label = "House rules"
	if _, err := svc.Put(t.Context(), "work-monograph", marshal(t, p), ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteOverride(t.Context(), "work-monograph"); err != nil {
		t.Fatalf("DeleteOverride: %v", err)
	}
	if svc.Overridden("work-monograph") {
		t.Fatal("override still present after delete")
	}
	if svc.Set()["work-monograph"].Label == "House rules" {
		t.Fatal("expected revert to the shipped default label")
	}
	if err := svc.DeleteOverride(t.Context(), "work-monograph"); err != ErrNotFound {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestReadOnlyWithoutBlob(t *testing.T) {
	svc := New(nil, "", nil)
	if err := svc.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if svc.Set()["work-monograph"] == nil {
		t.Fatal("defaults still load without a blob store")
	}
	p := loadOne(t, "work-monograph")
	if _, err := svc.Put(t.Context(), "work-monograph", marshal(t, p), ""); err != ErrReadOnly {
		t.Fatalf("err = %v, want ErrReadOnly", err)
	}
}
