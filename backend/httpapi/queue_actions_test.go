// POST /v1/queue/approve-all -- the filter-scoped bulk approve-all (task 473
// part 2): two-step confirm (count, then echo it back), runs as a job on the
// enrichment board, approves only rows matching the filter, reversible until
// publish.
package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/enrich"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// newQueueActionsAPI wires the review queue and the enrichment job board over
// one shared store, which is what the approve-all endpoint needs.
func newQueueActionsAPI(t *testing.T) (http.Handler, *suggest.Service) {
	t.Helper()
	bs := blob.NewMem()
	db := store.NewMem()
	verifier := staffVerifier{
		"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
	}
	queue := suggest.New(db, nil, suggest.Caps{})
	enr := &enrich.Service{DB: db, Queue: queue}
	h := New(Deps{Blob: bs, DB: db, Verifier: verifier, Suggest: queue, Enrich: enr})
	return h, queue
}

func homoTerm(id string) vocab.TermRef {
	return vocab.TermRef{Scheme: "homosaurus", ID: "https://homosaurus.org/v5/" + id, Label: "Queer people"}
}

// waitStatus polls a work's single suggestion until it reaches want or times out.
func waitStatus(t *testing.T, svc *suggest.Service, workID string, want suggest.Status) {
	t.Helper()
	for i := 0; i < 200; i++ {
		items, err := svc.ForWork(t.Context(), workID)
		if err == nil && len(items) == 1 && items[0].Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	items, _ := svc.ForWork(t.Context(), workID)
	t.Fatalf("%s did not reach %s in time: %+v", workID, want, items)
}

func TestApproveAllEndpointTwoStepConfirm(t *testing.T) {
	h, queue := newQueueActionsAPI(t)
	// Three homosaurus rows (in scope) and one folk row (out of scope).
	for _, id := range []string{"wqa0000001a", "wqa0000001b", "wqa0000001c"} {
		if err := queue.PipelineSuggest(t.Context(), id, homoTerm("homoit0000"+id[10:]), 0.9); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.PipelineSuggest(t.Context(), "wqa0000001d",
		vocab.TermRef{Scheme: vocab.FolkScheme, ID: "cozy"}, 0.9); err != nil {
		t.Fatal(err)
	}

	// Step 1: no confirm -> the count it would act on, no mutation.
	rec := request(t, h, http.MethodPost, "/v1/queue/approve-all?scheme=homosaurus", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run = %d (%s)", rec.Code, rec.Body.String())
	}
	var dry struct {
		Count           int  `json:"count"`
		ConfirmRequired bool `json:"confirmRequired"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dry); err != nil {
		t.Fatal(err)
	}
	if dry.Count != 3 || !dry.ConfirmRequired {
		t.Fatalf("dry run = %+v, want count 3 and confirmRequired", dry)
	}

	// Step 2a: a wrong confirm count is a 409, nothing runs.
	rec = request(t, h, http.MethodPost, "/v1/queue/approve-all?scheme=homosaurus&confirm=99", "lib-token", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatched confirm = %d, want 409", rec.Code)
	}

	// Step 2b: the right count starts the job.
	rec = request(t, h, http.MethodPost, "/v1/queue/approve-all?scheme=homosaurus&confirm=3", "lib-token", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("confirmed = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	var job struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.Kind != "QUEUE_APPROVE" {
		t.Fatalf("job = %+v, want a QUEUE_APPROVE job with an id", job)
	}

	// The job approves the three in-scope rows and leaves the folk row PENDING.
	for _, id := range []string{"wqa0000001a", "wqa0000001b", "wqa0000001c"} {
		waitStatus(t, queue, id, suggest.StatusApproved)
	}
	if items, _ := queue.ForWork(t.Context(), "wqa0000001d"); items[0].Status != suggest.StatusPending {
		t.Fatalf("folk row = %+v, want still PENDING (out of scope)", items[0])
	}
}
