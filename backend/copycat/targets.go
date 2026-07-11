package copycat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/freeeve/libcat/backend/store"
)

// Target is one external search source.
type Target struct {
	Name string `json:"name"`
	// URL: an SRU base URL, or a Z39.50 "host:port/database" target.
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	// SRU dialect knobs, all optional (Z39.50 targets ignore them).
	// Version is the SRU protocol version ("" = the client default, 1.2);
	// DNB, for one, only answers 1.1.
	Version string `json:"version,omitempty"`
	// Schema is the recordSchema requested ("" = marcxml); servers name
	// their MARC21 XML schema differently (DNB: "MARC21-xml").
	Schema string `json:"schema,omitempty"`
	// Indexes overrides the CQL index an access point maps to, for servers
	// off the Dublin Core / Bath mapping (K10plus: {"isbn": "pica.isb"}).
	Indexes map[string]string `json:"indexes,omitempty"`
}

func targetKey(name string) store.Key { return store.Key{PK: "COPYCAT", SK: "T#" + name} }
func seededKey() store.Key            { return store.Key{PK: "COPYCAT", SK: "SEEDED"} }

// DefaultTargets are the search sources seeded on a store that has never had
// targets: open, anonymous SRU endpoints serving MARC21, each verified live
// against the exact queries the fielded search emits.
// LOC speaks the Bath-profile identifier indexes as-is; DNB only answers SRU
// 1.1 and names its schema MARC21-xml, with dnb.num covering both standard
// numbers; K10plus wants its PICA identifier indexes.
var DefaultTargets = []Target{
	{Name: "dnb-sru", URL: "https://services.dnb.de/sru/dnb", Protocol: ProtocolSRU,
		Version: "1.1", Schema: "MARC21-xml",
		Indexes: map[string]string{"isbn": "dnb.num", "issn": "dnb.num"}},
	{Name: "k10plus-sru", URL: "https://sru.k10plus.de/opac-de-627", Protocol: ProtocolSRU,
		Indexes: map[string]string{"isbn": "pica.isb", "issn": "pica.iss"}},
	{Name: "loc-sru", URL: "http://lx2.loc.gov:210/LCDB", Protocol: ProtocolSRU},
}

// SuggestedTargets are the open, no-credential sources the copycat UI offers as
// one-click presets. It is the single source for the preset row: the UI fetches
// it (GET /v1/copycat/targets/suggested) rather than maintaining its own copy,
// which is how the k10plus preset came to lack the PICA indexes its seeded twin
// carries. A preset sharing a URL with a DefaultTargets entry must
// carry the same SRU knobs, or the one-click target speaks different CQL than the
// seeded one -- TestSuggestedTargetsAgreeWithDefaults pins that. Blurbs are the
// UI's to add; the wire config lives here.
var SuggestedTargets = []Target{
	{Name: "loc", URL: "lx2.loc.gov:210/LCDB", Protocol: ProtocolZ3950},
	{Name: "loc-sru", URL: "http://lx2.loc.gov:210/LCDB", Protocol: ProtocolSRU},
	{Name: "k10plus", URL: "https://sru.k10plus.de/opac-de-627", Protocol: ProtocolSRU,
		Indexes: map[string]string{"isbn": "pica.isb", "issn": "pica.iss"}},
	{Name: "indexdata-test", URL: "z3950.indexdata.com:210/marc", Protocol: ProtocolZ3950},
}

// SeedDefaultTargets installs DefaultTargets so a fresh deployment's subject
// lookup and copy cataloging work without configuration. It runs once ever
// per store (a marker record remembers the seeding), so an admin who
// deletes every target stays at zero across restarts.
func (s *Service) SeedDefaultTargets(ctx context.Context) error {
	if _, err := s.DB.Get(ctx, seededKey()); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	targets, err := s.Targets(ctx)
	if err != nil {
		return err
	}
	// Create-only marker: concurrent cold starts against a shared table
	// race the Get above, and CondIfAbsent makes every loser a clean no-op
	// instead of a duplicate seeding pass.
	if _, err := s.DB.Put(ctx, store.Record{Key: seededKey(), Data: []byte(`{}`)}, store.CondIfAbsent); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return nil
		}
		return err
	}
	if len(targets) > 0 {
		return nil
	}
	for _, t := range DefaultTargets {
		if err := s.PutTarget(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

// PutTarget creates or replaces a search target.
func (s *Service) PutTarget(ctx context.Context, t Target) error {
	if t.Name == "" || t.URL == "" || (t.Protocol != ProtocolSRU && t.Protocol != ProtocolZ3950) {
		return fmt.Errorf("%w: a target needs a name, url, and protocol sru|z3950", ErrValidation)
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.DB.Put(ctx, store.Record{Key: targetKey(t.Name), Data: data}, store.CondNone)
	return err
}

// DeleteTarget removes a target.
func (s *Service) DeleteTarget(ctx context.Context, name string) error {
	err := s.DB.Delete(ctx, store.Record{Key: targetKey(name)}, store.CondNone)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// Targets lists the configured sources, sorted by name.
func (s *Service) Targets(ctx context.Context) ([]Target, error) {
	out := []Target{}
	for rec, err := range s.DB.Query(ctx, "COPYCAT", "T#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var t Target
		if json.Unmarshal(rec.Data, &t) == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
