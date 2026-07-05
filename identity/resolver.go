package identity

import "fmt"

// Record is the identity-relevant projection of one incoming provider record:
// the keys it can be resolved by and the fields of its computed clustering key.
type Record struct {
	// ProviderKeys are resolution keys in priority order, each namespaced so keys
	// from different schemes never collide, e.g. "overdrive:3389970" then
	// "isbn:9781682308233". The first key that already maps wins; ISBN is the
	// cross-provider merge key (ARCHITECTURE §9).
	ProviderKeys []string
	Author       string
	Title        string
	Lang         string
}

// Assignment is the resolved identity for a record: its stable Instance and Work
// ids, and whether either was freshly minted this ingest (vs resolved to an
// already-committed id).
type Assignment struct {
	InstanceID     string
	WorkID         string
	MintedInstance bool
	MintedWork     bool
}

// Merge is an editorial merge decision recovered from the grains (ARCHITECTURE
// §4): every reference to From resolves to To. It is the under-merge fix -- two
// records that should be one Work but clustered apart -- and the computed key
// cannot undo it. Chains collapse to a single canonical id.
type Merge struct {
	From string
	To   string
}

// Pin is an editorial split decision recovered from the grains (ARCHITECTURE §4):
// Instance is assigned to Work regardless of the computed clustering key. It is the
// over-merge fix -- records the key wrongly clustered together are pinned apart --
// and makes the split reproducible across re-ingest.
type Pin struct {
	Instance string
	Work     string
}

// Resolver assigns stable Work/Instance ids across ingests. Seed it with the
// identity already committed (from the grains), then Resolve each incoming record
// to an existing id or a freshly minted one. It is the mint-or-resolve core of
// ARCHITECTURE §4 / tasks/002: an unchanged record keeps its previously minted
// ids, so its grain -- and its public URL -- do not churn.
//
// A Resolver is not safe for concurrent use; ingest is single-threaded per
// corpus.
type Resolver struct {
	instByProvider map[string]string // provider key -> instance id
	workByInst     map[string]string // instance id -> work id
	workByKey      map[string]string // computed cluster key -> work id
	mergedInto     map[string]string // work id -> canonical work id (editorial overlay)
	pinByInst      map[string]string // instance id -> pinned work id (editorial split overlay)
	usedInst       map[string]bool
	usedWork       map[string]bool
	// conflicts records provider keys seen mapped to more than one instance
	// (tasks/002 §4): surfaced rather than silently remapped.
	conflicts []string
}

// NewResolver returns an empty resolver. Seed it from the committed identity
// before resolving new records.
func NewResolver() *Resolver {
	return &Resolver{
		instByProvider: map[string]string{},
		workByInst:     map[string]string{},
		workByKey:      map[string]string{},
		mergedInto:     map[string]string{},
		pinByInst:      map[string]string{},
		usedInst:       map[string]bool{},
		usedWork:       map[string]bool{},
	}
}

// SeedInstance records a previously minted Instance: its id, the provider keys it
// answers to, and the Work it belongs to. Called once per committed Instance
// before ingest so re-ingest resolves rather than re-mints.
func (r *Resolver) SeedInstance(instanceID, workID string, providerKeys []string) {
	r.usedInst[instanceID] = true
	r.usedWork[workID] = true
	r.workByInst[instanceID] = workID
	for _, k := range providerKeys {
		r.instByProvider[k] = instanceID
	}
}

// SeedWorkKey records the computed clustering key of a committed Work, so a new
// record with the same key clusters onto it. The caller recomputes the key from
// the Work's data with WorkKey.
func (r *Resolver) SeedWorkKey(clusterKey, workID string) {
	r.usedWork[workID] = true
	if clusterKey != "" {
		r.workByKey[clusterKey] = workID
	}
}

// SeedMerge records an editorial merge (tasks/001): every reference to from
// resolves to to. Merges override the computed key, so a re-ingest cannot undo a
// human decision. Chains collapse to a single canonical id.
func (r *Resolver) SeedMerge(from, to string) {
	r.mergedInto[from] = to
}

// SeedPin records an editorial split pin (tasks/001): instanceID is assigned to
// workID regardless of the computed clustering key, so an over-merge the key would
// otherwise recreate stays split across re-ingest. The pinned Work id is reserved
// so it is never minted for anything else.
func (r *Resolver) SeedPin(instanceID, workID string) {
	r.pinByInst[instanceID] = workID
	r.usedWork[workID] = true
}

// Resolve returns the stable identity for a record, minting only what is genuinely
// new. An Instance resolves by its first already-known provider key. Its Work is
// an editorial pin if one exists (an over-merge split, applied first), else the
// existing instance->work link, else the computed cluster key, else a freshly
// minted Work. Editorial merges are applied last, so a pinned or clustered Work
// still follows a later merge to its survivor.
func (r *Resolver) Resolve(rec Record) Assignment {
	instanceID, mintedInst := r.resolveInstance(rec.ProviderKeys)

	mintedWork := false
	workID, pinned := r.pinByInst[instanceID]
	if !pinned {
		var ok bool
		workID, ok = r.workByInst[instanceID]
		if !ok {
			key := WorkKey(rec.Author, rec.Title, rec.Lang)
			if wid, seen := r.workByKey[key]; key != "" && seen {
				workID = wid
			} else {
				workID = r.mint(WorkPrefix, r.usedWork)
				if key != "" {
					r.workByKey[key] = workID
				}
				mintedWork = true
			}
		}
	}
	r.workByInst[instanceID] = workID

	return Assignment{
		InstanceID:     instanceID,
		WorkID:         r.canonical(workID),
		MintedInstance: mintedInst,
		MintedWork:     mintedWork,
	}
}

// resolveInstance finds the instance for a record's keys, minting a new one when
// none is known. It binds every key to the resolved instance, recording a
// conflict when a key was already bound to a different instance rather than
// silently remapping it.
func (r *Resolver) resolveInstance(keys []string) (string, bool) {
	instanceID := ""
	for _, k := range keys {
		if id, ok := r.instByProvider[k]; ok {
			instanceID = id
			break
		}
	}
	minted := false
	if instanceID == "" {
		instanceID = r.mint(InstancePrefix, r.usedInst)
		minted = true
	}
	for _, k := range keys {
		if prev, ok := r.instByProvider[k]; ok && prev != instanceID {
			r.conflicts = append(r.conflicts,
				fmt.Sprintf("provider key %q maps to both %s and %s", k, prev, instanceID))
			continue
		}
		r.instByProvider[k] = instanceID
	}
	return instanceID, minted
}

// canonical follows the editorial merge chain to the surviving Work id.
func (r *Resolver) canonical(workID string) string {
	seen := map[string]bool{}
	for {
		to, ok := r.mergedInto[workID]
		if !ok || seen[workID] {
			return workID
		}
		seen[workID] = true
		workID = to
	}
}

// Conflicts returns provider-key collisions seen during resolution (tasks/002
// §4), for the caller to surface. Nil when there were none.
func (r *Resolver) Conflicts() []string { return r.conflicts }

// mint draws unused ids so a (vanishingly unlikely) crypto/rand collision can
// never alias two records to one id.
func (r *Resolver) mint(prefix string, used map[string]bool) string {
	for {
		id := Mint(prefix)
		if !used[id] {
			used[id] = true
			return id
		}
	}
}
