package publish

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/freeeve/libcat/backend/store"
)

// Lease is the advisory ingest lease: feed re-ingest holds it (identity
// resolution is single-flight), and the publisher defers -- never drops --
// approved changes while it is held. Expiry is carried in the record body,
// not store TTL, because DynamoDB expires lazily.
type Lease struct {
	db  store.Store
	key store.Key
	ttl time.Duration
	now func() time.Time
}

// leaseState is the stored lease body.
type leaseState struct {
	Holder  string    `json:"holder"`
	Expires time.Time `json:"expires"`
}

// NewLease wires the lease with the given hold duration.
func NewLease(db store.Store, name string, ttl time.Duration) *Lease {
	return &Lease{
		db:  db,
		key: store.Key{PK: "LEASE#" + name, SK: "LOCK"},
		ttl: ttl,
		now: time.Now,
	}
}

// SetClock overrides the clock (tests).
func (l *Lease) SetClock(now func() time.Time) { l.now = now }

// Acquire attempts to take the lease for holder. It returns false when
// another holder has it un-expired. Re-acquiring one's own live lease
// extends it.
func (l *Lease) Acquire(ctx context.Context, holder string) (bool, error) {
	now := l.now().UTC()
	state := leaseState{Holder: holder, Expires: now.Add(l.ttl)}
	data, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	rec, err := l.db.Get(ctx, l.key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		_, err := l.db.Put(ctx, store.Record{Key: l.key, Data: data}, store.CondIfAbsent)
		if errors.Is(err, store.ErrConditionFailed) {
			return false, nil // lost the creation race
		}
		return err == nil, err
	case err != nil:
		return false, err
	}
	var cur leaseState
	if err := json.Unmarshal(rec.Data, &cur); err != nil {
		return false, err
	}
	if cur.Holder != holder && cur.Expires.After(now) {
		return false, nil
	}
	rec.Data = data
	if _, err := l.db.Put(ctx, rec, store.CondIfVersion); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return false, nil // raced; caller retries later
		}
		return false, err
	}
	return true, nil
}

// Heartbeat extends the holder's live lease.
func (l *Lease) Heartbeat(ctx context.Context, holder string) error {
	ok, err := l.Acquire(ctx, holder)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("publish: lease lost")
	}
	return nil
}

// Release drops the lease if holder still owns it.
func (l *Lease) Release(ctx context.Context, holder string) error {
	rec, err := l.db.Get(ctx, l.key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var cur leaseState
	if err := json.Unmarshal(rec.Data, &cur); err != nil {
		return err
	}
	if cur.Holder != holder {
		return nil
	}
	err = l.db.Delete(ctx, rec, store.CondIfVersion)
	if errors.Is(err, store.ErrConditionFailed) || errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// Held reports the current live holder, if any.
func (l *Lease) Held(ctx context.Context) (string, bool, error) {
	rec, err := l.db.Get(ctx, l.key)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var cur leaseState
	if err := json.Unmarshal(rec.Data, &cur); err != nil {
		return "", false, err
	}
	if !cur.Expires.After(l.now().UTC()) {
		return "", false, nil
	}
	return cur.Holder, true, nil
}
