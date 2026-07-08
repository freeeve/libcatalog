// Package storetest is the conformance suite every store.Store implementation
// must pass. The mem store runs it always; store/dynamo runs it against a
// local DynamoDB endpoint when configured.
package storetest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/store"
)

// Options tunes the suite per implementation.
type Options struct {
	// StrictTTL asserts that expired records are immediately invisible.
	// Native-TTL stores (DynamoDB) delete lazily and must leave this false.
	StrictTTL bool
}

// Run exercises s against the Store contract. The factory must yield a store
// whose keyspace is empty or test-unique.
func Run(t *testing.T, s store.Store, opts Options) {
	t.Run("GetMissing", func(t *testing.T) { testGetMissing(t, s) })
	t.Run("PutGetRoundTrip", func(t *testing.T) { testPutGetRoundTrip(t, s) })
	t.Run("EmptyData", func(t *testing.T) { testEmptyData(t, s) })
	t.Run("VersionLifecycle", func(t *testing.T) { testVersionLifecycle(t, s) })
	t.Run("CondIfAbsent", func(t *testing.T) { testCondIfAbsent(t, s) })
	t.Run("CondIfVersion", func(t *testing.T) { testCondIfVersion(t, s) })
	t.Run("Delete", func(t *testing.T) { testDelete(t, s) })
	t.Run("Query", func(t *testing.T) { testQuery(t, s) })
	t.Run("QueryPagination", func(t *testing.T) { testQueryPagination(t, s) })
	t.Run("Increment", func(t *testing.T) { testIncrement(t, s) })
	t.Run("InvalidKeys", func(t *testing.T) { testInvalidKeys(t, s) })
	if opts.StrictTTL {
		t.Run("StrictTTL", func(t *testing.T) { testStrictTTL(t, s) })
	}
}

func k(pk, sk string) store.Key { return store.Key{PK: pk, SK: sk} }

func testGetMissing(t *testing.T, s store.Store) {
	_, err := s.Get(t.Context(), k("MISS#1", "X"))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
	err = s.Delete(t.Context(), store.Record{Key: k("MISS#1", "X")}, store.CondNone)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete missing: err = %v, want ErrNotFound", err)
	}
}

func testPutGetRoundTrip(t *testing.T, s store.Store) {
	key := k("RT#1", "DOC")
	in := store.Record{Key: key, Data: []byte(`{"a":1}`)}
	stored, err := s.Put(t.Context(), in, store.CondNone)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if stored.Version != 1 {
		t.Fatalf("first Put version = %d, want 1", stored.Version)
	}
	got, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Data) != `{"a":1}` || got.Version != 1 || got.Key != key {
		t.Fatalf("Get = %+v", got)
	}
	if !got.ExpireAt.IsZero() {
		t.Fatalf("ExpireAt = %v, want zero", got.ExpireAt)
	}
}

// testEmptyData covers data-less records: index/existence markers that carry a
// key and version but no payload. The mem store admits them and callers rely on
// it, so every implementation must round-trip an empty Data (DynamoDB in
// particular rejects an empty attribute value if written naively).
func testEmptyData(t *testing.T, s store.Store) {
	key := k("ED#1", "MARKER")
	stored, err := s.Put(t.Context(), store.Record{Key: key}, store.CondNone)
	if err != nil {
		t.Fatalf("Put empty-data marker: %v", err)
	}
	if stored.Version != 1 {
		t.Fatalf("marker version = %d, want 1", stored.Version)
	}
	got, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get marker: %v", err)
	}
	if len(got.Data) != 0 || got.Version != 1 || got.Key != key {
		t.Fatalf("Get marker = %+v, want empty data / version 1", got)
	}
	// A marker must be visible to Query alongside data-bearing records.
	found := false
	for rec, err := range s.Query(t.Context(), "ED#1", "", store.QueryOpt{}) {
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if rec.Key == key {
			found = true
		}
	}
	if !found {
		t.Fatal("empty-data marker missing from Query results")
	}
	// data -> empty -> data overwrites must all round-trip.
	if _, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("payload")}, store.CondNone); err != nil {
		t.Fatalf("overwrite with data: %v", err)
	}
	back, err := s.Put(t.Context(), store.Record{Key: key}, store.CondNone)
	if err != nil {
		t.Fatalf("overwrite back to empty: %v", err)
	}
	if got, _ := s.Get(t.Context(), key); len(got.Data) != 0 || got.Version != back.Version {
		t.Fatalf("after clearing data, Get = %+v", got)
	}
}

func testVersionLifecycle(t *testing.T, s store.Store) {
	key := k("VER#1", "DOC")
	r1, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("v1")}, store.CondNone)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	r2, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("v2")}, store.CondNone)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if r2.Version != r1.Version+1 {
		t.Fatalf("version did not increment: %d -> %d", r1.Version, r2.Version)
	}
}

func testCondIfAbsent(t *testing.T, s store.Store) {
	key := k("ABS#1", "DOC")
	if _, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("first")}, store.CondIfAbsent); err != nil {
		t.Fatalf("create-only Put on empty key: %v", err)
	}
	_, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("second")}, store.CondIfAbsent)
	if !errors.Is(err, store.ErrConditionFailed) {
		t.Fatalf("create-only Put on existing: err = %v, want ErrConditionFailed", err)
	}
	got, err := s.Get(t.Context(), key)
	if err != nil || string(got.Data) != "first" {
		t.Fatalf("losing Put clobbered record: %+v, %v", got, err)
	}
}

func testCondIfVersion(t *testing.T, s store.Store) {
	key := k("CIV#1", "DOC")
	// Version 0 = create: succeeds on empty, fails once present.
	r1, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("v1")}, store.CondIfVersion)
	if err != nil {
		t.Fatalf("version-0 create: %v", err)
	}
	if _, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("clobber")}, store.CondIfVersion); !errors.Is(err, store.ErrConditionFailed) {
		t.Fatalf("version-0 on existing: err = %v, want ErrConditionFailed", err)
	}
	// Correct version succeeds; stale version fails.
	r2 := store.Record{Key: key, Data: []byte("v2"), Version: r1.Version}
	stored, err := s.Put(t.Context(), r2, store.CondIfVersion)
	if err != nil {
		t.Fatalf("current-version Put: %v", err)
	}
	stale := store.Record{Key: key, Data: []byte("v3"), Version: r1.Version}
	if _, err := s.Put(t.Context(), stale, store.CondIfVersion); !errors.Is(err, store.ErrConditionFailed) {
		t.Fatalf("stale-version Put: err = %v, want ErrConditionFailed", err)
	}
	got, err := s.Get(t.Context(), key)
	if err != nil || string(got.Data) != "v2" || got.Version != stored.Version {
		t.Fatalf("record after stale write: %+v, %v", got, err)
	}
	// Missing record with non-zero version fails.
	ghost := store.Record{Key: k("CIV#1", "GHOST"), Data: []byte("x"), Version: 3}
	if _, err := s.Put(t.Context(), ghost, store.CondIfVersion); !errors.Is(err, store.ErrConditionFailed) {
		t.Fatalf("versioned Put on missing: err = %v, want ErrConditionFailed", err)
	}
}

func testDelete(t *testing.T, s store.Store) {
	key := k("DEL#1", "DOC")
	stored, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("v")}, store.CondNone)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Stale conditional delete fails and leaves the record.
	err = s.Delete(t.Context(), store.Record{Key: key, Version: stored.Version + 7}, store.CondIfVersion)
	if !errors.Is(err, store.ErrConditionFailed) {
		t.Fatalf("stale conditional delete: err = %v, want ErrConditionFailed", err)
	}
	if err := s.Delete(t.Context(), store.Record{Key: key, Version: stored.Version}, store.CondIfVersion); err != nil {
		t.Fatalf("conditional delete: %v", err)
	}
	if _, err := s.Get(t.Context(), key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
}

func testQuery(t *testing.T, s store.Store) {
	pk := "Q#1"
	for _, sk := range []string{"SUGG#b", "SUGG#a", "SUGG#c", "OTHER#x"} {
		if _, err := s.Put(t.Context(), store.Record{Key: k(pk, sk), Data: []byte(sk)}, store.CondNone); err != nil {
			t.Fatalf("Put %s: %v", sk, err)
		}
	}
	if _, err := s.Put(t.Context(), store.Record{Key: k("Q#other", "SUGG#z"), Data: []byte("z")}, store.CondNone); err != nil {
		t.Fatalf("Put other partition: %v", err)
	}
	collect := func(opt store.QueryOpt) []string {
		var sks []string
		for rec, err := range s.Query(t.Context(), pk, "SUGG#", opt) {
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if string(rec.Data) != rec.Key.SK {
				t.Fatalf("record data %q != sk %q", rec.Data, rec.Key.SK)
			}
			sks = append(sks, rec.Key.SK)
		}
		return sks
	}
	asc := collect(store.QueryOpt{})
	if fmt.Sprint(asc) != "[SUGG#a SUGG#b SUGG#c]" {
		t.Fatalf("ascending = %v", asc)
	}
	desc := collect(store.QueryOpt{Descending: true})
	if fmt.Sprint(desc) != "[SUGG#c SUGG#b SUGG#a]" {
		t.Fatalf("descending = %v", desc)
	}
	limited := collect(store.QueryOpt{Limit: 2})
	if fmt.Sprint(limited) != "[SUGG#a SUGG#b]" {
		t.Fatalf("limited = %v", limited)
	}
	// Early break must not panic.
	for range s.Query(t.Context(), pk, "SUGG#", store.QueryOpt{}) {
		break
	}
}

func testQueryPagination(t *testing.T, s store.Store) {
	pk := "PAGE#1"
	for i := range 5 {
		sk := fmt.Sprintf("ITEM#%02d", i)
		if _, err := s.Put(t.Context(), store.Record{Key: k(pk, sk), Data: []byte(sk)}, store.CondNone); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	var page1 []string
	for rec, err := range s.Query(t.Context(), pk, "ITEM#", store.QueryOpt{Limit: 2}) {
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		page1 = append(page1, rec.Key.SK)
	}
	var page2 []string
	for rec, err := range s.Query(t.Context(), pk, "ITEM#", store.QueryOpt{Limit: 2, StartAfter: page1[len(page1)-1]}) {
		if err != nil {
			t.Fatalf("Query page 2: %v", err)
		}
		page2 = append(page2, rec.Key.SK)
	}
	if fmt.Sprint(page1) != "[ITEM#00 ITEM#01]" || fmt.Sprint(page2) != "[ITEM#02 ITEM#03]" {
		t.Fatalf("pages = %v / %v", page1, page2)
	}
	var descPage []string
	for rec, err := range s.Query(t.Context(), pk, "ITEM#", store.QueryOpt{Descending: true, Limit: 2, StartAfter: "ITEM#03"}) {
		if err != nil {
			t.Fatalf("Query desc: %v", err)
		}
		descPage = append(descPage, rec.Key.SK)
	}
	if fmt.Sprint(descPage) != "[ITEM#02 ITEM#01]" {
		t.Fatalf("desc page = %v", descPage)
	}
}

func testIncrement(t *testing.T, s store.Store) {
	key := k("CNT#1", "HOUR#00")
	v1, err := s.Increment(t.Context(), key, 1, time.Time{})
	if err != nil {
		t.Fatalf("Increment: %v", err)
	}
	v2, err := s.Increment(t.Context(), key, 2, time.Time{})
	if err != nil {
		t.Fatalf("Increment 2: %v", err)
	}
	if v1 != 1 || v2 != 3 {
		t.Fatalf("counter = %d then %d, want 1 then 3", v1, v2)
	}
	if v, err := s.Increment(t.Context(), key, -3, time.Time{}); err != nil || v != 0 {
		t.Fatalf("decrement = %d, %v", v, err)
	}
}

func testInvalidKeys(t *testing.T, s store.Store) {
	for _, key := range []store.Key{{}, {PK: "P"}, {SK: "S"}} {
		if _, err := s.Get(t.Context(), key); err == nil || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get accepted invalid key %+v (err %v)", key, err)
		}
		if _, err := s.Put(t.Context(), store.Record{Key: key}, store.CondNone); err == nil {
			t.Fatalf("Put accepted invalid key %+v", key)
		}
	}
}

func testStrictTTL(t *testing.T, s store.Store) {
	key := k("TTL#1", "DOC")
	past := time.Now().Add(-time.Minute)
	if _, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("gone"), ExpireAt: past}, store.CondNone); err != nil {
		t.Fatalf("Put expired: %v", err)
	}
	if _, err := s.Get(t.Context(), key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get expired: err = %v, want ErrNotFound", err)
	}
	for rec := range s.Query(t.Context(), "TTL#1", "", store.QueryOpt{}) {
		t.Fatalf("Query yielded expired record %+v", rec)
	}
	// An expired record does not block a create-only Put.
	if _, err := s.Put(t.Context(), store.Record{Key: key, Data: []byte("fresh")}, store.CondIfAbsent); err != nil {
		t.Fatalf("create over expired: %v", err)
	}
	// Expired counters reset.
	cnt := k("TTL#1", "CNT")
	if _, err := s.Increment(t.Context(), cnt, 5, past); err != nil {
		t.Fatalf("Increment expired: %v", err)
	}
	if v, err := s.Increment(t.Context(), cnt, 1, time.Time{}); err != nil || v != 1 {
		t.Fatalf("counter after expiry = %d, %v, want 1", v, err)
	}
}
