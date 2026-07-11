// a batch record whose grain write failed reported the store's raw
// error -- an absolute path, the shard layout, a temp-file name and a syscall --
// straight into the results list a cataloger reads. The single-record route
// answers "grain write failed" for the identical failure, and the read path
// twelve lines up in the same function already mapped its own errors.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/store"
)

// leakyMessage is the property the reporter's W2 asserts, and it is deliberately
// not keyed on the string this build happens to produce: a message must name no
// filesystem path, no syscall, and no temp file. A check for "permission denied"
// would pass vacuously the day the error text changes.
var leakyMessage = regexp.MustCompile(`(?i)(/[a-z0-9_.-]+/|\.blob-|permission denied|no such file|syscall|EACCES|blob:)`)

// putFailStore fails the write to one grain path, so a batch can carry a broken
// record and a healthy one.
type putFailStore struct {
	blob.Store
	failPath string
}

func (s *putFailStore) Put(ctx context.Context, p string, b []byte, o blob.PutOptions) (string, error) {
	if p == s.failPath {
		return "", &os.PathError{Op: "open", Path: "/srv/libcat/data/works/31/.blob-12955389", Err: syscall.EACCES}
	}
	return s.Store.Put(ctx, p, b, o)
}

type batchOpsResponse struct {
	Matched int `json:"matched"`
	Applied int `json:"applied"`
	Failed  int `json:"failed"`
	Results []struct {
		WorkID string `json:"workId"`
		ETag   string `json:"etag"`
		Error  string `json:"error"`
	} `json:"results"`
}

// postBatchOps runs a tag-add over ids and decodes the report.
func postBatchOps(t *testing.T, h http.Handler, ids ...string) batchOpsResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"selection": map[string]any{"kind": "ids", "ids": ids},
		"ops":       []any{map[string]any{"resource": "work", "path": "tags", "action": "add", "value": map[string]any{"v": "zz"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/batch/ops", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer lib-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out batchOpsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// batchAPIOver mounts the batch route over st, seeded with two works.
func batchAPIOver(t *testing.T, st blob.Store) http.Handler {
	t.Helper()
	svc := &batch.Service{Blob: st, DB: store.NewMem(), Mapper: testMapper()}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	return New(Deps{Blob: st, DB: store.NewMem(), Verifier: verifier, Batch: svc})
}

func seedTwo(t *testing.T) blob.Store {
	t.Helper()
	bs := blob.NewMem()
	seedBatchWork(t, bs, "wbatch0000001", "Gideon the Ninth")
	seedBatchWork(t, bs, "wbatch0000002", "Harrow the Ninth")
	return bs
}

func errorFor(t *testing.T, resp batchOpsResponse, workID string) string {
	t.Helper()
	for _, r := range resp.Results {
		if r.WorkID == workID {
			return r.Error
		}
	}
	t.Fatalf("no result for %s", workID)
	return ""
}

// The finding, end to end through the router.
func TestBatchOpsDoesNotLeakTheStoreErrorToTheClient(t *testing.T) {
	st := seedTwo(t)
	h := batchAPIOver(t, &putFailStore{Store: st, failPath: bibframe.GrainPath("wbatch0000001")})

	resp := postBatchOps(t, h, "wbatch0000001", "wbatch0000002")

	if resp.Applied != 1 || resp.Failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 1/1", resp.Applied, resp.Failed)
	}
	got := errorFor(t, resp, "wbatch0000001")
	if m := leakyMessage.FindString(got); m != "" {
		t.Fatalf("the response leaks %q to the cataloger: %q", m, got)
	}
	if got != "grain write failed" {
		t.Fatalf("message = %q, want the single-record route's wording", got)
	}
	// W1: the healthy work in the same batch still applied.
	if errorFor(t, resp, "wbatch0000002") != "" {
		t.Fatal("the healthy work did not apply")
	}
}

// The same failure, one route away, has always answered "grain write failed".
// Pinning the pair is the point: the finding was that they disagreed.
func TestBatchOpsAgreesWithTheSingleRecordRoute(t *testing.T) {
	st := seedTwo(t)
	h := batchAPIOver(t, &putFailStore{Store: st, failPath: bibframe.GrainPath("wbatch0000001")})
	resp := postBatchOps(t, h, "wbatch0000001")
	if got := errorFor(t, resp, "wbatch0000001"); got != "grain write failed" {
		t.Fatalf("batch says %q; the single-record route says %q", got, "grain write failed")
	}
}

// The read path's mapped error still reaches the client unchanged: mapping the
// write path must not have cost the read path its wording.
func TestBatchOpsStillSaysNoSuchWork(t *testing.T) {
	h := batchAPIOver(t, seedTwo(t))
	resp := postBatchOps(t, h, "wmissing00001")
	if got := errorFor(t, resp, "wmissing00001"); got != "no such work" {
		t.Fatalf("message = %q, want %q", got, "no such work")
	}
}

// settled that a client must not be able to tell which layer refused
// it. batch cannot import httpapi, so the notice lives in both; this pins them
// equal rather than trusting two string literals to stay in step.
func TestReadOnlyNoticeMatchesTheBatchNotice(t *testing.T) {
	if batch.ReadOnlyNotice != readOnlyNotice {
		t.Fatalf("batch says %q; the guard says %q", batch.ReadOnlyNotice, readOnlyNotice)
	}
}

// The batch execute route is allowlisted in read-only mode, so it
// reaches the store. Every record used to report the blob package's own name.
func TestBatchOpsReportsReadOnlyWithoutNamingThePackage(t *testing.T) {
	h := batchAPIOver(t, blob.ReadOnly(seedTwo(t)))
	resp := postBatchOps(t, h, "wbatch0000001")

	got := errorFor(t, resp, "wbatch0000001")
	if strings.Contains(got, "blob:") {
		t.Fatalf("the response names an internal package: %q", got)
	}
	if got != readOnlyNotice {
		t.Fatalf("message = %q, want the guard's own wording %q", got, readOnlyNotice)
	}
}
