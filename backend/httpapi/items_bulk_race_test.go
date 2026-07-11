package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// newRecordsAPIOver is newRecordsAPI against a caller-supplied blob store, so a
// test can wrap it and induce store behavior.
func newRecordsAPIOver(t *testing.T, bs blob.Store) http.Handler {
	t.Helper()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	return New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})
}

// seedBulkWork puts a grain with one instance and returns the instance id.
func seedBulkWork(t *testing.T, bs blob.Store, workID, title string) string {
	t.Helper()
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID),
		identityGrain(workID, title, "Le Guin, Ursula K.", "9780547773742"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	return workID + "i"
}

// bulkAdd posts one bulk-add request and returns the status and the barcodes
// the response claims it minted.
func bulkAdd(t *testing.T, h http.Handler, workID, instanceID, prefix string, count int) (int, []string) {
	t.Helper()
	body := map[string]any{"instanceId": instanceID, "count": count, "barcodePrefix": prefix, "barcodeWidth": 4}
	rec := request(t, h, "POST", "/v1/works/"+workID+"/items/bulk", "lib-token", "", body)
	var out struct {
		Items []struct {
			Barcode string `json:"barcode"`
		} `json:"items"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", rec.Body, err)
		}
	}
	got := make([]string, 0, len(out.Items))
	for _, i := range out.Items {
		got = append(got, i.Barcode)
	}
	return rec.Code, got
}

// persistedBarcodes reads the barcodes actually stored on a work's instance.
func persistedBarcodes(t *testing.T, bs blob.Store, workID, instanceID string) []string {
	t.Helper()
	grain, _, err := bs.Get(t.Context(), bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	items, err := bibframe.ItemsOf(grain, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.Barcode)
	}
	return out
}

// duplicates returns the values appearing more than once.
func duplicates(in []string) []string {
	seen, dup := map[string]int{}, []string{}
	for _, s := range in {
		seen[s]++
	}
	for s, n := range seen {
		if n > 1 {
			dup = append(dup, s)
		}
	}
	return dup
}

// Control: the same two adds run one after the other allocate cleanly. This is
// what makes the concurrent cases below about simultaneity and nothing else.
func TestBulkAddSequentialBarcodesAreDistinct(t *testing.T) {
	h, bs := newRecordsAPI(t)
	workID := "wbulkseq123"
	instanceID := seedBulkWork(t, bs, workID, "A Wizard of Earthsea")

	for range 2 {
		if code, _ := bulkAdd(t, h, workID, instanceID, "zzseq", 3); code != http.StatusOK {
			t.Fatalf("sequential add = %d", code)
		}
	}
	got := persistedBarcodes(t, bs, workID, instanceID)
	if len(got) != 6 {
		t.Fatalf("persisted %d barcodes, want 6: %v", len(got), got)
	}
	if dup := duplicates(got); len(dup) != 0 {
		t.Fatalf("sequential adds duplicated %v", dup)
	}
}

// two simultaneous bulk adds to the SAME work chose their barcodes
// from an index snapshot taken before either wrote, and mutateWorkGrain's CAS
// retry faithfully appended the stale allocation to the winning grain.
func TestBulkAddConcurrentSameWorkMintsNoDuplicates(t *testing.T) {
	h, bs := newRecordsAPI(t)
	workID := "wbulksame12"
	instanceID := seedBulkWork(t, bs, workID, "A Wizard of Earthsea")

	const adds, count = 2, 4
	codes := make([]int, adds)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range adds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i], _ = bulkAdd(t, h, workID, instanceID, "zzsam", count)
		}()
	}
	close(start)
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("add %d = %d; both adds must succeed for this to be a duplication bug", i, c)
		}
	}
	got := persistedBarcodes(t, bs, workID, instanceID)
	if len(got) != adds*count {
		t.Fatalf("persisted %d barcodes, want %d: %v", len(got), adds*count, got)
	}
	if dup := duplicates(got); len(dup) != 0 {
		t.Fatalf("two concurrent adds minted the same barcode(s) %v; all: %v", dup, got)
	}
}

// The cross-work case has no shared CAS object: two grains, two paths, two
// independent IfMatch checks, both satisfied. Nothing can detect the collision,
// and two different works end up carrying the same physical barcode.
func TestBulkAddConcurrentDifferentWorksMintNoDuplicates(t *testing.T) {
	h, bs := newRecordsAPI(t)
	workA, workB := "wbulkxa1234", "wbulkxb1234"
	instA := seedBulkWork(t, bs, workA, "A Wizard of Earthsea")
	instB := seedBulkWork(t, bs, workB, "The Tombs of Atuan")

	const count = 4
	type target struct{ work, inst string }
	targets := []target{{workA, instA}, {workB, instB}}
	codes := make([]int, len(targets))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, tg := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i], _ = bulkAdd(t, h, tg.work, tg.inst, "zzx", count)
		}()
	}
	close(start)
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("add %d = %d", i, c)
		}
	}
	all := append(persistedBarcodes(t, bs, workA, instA), persistedBarcodes(t, bs, workB, instB)...)
	if len(all) != 2*count {
		t.Fatalf("persisted %d barcodes across both works, want %d: %v", len(all), 2*count, all)
	}
	if dup := duplicates(all); len(dup) != 0 {
		t.Fatalf("two works carry the same barcode(s) %v; all: %v", dup, all)
	}
}

// conflictOnceBlob makes the first conditional grain write lose its CAS, after
// landing a competing grain underneath -- a writer that is not a bulk add, so
// the allocation lock cannot see it. This is the case the in-closure
// reallocation exists for: the retry must allocate against the grain it is
// actually appending to.
type conflictOnceBlob struct {
	blob.Store
	grainPath string
	competing []byte
	done      bool
}

func (c *conflictOnceBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if !c.done && path == c.grainPath && opts.IfMatch != "" {
		c.done = true
		if _, err := c.Store.Put(ctx, path, c.competing, blob.PutOptions{}); err != nil {
			return "", err
		}
		return "", blob.ErrPreconditionFailed
	}
	return c.Store.Put(ctx, path, data, opts)
}

// a CAS retry re-runs the closure because the grain moved. Barcodes
// chosen against the superseded grain must not be laundered into the winner.
func TestBulkAddRetryReallocatesAgainstTheFreshGrain(t *testing.T) {
	mem := blob.NewMem()
	workID := "wbulkcas123"
	instanceID := workID + "i"
	if _, err := mem.Put(t.Context(), bibframe.GrainPath(workID),
		identityGrain(workID, "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	// The competing grain already holds zzret0001, so the retry must skip it.
	base, _, err := mem.Get(t.Context(), bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	competing, err := bibframe.SetItems(base, instanceID, []bibframe.Item{{Barcode: "zzret0001"}})
	if err != nil {
		t.Fatal(err)
	}
	bs := &conflictOnceBlob{Store: mem, grainPath: bibframe.GrainPath(workID), competing: competing}
	h := newRecordsAPIOver(t, bs)

	code, claimed := bulkAdd(t, h, workID, instanceID, "zzret", 2)
	if code != http.StatusOK {
		t.Fatalf("bulk add = %d", code)
	}
	if !bs.done {
		t.Fatal("the CAS conflict never fired; the retry path is not under test")
	}
	got := persistedBarcodes(t, bs, workID, instanceID)
	if dup := duplicates(got); len(dup) != 0 {
		t.Fatalf("the retry re-used the competing writer's barcode(s) %v; all: %v", dup, got)
	}
	if len(got) != 3 {
		t.Fatalf("persisted %v, want the competing item plus 2 new ones", got)
	}
	for _, bc := range claimed {
		if bc == "zzret0001" {
			t.Fatalf("the response claims %q, which the competing writer already holds: %v", bc, claimed)
		}
	}
}

// The response must promise exactly what was written. A retry that reallocates
// has to report the barcodes it actually stored, not the ones it first chose.
func TestBulkAddResponseMatchesWhatWasPersisted(t *testing.T) {
	h, bs := newRecordsAPI(t)
	workID := "wbulkresp12"
	instanceID := seedBulkWork(t, bs, workID, "A Wizard of Earthsea")

	const adds, count = 2, 3
	claimed := make([][]string, adds)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range adds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimed[i] = bulkAdd(t, h, workID, instanceID, "zzrsp", count)
		}()
	}
	close(start)
	wg.Wait()

	persisted := map[string]bool{}
	for _, bc := range persistedBarcodes(t, bs, workID, instanceID) {
		persisted[bc] = true
	}
	for i, list := range claimed {
		if len(list) != count {
			t.Fatalf("add %d claimed %d barcodes, want %d", i, len(list), count)
		}
		for _, bc := range list {
			if !persisted[bc] {
				t.Fatalf("add %d reported barcode %q that is not on the record", i, bc)
			}
		}
	}
}

// The 200-item cap is checked against a grain read before the mutation. Two
// concurrent adds must not carry an instance past it.
func TestBulkAddConcurrentCannotExceedTheItemCap(t *testing.T) {
	h, bs := newRecordsAPI(t)
	workID := "wbulkcap123"
	instanceID := seedBulkWork(t, bs, workID, "A Wizard of Earthsea")

	// 180 items already, then two concurrent adds of 20 each: 220 > 200.
	for range 2 {
		if code, _ := bulkAdd(t, h, workID, instanceID, "zzcap", 90); code != http.StatusOK {
			t.Fatalf("seed add = %d", code)
		}
	}
	if n := len(persistedBarcodes(t, bs, workID, instanceID)); n != 180 {
		t.Fatalf("seeded %d items, want 180", n)
	}

	codes := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i], _ = bulkAdd(t, h, workID, instanceID, "zzcap", 20)
		}()
	}
	close(start)
	wg.Wait()

	if n := len(persistedBarcodes(t, bs, workID, instanceID)); n > 200 {
		t.Fatalf("instance holds %d items, past the 200 cap; codes %v", n, codes)
	}
	// Exactly one add can fit.
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("codes = %v, want exactly one 200 and one rejection", codes)
	}
}
