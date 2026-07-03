package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	codex "github.com/freeeve/libcodex"

	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/copycat"
	"github.com/freeeve/libcatalog/backend/store"
)

func newCopycatAPI(t *testing.T) (http.Handler, *copycat.Service) {
	t.Helper()
	svc := &copycat.Service{Blob: blob.NewMem(), DB: store.NewMem()}
	verifier := staffVerifier{
		"lib-token":   {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
		"admin-token": {Email: "admin@example.org", Roles: []auth.Role{auth.RoleAdmin}},
	}
	return New(Deps{Blob: svc.Blob, DB: store.NewMem(), Verifier: verifier, Copycat: svc}), svc
}

func TestCopycatFlow(t *testing.T) {
	h, svc := newCopycatAPI(t)

	// Targets: admin-gated writes, librarian reads.
	if rec := request(t, h, http.MethodPost, "/v1/copycat/targets", "lib-token", "", map[string]string{
		"name": "loc", "url": "http://lx2.loc.gov:210/LCDB", "protocol": "sru",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("librarian target write = %d", rec.Code)
	}
	if rec := request(t, h, http.MethodPost, "/v1/copycat/targets", "admin-token", "", map[string]string{
		"name": "loc", "url": "http://lx2.loc.gov:210/LCDB", "protocol": "sru",
	}); rec.Code != http.StatusOK {
		t.Fatalf("admin target write = %d %s", rec.Code, rec.Body.String())
	}
	rec := request(t, h, http.MethodGet, "/v1/copycat/targets", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "loc") {
		t.Fatalf("targets = %d %s", rec.Code, rec.Body.String())
	}

	// External search through the injected seam.
	svc.Search = func(_ context.Context, _ copycat.Target, query string, _ int) ([]*codex.Record, error) {
		r := codex.NewRecord()
		r.AddField(codex.NewControlField("001", "X1"))
		r.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', "Hit: "+query)))
		return []*codex.Record{r}, nil
	}
	rec = request(t, h, http.MethodPost, "/v1/copycat/search", "lib-token", "", map[string]any{"query": "gideon"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Hit: gideon") {
		t.Fatalf("search = %d %s", rec.Code, rec.Body.String())
	}
	var search struct {
		Results []copycat.SearchResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &search); err != nil {
		t.Fatal(err)
	}

	// Stage the search result, then a .mrc upload.
	rec = request(t, h, http.MethodPost, "/v1/copycat/batches", "lib-token", "", map[string]any{
		"label": "from loc", "source": "loc", "records": []any{search.Results[0].Record},
	})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"matchedWork":false`) {
		t.Fatalf("stage = %d %s", rec.Code, rec.Body.String())
	}

	mrc, err := os.ReadFile("../../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc")
	if err != nil {
		t.Fatal(err)
	}
	rec = request(t, h, http.MethodPost, "/v1/copycat/batches", "lib-token", "", map[string]any{
		"label": "upload", "mrc": base64.StdEncoding.EncodeToString(mrc),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	var staged struct {
		Batch copycat.Batch `json:"batch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &staged); err != nil {
		t.Fatal(err)
	}

	// Review (policy) + commit.
	rec = request(t, h, http.MethodPost, "/v1/copycat/batches/"+staged.Batch.ID+"/review", "lib-token", "",
		map[string]any{"policy": "replace-feed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("review = %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, h, http.MethodPost, "/v1/copycat/batches/"+staged.Batch.ID+"/commit", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"COMMITTED"`) {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body.String())
	}

	// The committed work is editable through the normal record surface: the
	// works listing sees it.
	rec = request(t, h, http.MethodGet, "/v1/works?q=", "lib-token", "", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("works after commit = %d %s", rec.Code, rec.Body.String())
	}

	// Batch list carries the outcome.
	rec = request(t, h, http.MethodGet, "/v1/copycat/batches", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "COMMITTED") {
		t.Fatalf("batches = %d %s", rec.Code, rec.Body.String())
	}
}
