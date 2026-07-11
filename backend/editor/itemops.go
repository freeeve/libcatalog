package editor

import (
	"fmt"

	"github.com/freeeve/libcat/bibframe"
)

// Item ops let the op machinery reach holdings.
// Until now the item panel wrote holdings with bibframe.SetItems, outside the
// op list entirely: a batch relocation meant opening every record. An op with
// resource "items" edits one field on every bf:Item in the grain, so drafts,
// macros, and batch runs all reach it through the one audited path, with the
// same dry-run diff.
//
// An item field holds at most one value, so only "set" (assign, with an
// optional Where guard) and "clear" mean anything. "add" and "remove" are
// refused rather than reinterpreted: a cataloger who writes `add location` is
// thinking of a repeatable field and should hear that they are wrong.
func applyItemOp(grainNQ []byte, op Op, seen map[string]bool, patch *bibframe.Patch) error {
	edit := bibframe.ItemEdit{Field: op.Path, Where: op.Where}
	switch op.Action {
	case "set":
		if len(op.Values) != 1 {
			return fmt.Errorf("set needs exactly one value: an item field holds one")
		}
		if op.Values[0].IRI {
			return fmt.Errorf("item fields are plain text, not IRIs")
		}
		if op.Values[0].V == "" {
			return fmt.Errorf("set needs a value; use clear to empty the field")
		}
		edit.Value = op.Values[0].V
	case "clear":
		if len(op.Values) > 0 || op.Value != nil {
			return fmt.Errorf("clear takes no value")
		}
	case "add", "remove":
		return fmt.Errorf("an item field holds one value: use set or clear")
	default:
		return fmt.Errorf("unknown action %q", op.Action)
	}
	// Two ops on one item field would each be computed against the original
	// grain, so both would assert -- leaving the item holding two values for a
	// single-valued field. Refuse instead of writing a grain no reader can
	// interpret.
	if seen[op.Path] {
		return fmt.Errorf("item field %q is edited twice in one op list", op.Path)
	}
	seen[op.Path] = true

	itemPatch, _, err := bibframe.ItemEditPatch(grainNQ, edit)
	if err != nil {
		return err
	}
	patch.Add = append(patch.Add, itemPatch.Add...)
	patch.Remove = append(patch.Remove, itemPatch.Remove...)
	return nil
}
