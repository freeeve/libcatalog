package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/batch"
	"github.com/freeeve/libcatalog/backend/editor"
	"github.com/freeeve/libcatalog/backend/export"
	"github.com/freeeve/libcatalog/backend/profiles"
	"github.com/freeeve/libcatalog/backend/store"
)

// testMapper builds an op mapper from the shipped defaults for batch tests.
func testMapper() *editor.Mapper {
	set, err := profiles.LoadDefaults()
	if err != nil {
		panic(err)
	}
	return &editor.Mapper{WorkProfile: set["work-monograph"], InstanceProfile: set["instance-ebook"]}
}

// newBatchAPI wires the handler with the batch service over three seeded
// works (two "Ninth" novels, one bystander).
func newBatchAPI(t *testing.T) (http.Handler, blob.Store) {
	t.Helper()
	bs := blob.NewMem()
	for id, title := range map[string]string{
		"wbatch0000001": "Gideon the Ninth",
		"wbatch0000002": "Harrow the Ninth",
		"wbatch0000003": "The Hobbit",
	} {
		seedBatchWork(t, bs, id, title)
	}
	svc := &batch.Service{Blob: bs, DB: store.NewMem(), Mapper: testMapper()}
	verifier := staffVerifier{
		"lib-token":   {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
		"lib2-token":  {Email: "lib2@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
		"mod-token":   {Email: "mod@example.org", Roles: []auth.Role{auth.RoleModerator}},
		"admin-token": {Email: "admin@example.org", Roles: []auth.Role{auth.RoleAdmin}},
	}
	return New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier, Batch: svc}), bs
}

func seedBatchWork(t *testing.T, bs blob.Store, workID, title string) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	tnode := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), tnode, feed)
	ds.Add(tnode, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral(title, "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestBatchOpsFlow(t *testing.T) {
	h, bs := newBatchAPI(t)

	// Below librarian is forbidden.
	if rec := request(t, h, http.MethodPost, "/v1/batch/resolve", "mod-token", "", map[string]any{
		"selection": map[string]any{"kind": "all"},
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("moderator resolve = %d", rec.Code)
	}

	// The op builder's profiles are served.
	rec := request(t, h, http.MethodGet, "/v1/profiles", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "work-monograph") {
		t.Fatalf("profiles = %d %s", rec.Code, rec.Body.String())
	}

	// Selection preview.
	rec = request(t, h, http.MethodPost, "/v1/batch/resolve", "lib-token", "", map[string]any{
		"selection": map[string]any{"kind": "search", "query": "ninth"},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"matched":2`) {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}

	ops := []map[string]any{{
		"resource": "work", "path": "summary", "action": "set",
		"values": []map[string]any{{"v": "A necromantic space opera.", "lang": "en"}},
	}}

	// Dry run: exact quad deltas, nothing written.
	rec = request(t, h, http.MethodPost, "/v1/batch/ops", "lib-token", "", map[string]any{
		"selection": map[string]any{"kind": "search", "query": "ninth"},
		"ops":       ops, "dryRun": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run = %d %s", rec.Code, rec.Body.String())
	}
	var dry batch.RunResult
	if err := json.Unmarshal(rec.Body.Bytes(), &dry); err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Matched != 2 || dry.Added == 0 || dry.Results[0].Diff == nil {
		t.Fatalf("dry = %+v", dry)
	}
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath("wbatch0000001"))
	if strings.Contains(string(grain), "necromantic") {
		t.Fatal("dry run wrote")
	}

	// Execute: per-record etags and the grains rewritten.
	rec = request(t, h, http.MethodPost, "/v1/batch/ops", "lib-token", "", map[string]any{
		"selection": map[string]any{"kind": "search", "query": "ninth"},
		"ops":       ops,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("execute = %d %s", rec.Code, rec.Body.String())
	}
	var run batch.RunResult
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Applied != 2 || run.Failed != 0 || run.Results[0].ETag == "" {
		t.Fatalf("run = %+v", run)
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath("wbatch0000002"))
	if !strings.Contains(string(grain), "A necromantic space opera.") {
		t.Fatalf("grain not rewritten:\n%s", grain)
	}

	// importBatch fails closed with a pointer to tasks/050.
	rec = request(t, h, http.MethodPost, "/v1/batch/resolve", "lib-token", "", map[string]any{
		"selection": map[string]any{"kind": "importBatch"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("importBatch = %d", rec.Code)
	}
}

func TestMacroEndpoints(t *testing.T) {
	h, bs := newBatchAPI(t)

	// Create a shared parameterized macro.
	rec := request(t, h, http.MethodPost, "/v1/macros", "lib-token", "", map[string]any{
		"label": "Series summary", "shared": true, "keys": "1",
		"ops": []map[string]any{{
			"resource": "work", "path": "summary", "action": "set",
			"values": []map[string]any{{"v": "${series} book.", "lang": "en"}},
		}},
		"params": []map[string]any{{"name": "series", "label": "Series name"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create macro = %d %s", rec.Code, rec.Body.String())
	}
	var m batch.Macro
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}

	// Another librarian sees it and runs it over a selection with params --
	// the MARC-modification-template shape.
	rec = request(t, h, http.MethodGet, "/v1/macros", "lib2-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Series summary") {
		t.Fatalf("shared list = %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, h, http.MethodPost, "/v1/batch/ops", "lib2-token", "", map[string]any{
		"selection": map[string]any{"kind": "search", "query": "ninth"},
		"macroId":   m.ID, "params": map[string]string{"series": "Locked Tomb"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("macro run = %d %s", rec.Code, rec.Body.String())
	}
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath("wbatch0000001"))
	if !strings.Contains(string(grain), "Locked Tomb book.") {
		t.Fatalf("template not applied:\n%s", grain)
	}

	// A missing parameter fails closed, and nothing is written.
	rec = request(t, h, http.MethodPost, "/v1/batch/ops", "lib2-token", "", map[string]any{
		"selection": map[string]any{"kind": "ids", "ids": []string{"wbatch0000003"}},
		"macroId":   m.ID,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing param = %d %s", rec.Code, rec.Body.String())
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath("wbatch0000003"))
	if strings.Contains(string(grain), "${series}") {
		t.Fatal("placeholder written to grain")
	}

	// Only the owner updates or deletes.
	if rec := request(t, h, http.MethodDelete, "/v1/macros/"+m.ID, "lib2-token", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign delete = %d", rec.Code)
	}
	if rec := request(t, h, http.MethodDelete, "/v1/macros/"+m.ID, "lib-token", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("owner delete = %d", rec.Code)
	}
}

// TestExportBatchSelection is the tasks/048 acceptance shape: an export of a
// search selection produces exactly those works, downloadable via the token
// route, and the job list reflects it.
func TestExportBatchSelection(t *testing.T) {
	bs := blob.NewMem()
	for id, title := range map[string]string{
		"wbatch0000001": "Gideon the Ninth",
		"wbatch0000002": "Harrow the Ninth",
		"wbatch0000003": "The Hobbit",
	} {
		seedBatchWork(t, bs, id, title)
	}
	db := store.NewMem()
	batchSvc := &batch.Service{Blob: bs, DB: db, Mapper: testMapper()}
	exports, err := export.New(store.NewMem(), bs, "overdrive", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: db, Verifier: verifier, Batch: batchSvc, Exports: exports})

	rec := request(t, h, http.MethodPost, "/v1/exports", "lib-token", "", map[string]any{
		"format":         "csv",
		"batchSelection": map[string]any{"kind": "search", "query": "ninth"},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create export = %d %s", rec.Code, rec.Body.String())
	}
	var job struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Records     int    `json:"records"`
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	// A 2-work selection runs synchronously and is immediately downloadable.
	if job.Status != "DONE" || job.Records != 2 || job.DownloadURL == "" {
		t.Fatalf("job = %+v", job)
	}
	rec = request(t, h, http.MethodGet, job.DownloadURL, "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "wbatch0000001") || !strings.Contains(body, "wbatch0000002") {
		t.Fatalf("export missing selected works:\n%s", body)
	}
	if strings.Contains(body, "wbatch0000003") {
		t.Fatalf("export includes unselected work:\n%s", body)
	}
	// The job list carries it.
	rec = request(t, h, http.MethodGet, "/v1/exports", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), job.ID) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	// An empty selection fails closed.
	rec = request(t, h, http.MethodPost, "/v1/exports", "lib-token", "", map[string]any{
		"format":         "csv",
		"batchSelection": map[string]any{"kind": "search", "query": "zebra-nothing"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty selection = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSavedQueryEndpoints(t *testing.T) {
	h, _ := newBatchAPI(t)
	rec := request(t, h, http.MethodPost, "/v1/queries", "lib-token", "", map[string]string{
		"label": "The Ninth series", "query": "ninth",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create query = %d %s", rec.Code, rec.Body.String())
	}
	var sq batch.SavedQuery
	if err := json.Unmarshal(rec.Body.Bytes(), &sq); err != nil {
		t.Fatal(err)
	}
	rec = request(t, h, http.MethodPost, "/v1/batch/resolve", "lib-token", "", map[string]any{
		"selection": map[string]any{"kind": "savedQuery", "savedQueryId": sq.ID},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"matched":2`) {
		t.Fatalf("savedQuery resolve = %d %s", rec.Code, rec.Body.String())
	}
	// Saved queries are per-user.
	rec = request(t, h, http.MethodPost, "/v1/batch/resolve", "lib2-token", "", map[string]any{
		"selection": map[string]any{"kind": "savedQuery", "savedQueryId": sq.ID},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign savedQuery = %d", rec.Code)
	}
	if rec := request(t, h, http.MethodDelete, "/v1/queries/"+sq.ID, "lib-token", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete query = %d", rec.Code)
	}
}
