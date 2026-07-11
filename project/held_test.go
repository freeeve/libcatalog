package project

import "testing"

// heldCatalog: w1 digital (reserve id), w2 digital but withdrawn from the
// feed, w3 physical (an item, no availability identifier).
const heldCatalog = `<#w1Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/title> _:t1 <feed:overdrive> .
_:t1 <http://id.loc.gov/ontologies/bibframe/mainTitle> "Digital" <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#i1Instance> <feed:overdrive> .
<#i1Instance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:id1 <feed:overdrive> .
_:id1 <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "res-1" <feed:overdrive> .
_:id1 <http://id.loc.gov/ontologies/bibframe/source> _:s1 <feed:overdrive> .
_:s1 <http://www.w3.org/2000/01/rdf-schema#label> "overdrive-reserve" <feed:overdrive> .
<#w2Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/title> _:t2 <feed:overdrive> .
_:t2 <http://id.loc.gov/ontologies/bibframe/mainTitle> "Withdrawn" <feed:overdrive> .
<#w2Work> <https://github.com/freeeve/libcat/ns#withdrawnFromFeed> "2026-07-03" <editorial:> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#i2Instance> <feed:overdrive> .
<#i2Instance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:id2 <feed:overdrive> .
_:id2 <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "res-2" <feed:overdrive> .
_:id2 <http://id.loc.gov/ontologies/bibframe/source> _:s2 <feed:overdrive> .
_:s2 <http://www.w3.org/2000/01/rdf-schema#label> "overdrive-reserve" <feed:overdrive> .
<#w3Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:copycat> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/title> _:t3 <feed:copycat> .
_:t3 <http://id.loc.gov/ontologies/bibframe/mainTitle> "Physical" <feed:copycat> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#i3Instance> <feed:copycat> .
<#i3Instance> <http://id.loc.gov/ontologies/bibframe/hasItem> <#i3Instance-item-1> <editorial:> .
<#i3Instance-item-1> <https://github.com/freeeve/libcat/ns#barcode> "30001" <editorial:> .
`

// TestHeld pins the holdings signal: an availability identifier
// counts as a digital holding unless the work is withdrawn; a physical item
// always counts.
func TestHeld(t *testing.T) {
	for _, provider := range []string{"overdrive", "copycat"} {
		cat, err := Project([]byte(heldCatalog), provider)
		if err != nil {
			t.Fatal(err)
		}
		held := map[string]bool{}
		for _, w := range cat.Works {
			held[w.ID] = w.Held
			if len(w.Instances) == 1 && w.Instances[0].Held != w.Held {
				t.Errorf("%s (%s): instance/work held mismatch", w.ID, provider)
			}
		}
		if provider != "overdrive" {
			continue
		}
		if !held["w1"] {
			t.Error("w1: availability identifier should count as held")
		}
		if held["w2"] {
			t.Error("w2: a withdrawn work's identifier should not count as held")
		}
	}
	// w3's items are editorial, so they hold regardless of provider graph.
	cat, err := Project([]byte(heldCatalog), "copycat")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cat.Works {
		if w.ID == "w3" && !w.Held {
			t.Error("w3: an item should count as held")
		}
	}
}
