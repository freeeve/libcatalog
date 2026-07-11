// authoritiesvc.Merge writes through publish.MutateGrain twice, and
// the handler mapped every non-validation, non-notfound error onto 409 with
// err.Error() as the body. A store failure therefore answered "somebody else
// edited this record" -- the wrong status -- with an *os.PathError naming the
// blob root as its message. Merge rewrites every carrier of the losing term, so
// it is one of the two routes that touch the most records at once.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// armedStore passes every write through until arm() is called. Seeding an
// authority and a carrier work are themselves grain writes, so a store that
// failed from the start would fail the setup rather than the merge.
type armedStore struct {
	blob.Store
	armed atomic.Bool
	err   error
}

func (s *armedStore) arm() { s.armed.Store(true) }

func (s *armedStore) Put(ctx context.Context, p string, b []byte, o blob.PutOptions) (string, error) {
	if s.armed.Load() {
		return "", s.err
	}
	return s.Store.Put(ctx, p, b, o)
}

// mergeAPIOver mounts the authorities surface over an armable store.
func mergeAPIOver(t *testing.T, failWith error) (http.Handler, *armedStore) {
	t.Helper()
	inner := blob.NewMem()
	if _, err := inner.Put(t.Context(), "data/authorities/ho/homosaurus.nq", []byte(authoritiesFixture), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	bs := &armedStore{Store: inner, err: failWith}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	queue := suggest.New(db, ix, suggest.Caps{})
	svc := &authoritiesvc.Service{Blob: bs, Vocab: ix, Queue: queue}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	return New(Deps{Blob: bs, DB: db, Verifier: verifier, Authorities: svc}), bs
}

// seedMergeable creates a local authority and a work carrying it, then arms the
// store so the merge itself is the first write that fails.
func seedMergeable(t *testing.T, h http.Handler, bs *armedStore) string {
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
	bs.arm()
	return created.ID
}

func postMerge(t *testing.T, h http.Handler, loser string) (int, string) {
	t.Helper()
	rec := request(t, h, http.MethodPost, "/v1/authorities/merge", "lib-token", "", map[string]any{
		"loser":  loser,
		"winner": map[string]string{"scheme": "homosaurus", "id": "https://homosaurus.org/v4/homoit0001235"},
	})
	return rec.Code, errorOf(t, rec.Body.Bytes())
}

// A broken store is a 500, and its message names nothing.
//
// The store here fails the very first write, which is the first
// Work rewrite rather than the loser's retirement. Nothing landed, and the message
// says exactly that -- the one case where "the merge did not happen" is true.
func TestAuthorityMergeStoreFailureIsNotAConflict(t *testing.T) {
	h, bs := mergeAPIOver(t, &os.PathError{Op: "open", Path: "/srv/libcat/data/authorities/ho/.blob-99", Err: syscall.EACCES})
	loser := seedMergeable(t, h, bs)

	code, msg := postMerge(t, h, loser)

	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: a broken store is not a lost race", code)
	}
	if m := leakyMessage.FindString(msg); m != "" {
		t.Fatalf("the response leaks %q: %q", m, msg)
	}
	if msg != "merge failed; nothing was changed" {
		t.Fatalf("message = %q", msg)
	}
}

// A read-only deployment answers the guard's own 403 and wording, so a client
// cannot tell which layer refused it.
func TestAuthorityMergeReportsReadOnlyLikeTheGuard(t *testing.T) {
	h, bs := mergeAPIOver(t, blob.ErrReadOnly)
	loser := seedMergeable(t, h, bs)

	code, msg := postMerge(t, h, loser)

	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
	if msg != readOnlyNotice {
		t.Fatalf("message = %q, want %q", msg, readOnlyNotice)
	}
}

// A genuinely lost race keeps its 409, and says so in words.
func TestAuthorityMergeConflictKeepsIts409(t *testing.T) {
	h, bs := mergeAPIOver(t, blob.ErrPreconditionFailed)
	loser := seedMergeable(t, h, bs)

	code, msg := postMerge(t, h, loser)

	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
	if m := leakyMessage.FindString(msg); m != "" {
		t.Fatalf("the response leaks %q: %q", m, msg)
	}
	if !strings.Contains(msg, "retry") {
		t.Fatalf("message = %q, want a retryable-conflict message", msg)
	}
}
