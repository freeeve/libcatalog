package export

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

const authFixture = `<https://example.org/auth/t1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Zines"@en <authority:local> .
<https://example.org/auth/t1> <http://www.w3.org/2004/02/skos/core#altLabel> "Fanzines"@en <authority:local> .
<https://example.org/auth/t1> <http://www.w3.org/2004/02/skos/core#broader> <https://example.org/auth/t2> <authority:local> .
<https://example.org/auth/t1> <http://www.w3.org/2004/02/skos/core#exactMatch> <http://id.loc.gov/authorities/genreForms/gf2014026266> <authority:local> .
<https://example.org/auth/t1> <http://www.w3.org/2004/02/skos/core#definition> "Self-published small-circulation works"@en <authority:local> .
<https://example.org/auth/t2> <http://www.w3.org/2004/02/skos/core#prefLabel> "Periodicals"@en <authority:local> .
<https://example.org/auth/g1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Graphic novels"@en <authority:lcgft> .
`

func newAuthorityService(t *testing.T) *Service {
	t.Helper()
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "data/authorities/ab/fixture.nq", []byte(authFixture), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(store.NewMem(), bs, "overdrive", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc.Vocab = ix
	return svc
}

func TestAuthorityExportValidation(t *testing.T) {
	svc := newAuthorityService(t)
	ctx := t.Context()
	if _, err := svc.CreateAuthorities(ctx, "eve", FormatCSV, AuthoritySelection{All: true}); err == nil {
		t.Fatal("csv authority export must refuse")
	}
	if _, err := svc.CreateAuthorities(ctx, "eve", FormatMARC, AuthoritySelection{}); err == nil {
		t.Fatal("empty selection must refuse")
	}
}

func TestAuthorityExportNQuads(t *testing.T) {
	svc := newAuthorityService(t)
	ctx := t.Context()
	job, err := svc.CreateAuthorities(ctx, "eve", FormatNQuads, AuthoritySelection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusQueued {
		t.Fatalf("full dump should queue, got %s", job.Status)
	}
	if _, err := svc.RunQueued(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ = svc.Get(ctx, "eve", job.ID, true)
	if job.Status != StatusDone || job.Records != 3 {
		t.Fatalf("job = %+v", job)
	}
	out, err := svc.Open(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{`"Zines"@en`, "<authority:local>", "<authority:lcgft>", "skos/core#exactMatch"} {
		if !strings.Contains(text, want) {
			t.Errorf("nquads missing %q:\n%s", want, text)
		}
	}
}

func TestAuthorityExportMARCLabelFiltered(t *testing.T) {
	svc := newAuthorityService(t)
	ctx := t.Context()
	// Label-filtered exports run in-request.
	job, err := svc.CreateAuthorities(ctx, "eve", FormatMARC, AuthoritySelection{Vocabs: []string{"local"}, Label: "zin"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusDone || job.Records != 1 {
		t.Fatalf("job = %+v", job)
	}
	out, err := svc.Open(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := bibframe.ReadMARC(bytes.NewReader(out))
	if err != nil || len(recs) != 1 {
		t.Fatalf("marc parse: %d records, %v", len(recs), err)
	}
	rec := recs[0]
	if rec.Leader().String()[6] != 'z' {
		t.Errorf("leader type = %c, want z (authority)", rec.Leader().String()[6])
	}
	if got := rec.SubfieldValue("150", 'a'); got != "Zines" {
		t.Errorf("150$a = %q", got)
	}
	if got := rec.SubfieldValue("450", 'a'); got != "Fanzines" {
		t.Errorf("450$a = %q", got)
	}
	if got := rec.SubfieldValue("550", 'a'); got != "Periodicals" {
		t.Errorf("550$a = %q (broader label)", got)
	}
	if got := rec.SubfieldValue("750", '0'); !strings.Contains(got, "gf2014026266") {
		t.Errorf("750$0 = %q (exactMatch)", got)
	}
	if got := rec.SubfieldValue("680", 'i'); !strings.Contains(got, "Self-published") {
		t.Errorf("680$i = %q", got)
	}
}

func TestAuthorityExportJSONLD(t *testing.T) {
	svc := newAuthorityService(t)
	ctx := t.Context()
	job, err := svc.CreateAuthorities(ctx, "eve", FormatJSONLD, AuthoritySelection{Vocabs: []string{"lcgft"}, Label: "graphic"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Open(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Graph []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Graph) != 1 || doc.Graph[0]["@id"] != "https://example.org/auth/g1" {
		t.Fatalf("graph = %+v", doc.Graph)
	}
}
