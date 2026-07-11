// the merge handler discarded the MergeResult on the error path and
// answered a flat `500 merge failed`. The rewrite really had run partway -- some
// works repointed at the winner, the rest still on the heading -- and the one
// message guaranteed to stop anyone investigating said nothing happened.
//
// The merge is resumable by re-issuing the identical request, so the response has
// to say the count and say that.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// failNthWorkWrite fails the Nth Work-grain write and passes everything else, so
// the seeding writes land and the merge stops in the middle of its rewrite pass.
type failNthWorkWrite struct {
	blob.Store
	after  int32
	writes atomic.Int32
	off    atomic.Bool
}

var errShardDown = errors.New("shard is read-only")

func (f *failNthWorkWrite) Put(ctx context.Context, p string, b []byte, o blob.PutOptions) (string, error) {
	if !f.off.Load() && strings.HasPrefix(p, "data/works/") {
		if f.writes.Add(1) > f.after {
			return "", errShardDown
		}
	}
	return f.Store.Put(ctx, p, b, o)
}

// mergeResponse is what the handler answers on either path.
type mergeResponse struct {
	Error     string `json:"error"`
	Loser     string `json:"loser"`
	Winner    string `json:"winner"`
	Rewritten int    `json:"rewritten"`
	Carriers  int    `json:"carriers"`
	Complete  bool   `json:"complete"`
}

func mergeAPIOverFlaky(t *testing.T, bs *failNthWorkWrite) http.Handler {
	t.Helper()
	if _, err := bs.Store.Put(t.Context(), "data/authorities/ho/homosaurus.nq", []byte(authoritiesFixture), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	queue := suggest.New(db, ix, suggest.Caps{})
	svc := &authoritiesvc.Service{Blob: bs, Vocab: ix, Queue: queue}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	return New(Deps{Blob: bs, DB: db, Verifier: verifier, Authorities: svc})
}

// mergeTwoCarriers seeds a local heading carried by two Works and returns its id.
func mergeTwoCarriers(t *testing.T, h http.Handler, bs *failNthWorkWrite) string {
	t.Helper()
	rec := request(t, h, http.MethodPost, "/v1/authorities", "lib-token", "", map[string]any{
		"prefLabel": map[string]string{"en": "Trans folks"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID, URI string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	seedTypedWork(t, bs.Store, "wcarrier00001", nil, created.URI)
	seedTypedWork(t, bs.Store, "wcarrier00002", nil, created.URI)
	return created.ID
}

func postMergeFull(t *testing.T, h http.Handler, loser string) (int, mergeResponse) {
	t.Helper()
	rec := request(t, h, http.MethodPost, "/v1/authorities/merge", "lib-token", "", map[string]any{
		"loser":  loser,
		"winner": map[string]string{"scheme": "homosaurus", "id": "https://homosaurus.org/v4/homoit0001235"},
	})
	var out mergeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, out
}

// A rewrite that stops partway answers 500 with the count and with what to do.
func TestPartialMergeReportsHowFarItGotAndThatItResumes(t *testing.T) {
	bs := &failNthWorkWrite{Store: blob.NewMem(), after: 1}
	h := mergeAPIOverFlaky(t, bs)
	loser := mergeTwoCarriers(t, h, bs)

	code, got := postMergeFull(t, h, loser)
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
	if got.Rewritten != 1 || got.Carriers != 2 {
		t.Errorf("rewritten=%d carriers=%d, want 1 of 2 -- the count was discarded", got.Rewritten, got.Carriers)
	}
	if got.Complete {
		t.Error("a partial merge reported itself complete")
	}
	if !strings.Contains(got.Error, "1 of 2") {
		t.Errorf("the message does not say how far it got: %q", got.Error)
	}
	if !strings.Contains(got.Error, "retry") {
		t.Errorf("the message does not say the merge resumes: %q", got.Error)
	}
	if m := leakyMessage.FindString(got.Error); m != "" {
		t.Errorf("the response leaks %q: %q", m, got.Error)
	}
	if got.Error == "merge failed" {
		t.Error("the old message survived: it says nothing happened, and something did")
	}
}

// The control: with the store healthy, re-issuing the identical request finishes
// the job and answers 200. Without this, "500 with a count" is satisfiable by a
// merge that can never succeed.
func TestPartialMergeFinishesOnRetry(t *testing.T) {
	bs := &failNthWorkWrite{Store: blob.NewMem(), after: 1}
	h := mergeAPIOverFlaky(t, bs)
	loser := mergeTwoCarriers(t, h, bs)

	if code, _ := postMergeFull(t, h, loser); code != http.StatusInternalServerError {
		t.Fatalf("the seeded failure did not fire: status %d", code)
	}
	bs.off.Store(true) // the shard comes back

	code, got := postMergeFull(t, h, loser)
	if code != http.StatusOK {
		t.Fatalf("retry status = %d (%q), want 200", code, got.Error)
	}
	// One work was already repointed, so the resume rewrites exactly the other.
	if got.Rewritten != 1 || got.Carriers != 1 {
		t.Errorf("retry rewrote %d of %d, want 1 of 1", got.Rewritten, got.Carriers)
	}
	if !got.Complete {
		t.Error("the finished merge did not report itself complete")
	}
	// And the heading is now retired, which the failed attempt must not have done.
	grain, _, err := bs.Store.Get(context.Background(), bibframe.AuthorityGrainPath(loser))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(grain), "mergedInto"); n != 1 {
		t.Errorf("loser grain carries %d mergedInto quads, want 1", n)
	}
}
