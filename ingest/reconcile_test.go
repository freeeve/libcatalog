package ingest_test

import (
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
)

const (
	rdfType = "<http://www.w3.org/1999/02/22-rdf-syntax-ns#type>"
	bfWork  = "<http://id.loc.gov/ontologies/bibframe/Work>"
)

func feedGrain(id, graph string, editorial string) []byte {
	nq := "<#" + id + "Work> " + rdfType + " " + bfWork + " <" + graph + "> .\n" +
		"<#" + id + "Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#" + id + "iInstance> <" + graph + "> .\n"
	if editorial != "" {
		nq += "<#" + id + "Work> <https://github.com/freeeve/libcat/ns#tag> \"" + editorial + "\" <editorial:> .\n"
	}
	return []byte(nq)
}

func putGrain(t *testing.T, st blob.Store, id string, grain []byte) {
	t.Helper()
	if _, err := st.Put(t.Context(), bibframe.GrainPath(id), grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func visibility(t *testing.T, st blob.Store, id string) bibframe.WorkVisibility {
	t.Helper()
	grain, _, err := st.Get(t.Context(), bibframe.GrainPath(id))
	if err != nil {
		t.Fatal(err)
	}
	v, err := bibframe.Visibility(grain, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestReconcile is the acceptance: a feed-only work missing from
// the scan is flagged (and under auto-suppress, hidden); works with another
// bib source or editorial statements are never flagged; a returning work
// loses the flag and a reconcile-set suppression.
func TestReconcile(t *testing.T) {
	ctx := t.Context()
	st := blob.NewMem()
	putGrain(t, st, "wstay", feedGrain("wstay", "feed:overdrive", ""))
	putGrain(t, st, "wgone", feedGrain("wgone", "feed:overdrive", ""))
	putGrain(t, st, "wtagged", feedGrain("wtagged", "feed:overdrive", "Nonfiction."))
	putGrain(t, st, "wcopycat", feedGrain("wcopycat", "feed:copycat", ""))

	present := map[string]bool{"wstay": true}
	res, err := ingest.Reconcile(ctx, st, "", "overdrive", present, ingest.ReconcileReview, "2026-07-03")
	if err != nil {
		t.Fatal(err)
	}
	if res.Flagged != 1 || len(res.FlaggedIDs) != 1 || res.FlaggedIDs[0] != "wgone" || res.Suppressed != 0 {
		t.Fatalf("review pass = %+v", res)
	}
	if v := visibility(t, st, "wgone"); v.Withdrawn != "2026-07-03" || v.Suppressed {
		t.Fatalf("wgone = %+v", v)
	}
	// Protected works untouched: another feed, editorial investment, present.
	for _, id := range []string{"wstay", "wtagged", "wcopycat"} {
		if v := visibility(t, st, id); v.Withdrawn != "" {
			t.Fatalf("%s flagged: %+v", id, v)
		}
	}

	// A second pass under auto-suppress hides the already-flagged work and
	// records itself as the suppressor. Idempotent on the flag.
	res, err = ingest.Reconcile(ctx, st, "", "overdrive", present, ingest.ReconcileAutoSuppress, "2026-07-04")
	if err != nil {
		t.Fatal(err)
	}
	if res.Flagged != 0 || res.Suppressed != 1 {
		t.Fatalf("auto-suppress pass = %+v", res)
	}
	if v := visibility(t, st, "wgone"); v.Withdrawn != "2026-07-03" || !v.Suppressed || v.SuppressedBy != bibframe.SuppressedByReconcile {
		t.Fatalf("wgone after auto-suppress = %+v", v)
	}

	// The title returns: flag and reconcile-set suppression both clear.
	present["wgone"] = true
	res, err = ingest.Reconcile(ctx, st, "", "overdrive", present, ingest.ReconcileReview, "2026-07-05")
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleared != 1 || res.Unsuppressed != 1 {
		t.Fatalf("return pass = %+v", res)
	}
	if v := visibility(t, st, "wgone"); v.Withdrawn != "" || v.Suppressed || v.SuppressedBy != "" {
		t.Fatalf("wgone after return = %+v", v)
	}

	// A curator's own suppression is never cleared by a return.
	grain, _, _ := st.Get(ctx, bibframe.GrainPath("wstay"))
	grain, err = bibframe.SetSuppressed(grain, "wstay", true)
	if err != nil {
		t.Fatal(err)
	}
	putGrain(t, st, "wstay", grain)
	if _, err := ingest.Reconcile(ctx, st, "", "overdrive", present, ingest.ReconcileReview, "2026-07-06"); err != nil {
		t.Fatal(err)
	}
	if v := visibility(t, st, "wstay"); !v.Suppressed {
		t.Fatalf("curator suppression cleared: %+v", v)
	}

	// A kept work is never re-flagged.
	grain, _, _ = st.Get(ctx, bibframe.GrainPath("wgone"))
	grain, err = bibframe.SetFeedKept(grain, "wgone", true)
	if err != nil {
		t.Fatal(err)
	}
	putGrain(t, st, "wgone", grain)
	delete(present, "wgone")
	res, err = ingest.Reconcile(ctx, st, "", "overdrive", present, ingest.ReconcileReview, "2026-07-07")
	if err != nil {
		t.Fatal(err)
	}
	if res.Flagged != 0 {
		t.Fatalf("kept work re-flagged: %+v", res)
	}

	if _, err := ingest.Reconcile(ctx, st, "", "overdrive", present, "purge", "2026-07-08"); err == nil {
		t.Fatal("bad policy accepted")
	}
}
