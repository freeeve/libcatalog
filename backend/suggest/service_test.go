package suggest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/vocab"
	"github.com/freeeve/libcatalog/storage/blob"
)

const (
	transURI = "https://homosaurus.org/v4/homoit0001235"
	sciFiURI = "http://id.loc.gov/authorities/subjects/sh85118553"
)

func controlled(id string) vocab.TermRef {
	return vocab.TermRef{Scheme: "homosaurus", ID: id}
}

func folk(raw string) vocab.TermRef {
	return vocab.TermRef{Scheme: vocab.FolkScheme, ID: raw}
}

func newService(t *testing.T) (*Service, *store.Mem) {
	t.Helper()
	data, err := os.ReadFile("../vocab/testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "data/authorities/x.nq", data, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	return New(db, ix, Caps{}), db
}

func submit(t *testing.T, svc *Service, workID string, term vocab.TermRef, typ SuggType, hash string) SubmitResult {
	t.Helper()
	res, err := svc.Submit(t.Context(), SubmitInput{
		WorkID: workID, Term: term, Type: typ,
		Reason:        pick(typ == TypeRemove, ReasonDoesNotApply, Reason("")),
		SupporterHash: hash, WorkTitle: "A Book",
	})
	if err != nil {
		t.Fatalf("Submit(%s %s %s): %v", workID, term.ID, typ, err)
	}
	return res
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

func TestSubmitAggregatesAndDedups(t *testing.T) {
	svc, _ := newService(t)
	if res := submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "hash1"); res.Duplicate {
		t.Fatal("first vote marked duplicate")
	}
	if res := submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "hash2"); res.Duplicate {
		t.Fatal("second supporter marked duplicate")
	}
	// Same supporter again: idempotent duplicate, count unchanged.
	if res := submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "hash1"); !res.Duplicate {
		t.Fatal("repeat vote not deduped")
	}
	items, err := svc.ForWork(t.Context(), "wabc123def456")
	if err != nil || len(items) != 1 {
		t.Fatalf("ForWork = %v, %v", items, err)
	}
	sg := items[0]
	if sg.SupporterCount != 2 || sg.Status != StatusPending || sg.Provenance != ProvenancePatron {
		t.Fatalf("aggregate = %+v", sg)
	}
	// Label resolved from the vocabulary, not caller-supplied.
	if sg.Term.Label != "Transgender people" {
		t.Fatalf("label = %q", sg.Term.Label)
	}
}

func TestSubmitValidation(t *testing.T) {
	svc, _ := newService(t)
	// Unknown controlled term.
	_, err := svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: controlled("https://homosaurus.org/v4/nope"),
		Type: TypeAdd, SupporterHash: "h",
	})
	if !errors.Is(err, ErrBadTerm) {
		t.Fatalf("unknown term: %v", err)
	}
	// Unknown scheme.
	_, err = svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: vocab.TermRef{Scheme: "fast", ID: sciFiURI},
		Type: TypeAdd, SupporterHash: "h",
	})
	if !errors.Is(err, ErrBadTerm) {
		t.Fatalf("unknown scheme: %v", err)
	}
	// REMOVE requires a valid reason.
	_, err = svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: controlled(transURI),
		Type: TypeRemove, Reason: "because", SupporterHash: "h",
	})
	if err == nil {
		t.Fatal("free-text reason accepted")
	}
	// Bad type.
	_, err = svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: controlled(transURI),
		Type: "UPSERT", SupporterHash: "h",
	})
	if err == nil {
		t.Fatal("bad type accepted")
	}
}

func TestDisputeReconciliation(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "adder")
	res := submit(t, svc, "wabc123def456", controlled(transURI), TypeRemove, "remover")
	if !res.Disputed {
		t.Fatal("countervote did not dispute")
	}
	items, _ := svc.ForWork(t.Context(), "wabc123def456")
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	for _, sg := range items {
		if sg.Status != StatusDisputed {
			t.Fatalf("side %s status = %s, want DISPUTED", sg.Type, sg.Status)
		}
	}
}

func TestRateLimits(t *testing.T) {
	svc, _ := newService(t)
	svc.caps.PerHour = 3
	var lastErr error
	for i := range 5 {
		_, lastErr = svc.Submit(t.Context(), SubmitInput{
			WorkID: fmt.Sprintf("wabc123def45%d", i), Term: controlled(transURI),
			Type: TypeAdd, SupporterHash: "same-hash",
		})
	}
	if !errors.Is(lastErr, ErrRateLimited) {
		t.Fatalf("after burst: %v, want ErrRateLimited", lastErr)
	}
	// A different supporter is unaffected.
	if _, err := svc.Submit(t.Context(), SubmitInput{
		WorkID: "wzzz999zzz999", Term: controlled(transURI),
		Type: TypeAdd, SupporterHash: "other-hash",
	}); err != nil {
		t.Fatalf("other supporter: %v", err)
	}
}

func TestTombstoneBlocks(t *testing.T) {
	svc, db := newService(t)
	// Simulate a librarian tombstone (written by the review path in 034).
	key := store.Key{PK: workPK("wabc123def456"), SK: tombstoneSK(controlled(transURI))}
	if _, err := db.Put(t.Context(), store.Record{Key: key}, store.CondNone); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: controlled(transURI),
		Type: TypeAdd, SupporterHash: "h",
	})
	if !errors.Is(err, ErrTombstoned) {
		t.Fatalf("tombstoned pair: %v", err)
	}
}

func TestFolkLifecycle(t *testing.T) {
	svc, _ := newService(t)
	// Novel folk term: proposed, aggregated, normalized.
	res := submit(t, svc, "wabc123def456", folk("  Cozy   FANTASY "), TypeAdd, "h1")
	if !res.FolkProposed {
		t.Fatal("novel term not proposed")
	}
	ft, err := svc.FolkTermStatus(t.Context(), "cozy fantasy")
	if err != nil || ft.Status != FolkProposed || ft.UseCount != 1 {
		t.Fatalf("folk record = %+v, %v", ft, err)
	}
	// Second suggestion of the same term (any casing): not re-proposed.
	res = submit(t, svc, "wzzz999zzz999", folk("Cozy Fantasy"), TypeAdd, "h2")
	if res.FolkProposed {
		t.Fatal("known term re-proposed")
	}
	ft, _ = svc.FolkTermStatus(t.Context(), "cozy fantasy")
	if ft.UseCount != 2 {
		t.Fatalf("use count = %d", ft.UseCount)
	}
	// Aggregates keyed by normalized identity.
	items, _ := svc.ForWork(t.Context(), "wabc123def456")
	if len(items) != 1 || items[0].Term.ID != "cozy fantasy" || items[0].Term.Scheme != vocab.FolkScheme {
		t.Fatalf("items = %+v", items)
	}
	// Blocked terms refuse submission.
	if err := svc.mutateFolk(t.Context(), "cozy fantasy", func(ft *FolkTerm) { ft.Status = FolkBlocked }); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: folk("cozy fantasy"),
		Type: TypeAdd, SupporterHash: "h3",
	})
	if !errors.Is(err, ErrFolkBlocked) {
		t.Fatalf("blocked term: %v", err)
	}
	// Unusable raw text rejected outright.
	_, err = svc.Submit(t.Context(), SubmitInput{
		WorkID: "wabc123def456", Term: folk("http://spam.example"),
		Type: TypeAdd, SupporterHash: "h4",
	})
	if !errors.Is(err, ErrBadTerm) {
		t.Fatalf("spam folk term: %v", err)
	}
}

func TestStatusIndexWritten(t *testing.T) {
	svc, db := newService(t)
	submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "h1")
	var found int
	for rec, err := range db.Query(t.Context(), "STATUS#PENDING", "", store.QueryOpt{}) {
		if err != nil {
			t.Fatal(err)
		}
		aggKey, err := aggKeyFromIndexSK(rec.Key.SK)
		if err != nil {
			t.Fatal(err)
		}
		if aggKey.PK != "WORK#wabc123def456" {
			t.Fatalf("index points at %+v", aggKey)
		}
		found++
	}
	if found != 1 {
		t.Fatalf("pending index items = %d", found)
	}
	// Disputing moves both sides' index items.
	submit(t, svc, "wabc123def456", controlled(transURI), TypeRemove, "h2")
	for range db.Query(t.Context(), "STATUS#PENDING", "", store.QueryOpt{}) {
		t.Fatal("stale PENDING index item after dispute")
	}
	var disputed int
	for _, err := range db.Query(t.Context(), "STATUS#DISPUTED", "", store.QueryOpt{}) {
		if err != nil {
			t.Fatal(err)
		}
		disputed++
	}
	if disputed != 2 {
		t.Fatalf("disputed index items = %d", disputed)
	}
}

func TestChallenge(t *testing.T) {
	abuse, err := NewAbuse([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAbuse([]byte("short")); err == nil {
		t.Fatal("short secret accepted")
	}
	now := time.Now()
	abuse.SetClock(func() time.Time { return now })
	token := abuse.Challenge()
	// Too fresh (bot-speed) fails.
	if abuse.VerifyChallenge(token) {
		t.Fatal("instant challenge accepted")
	}
	now = now.Add(10 * time.Second)
	if !abuse.VerifyChallenge(token) {
		t.Fatal("aged challenge rejected")
	}
	// Tampered fails.
	if abuse.VerifyChallenge(token + "0") {
		t.Fatal("tampered challenge accepted")
	}
	// Expired fails.
	now = now.Add(3 * time.Hour)
	if abuse.VerifyChallenge(token) {
		t.Fatal("expired challenge accepted")
	}
	// Distinct IPs hash distinctly; same IP stable.
	h1a, h1b, h2 := abuse.HashIP("192.0.2.1"), abuse.HashIP("192.0.2.1"), abuse.HashIP("192.0.2.2")
	if h1a == h2 {
		t.Fatal("hash collision")
	}
	if h1a != h1b {
		t.Fatal("hash unstable")
	}
}

func TestConcurrentVotes(t *testing.T) {
	svc, _ := newService(t)
	const voters = 20
	errs := make(chan error, voters)
	for i := range voters {
		go func(i int) {
			_, err := svc.Submit(context.Background(), SubmitInput{
				WorkID: "wabc123def456", Term: controlled(transURI),
				Type: TypeAdd, SupporterHash: fmt.Sprintf("hash-%02d", i),
			})
			errs <- err
		}(i)
	}
	for range voters {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent submit: %v", err)
		}
	}
	items, err := svc.ForWork(t.Context(), "wabc123def456")
	if err != nil || len(items) != 1 {
		t.Fatalf("ForWork = %v, %v", items, err)
	}
	// Rate caps are per supporter hash, so 20 distinct voters all land and
	// each is counted exactly once despite the CAS contention.
	if items[0].SupporterCount != voters {
		t.Fatalf("supporterCount = %d, want %d", items[0].SupporterCount, voters)
	}
}
