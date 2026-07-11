package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/freeeve/libcat/backend/store"
)

// FreeTextMode governs whether patrons may propose folksonomy (free-text) tags,
// and which ones.
type FreeTextMode string

const (
	// FreeTextOff refuses every folksonomy tag from patrons.
	FreeTextOff FreeTextMode = "off"
	// FreeTextExisting accepts only folk tags already in use, never a novel one.
	FreeTextExisting FreeTextMode = "existing"
	// FreeTextAny accepts both novel and existing folk tags.
	FreeTextAny FreeTextMode = "any"
)

// Policy is the deployment's stored, admin-editable patron-suggestion policy
// . It is opt-in: absent, or with Enabled false, the public
// suggestion intake accepts nothing -- so a library that wants a review queue
// without anonymous suggestion simply leaves it off, decoupling the two
// surfaces that used to be one. When enabled, Schemes allowlists the controlled
// vocabularies patrons may propose from (empty = every loaded scheme), and
// FreeText governs folksonomy tags. The policy binds patrons only: a cataloger
// still adds any term through the review path (ManualTerm), regardless of what
// patrons are permitted -- the cataloger is the authority, the policy is the
// public intake gate.
type Policy struct {
	Enabled  bool         `json:"enabled"`
	Schemes  []string     `json:"schemes,omitempty"`
	FreeText FreeTextMode `json:"freeText"`
}

// DefaultPolicy is the policy of a store that has never had one set: patron
// suggestions off. Opt-in is the whole point.
func DefaultPolicy() Policy {
	return Policy{Enabled: false, FreeText: FreeTextOff}
}

func policyKey() store.Key {
	return store.Key{PK: "CONFIG#SUGGEST", SK: "POLICY"}
}

// allowsScheme reports whether an enabled policy permits a controlled scheme:
// an empty allowlist admits every loaded scheme, a non-empty one only its
// members.
func (p Policy) allowsScheme(scheme string) bool {
	return len(p.Schemes) == 0 || slices.Contains(p.Schemes, scheme)
}

// GetPolicy loads the stored policy, or DefaultPolicy when none is set.
func (s *Service) GetPolicy(ctx context.Context) (Policy, error) {
	rec, err := s.db.Get(ctx, policyKey())
	if errors.Is(err, store.ErrNotFound) {
		return DefaultPolicy(), nil
	}
	if err != nil {
		return Policy{}, err
	}
	var p Policy
	if err := json.Unmarshal(rec.Data, &p); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// PutPolicy validates and stores the policy, returning the normalized value.
// An unset FreeText defaults to off (the safe end); empty scheme entries are
// dropped so the allowlist means what it says.
func (s *Service) PutPolicy(ctx context.Context, p Policy) (Policy, error) {
	switch p.FreeText {
	case "":
		p.FreeText = FreeTextOff
	case FreeTextOff, FreeTextExisting, FreeTextAny:
	default:
		return Policy{}, fmt.Errorf("%w: freeText must be off, existing, or any", ErrBadPolicy)
	}
	schemes := make([]string, 0, len(p.Schemes))
	for _, sc := range p.Schemes {
		if sc != "" && !slices.Contains(schemes, sc) {
			schemes = append(schemes, sc)
		}
	}
	if len(schemes) == 0 {
		schemes = nil
	}
	p.Schemes = schemes
	data, err := json.Marshal(p)
	if err != nil {
		return Policy{}, err
	}
	if _, err := s.db.Put(ctx, store.Record{Key: policyKey(), Data: data}, store.CondNone); err != nil {
		return Policy{}, err
	}
	return p, nil
}
