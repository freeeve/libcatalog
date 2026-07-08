package copycat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/freeeve/libcat/backend/store"
)

// Profile is a saved staging configuration (tasks/068): which targets a
// search fans out to and the overlay policy a staged batch starts with --
// recurring imports stop re-entering the same choices per batch.
type Profile struct {
	Name    string   `json:"name"`
	Targets []string `json:"targets,omitempty"`
	Policy  string   `json:"policy,omitempty"`
}

func profileKey(name string) store.Key { return store.Key{PK: "COPYCAT", SK: "P#" + name} }

func validPolicy(policy string) bool {
	return slices.Contains([]string{"", PolicyReplaceFeed, PolicyFillHoles, PolicyNever}, policy)
}

// PutProfile creates or replaces a staging profile.
func (s *Service) PutProfile(ctx context.Context, p Profile) error {
	if p.Name == "" {
		return fmt.Errorf("%w: a profile needs a name", ErrValidation)
	}
	if !validPolicy(p.Policy) {
		return fmt.Errorf("%w: unknown policy %q", ErrValidation, p.Policy)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.DB.Put(ctx, store.Record{Key: profileKey(p.Name), Data: data}, store.CondNone)
	return err
}

// DeleteProfile removes a staging profile.
func (s *Service) DeleteProfile(ctx context.Context, name string) error {
	err := s.DB.Delete(ctx, store.Record{Key: profileKey(name)}, store.CondNone)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// Profiles lists the saved staging profiles, sorted by name.
func (s *Service) Profiles(ctx context.Context) ([]Profile, error) {
	out := []Profile{}
	for rec, err := range s.DB.Query(ctx, "COPYCAT", "P#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var p Profile
		if json.Unmarshal(rec.Data, &p) == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
