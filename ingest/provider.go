// Package ingest defines the compile-time provider model (ARCHITECTURE §9a): a
// bibliographic source plugs in as a Go type satisfying Provider, is registered in
// a Registry, and runs through the shared identity + clustering pipeline (Run). The
// provider set is finite, first-party or deployment-authored, and a deployment
// builds its own lcat, so binding is static -- no dynamic loading. The interface is
// shaped so a future subprocess or WASM transport can implement it unchanged.
package ingest

import (
	"context"
	"fmt"
	"sort"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// Role is what a provider contributes to the graph, which fixes the provenance
// named graph its statements land in (ARCHITECTURE §5/§9).
type Role int

const (
	// RoleIngest is a bibliographic source: its records are resolved and clustered
	// into Works, landing in the feed:<name> graph. OverDrive and MARC are ingest
	// providers.
	RoleIngest Role = iota
	// RoleEnrich is a provider that adds statements about existing entities
	// (authority labels, clustering hints, embeddings). Its statements land in the
	// editorial: or enrichment: graph, not feed:. Reserved: Run handles ingest
	// providers today; enrichment execution is future work.
	RoleEnrich
)

// String renders a Role for messages.
func (r Role) String() string {
	switch r {
	case RoleIngest:
		return "ingest"
	case RoleEnrich:
		return "enrich"
	default:
		return fmt.Sprintf("Role(%d)", int(r))
	}
}

// Provider is one bibliographic source bound at compile time. Name is the
// provenance feed graph the provider's statements belong to (feed:<name>); Role
// fixes the graph class; Records yields the source's resolvable records for the
// shared pipeline. A network-backed provider honors ctx for cancellation.
type Provider interface {
	Name() string
	Role() Role
	Records(ctx context.Context) ([]Record, error)
}

// Record is one resolvable bibliographic record: the keys that assign it stable
// two-tier ids (Identity) and the Work/Instance BIBFRAME it contributes once
// clustered. Any source that produces these three is an ingest Record -- an
// OverDrive item already is one -- so the pipeline is provider-agnostic.
type Record interface {
	Identity() identity.Record
	Work() codexbf.Work
	Instance() codexbf.Instance
}

// VerbatimProvider is an optional capability a Record may implement to carry the
// crosswalk-lossy MARC fields of its source record, serialized field-exact via
// bibframe.EncodeVerbatimField. Run writes them into the Instance's feed graph
// under bibframe.PredMARCVerbatim, so a lossy tag is preserved verbatim rather
// than silently dropped, and MARC export / the MARC view reproduce it (tasks/049).
type VerbatimProvider interface {
	Verbatim() []string
}

// ExtraProvider is an optional capability a Record may implement to carry per-Work
// display fields that are not part of BIBFRAME -- e.g. a cover URL, a personal rating,
// or a read date. Run writes them into the Work's feed provenance graph under
// bibframe.ExtraPred, and the projector surfaces them as catalog.json's `extra` object
// (tasks/026), which the Hugo module forwards to page params (tasks/022). A Record that
// does not implement it carries no extras, leaving the graph unchanged. For a clustered
// Work the first record's extras win, matching how shared Work metadata is taken.
type ExtraProvider interface {
	Extras() map[string]string
}

// AuthoritySubject is one controlled-vocabulary subject a provider asserts for a Work
// (authority URI + localized labels + skos:broader parents). It is an alias of the
// bibframe emission type, defined in bibframe to avoid an ingest<-bibframe import cycle
// while keeping the capability's shape visible here.
type AuthoritySubject = bibframe.AuthoritySubject

// SubjectEnricher is an optional capability a Record may implement to contribute
// controlled-vocabulary subjects for its Work -- e.g. by promoting free genre tags
// through an authority table (tasks/026). Run emits each as a bf:subject link to the
// authority URI plus its skos:prefLabel/broader statements in the feed graph, so the
// projector resolves them as controlled subjects with labels and hierarchy
// (tasks/012/015). For a clustered Work the first record's subjects win, matching how
// shared Work metadata and extras are taken; a Record that does not implement it
// contributes none.
type SubjectEnricher interface {
	ControlledSubjects() []AuthoritySubject
}

// Config carries a provider's build-time configuration into its Factory. Feed
// overrides the provenance graph name (default: the registry key); Source is the
// primary input (a cache directory, a file, a URL); Params holds provider-specific
// extras so the registry stays uniform.
type Config struct {
	Feed   string
	Source string
	Params map[string]string
}

// Factory constructs a configured Provider from a Config. A deployment registers
// one per provider type, so adding a source is a Register call plus the provider's
// own package -- no libcat fork.
type Factory func(Config) (Provider, error)

// Registry maps a provider type key to its Factory. Registration is explicit (in
// main, not init), so the built-in set is auditable and a deployment composes its
// own set.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register binds a provider type key to its Factory. It errors on an empty key, a
// nil factory, or a duplicate key, so a composition mistake fails at startup.
func (r *Registry) Register(name string, f Factory) error {
	if name == "" {
		return fmt.Errorf("ingest: empty provider name")
	}
	if f == nil {
		return fmt.Errorf("ingest: nil factory for provider %q", name)
	}
	if _, dup := r.factories[name]; dup {
		return fmt.Errorf("ingest: provider %q already registered", name)
	}
	r.factories[name] = f
	return nil
}

// New constructs the registered provider for name with cfg, erroring if the key is
// unknown. When cfg.Feed is empty it defaults to name, so the registry key doubles
// as the provenance graph unless a deployment overrides it.
func (r *Registry) New(name string, cfg Config) (Provider, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("ingest: unknown provider %q (registered: %v)", name, r.Names())
	}
	if cfg.Feed == "" {
		cfg.Feed = name
	}
	return f(cfg)
}

// Names returns the registered provider keys in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
