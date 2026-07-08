package batch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"time"

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
// partition when m.Shared). The id is minted server-side.
func (s *Service) CreateMacro(ctx context.Context, m Macro, owner string) (Macro, error) {
	return createOwned(ctx, s.DB, macroKind, m, owner)
}

// UpdateMacro replaces a macro's definition. Only the owner may update, and
// flipping Shared moves the record between partitions.
func (s *Service) UpdateMacro(ctx context.Context, id string, m Macro, owner string) (Macro, error) {
	return updateOwned(ctx, s.DB, macroKind, id, m, owner)
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
func ApplyParams(m Macro, values map[string]string) ([]editor.Op, error) {
	lookup := map[string]string{}
	for _, p := range m.Params {
		if p.Default != "" {
			lookup[p.Name] = p.Default
		}
	}
	maps.Copy(lookup, values)
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

func validateMacro(m Macro) error {
	if m.Label == "" {
		return fmt.Errorf("%w: macro needs a label", ErrValidation)
	}
	if len(m.Ops) == 0 || len(m.Ops) > maxOps {
		return fmt.Errorf("%w: macro needs 1-%d ops", ErrValidation, maxOps)
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

// CreateQuery stores a named search for owner.
func (s *Service) CreateQuery(ctx context.Context, label, query, owner string) (SavedQuery, error) {
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

// ListQueries returns the owner's saved queries in creation order.
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
