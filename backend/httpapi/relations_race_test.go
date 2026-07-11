package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// newRelationsAPI builds the API over bs with an audit-readable queue.
func newRelationsAPI(t *testing.T, bs blob.Store) (http.Handler, *suggest.Service) {
	t.Helper()
	db := store.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	queue := suggest.New(db, nil, suggest.Caps{})
	return New(Deps{Blob: bs, DB: db, Verifier: verifier, Suggest: queue}), queue
}

func seedRelationWork(t *testing.T, bs blob.Store, workID, title string) {
	t.Helper()
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID),
		identityGrain(workID, title, "Le Guin, Ursula K.", ""), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func relate(t *testing.T, h http.Handler, workID, kind, target string) int {
	t.Helper()
	rec := request(t, h, "POST", "/v1/works/"+workID+"/relations", "lib-token", "",
		map[string]any{"kind": kind, "target": target})
	return rec.Code
}

func unrelate(t *testing.T, h http.Handler, workID, kind, target string) int {
	t.Helper()
	rec := request(t, h, "DELETE", "/v1/works/"+workID+"/relations", "lib-token", "",
		map[string]any{"kind": kind, "target": target})
	return rec.Code
}

// relationsOf reads a work's direct hasPart/partOf edges.
func relationsOf(t *testing.T, h http.Handler, workID string) (hasPart, partOf []string) {
	t.Helper()
	rec := request(t, h, "GET", "/v1/works/"+workID+"/relations", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET relations %s: %d %s", workID, rec.Code, rec.Body)
	}
	var out struct {
		HasPart []struct {
			WorkID string `json:"workId"`
		} `json:"hasPart"`
		PartOf []struct {
			WorkID string `json:"workId"`
		} `json:"partOf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, e := range out.HasPart {
		hasPart = append(hasPart, e.WorkID)
	}
	for _, e := range out.PartOf {
		partOf = append(partOf, e.WorkID)
	}
	return hasPart, partOf
}

// Control: run sequentially, the second add is refused. This is what makes the
// concurrent test below about simultaneity and nothing else.
func TestRelationCycleGuardRefusesSequentialOppositeAdds(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newRelationsAPI(t, bs)
	seedRelationWork(t, bs, "wrelseqa123", "A")
	seedRelationWork(t, bs, "wrelseqb123", "B")

	if code := relate(t, h, "wrelseqa123", "hasPart", "wrelseqb123"); code != http.StatusNoContent {
		t.Fatalf("first add = %d", code)
	}
	if code := relate(t, h, "wrelseqb123", "hasPart", "wrelseqa123"); code != http.StatusBadRequest {
		t.Fatalf("the opposite add = %d, want 400 (would create a containment cycle)", code)
	}
}

// containmentCycle is a time-of-check test run before either grain
// is written. Two adds fired in opposite directions each saw a graph without
// the other's edge, both passed, and both wrote their forward statement -- a
// C hasPart D hasPart C cycle, across two grains the backstop cannot compare.
func TestRelationConcurrentOppositeAddsCannotCloseACycle(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newRelationsAPI(t, bs)
	c, d := "wrelracec12", "wrelraced12"
	seedRelationWork(t, bs, c, "C")
	seedRelationWork(t, bs, d, "D")

	codes := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	pairs := [2][2]string{{c, d}, {d, c}}
	for i, p := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = relate(t, h, p[0], "hasPart", p[1])
		}()
	}
	close(start)
	wg.Wait()

	cHas, cPart := relationsOf(t, h, c)
	dHas, dPart := relationsOf(t, h, d)

	// The cycle is the defect: C hasPart D and D hasPart C together.
	if len(cHas) == 1 && cHas[0] == d && len(dHas) == 1 && dHas[0] == c {
		t.Fatalf("the graph holds a containment cycle C->D->C; codes %v", codes)
	}

	// Exactly one add must win, and it must leave a well-formed pair of edges.
	ok, refused := 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusNoContent:
			ok++
		case http.StatusBadRequest:
			refused++
		default:
			t.Fatalf("codes = %v; a relation add must answer 204 or 400, never 500", codes)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("codes = %v, want exactly one 204 and one 400", codes)
	}
	// Whichever won, the two grains agree: one contains, the other is part of.
	switch {
	case len(cHas) == 1 && cHas[0] == d:
		if len(dPart) != 1 || dPart[0] != c || len(dHas) != 0 || len(cPart) != 0 {
			t.Fatalf("C hasPart D won but the edges are %v/%v and %v/%v", cHas, cPart, dHas, dPart)
		}
	case len(dHas) == 1 && dHas[0] == c:
		if len(cPart) != 1 || cPart[0] != d || len(cHas) != 0 || len(dPart) != 0 {
			t.Fatalf("D hasPart C won but the edges are %v/%v and %v/%v", cHas, cPart, dHas, dPart)
		}
	default:
		t.Fatalf("no link survived: %v/%v and %v/%v", cHas, cPart, dHas, dPart)
	}
}

// The same race through the other kind: A partOf B and B partOf A both write
// their forward statement, and the inverse walk sees the same cycle.
func TestRelationConcurrentOppositePartOfAddsCannotCloseACycle(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newRelationsAPI(t, bs)
	c, d := "wrelpartc12", "wrelpartd12"
	seedRelationWork(t, bs, c, "C")
	seedRelationWork(t, bs, d, "D")

	codes := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	pairs := [2][2]string{{c, d}, {d, c}}
	for i, p := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = relate(t, h, p[0], "partOf", p[1])
		}()
	}
	close(start)
	wg.Wait()

	cHas, _ := relationsOf(t, h, c)
	dHas, _ := relationsOf(t, h, d)
	if len(cHas) == 1 && cHas[0] == d && len(dHas) == 1 && dHas[0] == c {
		t.Fatalf("the graph holds a containment cycle; codes %v", codes)
	}
	for _, code := range codes {
		if code != http.StatusNoContent && code != http.StatusBadRequest {
			t.Fatalf("codes = %v, want 204 and 400", codes)
		}
	}
}

// A longer cycle: A contains B contains C, then C contains A, fired against a
// concurrent add that would also close it.
func TestRelationConcurrentAddsCannotCloseALongerCycle(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newRelationsAPI(t, bs)
	a, b, c := "wrellonga12", "wrellongb12", "wrellongc12"
	for id, title := range map[string]string{a: "A", b: "B", c: "C"} {
		seedRelationWork(t, bs, id, title)
	}
	if code := relate(t, h, a, "hasPart", b); code != http.StatusNoContent {
		t.Fatalf("A hasPart B = %d", code)
	}
	if code := relate(t, h, b, "hasPart", c); code != http.StatusNoContent {
		t.Fatalf("B hasPart C = %d", code)
	}

	// C hasPart A closes A->B->C->A. Fire it twice at once.
	codes := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = relate(t, h, c, "hasPart", a)
		}()
	}
	close(start)
	wg.Wait()

	cHas, _ := relationsOf(t, h, c)
	for _, id := range cHas {
		if id == a {
			t.Fatalf("C hasPart A closed the cycle A->B->C->A; codes %v", codes)
		}
	}
	for _, code := range codes {
		if code != http.StatusBadRequest {
			t.Fatalf("codes = %v, want both 400", codes)
		}
	}
}

// relationConflictBlob makes the first conditional write to one grain lose its
// CAS, after landing a competing grain that closes the cycle. The competing
// writer is not a relation edit, so relationMu cannot see it -- this is the case
// the in-closure re-check exists for.
type relationConflictBlob struct {
	blob.Store
	conflictPath string // the grain whose first conditional write loses its CAS
	sidePath     string // the grain the competing writer changes, mid-flight
	sideData     []byte
	done         bool
}

func (c *relationConflictBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if !c.done && path == c.conflictPath && opts.IfMatch != "" {
		c.done = true
		// A writer that is not a relation edit moves the graph, then this
		// conditional write loses.
		if _, err := c.Store.Put(ctx, c.sidePath, c.sideData, blob.PutOptions{}); err != nil {
			return "", err
		}
		return "", blob.ErrPreconditionFailed
	}
	return c.Store.Put(ctx, path, data, opts)
}

// relationMu keeps other relation edits out, but a plain grain write
// -- PUT /v1/works/{id}, a batch patch -- can still move the graph under a
// relation add and lose it the CAS. The guard passed before the write, against a
// graph that no longer exists. The retry must re-check against the graph it is
// actually writing into.
func TestRelationRetryRechecksTheCycleAgainstTheFreshGraph(t *testing.T) {
	mem := blob.NewMem()
	a, b := "wrelcasa123", "wrelcasb123"
	seedRelationWork(t, mem, a, "A")
	seedRelationWork(t, mem, b, "B")

	// The competing grain gives B a hasPart edge to A, so A hasPart B would
	// close a cycle -- but only once it lands, which is after the guard ran.
	bGrain, _, err := mem.Get(t.Context(), bibframe.GrainPath(b))
	if err != nil {
		t.Fatal(err)
	}
	bContainsA, err := bibframe.SetWorkRelation(bGrain, b, bibframe.PredHasPart, a, true)
	if err != nil {
		t.Fatal(err)
	}
	bs := &relationConflictBlob{
		Store:        mem,
		conflictPath: bibframe.GrainPath(a),
		sidePath:     bibframe.GrainPath(b),
		sideData:     bContainsA,
	}
	h, _ := newRelationsAPI(t, bs)

	code := relate(t, h, a, "hasPart", b)
	if !bs.done {
		t.Fatal("the CAS conflict never fired; the retry path is not under test")
	}
	if code != http.StatusBadRequest {
		t.Fatalf("add = %d, want 400: by the time the retry ran, B already contained A", code)
	}
	aHas, _ := relationsOf(t, h, a)
	for _, id := range aHas {
		if id == b {
			t.Fatalf("the retry wrote A hasPart B, closing the cycle: %v", aHas)
		}
	}
}

// grainPutFailBlob refuses conditional writes to one grain path, leaving every
// other path working: the inverse write fails while the forward one lands.
type grainPutFailBlob struct {
	blob.Store
	failPath string
}

func (g *grainPutFailBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if g.failPath != "" && path == g.failPath {
		return "", errStorage
	}
	return g.Store.Put(ctx, path, data, opts)
}

// when the inverse write fails, the forward statement used to stay,
// and the 500 told the cataloger to "retry to converge". Compensate instead, so
// nothing is applied and the retry the message prescribes is actually possible.
func TestRelationFailedInverseRollsBackTheForwardLink(t *testing.T) {
	mem := blob.NewMem()
	a, b := "wrelcompa12", "wrelcompb12"
	seedRelationWork(t, mem, a, "A")
	seedRelationWork(t, mem, b, "B")
	bs := &grainPutFailBlob{Store: mem, failPath: bibframe.GrainPath(b)}
	h, queue := newRelationsAPI(t, bs)

	if code := relate(t, h, a, "hasPart", b); code != http.StatusInternalServerError {
		t.Fatalf("add with a failing inverse write = %d, want 500", code)
	}
	aHas, aPart := relationsOf(t, h, a)
	bHas, bPart := relationsOf(t, h, b)
	if len(aHas)+len(aPart)+len(bHas)+len(bPart) != 0 {
		t.Fatalf("the forward link survived a failed inverse: %v/%v and %v/%v", aHas, aPart, bHas, bPart)
	}
	// Nothing was applied, so nothing is audited.
	if got := coverAudit(t, queue); len(got) != 0 {
		t.Fatalf("audit = %v, want no entry for a relation that was not applied", got)
	}

	// The remedy the 500 prescribes now works.
	bs.failPath = ""
	if code := relate(t, h, a, "hasPart", b); code != http.StatusNoContent {
		t.Fatalf("retry after recovery = %d, want 204", code)
	}
	if aHas, _ = relationsOf(t, h, a); len(aHas) != 1 || aHas[0] != b {
		t.Fatalf("retry did not link: %v", aHas)
	}
	if got := coverAudit(t, queue); len(got) != 1 || got[0] != "WORK_RELATE" {
		t.Fatalf("audit = %v", got)
	}
}

// The same compensation on the remove path: a failed inverse remove restores
// the forward link, so a half-unlinked pair is never reported as a clean 500.
func TestRelationFailedInverseRemoveRestoresTheForwardLink(t *testing.T) {
	mem := blob.NewMem()
	a, b := "wreluncoa12", "wreluncob12"
	seedRelationWork(t, mem, a, "A")
	seedRelationWork(t, mem, b, "B")
	bs := &grainPutFailBlob{Store: mem}
	h, _ := newRelationsAPI(t, bs)

	if code := relate(t, h, a, "hasPart", b); code != http.StatusNoContent {
		t.Fatalf("setup add = %d", code)
	}
	bs.failPath = bibframe.GrainPath(b)
	if code := unrelate(t, h, a, "hasPart", b); code != http.StatusInternalServerError {
		t.Fatalf("remove with a failing inverse = %d, want 500", code)
	}
	aHas, _ := relationsOf(t, h, a)
	bs.failPath = ""
	bHas, bPart := relationsOf(t, h, b)
	if len(aHas) != 1 || aHas[0] != b || len(bPart) != 1 || bPart[0] != a || len(bHas) != 0 {
		t.Fatalf("the half-removed link was not restored: A hasPart %v; B %v/%v", aHas, bHas, bPart)
	}
	if code := unrelate(t, h, a, "hasPart", b); code != http.StatusNoContent {
		t.Fatalf("retry after recovery = %d", code)
	}
}

// strandFailBlob fails every write to the target's grain, and fails the source
// grain's second write -- the rollback. The forward link is left asserted with
// no inverse: the one state a person has to repair.
type strandFailBlob struct {
	blob.Store
	targetPath string
	sourcePath string
	sourcePuts int
}

func (s *strandFailBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if path == s.targetPath && opts.IfMatch != "" {
		return "", errStorage
	}
	if path == s.sourcePath && opts.IfMatch != "" {
		s.sourcePuts++
		if s.sourcePuts > 1 {
			return "", errStorage
		}
	}
	return s.Store.Put(ctx, path, data, opts)
}

// When the rollback fails too, one work asserts a link the other does not. That
// record changed, so it must be attributable -- and the error must name the real
// repair rather than prescribe a retry the cycle guard would refuse.
func TestRelationStrandedLinkIsAuditedAndNamesTheRepair(t *testing.T) {
	mem := blob.NewMem()
	a, b := "wrelstrna12", "wrelstrnb12"
	seedRelationWork(t, mem, a, "A")
	seedRelationWork(t, mem, b, "B")
	bs := &strandFailBlob{Store: mem, targetPath: bibframe.GrainPath(b), sourcePath: bibframe.GrainPath(a)}
	h, queue := newRelationsAPI(t, bs)

	rec := request(t, h, "POST", "/v1/works/"+a+"/relations", "lib-token", "",
		map[string]any{"kind": "hasPart", "target": b})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("add = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "delete the link from both records") {
		t.Fatalf("the error does not name the repair: %s", body)
	}
	if strings.Contains(body, "retry to converge") {
		t.Fatalf("the error still prescribes a retry the cycle guard refuses: %s", body)
	}
	// The forward link is stranded, and the audit log names the work that changed.
	aHas, _ := relationsOf(t, h, a)
	if len(aHas) != 1 || aHas[0] != b {
		t.Fatalf("the premise failed: the rollback succeeded, A hasPart %v", aHas)
	}
	entries := auditRows(t, queue)
	if len(entries) != 1 || entries[0].WorkID != a || entries[0].Action != "WORK_RELATE" {
		t.Fatalf("audit = %+v, want one WORK_RELATE naming %s", entries, a)
	}
	if !strings.Contains(entries[0].Note, "inverse missing") {
		t.Fatalf("audit note does not record the asymmetry: %q", entries[0].Note)
	}
}

// Control: recovery from a well-formed link still works from both sides.
func TestRelationDeleteUnlinksBothSides(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newRelationsAPI(t, bs)
	a, b := "wreldela123", "wreldelb123"
	seedRelationWork(t, bs, a, "A")
	seedRelationWork(t, bs, b, "B")

	if code := relate(t, h, a, "hasPart", b); code != http.StatusNoContent {
		t.Fatalf("add = %d", code)
	}
	if code := unrelate(t, h, a, "hasPart", b); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	aHas, _ := relationsOf(t, h, a)
	bHas, bPart := relationsOf(t, h, b)
	if len(aHas)+len(bHas)+len(bPart) != 0 {
		t.Fatalf("delete left edges: %v, %v, %v", aHas, bHas, bPart)
	}
}
