package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// readOnlyMessage is the guard's own wording. The allowlisted execute paths must
// be indistinguishable from a blocked route to a client.
const readOnlyMessage = "read-only demo: changes are not saved"

// newReadOnlyAPI seeds a work, then wraps the store read-only, exactly as
// appdeps does when LCATD_READ_ONLY=1.
func newReadOnlyAPI(t *testing.T) (http.Handler, *suggest.Service, string) {
	t.Helper()
	mem := blob.NewMem()
	if _, err := mem.Put(t.Context(), bibframe.GrainPath(editWorkID),
		identityGrain(editWorkID, "A Book", "Le Guin, Ursula K.", "9780547773742"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	_, etag, err := mem.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	queue := suggest.New(db, nil, suggest.Caps{})
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: blob.ReadOnly(mem), DB: db, Verifier: verifier, Suggest: queue, ReadOnly: true})
	return h, queue, etag
}

func errorOf(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out.Error
}

// Control: a blocked route answers the guard's 403. Without this the tests
// below cannot show that the allowlisted routes disagree with it.
func TestReadOnlyBlockedRouteAnswers403(t *testing.T) {
	h, _, _ := newReadOnlyAPI(t)
	rec := request(t, h, "POST", "/v1/vocabsources", "lib-token", "", map[string]any{"name": "zz", "scheme": "zz"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("blocked route = %d %s, want 403", rec.Code, rec.Body)
	}
	if got := errorOf(t, rec.Body.Bytes()); got != readOnlyMessage {
		t.Fatalf("blocked route message = %q", got)
	}
}

// Control: the allowlist exists so a visitor can still see a diff. The dry run
// must keep working.
func TestReadOnlyDryRunStillPreviews(t *testing.T) {
	h, _, etag := newReadOnlyAPI(t)
	rec := request(t, h, "POST", "/v1/works/"+editWorkID+"/ops", "lib-token", etag, map[string]any{
		"dryRun": true,
		"ops":    []map[string]any{{"resource": "work", "path": "tags", "action": "add", "value": map[string]any{"v": "zz"}}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run = %d %s, want 200", rec.Code, rec.Body)
	}
}

// the two allowlisted execute paths answered 500 "grain write
// failed" -- a claim the server is broken, on an ordinary user action, in the
// deployment mode strangers touch. blob.ErrReadOnly was exported and consulted
// nowhere.
func TestReadOnlyExecutePathsAnswer403(t *testing.T) {
	ops := map[string]any{
		"ops": []map[string]any{{"resource": "work", "path": "tags", "action": "add", "value": map[string]any{"v": "zz"}}},
	}
	marc := map[string]any{
		"records": []map[string]any{{"leader": "00000nam a2200000 a 4500", "fields": []map[string]any{
			{"tag": "245", "ind1": "1", "ind2": "0", "subfields": []map[string]any{{"code": "a", "value": "Changed"}}},
		}}},
	}
	for _, tc := range []struct {
		name, path string
		body       map[string]any
	}{
		{"ops", "/v1/works/" + editWorkID + "/ops", ops},
		{"marc", "/v1/works/" + editWorkID + "/marc", marc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, queue, etag := newReadOnlyAPI(t)
			rec := request(t, h, "POST", tc.path, "lib-token", etag, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("execute = %d %s, want 403 (a read-only store is not a server fault)", rec.Code, rec.Body)
			}
			if got := errorOf(t, rec.Body.Bytes()); got != readOnlyMessage {
				t.Fatalf("message = %q, want the guard's own wording %q", got, readOnlyMessage)
			}
			// The part that always held: nothing persists.
			if rows := auditRows(t, queue); len(rows) != 0 {
				t.Fatalf("audit = %+v, want none", rows)
			}
		})
	}
}

// PUT /v1/works/{id} is blocked by the guard, but its handler shares the write
// site. If the guard ever lets it through, the store's refusal must still read
// as 403 rather than a server fault.
func TestReadOnlyGrainPutReportsForbiddenNotServerError(t *testing.T) {
	mem := blob.NewMem()
	if _, err := mem.Put(t.Context(), bibframe.GrainPath(editWorkID),
		identityGrain(editWorkID, "A Book", "Le Guin, Ursula K.", ""), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	_, etag, err := mem.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	// ReadOnly store, but the guard is NOT installed: the handler is on its own.
	h := New(Deps{Blob: blob.ReadOnly(mem), DB: store.NewMem(), Verifier: verifier})

	// A real edit, so the handler reaches the store rather than refusing an
	// empty patch.
	patch := editor.Patch{Add: []editor.Statement{{
		S: bibframe.WorkIRI(editWorkID),
		P: "https://github.com/freeeve/libcat/ns#tag",
		O: editor.Term{Kind: "literal", Value: "zz"},
	}}}
	rec := request(t, h, "PUT", "/v1/works/"+editWorkID, "lib-token", etag, patch)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT against a read-only store = %d %s, want 403", rec.Code, rec.Body)
	}
	if got := errorOf(t, rec.Body.Bytes()); got != readOnlyMessage {
		t.Fatalf("message = %q", got)
	}
}

// Every route that writes through mutateWorkGrain -- items, covers, relations,
// attachments -- reports store errors through writeMutateError. The sentinel has
// to survive that helper's wrapping, or those routes 500 the day the guard is
// relaxed or a store is mounted read-only for another reason.
func TestReadOnlyMutateWorkGrainReportsForbidden(t *testing.T) {
	mem := blob.NewMem()
	if _, err := mem.Put(t.Context(), bibframe.GrainPath(editWorkID),
		identityGrain(editWorkID, "A Book", "Le Guin, Ursula K.", ""), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	_, err := mutateWorkGrain(req, blob.ReadOnly(mem), nil, editWorkID, func(g []byte) ([]byte, error) {
		return append(g, []byte("\n")...), nil
	})
	if !errors.Is(err, blob.ErrReadOnly) {
		t.Fatalf("mutateWorkGrain err = %v; the read-only sentinel did not survive wrapping", err)
	}
	rec := httptest.NewRecorder()
	writeMutateError(rec, err)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("writeMutateError = %d, want 403", rec.Code)
	}
	if got := errorOf(t, rec.Body.Bytes()); got != readOnlyMessage {
		t.Fatalf("message = %q", got)
	}
}

// readOnlyAllowed matched on suffix, so any future route ending in
// "/ops" or "/marc" fell through the guard onto the blob store's mercy -- and a
// route that wrote to the document store would not be caught at all.
func TestReadOnlyAllowlistDoesNotMatchOnSuffix(t *testing.T) {
	blocked := []string{
		"/v1/copycat/targets/t1/ops",
		"/v1/profiles/marc",
		"/v1/exports/marc",
		"/v1/works/w123/items/ops",
		"/v1/queue/ops",
	}
	for _, path := range blocked {
		if readOnlyAllowed(path) {
			t.Errorf("readOnlyAllowed(%q) = true; only the named editor routes may pass", path)
		}
	}
	allowed := []string{
		"/v1/works/w123/ops",
		"/v1/works/w123/marc",
		"/v1/works/w123/marc/preview",
		"/v1/works/w123/validate",
		"/v1/works/w123/subjects/lookup",
		"/v1/batch/ops",
		"/v1/batch/resolve",
		"/v1/copycat/search",
		"/v1/auth/login",
	}
	for _, path := range allowed {
		if !readOnlyAllowed(path) {
			t.Errorf("readOnlyAllowed(%q) = false; the editor's preview paths must stay open", path)
		}
	}
}
