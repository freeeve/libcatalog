package bibframe

// The MARC <-> BIBFRAME fidelity contract as a consumable table (tasks/049):
// what roundtrip_test.go gates in CI, the MARC view annotates in the editor,
// and the ingest sidecar preserves verbatim. docs/marc-fidelity.md is the
// prose companion; update both together when the crosswalk changes.

// CoreFields are the MARC tags guaranteed to survive MARC -> BIBFRAME -> MARC
// on every vendored sample: the identifiers, primary/added agents, title,
// publication, extent, carrier, summary, subjects, genre, and access link an
// adopter judges fidelity by. A regression breaks the build.
var CoreFields = []string{
	"001", "006", "007", "008", "020", "040", "100", "245", "250", "260",
	"300", "306", "336", "337", "338", "347", "490", "500", "511", "520",
	"521", "533", "538", "650", "655", "700", "776", "856",
}

// KnownLoss maps each tag that does NOT survive the round-trip to why --
// now only the vendor-convention fields, which decode to their modeled
// equivalents rather than their original tags: deliberate non-goals, not
// losses of data. The reconstruction arc: libcodex v0.9.0 moved 008/336/500
// to CoreFields (tasks/053), v0.11.0 moved 306/347/490/511/521/533/538/776
// (tasks/055), and v0.12.0 finished 006/007 (tasks/056). These tags are what
// the lcat:marcVerbatim sidecar stores at MARC ingest so exports and the
// MARC view can reproduce the original forms (tasks/049).
// (libcodex v0.18.0 moved 040 to CoreFields: bf:AdminMetadata models the
// agency chain and an internal marcKey note carries the field exactly, so
// cataloging source is a modeled field again -- record-level provenance
// beside, not instead of, the named graphs; tasks/192/194.)
var KnownLoss = map[string]string{
	"037": "source of acquisition decodes as an 024-shaped identifier (vendor convention)",
	"084": "other classification number decodes as 072",
	// Editorial whole/part links (tasks/221/222) have no MARC emission yet:
	// 773/774 need host-item shaping (title plus a $w record id) that the
	// grain's bare {#idWork} references do not carry per target. The OPAC
	// and catalog.json carry the links; MARC exports omit them until a
	// mapping is designed.
	"773": "editorial hasPart/partOf links are not yet shaped as host-item entries",
	"774": "editorial hasPart/partOf links are not yet shaped as constituent-unit entries",
	// External work-identity links (tasks/066): the enrichment pass writes
	// <work> owl:sameAs <hub URI> (e.g. OpenLibrary), carried in N-Quads/JSON-LD
	// exports, the OPAC, and the editor. 758 (RDA Resource Identifier) is the
	// right MARC home, but the libcodex crosswalk emits MARC identifiers only
	// from bf:identifiedBy (020/022/024), not owl:sameAs, and has no 758 at all.
	// Forcing owl:sameAs into a 024 would mislabel a hub link as a standard
	// identifier of the item, so MARC omits it until libcodex crosswalks
	// owl:sameAs -> 758 (filed there).
	"758": "external-identity owl:sameAs links have no MARC emission yet (libcodex has no owl:sameAs -> 758 crosswalk)",
}

// LossyTag reports whether a tag is on the known-loss table, with the
// documented reason -- the editor's non-blocking warning and the sidecar's
// capture predicate share this one gate.
func LossyTag(tag string) (string, bool) {
	reason, ok := KnownLoss[tag]
	return reason, ok
}
