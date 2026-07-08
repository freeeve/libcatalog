package batch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/freeeve/libcat/backend/store"
)

// OwnedMeta is the shared identity/sharing surface of the owned-or-shared
// record shapes (macros, item templates): one record per item, living in the
// owner's partition or the library-shared one, owner-gated for writes.
type OwnedMeta struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Shared    bool      `json:"shared"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ownedKind describes one owned/shared record family for the generic CRUD
// engine (tasks/116): its partition/sort-key prefixes, its validation, and
// access to the embedded meta (generics cannot reach promoted fields).
type ownedKind[T any] struct {
	pk, sk   string
	validate func(T) error
	meta     func(*T) *OwnedMeta
}

func (k ownedKind[T]) key(scope, id string) store.Key {
	return store.Key{PK: k.pk + scope, SK: k.sk + id}
}

func (k ownedKind[T]) scope(m OwnedMeta) string {
	if m.Shared {
		return sharedPartition
	}
	return m.Owner
}

// createOwned validates and stores a new item for owner (in the shared
// partition when its meta says so). The id is minted server-side.
func createOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], item T, owner string) (T, error) {
	var zero T
	if err := k.validate(item); err != nil {
		return zero, err
	}
	m := k.meta(&item)
	m.ID = mintID()
	m.Owner = owner
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now
	if err := putOwned(ctx, db, k, item, store.CondIfAbsent); err != nil {
		return zero, err
	}
	return item, nil
}

// updateOwned replaces an item's definition. Only the owner may update;
// flipping Shared moves the record between partitions.
func updateOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], id string, item T, owner string) (T, error) {
	var zero T
	if err := k.validate(item); err != nil {
		return zero, err
	}
	current, err := getOwned(ctx, db, k, owner, id)
	if err != nil {
		return zero, err
	}
	cm := *k.meta(&current)
	if cm.Owner != owner {
		return zero, ErrForbidden
	}
	m := k.meta(&item)
	m.ID, m.Owner, m.CreatedAt = cm.ID, cm.Owner, cm.CreatedAt
	m.UpdatedAt = time.Now().UTC()
	// Write the new partition before deleting the old one: a fault between
	// the two leaves a harmless duplicate instead of losing the record
	// (tasks/115).
	if err := putOwned(ctx, db, k, item, store.CondNone); err != nil {
		return zero, err
	}
	if cm.Shared != m.Shared {
		if err := db.Delete(ctx, store.Record{Key: k.key(k.scope(cm), cm.ID)}, store.CondNone); err != nil && !errors.Is(err, store.ErrNotFound) {
			return zero, err
		}
	}
	return item, nil
}

// deleteOwned removes an owned item (shared or personal).
func deleteOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], owner, id string) error {
	item, err := getOwned(ctx, db, k, owner, id)
	if err != nil {
		return err
	}
	m := *k.meta(&item)
	if m.Owner != owner {
		return ErrForbidden
	}
	err = db.Delete(ctx, store.Record{Key: k.key(k.scope(m), m.ID)}, store.CondNone)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// getOwned resolves an item the caller can use: their own, or a shared one.
func getOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], owner, id string) (T, error) {
	var zero T
	for _, scope := range []string{owner, sharedPartition} {
		rec, err := db.Get(ctx, k.key(scope, id))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return zero, err
		}
		var item T
		if err := json.Unmarshal(rec.Data, &item); err != nil {
			return zero, err
		}
		return item, nil
	}
	return zero, ErrNotFound
}

// listOwned returns the caller's items plus every shared one, sorted by
// label then id. Always non-nil, so handlers render [] rather than null.
func listOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], owner string) ([]T, error) {
	out := []T{}
	for _, scope := range []string{owner, sharedPartition} {
		for rec, err := range db.Query(ctx, k.pk+scope, k.sk, store.QueryOpt{}) {
			if err != nil {
				return nil, err
			}
			var item T
			if json.Unmarshal(rec.Data, &item) == nil {
				out = append(out, item)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := *k.meta(&out[i]), *k.meta(&out[j])
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.ID < b.ID
	})
	return out, nil
}

func putOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], item T, cond store.Cond) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	m := *k.meta(&item)
	_, err = db.Put(ctx, store.Record{Key: k.key(k.scope(m), m.ID), Data: data}, cond)
	return err
}
