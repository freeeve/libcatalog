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

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/marcview"
	"github.com/freeeve/libcat/backend/store"
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
	svc.Search = func(_ context.Context, _ copycat.Target, terms []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
		line := ""
		for _, ft := range terms {
			line += ft.Index + "=" + ft.Term + ";"
		}
		r := codex.NewRecord()
		r.AddField(codex.NewControlField("001", "X1"))
		r.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', "Hit: "+line)))
		return []*codex.Record{r}, nil
	}
	rec = request(t, h, http.MethodPost, "/v1/copycat/search", "lib-token", "", map[string]any{"query": "gideon"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Hit: any=gideon;") {
		t.Fatalf("search = %d %s", rec.Code, rec.Body.String())
	}

	// Fielded search (tasks/074): fields ride the same endpoint and AND on;
	// an unknown index is refused.
	rec = request(t, h, http.MethodPost, "/v1/copycat/search", "lib-token", "", map[string]any{
		"query":  "gideon",
		"fields": []map[string]string{{"index": "isbn", "term": "9781250313195"}},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Hit: any=gideon;isbn=9781250313195;") {
		t.Fatalf("fielded search = %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(t, h, http.MethodPost, "/v1/copycat/search", "lib-token", "", map[string]any{
		"fields": []map[string]string{{"index": "dewey", "term": "813"}},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown index = %d %s", rec.Code, rec.Body.String())
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

// TestOriginalRecordFlow is the tasks/077 surface: templates list, the
// field-anchored refusal, and staging a titled draft as source "original".
func TestOriginalRecordFlow(t *testing.T) {
	h, _ := newCopycatAPI(t)

	rec := request(t, h, http.MethodGet, "/v1/copycat/templates", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"book"`) {
		t.Fatalf("templates = %d %s", rec.Code, rec.Body.String())
	}
	var tpls struct {
		Templates []copycat.Template `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tpls); err != nil {
		t.Fatal(err)
	}
	var book copycat.Template
	for _, tpl := range tpls.Templates {
		if tpl.ID == "book" {
			book = tpl
		}
	}

	// Untitled skeleton: refused with the error anchored to 245.
	rec = request(t, h, http.MethodPost, "/v1/copycat/original", "lib-token", "", map[string]any{"record": book.Record})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"tag":"245"`) {
		t.Fatalf("untitled = %d %s", rec.Code, rec.Body.String())
	}

	// Titled: stages as a normal batch with source "original".
	for i, f := range book.Record.Fields {
		if f.Tag == "245" {
			book.Record.Fields[i].Subfields = []marcview.Subfield{{Code: "a", Value: "Original works"}}
		}
	}
	rec = request(t, h, http.MethodPost, "/v1/copycat/original", "lib-token", "", map[string]any{"record": book.Record})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"source":"original"`) {
		t.Fatalf("stage = %d %s", rec.Code, rec.Body.String())
	}
}
