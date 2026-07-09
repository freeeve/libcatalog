package bibframe

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"
)

// The minimal bf:Item holdings model (tasks/051): call number, shelving
// location, barcode, and a note -- never circulation state, which is the
// ILS's (ARCHITECTURE §5 keeps live state out of the graph). Items are
// editorial statements on skolem nodes under their Instance, so they
// survive re-ingest and edit like everything else.
const (
	predHasItem          = "http://id.loc.gov/ontologies/bibframe/hasItem"
	classItem            = "http://id.loc.gov/ontologies/bibframe/Item"
	predShelfMark        = "http://id.loc.gov/ontologies/bibframe/shelfMark"
	predPhysicalLocation = "http://id.loc.gov/ontologies/bibframe/physicalLocation"
	rdfTypeIRI           = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	// PredBarcode and PredItemNote are lcat: extensions -- BIBFRAME models
	// them as identifier/note subgraphs, heavier than a holdings row needs.
	PredBarcode  = LcatNS + "barcode"
	PredItemNote = LcatNS + "itemNote"
)

// Item is one holding of an Instance.
type Item struct {
	ID         string `json:"id"`
	CallNumber string `json:"callNumber,omitempty"`
	Location   string `json:"location,omitempty"`
	Barcode    string `json:"barcode,omitempty"`
	Note       string `json:"note,omitempty"`
}

// itemIRI names an Instance's nth item node.
func itemIRI(instanceID string, n int) string {
	return InstanceIRI(instanceID) + "-item-" + fmt.Sprint(n+1)
}

// itemPrefix is the namespace an Instance's item nodes live under.
func itemPrefix(instanceID string) string {
	return InstanceIRI(instanceID) + "-item-"
}

// ItemsOf reads an Instance's holdings from a grain, sorted by node id.
func ItemsOf(grainNQ []byte, instanceID string) ([]Item, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	prefix := itemPrefix(instanceID)
	byNode := map[string]*Item{}
	for _, q := range ds.Quads {
		if !q.S.IsIRI() || !strings.HasPrefix(q.S.Value, prefix) {
			continue
		}
		item := byNode[q.S.Value]
		if item == nil {
			item = &Item{ID: q.S.Value}
			byNode[q.S.Value] = item
		}
		switch q.P.Value {
		case predShelfMark:
			item.CallNumber = q.O.Value
		case predPhysicalLocation:
			item.Location = q.O.Value
		case PredBarcode:
			item.Barcode = q.O.Value
		case PredItemNote:
			item.Note = q.O.Value
		}
	}
	out := make([]Item, 0, len(byNode))
	for _, item := range byNode {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ErrNoSuchInstance refuses an item write against an instance id the grain
// does not describe (tasks/211); handlers map it to a 400.
var ErrNoSuchInstance = errors.New("no such instance on this work")

// SetItems replaces an Instance's holdings wholesale: every editorial item
// statement under the Instance's item namespace is dropped and the given
// items re-asserted on freshly numbered skolem nodes. Returns the
// re-canonicalized grain.
func SetItems(grainNQ []byte, instanceID string, items []Item) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	// The Instance must actually exist in this grain: SetItems used to mint
	// the IRI from whatever id it was handed, grafting holdings onto a
	// phantom node that no reader enumerates -- consuming real barcodes and
	// asserting bf:hasItem on another work's Instance when the id was
	// copied from the wrong record (tasks/211).
	inst := rdf.NewIRI(InstanceIRI(instanceID))
	described := false
	for i := range ds.Quads {
		if ds.Quads[i].S == inst {
			described = true
			break
		}
	}
	if !described {
		return nil, fmt.Errorf("bibframe: %w: no instance %s in this grain", ErrNoSuchInstance, instanceID)
	}
	ed := EditorialGraph()
	prefix := itemPrefix(instanceID)
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		itemQuad := (q.S.IsIRI() && strings.HasPrefix(q.S.Value, prefix)) ||
			(q.O.IsIRI() && strings.HasPrefix(q.O.Value, prefix))
		if q.G == ed && itemQuad {
			continue
		}
		keep = append(keep, q)
	}
	ds.Quads = keep
	stripped, err := ds.Canonical()
	if err != nil {
		return nil, err
	}
	patch := Patch{}
	for n, item := range items {
		node := rdf.NewIRI(itemIRI(instanceID, n))
		patch.Add = append(patch.Add,
			rdf.Quad{S: inst, P: rdf.NewIRI(predHasItem), O: node},
			rdf.Quad{S: node, P: rdf.NewIRI(rdfTypeIRI), O: rdf.NewIRI(classItem)},
		)
		add := func(pred, v string) {
			if v != "" {
				patch.Add = append(patch.Add, rdf.Quad{S: node, P: rdf.NewIRI(pred), O: rdf.NewLiteral(v, "", "")})
			}
		}
		add(predShelfMark, item.CallNumber)
		add(predPhysicalLocation, item.Location)
		add(PredBarcode, item.Barcode)
		add(PredItemNote, item.Note)
	}
	if len(patch.Add) == 0 {
		return stripped, nil
	}
	return ApplyEditorialPatch(stripped, patch)
}
