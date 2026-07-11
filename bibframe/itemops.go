package bibframe

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"
)

// Batch-addressable item fields. SetItems replaces an
// Instance's holdings wholesale, which is right for the item panel and wrong
// for a selection: it re-mints every node and churns every quad. An ItemEdit
// is the surgical shape -- one field, on the items that actually change --
// so a batch run's diff shows the relocation and nothing else.
//
// Barcode is deliberately absent. A barcode names one physical copy, so
// assigning one across a selection would mint duplicates; clearing one across
// a selection would silently unlink the shelf from the catalog.
var itemFieldPreds = map[string]string{
	"callNumber": predShelfMark,
	"location":   predPhysicalLocation,
	"note":       PredItemNote,
}

// ErrNoSuchItemField refuses an edit naming a field items do not have, or one
// (barcode) that is not safe to assign across a selection.
var ErrNoSuchItemField = errors.New("no such item field")

// ItemFieldNames lists the batch-editable item fields, sorted, for the error
// messages and the UI's field picker.
func ItemFieldNames() []string {
	names := make([]string, 0, len(itemFieldPreds))
	for name := range itemFieldPreds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ItemEdit is one field change applied to every bf:Item in a grain.
//
// Value is the new value; the empty string clears the field. Where, when set,
// restricts the edit to items whose current value is exactly that string,
// which is what makes "move Stacks to Annex" leave the Reference copies
// alone. An item that does not assert the field at all reads as the empty
// string, so Where of "" means "only the ones missing it".
type ItemEdit struct {
	Field string
	Value string
	Where *string
}

// ItemEditPatch translates an ItemEdit into an editorial patch over grainNQ,
// returning the patch and the number of items it touches.
//
// Items already holding the target value are skipped rather than rewritten:
// an unchanged item has no business appearing in a batch run's diff, and a
// remove-then-add of an identical quad would report churn as work.
func ItemEditPatch(grainNQ []byte, e ItemEdit) (Patch, int, error) {
	pred, ok := itemFieldPreds[e.Field]
	if !ok {
		return Patch{}, 0, fmt.Errorf("bibframe: %w: %q (have %s)", ErrNoSuchItemField, e.Field, strings.Join(ItemFieldNames(), ", "))
	}
	if strings.ContainsAny(e.Value, "\n\r") {
		return Patch{}, 0, fmt.Errorf("bibframe: item %s cannot contain a line break", e.Field)
	}
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return Patch{}, 0, err
	}
	// Item nodes are found by their rdf:type, not by an Instance's item-IRI
	// prefix: a grain edited before the prefix scheme settled still types its
	// items, and an edit that silently skipped those would report success
	// while leaving the shelf wrong.
	items := map[string]bool{}
	for _, q := range ds.Quads {
		if q.S.IsIRI() && q.P.Value == rdfTypeIRI && q.O.IsIRI() && q.O.Value == classItem {
			items[q.S.Value] = true
		}
	}
	held := map[string]rdf.Quad{}
	for _, q := range ds.Quads {
		if items[q.S.Value] && q.P.Value == pred {
			held[q.S.Value] = q
		}
	}
	nodes := make([]string, 0, len(items))
	for node := range items {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	patch := Patch{}
	touched := 0
	for _, node := range nodes {
		cur, has := held[node]
		current := ""
		if has {
			current = cur.O.Value
		}
		if e.Where != nil && current != *e.Where {
			continue
		}
		if current == e.Value {
			continue
		}
		if has {
			patch.Remove = append(patch.Remove, rdf.Quad{S: cur.S, P: cur.P, O: cur.O})
		}
		if e.Value != "" {
			patch.Add = append(patch.Add, rdf.Quad{
				S: rdf.NewIRI(node),
				P: rdf.NewIRI(pred),
				O: rdf.NewLiteral(e.Value, "", ""),
			})
		}
		touched++
	}
	return patch, touched, nil
}
