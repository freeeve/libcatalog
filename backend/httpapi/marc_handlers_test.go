package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	codexbf "github.com/freeeve/libcodex/bibframe"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/marcview"
	"github.com/freeeve/libcat/backend/store"
)

// seedMARCGrain ingests the vendored MARC Express sample the way the marc
// provider does (crosswalk + verbatim sidecar) and puts the grain in bs.
func seedMARCGrain(t *testing.T, bs blob.Store, workID, instanceID string) {
	t.Helper()
	f, err := os.Open("../../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := bibframe.ReadMARC(f)
	if err != nil || len(recs) == 0 {
		t.Fatalf("read sample: %v", err)
	}
	bib := codexbf.FromRecord(recs[0])
	wg := bibframe.WorkGroup{
		WorkID: workID,
		Work:   bib.Work,
		Instances: []bibframe.GroupInstance{{
			InstanceID: instanceID, Instance: bib.Instance,
			Verbatim: bibframe.VerbatimFields(recs[0]),
		}},
	}
	dir := t.TempDir()
	if _, err := bibframe.BuildWorks(storage.Dir(dir), []bibframe.WorkGroup{wg}, "marc"); err != nil {
		t.Fatal(err)
	}
	grain, err := os.ReadFile(dir + "/" + bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestMARCViewFlow(t *testing.T) {
	bs := blob.NewMem()
	const workID = "wmarc00000001"
	seedMARCGrain(t, bs, workID, "imarc00000001")
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})

	// Materialize: records, etag, the loss table for warnings.
	rec := request(t, h, http.MethodGet, "/v1/works/"+workID+"/marc", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get marc = %d %s", rec.Code, rec.Body.String())
	}
	var view struct {
		ETag      string               `json:"etag"`
		Records   []marcview.RecordDoc `json:"records"`
		KnownLoss map[string]string    `json:"knownLoss"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Records) != 1 || view.KnownLoss["037"] == "" {
		t.Fatalf("view = %d records, knownLoss %v", len(view.Records), view.KnownLoss)
	}

	// Untouched save: 200, empty diff, same etag, grain untouched.
	before, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/marc", "lib-token", view.ETag,
		map[string]any{"index": 0, "record": view.Records[0]})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), view.ETag) {
		t.Fatalf("untouched save = %d %s", rec.Code, rec.Body.String())
	}
	after, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	if string(before) != string(after) {
		t.Fatal("untouched save rewrote the grain")
	}

	// Edit one field; dry-run shows the delta, save applies it.
	edited := view.Records[0]
	for i, f := range edited.Fields {
		if f.Tag == "520" {
			edited.Fields[i].Subfields = []marcview.Subfield{{Code: "a", Value: "An edited summary."}}
		}
	}
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/marc", "lib-token", "",
		map[string]any{"index": 0, "record": edited, "dryRun": true})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "An edited summary.") {
		t.Fatalf("dry run = %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/marc", "lib-token", view.ETag,
		map[string]any{"index": 0, "record": edited})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	// A stale etag now conflicts.
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/marc", "lib-token", view.ETag,
		map[string]any{"index": 0, "record": edited})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale save = %d", rec.Code)
	}
	// The view reflects the edit exactly once.
	rec = request(t, h, http.MethodGet, "/v1/works/"+workID+"/marc", "lib-token", "", nil)
	if strings.Count(rec.Body.String(), "An edited summary.") != 1 {
		t.Fatalf("edited summary count wrong: %s", rec.Body.String())
	}
}
