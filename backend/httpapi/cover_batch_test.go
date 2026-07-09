package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

const isbnWorkID = "wisbn12345678"

// seedISBNGrain adds a second work whose instance carries an ISBN, so batch
// entries can resolve by identifier.
func seedISBNGrain(t *testing.T, bs blob.Store, isbn string) {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	bf := "http://id.loc.gov/ontologies/bibframe/"
	work := rdf.NewIRI(bibframe.WorkIRI(isbnWorkID))
	inst := rdf.NewIRI(bibframe.InstanceIRI("iisbn12345678"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bf+"Work"), feed)
	ds.Add(work, rdf.NewIRI(bf+"hasInstance"), inst, feed)
	idNode := rdf.NewBlank("id0")
	ds.Add(inst, rdf.NewIRI(bf+"identifiedBy"), idNode, feed)
	ds.Add(idNode, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bf+"Isbn"), feed)
	ds.Add(idNode, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#value"), rdf.NewLiteral(isbn, "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(isbnWorkID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// buildZip packs name->bytes entries into a zip.
func buildZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestCoverBatch covers tasks/220 (058 item 2 remainder): a zip keyed by
// work id and by hyphenated ISBN applies covers through the grain-first
// path; unknown names, phantom ids, and bad types skip with reasons.
func TestCoverBatch(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	seedISBNGrain(t, bs, "9781250313195")
	png := []byte("\x89PNG\r\n\x1a\nfakebytes")

	zipBytes := buildZip(t, map[string][]byte{
		editWorkID + ".png":        png,
		"978-1-250-31319-5.jpg":    png, // resolves via normalized ISBN
		"covers/nonsense.png":      png, // not an id or isbn
		"wzzzz00phantom.png":       png, // id-shaped but no grain
		editWorkID + ".gif":        png, // bad type
		"__MACOSX/._" + editWorkID: png, // resource-fork noise, ignored
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/covers/batch", bytes.NewReader(zipBytes))
	req.Header.Set("Authorization", "Bearer lib-token")
	req.Header.Set("Content-Type", "application/zip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Applied int
		Results []coverBatchResult
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Applied != 2 {
		t.Fatalf("applied = %d, want 2: %+v", out.Applied, out.Results)
	}
	byFile := map[string]coverBatchResult{}
	for _, r := range out.Results {
		byFile[r.File] = r
	}
	if r := byFile[editWorkID+".png"]; r.Skipped != "" || r.Cover != "covers/"+editWorkID+".png" {
		t.Fatalf("workId entry = %+v", r)
	}
	if r := byFile["978-1-250-31319-5.jpg"]; r.Skipped != "" || r.WorkID != isbnWorkID || r.Cover != "covers/"+isbnWorkID+".jpg" {
		t.Fatalf("isbn entry = %+v", r)
	}
	if r := byFile["covers/nonsense.png"]; r.Skipped != "not a work id or known isbn" {
		t.Fatalf("nonsense entry = %+v", r)
	}
	if r := byFile["wzzzz00phantom.png"]; r.Skipped != "no such work" {
		t.Fatalf("phantom entry = %+v", r)
	}
	if r := byFile[editWorkID+".gif"]; r.Skipped != "not jpg/png/webp" {
		t.Fatalf("gif entry = %+v", r)
	}
	if _, ok := byFile["__MACOSX/._"+editWorkID]; ok {
		t.Fatal("resource-fork entry not ignored")
	}

	// Both grains carry the editorial cover statement and the bytes exist.
	for _, pair := range [][2]string{{editWorkID, "png"}, {isbnWorkID, "jpg"}} {
		grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(pair[0]))
		if !strings.Contains(string(grain), "covers/"+pair[0]+"."+pair[1]) {
			t.Fatalf("grain %s missing cover statement:\n%s", pair[0], grain)
		}
		if _, _, err := bs.Get(t.Context(), bibframe.CoverBlobPath(pair[0], pair[1])); err != nil {
			t.Fatalf("cover bytes %s: %v", pair[0], err)
		}
	}

	// Anonymous refuses; garbage refuses.
	anon := httptest.NewRequest(http.MethodPost, "/v1/covers/batch", bytes.NewReader(zipBytes))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, anon)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon batch = %d", rec.Code)
	}
	bad := httptest.NewRequest(http.MethodPost, "/v1/covers/batch", strings.NewReader("not a zip"))
	bad.Header.Set("Authorization", "Bearer lib-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad zip = %d", rec.Code)
	}
}

func TestNormalizeISBN(t *testing.T) {
	cases := map[string]string{
		"978-1-250-31319-5": "9781250313195",
		"0 306 40615 2":     "0306406152",
		"030640615x":        "030640615X",
		"12345":             "",
		"97812503131959":    "",
		"not-an-isbn":       "",
	}
	for in, want := range cases {
		if got := normalizeISBN(in); got != want {
			t.Errorf("normalizeISBN(%q) = %q, want %q", in, got, want)
		}
	}
}
