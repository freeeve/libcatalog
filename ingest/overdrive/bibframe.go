package overdrive

import (
	"strings"

	"github.com/freeeve/libcat/identity"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// bf:source scheme codes for OverDrive's identifiers and classification, tagged so
// each is unambiguously recoverable from a grain (ARCHITECTURE §9). They
// are exported so downstream consumers -- notably the runtime availability adapter
// , which keys on the Reserve ID -- select the right node by scheme.
const (
	// SourceBISAC is the bf:source of a BISAC subject-category code.
	SourceBISAC = "bisacsh"
	// SourceOverDrive is the bf:source of the OverDrive title id.
	SourceOverDrive = "overdrive"
	// SourceReserveID is the bf:source of the OverDrive Reserve ID -- the stable
	// per-edition key the availability adapter queries at view time.
	SourceReserveID = "overdrive-reserve"
)

// BIBFRAME crosswalks one OverDrive item directly to a libcodex BIBFRAME
// Work/Instance pair -- the OverDrive reference provider's real path
// (ARCHITECTURE §9), mapping the Thunder JSON feed straight to BIBFRAME with no
// MARC intermediate. This keeps data the MARC detour drops (notably BISAC
// classification) and models subjects as bf:Topic without MARC's 6xx/653
// constraints. The result is serialized by BIBFRAME.Graph, so it takes the same
// graph shape as a record-derived BIBFRAME.
func (it Item) BIBFRAME() *codexbf.BIBFRAME {
	return &codexbf.BIBFRAME{Work: it.Work(), Instance: it.Instance()}
}

// Work returns the item's Work-level BIBFRAME (intellectual content): content
// class, preferred title, agents, topical subjects, languages, and BISAC
// classification. When items cluster into one Work, this is the shared node.
func (it Item) Work() codexbf.Work {
	w := codexbf.Work{Class: workClass(it.Type.ID)}
	if title := it.title(); title.MainTitle != "" {
		w.Titles = append(w.Titles, title)
	}
	w.Contributions = it.bibContributions()
	for _, s := range it.Subjects {
		if s.Name != "" {
			w.Subjects = append(w.Subjects, codexbf.Subject{Class: "Topic", Label: s.Name})
		}
	}
	for _, l := range it.Languages {
		if code := iso639_2(l.ID); code != "" {
			w.Languages = append(w.Languages, code)
		}
	}
	// BISAC is a controlled classification: the code carries bf:source "bisacsh" so
	// the scheme is explicit. The MARC detour dropped these entirely.
	// The feed's heading text rides the display-only Label (rdfs:label in the
	// graph, libcodex v0.14.0), so facets show the heading while MARC 084
	// keeps the code.
	for _, b := range it.BISAC {
		if b.Code != "" {
			w.Classifications = append(w.Classifications,
				codexbf.Classification{Class: "Classification", Value: b.Code, Label: b.Description, Source: SourceBISAC})
		}
	}
	// The feed description is an HTML fragment; bf:summary carries plain
	// text (same promotion the Hardcover blurb got).
	if text := htmlText(it.Description); text != "" {
		w.Summary = []string{text}
	}
	return w
}

// Instance returns the item's Instance-level BIBFRAME (this publication):
// transcribed title, edition, provision, and identifiers (ISBNs, the OverDrive
// title id, and the Reserve ID). Each clustered item contributes one Instance.
func (it Item) Instance() codexbf.Instance {
	var inst codexbf.Instance
	if title := it.title(); title.MainTitle != "" {
		inst.Titles = append(inst.Titles, title)
	}
	inst.EditionStatement = it.Edition
	// Format lives on the Instance, not just the Work content class: when
	// an ebook and audiobook cluster into one Work, the Work class reflects only the
	// first edition, so per-edition format must be an Instance property. Both are
	// digital ("online resource" carrier); the RDA media type distinguishes them.
	inst.Media = []codexbf.RDATerm{rdaMediaTerm(it.Type.ID)}
	inst.Carrier = []codexbf.RDATerm{{Code: "cr", Label: "online resource"}}
	if p := it.provisionBF(); p != nil {
		inst.Provisions = append(inst.Provisions, *p)
	}
	for _, isbn := range it.ISBNs() {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Isbn", Value: isbn})
	}
	// The OverDrive title id and Reserve ID are local identifiers, distinguished by
	// bf:source: the title id carries "overdrive", the Reserve ID
	// "overdrive-reserve" so the availability adapter can recover it unambiguously.
	// The Reserve ID is a stable per-edition key (not volatile availability data),
	// so it stays in the feed grain per the ARCHITECTURE §5 provenance model.
	if it.ID != "" {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Identifier", Value: it.ID, Source: SourceOverDrive})
	}
	if it.ReserveID != "" {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Identifier", Value: it.ReserveID, Source: SourceReserveID})
	}
	return inst
}

// title is the item's transcribed title, shared by the Work and each Instance.
func (it Item) title() codexbf.Title {
	return codexbf.Title{MainTitle: it.Title, Subtitle: it.Subtitle}
}

// WorkID returns a stable, IRI- and filesystem-safe id for the item, taken from
// the OverDrive title id (falling back to the Reserve ID). It names the grain
// file and the #<id>Work / #<id>Instance node IRIs. Phase 0 only: ARCHITECTURE
// §4's identity model replaces this with a minted, provider-independent id.
func (it Item) WorkID() string {
	if id := sanitizeID(it.ID); id != "" {
		return id
	}
	return sanitizeID(it.ReserveID)
}

// Identity returns the record's resolution keys and clustering fields for
// identity.Resolver to assign stable Work/Instance ids (ARCHITECTURE §4). The
// keys are the OverDrive title id, each ISBN, and the Reserve ID, namespaced by
// scheme -- ordered so the most specific (the title id) resolves first and ISBN
// serves as the cross-provider merge key. The clustering fields are the primary
// author, the main title, and the original language.
func (it Item) Identity() identity.Record {
	rec := identity.Record{
		Author: it.primaryAuthor(),
		Title:  it.Title,
		Lang:   it.primaryLang(),
	}
	if it.ID != "" {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, it.ID))
	}
	for _, isbn := range it.ISBNs() {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeISBN, isbn))
	}
	if it.ReserveID != "" {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, it.ReserveID))
	}
	return rec
}

// primaryAuthor returns the transcribed name of the item's first author, or "".
func (it Item) primaryAuthor() string {
	authors, _ := it.contributors()
	if len(authors) > 0 {
		return authors[0].name
	}
	return ""
}

// primaryLang returns the ISO 639-2 code of the item's first mappable language,
// or "".
func (it Item) primaryLang() string {
	for _, l := range it.Languages {
		if code := iso639_2(l.ID); code != "" {
			return code
		}
	}
	return ""
}

// bibContributions maps the item's creators to BIBFRAME contributions, marking
// the first author as the primary contribution (as a MARC 1xx would be) and the
// rest -- extra authors, narrators, illustrators -- as added contributions.
func (it Item) bibContributions() []codexbf.Contribution {
	authors, others := it.contributors()
	out := make([]codexbf.Contribution, 0, len(authors)+len(others))
	for i, c := range authors {
		out = append(out, codexbf.Contribution{
			Primary: i == 0, Class: "Person", Label: c.name, Roles: bibRoles(c),
		})
	}
	for _, c := range others {
		out = append(out, codexbf.Contribution{
			Primary: false, Class: "Person", Label: c.name, Roles: bibRoles(c),
		})
	}
	return out
}

// relatorVocab is the LoC relator-term vocabulary namespace; a mapped relator code
// (e.g. "aut") names a term IRI beneath it. It mirrors libcodex's relator IRIs so the
// direct-to-BIBFRAME path carries the same controlled roles as the MARC $4 path.
const relatorVocab = "http://id.loc.gov/vocabulary/relators/"

// bibRoles maps a contributor to a BIBFRAME role: a LoC relators IRI (from the mapped
// relator code) labeled with the role term, or a bare literal term when the role has
// no relator mapping.
func bibRoles(c contributor) []codexbf.Role {
	switch {
	case c.relator != "":
		return []codexbf.Role{{IRI: relatorVocab + c.relator, Term: c.role}}
	case c.role != "":
		return []codexbf.Role{{Term: c.role}}
	default:
		return nil
	}
}

// provisionBF builds the publication provision (publisher, year) or nil when the
// item names neither.
func (it Item) provisionBF() *codexbf.Provision {
	p := &codexbf.Provision{Class: "Publication"}
	if it.Publisher != nil {
		p.Publisher = it.Publisher.Name
	}
	if len(it.PublishDate) >= 4 {
		p.Date = it.PublishDate[:4]
	}
	if p.Publisher == "" && p.Date == "" {
		return nil
	}
	return p
}

// workClass maps an OverDrive media type to the BIBFRAME class refining bf:Work:
// nonmusical audio for an audiobook, text otherwise. It mirrors libcodex's
// leader-byte crosswalk ('i' -> Audio, 'a' -> Text). The Work class is retained for
// single-format Works, but the projector's format facet reads the Instance media
// type (rdaMediaTerm) so a clustered mixed-format Work exposes each edition's format
// .
func workClass(typeID string) string {
	if typeID == "audiobook" {
		return "Audio"
	}
	return "Text"
}

// rdaMediaTerm maps an OverDrive media type to its RDA media term (337 -> bf:media):
// audio for an audiobook, computer for an ebook, each carrying the id.loc.gov
// mediaTypes code ($b) so the grain matches a record-derived BIBFRAME. This
// per-Instance discriminant is what the projector maps to a discovery format
// (audiobook vs ebook), so format survives edition clustering.
func rdaMediaTerm(typeID string) codexbf.RDATerm {
	if typeID == "audiobook" {
		return codexbf.RDATerm{Code: "s", Label: "audio"}
	}
	return codexbf.RDATerm{Code: "c", Label: "computer"}
}

// sanitizeID keeps the characters valid in an IRI fragment and a filename,
// dropping the rest.
func sanitizeID(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_':
			b.WriteByte(c)
		}
	}
	return b.String()
}
