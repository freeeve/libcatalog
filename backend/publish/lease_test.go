package publish

import (
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/store"
)

func TestLease(t *testing.T) {
	db := store.NewMem()
	l := NewLease(db, "ingest", time.Minute)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	l.SetClock(func() time.Time { return now })

	ok, err := l.Acquire(t.Context(), "ingest-run-1")
	if err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	// Exclusive against another holder.
	ok, err = l.Acquire(t.Context(), "publisher")
	if err != nil || ok {
		t.Fatalf("second holder acquired: %v %v", ok, err)
	}
	holder, held, err := l.Held(t.Context())
	if err != nil || !held || holder != "ingest-run-1" {
		t.Fatalf("held = %q %v %v", holder, held, err)
	}
	// Own re-acquire extends.
	if ok, _ := l.Acquire(t.Context(), "ingest-run-1"); !ok {
		t.Fatal("self re-acquire failed")
	}
	if err := l.Heartbeat(t.Context(), "ingest-run-1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	// Expiry frees it.
	now = now.Add(2 * time.Minute)
	if _, held, _ := l.Held(t.Context()); held {
		t.Fatal("expired lease still held")
	}
	if ok, _ := l.Acquire(t.Context(), "publisher"); !ok {
		t.Fatal("acquire after expiry failed")
	}
	// Release by non-holder is a no-op; by holder frees.
	if err := l.Release(t.Context(), "someone-else"); err != nil {
		t.Fatal(err)
	}
	if _, held, _ := l.Held(t.Context()); !held {
		t.Fatal("non-holder release dropped the lease")
	}
	if err := l.Release(t.Context(), "publisher"); err != nil {
		t.Fatal(err)
	}
	if _, held, _ := l.Held(t.Context()); held {
		t.Fatal("holder release did not drop the lease")
	}
}
