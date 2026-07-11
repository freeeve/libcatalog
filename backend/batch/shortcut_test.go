package batch_test

import (
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/editor"
)

// stampOp is a minimal valid op list, so these tests fail only on the key.
func stampOp() []editor.Op {
	return []editor.Op{{
		Resource: "work", Path: "summary", Action: "set",
		Values: []editor.OpValue{{V: "x", Lang: "en"}},
	}}
}

// TestMacroShortcutValidation covers a macro may not take a chord
// the editor already binds, may not carry a shortcut that can never fire, and
// may not shadow another macro's key.
func TestMacroShortcutValidation(t *testing.T) {
	svc, _, _, _ := newService(t)
	ctx := t.Context()
	const owner = "lib@example.org"

	create := func(label, keys string) error {
		_, err := svc.CreateMacro(ctx, batch.Macro{
			OwnedMeta: batch.OwnedMeta{Label: label}, Keys: keys, Ops: stampOp(),
		}, owner)
		return err
	}

	// Every reserved chord refuses, and says what holds it.
	for key, held := range batch.ReservedShortcutKeys {
		err := create("Reserved "+key, key)
		if !errors.Is(err, batch.ErrValidation) {
			t.Errorf("macro keyed %q = %v, want a validation error", key, err)
			continue
		}
		if !strings.Contains(err.Error(), held) {
			t.Errorf("macro keyed %q said %q, which does not name %q", key, err, held)
		}
	}

	// A shortcut that can never fire refuses rather than storing inert.
	for _, keys := range []string{"zz", "ab", "  "} {
		if err := create("Multi "+keys, keys); !errors.Is(err, batch.ErrValidation) {
			t.Errorf("macro keyed %q = %v, want a validation error", keys, err)
		}
	}

	// A free key works, and no shortcut at all is fine.
	if err := create("Free", "7"); err != nil {
		t.Fatalf("free key refused: %v", err)
	}
	if err := create("Unbound", ""); err != nil {
		t.Fatalf("empty key refused: %v", err)
	}

	// A second macro on the same key refuses, naming the macro that holds it.
	err := create("Shadow", "7")
	if !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("duplicate key = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "Free") {
		t.Errorf("duplicate refusal %q does not name the holding macro", err)
	}

	// Updating a macro to a taken key refuses; keeping its own key does not.
	macros, err := svc.ListMacros(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	var free batch.Macro
	for _, m := range macros {
		if m.Label == "Free" {
			free = m
		}
	}
	if free.ID == "" {
		t.Fatal("the free macro is missing")
	}
	if _, err := svc.UpdateMacro(ctx, free.ID, batch.Macro{
		OwnedMeta: batch.OwnedMeta{Label: "Free", ID: free.ID}, Keys: "7", Ops: stampOp(),
	}, owner, false); err != nil {
		t.Fatalf("a macro keeping its own key was refused: %v", err)
	}
	if _, err := svc.UpdateMacro(ctx, free.ID, batch.Macro{
		OwnedMeta: batch.OwnedMeta{Label: "Free", ID: free.ID}, Keys: "2", Ops: stampOp(),
	}, owner, false); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("update onto the MARC-tab chord = %v, want a validation error", err)
	}
}

// TestReservedShortcutKeysMatchUI pins the Go table to the TypeScript one it
// mirrors. Two copies of a security-shaped rule drift silently; this makes
// the drift a build failure instead of a cataloger's broken keyboard.
func TestReservedShortcutKeysMatchUI(t *testing.T) {
	const uiPath = "../ui/src/lib/keyboard.ts"
	src, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const EDITOR_CHORDS[^{]*\{(.*?)\n\};`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("%s: EDITOR_CHORDS table not found; did it move?", uiPath)
	}
	extra := regexp.MustCompile(`(?s)export const RESERVED_SHORTCUT_KEYS[^{]*\{(.*?)\n\};`).FindSubmatch(src)
	if extra == nil {
		t.Fatalf("%s: RESERVED_SHORTCUT_KEYS table not found; did it move?", uiPath)
	}

	// Keys are quoted ("1", "?") or bare identifiers (p, m, g).
	entry := regexp.MustCompile(`(?m)^\s*(?:"([^"]+)"|([A-Za-z]))\s*:`)
	var ui []string
	for _, chunk := range [][]byte{block[1], extra[1]} {
		for _, m := range entry.FindAllSubmatch(chunk, -1) {
			key := string(m[1])
			if key == "" {
				key = string(m[2])
			}
			ui = append(ui, key)
		}
	}
	sort.Strings(ui)
	ui = slicesCompact(ui)

	var got []string
	for k := range batch.ReservedShortcutKeys {
		got = append(got, k)
	}
	sort.Strings(got)

	if strings.Join(ui, ",") != strings.Join(got, ",") {
		t.Errorf("reserved shortcut keys disagree:\n  %s: %v\n  batch.ReservedShortcutKeys: %v\nupdate both tables together", uiPath, ui, got)
	}
}

func slicesCompact(in []string) []string {
	out := in[:0]
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}
