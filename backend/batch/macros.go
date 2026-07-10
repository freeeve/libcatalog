package batch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/store"
)

// Param declares one macro parameter, referenced in op values as ${name}.
type Param struct {
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Default string `json:"default,omitempty"`
}

// Macro is a replayable op list (tasks/047): recorded in the editor, replayed
// against another record, or -- when shared -- run over a batch selection,
// which is the MARC-modification-template shape. Keys optionally names a
// single-character editor shortcut.
type Macro struct {
	OwnedMeta
	Keys   string      `json:"keys,omitempty"`
	Ops    []editor.Op `json:"ops"`
	Params []Param     `json:"params,omitempty"`
}

// macroKind wires Macro into the generic owned/shared CRUD engine.
var macroKind = ownedKind[Macro]{
	pk: "MACRO#", sk: "M#",
	validate: validateMacro,
	meta:     func(m *Macro) *OwnedMeta { return &m.OwnedMeta },
}

// SavedQuery is a named works search, the reusable half of a Selection.
type SavedQuery struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Query     string    `json:"query"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"createdAt"`
}

// ErrNotFound reports a missing macro or saved query.
var ErrNotFound = errors.New("batch: not found")

// ErrForbidden reports an edit to somebody else's macro.
var ErrForbidden = errors.New("batch: not the owner")

// sharedPartition holds library-shared macros; personal macros live under
// the owner's partition. One record per macro either way.
const sharedPartition = "shared"

func queryKey(owner, id string) store.Key {
	return store.Key{PK: "SQUERY#" + owner, SK: "Q#" + id}
}

func mintID() string {
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	return hex.EncodeToString(suffix)
}

// paramRef matches ${name} placeholders in op values.
var paramRef = regexp.MustCompile(`\$\{([A-Za-z0-9_-]+)\}`)

// CreateMacro validates and stores a macro for owner (in the shared
// partition when m.Shared). The id is minted server-side. A shortcut key
// already held by another macro visible to this owner refuses (tasks/237).
func (s *Service) CreateMacro(ctx context.Context, m Macro, owner string) (Macro, error) {
	if err := s.shortcutFree(ctx, m.Keys, "", owner); err != nil {
		return Macro{}, err
	}
	return createOwned(ctx, s.DB, macroKind, m, owner)
}

// UpdateMacro replaces a macro's definition. Only the owner may update, and
// flipping Shared moves the record between partitions.
func (s *Service) UpdateMacro(ctx context.Context, id string, m Macro, owner string) (Macro, error) {
	if err := s.shortcutFree(ctx, m.Keys, id, owner); err != nil {
		return Macro{}, err
	}
	return updateOwned(ctx, s.DB, macroKind, id, m, owner)
}

// shortcutFree refuses a shortcut another macro in the owner's visible set
// (their own plus shared) already holds. The isolated checks run first so a
// bad key is reported as bad rather than as taken.
func (s *Service) shortcutFree(ctx context.Context, keys, selfID, owner string) error {
	if keys == "" {
		return nil
	}
	if err := validateShortcutKey(keys); err != nil {
		return err
	}
	existing, err := s.ListMacros(ctx, owner)
	if err != nil {
		return err
	}
	if label := shortcutTaken(existing, keys, selfID); label != "" {
		return fmt.Errorf("%w: shortcut key %q is already used by the macro %q", ErrValidation, keys, label)
	}
	return nil
}

// DeleteMacro removes an owned macro (shared or personal).
func (s *Service) DeleteMacro(ctx context.Context, owner, id string) error {
	return deleteOwned(ctx, s.DB, macroKind, owner, id)
}

// GetMacro resolves a macro the caller can run: their own, or a shared one.
func (s *Service) GetMacro(ctx context.Context, owner, id string) (Macro, error) {
	return getOwned(ctx, s.DB, macroKind, owner, id)
}

// ListMacros returns the caller's macros plus every shared macro, sorted by
// label.
func (s *Service) ListMacros(ctx context.Context, owner string) ([]Macro, error) {
	return listOwned(ctx, s.DB, macroKind, owner)
}

// ApplyParams substitutes ${name} references in the macro's op values from
// the caller's values (falling back to declared defaults) and returns the
// concrete op list. An unresolved reference fails closed -- a template never
// silently writes its placeholder text into a record.
//
// A blank caller value means "use the default", exactly like an omitted one
// (tasks/231): the parameter field advertises the default as its
// placeholder and the client's own lookup skips blanks, so a cleared field
// must not override the default here either -- a macro means the same thing
// replayed in the editor or run over a selection.
func ApplyParams(m Macro, values map[string]string) ([]editor.Op, error) {
	lookup := map[string]string{}
	for _, p := range m.Params {
		if p.Default != "" {
			lookup[p.Name] = p.Default
		}
	}
	for name, v := range values {
		if v != "" {
			lookup[name] = v
		}
	}
	subst := func(raw string) (string, error) {
		var missing error
		out := paramRef.ReplaceAllStringFunc(raw, func(ref string) string {
			name := paramRef.FindStringSubmatch(ref)[1]
			v, ok := lookup[name]
			if !ok {
				missing = fmt.Errorf("%w: parameter %q has no value", ErrValidation, name)
				return ref
			}
			return v
		})
		return out, missing
	}
	ops := make([]editor.Op, len(m.Ops))
	for i, op := range m.Ops {
		out := op
		if op.Value != nil {
			v := *op.Value
			s, err := subst(v.V)
			if err != nil {
				return nil, err
			}
			v.V = s
			out.Value = &v
		}
		if op.Values != nil {
			vs := make([]editor.OpValue, len(op.Values))
			for j, v := range op.Values {
				s, err := subst(v.V)
				if err != nil {
					return nil, err
				}
				v.V = s
				vs[j] = v
			}
			out.Values = vs
		}
		ops[i] = out
	}
	return ops, nil
}

// ReservedShortcutKeys are the single-character chords the editor already
// claims, mapped to the action that holds each (tasks/237). A macro keyed to
// one of them used to win by registering later, silently disabling the chord
// -- and the "?" overlay, which renders from the same registry, stopped
// listing it. Rejecting the macro at the source is the only place the
// cataloger can be told which action they would have broken.
//
// This table is mirrored in backend/ui/src/lib/keyboard.ts (EDITOR_CHORDS),
// and TestReservedShortcutKeysMatchUI / keyboard.test.ts pin them together.
// "mod+s" is absent because a shortcut key is one character and can never
// collide with it.
var ReservedShortcutKeys = map[string]string{
	"1": "the Native tab",
	"2": "the MARC tab",
	"3": "the History tab",
	"p": "preview staged changes",
	"m": "the live MARC preview pane",
	"?": "the help overlay",
	"g": "the go-to-screen prefix",
}

// validateShortcutKey checks a macro's shortcut in isolation: absent, or one
// character that the editor does not already claim. Uniqueness across macros
// needs the caller's macro list and is checked in CreateMacro/UpdateMacro.
func validateShortcutKey(keys string) error {
	if keys == "" {
		return nil
	}
	if utf8.RuneCountInString(keys) != 1 {
		return fmt.Errorf("%w: shortcut key %q must be a single character", ErrValidation, keys)
	}
	if held, ok := ReservedShortcutKeys[keys]; ok {
		return fmt.Errorf("%w: shortcut key %q is reserved for %s", ErrValidation, keys, held)
	}
	return nil
}

// shortcutTaken reports the label of another macro already bound to keys,
// ignoring the macro being updated. Two macros on one key means one of them
// can never fire, and which one is an accident of registration order.
func shortcutTaken(macros []Macro, keys, selfID string) string {
	if keys == "" {
		return ""
	}
	for _, other := range macros {
		if other.ID != selfID && other.Keys == keys {
			return other.Label
		}
	}
	return ""
}

func validateMacro(m Macro) error {
	if m.Label == "" {
		return fmt.Errorf("%w: macro needs a label", ErrValidation)
	}
	if len(m.Ops) == 0 || len(m.Ops) > maxOps {
		return fmt.Errorf("%w: macro needs 1-%d ops", ErrValidation, maxOps)
	}
	if err := validateShortcutKey(m.Keys); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range m.Params {
		if p.Name == "" || !paramRef.MatchString("${"+p.Name+"}") {
			return fmt.Errorf("%w: bad parameter name %q", ErrValidation, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("%w: duplicate parameter %q", ErrValidation, p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// CreateQuery stores a named search for owner. Label and query validate
// after normalization: a whitespace-only query would persist a selection
// that forever resolves to the entire catalog (tasks/205).
func (s *Service) CreateQuery(ctx context.Context, label, query, owner string) (SavedQuery, error) {
	label, query = strings.TrimSpace(label), normQuery(query)
	if label == "" || query == "" {
		return SavedQuery{}, fmt.Errorf("%w: a saved query needs a label and a query", ErrValidation)
	}
	sq := SavedQuery{ID: mintID(), Label: label, Query: query, Owner: owner, CreatedAt: time.Now().UTC()}
	data, err := json.Marshal(sq)
	if err != nil {
		return SavedQuery{}, err
	}
	if _, err := s.DB.Put(ctx, store.Record{Key: queryKey(owner, sq.ID), Data: data}, store.CondIfAbsent); err != nil {
		return SavedQuery{}, err
	}
	return sq, nil
}

// GetQuery reads one of the owner's saved queries.
func (s *Service) GetQuery(ctx context.Context, owner, id string) (SavedQuery, error) {
	rec, err := s.DB.Get(ctx, queryKey(owner, id))
	if errors.Is(err, store.ErrNotFound) {
		return SavedQuery{}, ErrNotFound
	}
	if err != nil {
		return SavedQuery{}, err
	}
	var sq SavedQuery
	err = json.Unmarshal(rec.Data, &sq)
	return sq, err
}

// ListQueries returns the owner's saved queries sorted by label then id, matching
// the macro and item-template lists (listOwned) the same dropdowns sit beside. The
// store yields them in sort-key order, and the key embeds a crypto/rand id, so without
// this they came back in an order the librarian never sees -- the one just saved landing
// wherever its random id sorted, not last (tasks/294). Creation order was the older
// contract; CreatedAt still carries it for a caller that wants the newest last.
func (s *Service) ListQueries(ctx context.Context, owner string) ([]SavedQuery, error) {
	out := []SavedQuery{}
	for rec, err := range s.DB.Query(ctx, "SQUERY#"+owner, "Q#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var sq SavedQuery
		if json.Unmarshal(rec.Data, &sq) == nil {
			out = append(out, sq)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// DeleteQuery removes one of the owner's saved queries.
func (s *Service) DeleteQuery(ctx context.Context, owner, id string) error {
	err := s.DB.Delete(ctx, store.Record{Key: queryKey(owner, id)}, store.CondNone)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
