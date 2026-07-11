// the APPROVED stamp was durable before the rewrite it described had
// run. When PromoteTag failed, nothing rolled it back and the record was
// unreachable in every direction -- DecidePromotion refuses anything not PENDING,
// ProposePromotion supersedes only REJECTED, and there was no DELETE. The tag
// stayed on every work, behind a promotion the queue called approved, and the
// record said `works: 0` whether one work had been rewritten or none.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// flakyPromoter stands in for a publisher whose store fails partway through the
// widest write in the product. It reports how many works it rewrote before the
// failure, which is what PromoteTag really does.
type flakyPromoter struct {
	rewritten int   // works claimed rewritten before failing
	err       error // nil to succeed
	calls     int
}

func (f *flakyPromoter) PublishApproved(context.Context, string) (publish.Result, error) {
	return publish.Result{}, nil
}
func (f *flakyPromoter) PromoteTag(_ context.Context, promo suggest.Promotion, _ string) (int, error) {
	f.calls++
	// The pending record is all PromoteTag needs; it must never be handed a
	// promotion the store already calls approved.
	if promo.Status != suggest.StatusPending {
		return 0, errors.New("PromoteTag was handed a " + string(promo.Status) + " promotion")
	}
	return f.rewritten, f.err
}

// newPromotionAPIWith wires the promotion surface over a caller-supplied promoter.
func newPromotionAPIWith(t *testing.T, promoter GraphPublisher) (http.Handler, *suggest.Service) {
	t.Helper()
	data, err := os.ReadFile("../vocab/testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	authorities := blob.NewMem()
	_, _ = authorities.Put(t.Context(), "a/x.nq", data, blob.PutOptions{})
	ix, err := vocab.Load(t.Context(), authorities, "a/", nil)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	svc := suggest.New(db, ix, suggest.Caps{})
	grains := blob.NewMem()

	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI("wpromo0000001"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Work"), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nq, err = bibframe.ApplyEditorialPatch(nq, bibframe.Patch{Add: []rdf.Quad{bibframe.TagQuad("wpromo0000001", "queer joy")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grains.Put(t.Context(), bibframe.GrainPath("wpromo0000001"), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	verifier := staffVerifier{
		"mod-token": {Email: "mod@example.org", Roles: []auth.Role{auth.RoleModerator}},
		"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
	}
	h := New(Deps{Suggest: svc, Vocab: ix, Verifier: verifier, Publisher: promoter, Blob: grains, DB: db})
	return h, svc
}

func proposeQueerJoy(t *testing.T, h http.Handler) {
	t.Helper()
	rec := doJSON(t, h, http.MethodPost, "/v1/promotions", "mod-token", map[string]any{
		"tag": "Queer Joy", "term": map[string]string{"scheme": "homosaurus", "id": transURI},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("propose: %d %s", rec.Code, rec.Body)
	}
}

func promotionOf(t *testing.T, svc *suggest.Service) suggest.Promotion {
	t.Helper()
	p, err := svc.GetPromotion(context.Background(), "queer joy")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var approve = map[string]any{"tag": "queer joy", "approve": true}

// A failed rewrite must leave the promotion PENDING, so the Approve button stays
// live and the queue does not carry a permanent lie.
func TestFailedRewriteLeavesThePromotionPending(t *testing.T) {
	promoter := &flakyPromoter{err: errors.New("store on fire")}
	h, svc := newPromotionAPIWith(t, promoter)
	proposeQueerJoy(t, h)

	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed rewrite: %d %s", rec.Code, rec.Body)
	}
	if got := promotionOf(t, svc); got.Status != suggest.StatusPending {
		t.Fatalf("status = %s after a failed rewrite, want PENDING", got.Status)
	}
}

// Retrying a failed approval is free: no new route, no state to clear. The rewrite
// loop skips works that no longer carry the tag, so the second attempt resumes.
func TestApprovalIsRetryableAfterTheStoreRecovers(t *testing.T) {
	promoter := &flakyPromoter{err: errors.New("store on fire")}
	h, svc := newPromotionAPIWith(t, promoter)
	proposeQueerJoy(t, h)
	doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve)

	promoter.err = nil
	promoter.rewritten = 3
	rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry after recovery: %d %s", rec.Code, rec.Body)
	}
	if promoter.calls != 2 {
		t.Fatalf("PromoteTag ran %d times, want 2", promoter.calls)
	}
	got := promotionOf(t, svc)
	if got.Status != suggest.StatusApproved || got.Works != 3 {
		t.Fatalf("after retry: status=%s works=%d, want APPROVED/3", got.Status, got.Works)
	}
}

// A partial rewrite is recoverable; one that reports zero is not. The count of
// what landed used to be discarded on the error path.
func TestPartialRewriteRecordsHowFarItGot(t *testing.T) {
	promoter := &flakyPromoter{rewritten: 1, err: errors.New("second shard is read-only")}
	h, svc := newPromotionAPIWith(t, promoter)
	proposeQueerJoy(t, h)
	doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve)

	got := promotionOf(t, svc)
	if got.Works != 1 {
		t.Fatalf("works = %d after a 1-of-2 rewrite, want 1", got.Works)
	}
	if got.Status != suggest.StatusPending {
		t.Fatalf("status = %s, want PENDING", got.Status)
	}

	// The retry rewrites the one that failed; the counts accumulate.
	promoter.err, promoter.rewritten = nil, 1
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if got := promotionOf(t, svc); got.Works != 2 {
		t.Fatalf("works = %d after resuming, want 2 (1 + 1)", got.Works)
	}
}

// The rewrite must never see a decided promotion: the stamp cannot precede it.
// flakyPromoter fails the call outright if it does, which is what makes the
// success case above evidence and not coincidence.
func TestPromoteTagReceivesThePendingPromotion(t *testing.T) {
	promoter := &flakyPromoter{rewritten: 1}
	h, svc := newPromotionAPIWith(t, promoter)
	proposeQueerJoy(t, h)
	rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Works int `json:"works"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Works != 1 {
		t.Fatalf("works = %d, want 1", resp.Works)
	}
	if got := promotionOf(t, svc); got.Status != suggest.StatusApproved || got.Works != 1 {
		t.Fatalf("record = %s/%d", got.Status, got.Works)
	}
}

// With no publisher wired the handler mints an approved-but-unexecuted record on
// purpose. DELETE is the only way out of it, and it frees the tag.
func TestDeleteFreesAnApprovedButUnexecutedTag(t *testing.T) {
	h, svc := newPromotionAPIWith(t, nil)
	proposeQueerJoy(t, h)

	rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve without publisher: %d %s", rec.Code, rec.Body)
	}
	if got := promotionOf(t, svc); got.Status != suggest.StatusApproved {
		t.Fatalf("status = %s", got.Status)
	}
	// Stuck: cannot re-decide, cannot re-propose.
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions/decide", "lib-token", approve); rec.Code != http.StatusConflict {
		t.Fatalf("re-decide: %d", rec.Code)
	}
	proposal := map[string]any{"tag": "Queer Joy", "term": map[string]string{"scheme": "homosaurus", "id": transURI}}
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions", "mod-token", proposal); rec.Code != http.StatusConflict {
		t.Fatalf("re-propose while approved: %d", rec.Code)
	}

	// Moderators may not delete; librarians may.
	if rec := doJSON(t, h, http.MethodDelete, "/v1/promotions/queer%20joy", "mod-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("moderator delete: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodDelete, "/v1/promotions/queer%20joy", "lib-token", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("librarian delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := svc.GetPromotion(t.Context(), "queer joy"); err == nil {
		t.Fatal("promotion still readable after delete")
	}
	// The tag is proposable again -- the point of the escape hatch.
	if rec := doJSON(t, h, http.MethodPost, "/v1/promotions", "mod-token", proposal); rec.Code != http.StatusCreated {
		t.Fatalf("re-propose after delete: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodDelete, "/v1/promotions/never-proposed", "lib-token", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: %d", rec.Code)
	}
}
