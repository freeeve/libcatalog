package store_test

import (
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/store/storetest"
)

func TestMemConformance(t *testing.T) {
	storetest.Run(t, store.NewMem(), storetest.Options{StrictTTL: true})
}

func TestMemClockedTTL(t *testing.T) {
	m := store.NewMem()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return now })
	key := store.Key{PK: "RATE#x", SK: "HOUR#12"}
	if _, err := m.Increment(t.Context(), key, 1, now.Add(time.Hour)); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	if v, err := m.Increment(t.Context(), key, 1, time.Time{}); err != nil || v != 2 {
		t.Fatalf("counter = %d, %v, want 2", v, err)
	}
	now = now.Add(2 * time.Hour)
	if v, err := m.Increment(t.Context(), key, 1, time.Time{}); err != nil || v != 1 {
		t.Fatalf("counter after window = %d, %v, want reset to 1", v, err)
	}
	rec := store.Record{Key: store.Key{PK: "SUPP#x", SK: "M"}, Data: []byte("m"), ExpireAt: now.Add(time.Minute)}
	if _, err := m.Put(t.Context(), rec, store.CondIfAbsent); err != nil {
		t.Fatalf("Put: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := m.Get(t.Context(), rec.Key); err == nil {
		t.Fatal("expired record still visible")
	}
}
