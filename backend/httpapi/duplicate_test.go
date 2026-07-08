package httpapi

import (
	"fmt"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/workindex"
)

// identityGrain renders a minimal grain carrying the signals ScanGrain reads:
// a typed Work with primary author, main title, and language (the cluster
// key), and a typed Instance with one ISBN.
func identityGrain(workID, title, author, isbn string) []byte {
	return fmt.Appendf(nil, `<#%[1]sWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#%[1]sWork> <http://id.loc.gov/ontologies/bibframe/title> _:t <feed:overdrive> .
<#%[1]sWork> <http://id.loc.gov/ontologies/bibframe/language> <http://id.loc.gov/vocabulary/languages/eng> <feed:overdrive> .
<#%[1]sWork> <http://id.loc.gov/ontologies/bibframe/contribution> _:c <feed:overdrive> .
_:t <http://id.loc.gov/ontologies/bibframe/mainTitle> "%[2]s" <feed:overdrive> .
_:c <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bflc/PrimaryContribution> <feed:overdrive> .
_:c <http://id.loc.gov/ontologies/bibframe/agent> _:ag <feed:overdrive> .
_:ag <http://www.w3.org/2000/01/rdf-schema#label> "%[3]s" <feed:overdrive> .
<#%[1]siInstance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .
<#%[1]siInstance> <http://id.loc.gov/ontologies/bibframe/instanceOf> <#%[1]sWork> <feed:overdrive> .
<#%[1]siInstance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:a <feed:overdrive> .
_:a <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Isbn> <feed:overdrive> .
_:a <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "%[4]s" <feed:overdrive> .
`, workID, title, author, isbn)
}

func TestFindDuplicate(t *testing.T) {
	ctx := t.Context()
	bs := blob.NewMem()
	put := func(workID string, grain []byte) {
		t.Helper()
		if _, err := bs.Put(ctx, bibframe.GrainPath(workID), grain, blob.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	put("wother1", identityGrain("wother1", "The Left Hand of Darkness", "Le Guin, Ursula K.", "9780441478125"))
	put("wother2", identityGrain("wother2", "The Dispossessed", "Le Guin, Ursula K.", "9780061054884"))
	put("wself11", identityGrain("wself11", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742"))
	ix := workindex.New(bs, "data/works/")

	// The saved doc keeps its own identity: no warning.
	if dup := findDuplicate(ctx, ix, "wself11", identityGrain("wself11", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742")); dup != nil {
		t.Fatalf("self-match reported: %+v", dup)
	}
	// An edit that lands another work's ISBN warns via the identifier.
	dup := findDuplicate(ctx, ix, "wself11", identityGrain("wself11", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780441478125"))
	if dup == nil || dup.WorkID != "wother1" || dup.Via != "identifier" {
		t.Fatalf("isbn collision = %+v", dup)
	}
	// A title/author collision warns via the clustering key.
	dup = findDuplicate(ctx, ix, "wself11", identityGrain("wself11", "The Dispossessed", "Le Guin, Ursula K.", "9780547773742"))
	if dup == nil || dup.WorkID != "wother2" || dup.Via != "title-author" {
		t.Fatalf("cluster collision = %+v", dup)
	}
	// The identifier signal outranks title/author when both collide.
	dup = findDuplicate(ctx, ix, "wself11", identityGrain("wself11", "The Dispossessed", "Le Guin, Ursula K.", "9780441478125"))
	if dup == nil || dup.Via != "identifier" {
		t.Fatalf("precedence = %+v", dup)
	}
	// A doc with distinct identity in a corpus of others: no warning.
	if dup := findDuplicate(ctx, ix, "wself11", identityGrain("wself11", "Always Coming Home", "Le Guin, Ursula K.", "9780520227354")); dup != nil {
		t.Fatalf("false positive: %+v", dup)
	}
}
