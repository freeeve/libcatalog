package blob_test

import (
	"errors"
	"testing"

	"github.com/freeeve/libcat/storage/blob"
)

func TestReadOnly(t *testing.T) {
	base := blob.NewMem()
	if _, err := base.Put(t.Context(), "data/works/w1.nq", []byte("seed"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ro := blob.ReadOnly(base)

	// Reads pass through.
	got, _, err := ro.Get(t.Context(), "data/works/w1.nq")
	if err != nil || string(got) != "seed" {
		t.Fatalf("Get through read-only = %q, %v", got, err)
	}
	found := false
	for e, err := range ro.List(t.Context(), "data/works/") {
		if err != nil {
			t.Fatal(err)
		}
		if e.Path == "data/works/w1.nq" {
			found = true
		}
	}
	if !found {
		t.Fatal("List through read-only missed the seeded object")
	}

	// Writes are rejected and never reach the underlying store.
	if _, err := ro.Put(t.Context(), "data/works/w2.nq", []byte("new"), blob.PutOptions{}); !errors.Is(err, blob.ErrReadOnly) {
		t.Fatalf("Put err = %v, want ErrReadOnly", err)
	}
	if err := ro.Delete(t.Context(), "data/works/w1.nq"); !errors.Is(err, blob.ErrReadOnly) {
		t.Fatalf("Delete err = %v, want ErrReadOnly", err)
	}
	if _, _, err := base.Get(t.Context(), "data/works/w1.nq"); err != nil {
		t.Fatal("a rejected write must leave the underlying object intact")
	}
	if _, _, err := base.Get(t.Context(), "data/works/w2.nq"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("a rejected Put must not have written through")
	}
}
