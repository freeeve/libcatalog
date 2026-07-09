package nquads

import (
	"maps"
	"regexp"
	"sort"
	"strings"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// mappedID is one non-merge-key identifier as it rides the Instance: class
// ("Isbn"/"Identifier"), optional bf:source, the emitted value, and the
// SchemeID resolution key when the rule opted in ("" = display-only).
type mappedID struct{ class, source, value, key string }

// work is one accumulated export record (a work, or one format bucket of a
// grouped work): the mapped fields plus which source objects attested it.
type work struct {
	id           string
	group        string // grouping id (tasks/182); self when the export has none
	title        string
	subtitle     string
	summary      string
	creators     []string
	contributors []string // "Last, First (role)" literals, statement order
	publisher    string
	issued       string
	format       string
	isbns        []string
	idents       []mappedID
	languages    []string // ISO 639-2/B, statement order; empty -> mapping default
	subjectURIs  []string
	tags         []string // uncontrolled topics, no source; before keywords
	keywords     []string // uncontrolled topics, mapping's keyword-source
	classCodes   []string // classification codes (object IRI minus prefix)
	classIRIs    []string // the full object IRIs, aligned with classCodes
	extras       map[string]string
	sources      []string
	confident    bool // attested by any non-tentative source
}

// record adapts a work to ingest.Record. One record per work subject; records
// sharing a grouping id cluster into one Work with one Instance each
// (tasks/182). terms is the shared harvested term-description side.
type record struct {
	w        *work
	terms    *terms
	m        *Mapping
	idScheme string
}

// Identity namespaces the author key with the export GROUP id: the export
// already deduped its works, so the computed author|title key must not
// re-merge distinct works that share an access point -- while a group's
// format-bucket records must share the key and cluster into one Work
// (tasks/182). Cross-feed merging with a primary feed happens only through
// the identifier keys (ISBNs and the mapping's keyed schemes -- durable for
// isbn-less works whose export ids renumber between dumps).
func (r record) Identity() identity.Record {
	title := r.w.title
	if title == "" {
		title = "[untitled]"
	}
	author := ""
	if len(r.w.creators) > 0 {
		author = lastFirst(r.w.creators[0])
	}
	lang := r.m.IdentityLanguage
	if lang == "" {
		lang = r.lang()
	}
	rec := identity.Record{
		Author: r.idScheme + ":" + r.w.group + " " + author,
		Title:  title,
		Lang:   lang,
	}
	rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, r.providerID()))
	for _, isbn := range r.w.isbns {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeISBN, isbn))
	}
	for _, id := range r.w.idents {
		if id.key != "" {
			rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, id.key))
		}
	}
	return rec
}

// lang is the work's first ISO 639-2/B language, defaulting per the mapping
// when the export carries none.
func (r record) lang() string {
	if len(r.w.languages) > 0 {
		return r.w.languages[0]
	}
	return r.m.DefaultLanguage
}

// Work returns the export work's BIBFRAME Work: the mapping's class, title
// (+subtitle), contributions (mapped contributor literals with roles, else
// one author per creator), all languages in statement order, uncontrolled
// topic subjects (tags before keywords), classifications, and the summary.
// Grouped records repeat work-level statements, so this is identical across
// a group and first-record-wins grouping is well defined.
func (r record) Work() codexbf.Work {
	w := codexbf.Work{Class: r.m.Class, Languages: r.w.languages}
	if len(w.Languages) == 0 {
		w.Languages = []string{r.m.DefaultLanguage}
	}
	if r.w.title != "" {
		w.Titles = append(w.Titles, r.title())
	}
	w.Contributions = r.contributions()
	for _, tag := range r.w.tags {
		w.Subjects = append(w.Subjects, codexbf.Subject{Class: "Topic", Label: tag})
	}
	for _, kw := range r.w.keywords {
		w.Subjects = append(w.Subjects, codexbf.Subject{Class: "Topic", Label: kw, Source: r.m.KeywordSource})
	}
	for i, code := range r.w.classCodes {
		w.Classifications = append(w.Classifications, codexbf.Classification{
			Class:  "Classification",
			Value:  code,
			Label:  r.terms.label(r.w.classIRIs[i]),
			Source: r.m.Classifications.Source,
		})
	}
	if r.w.summary != "" {
		w.Summary = []string{r.w.summary}
	}
	return w
}

// contributions builds the Work's agents: mapped contributor literals
// ("Last, First (role)"; first statement primary, role lowercased, default
// author) when the export carries them, else one author per creator literal
// as before (tasks/182). Both paths run the junk/length gate (tasks/186): a
// record whose every agent is debris yields a Work with no contributions --
// the raw creator literal still feeds the identity author key regardless
// (Identity reads it directly).
func (r record) contributions() []codexbf.Contribution {
	var out []codexbf.Contribution
	for _, entry := range r.w.contributors {
		name, role := splitNameRole(strings.TrimSpace(entry))
		if name == "" || isJunkContributor(name, role) {
			continue
		}
		if role == "" {
			role = "author"
		}
		out = append(out, codexbf.Contribution{
			Primary: len(out) == 0,
			Class:   "Person",
			Label:   lastFirst(name),
			Roles:   []codexbf.Role{{Term: role}},
		})
	}
	if len(out) > 0 {
		return out
	}
	for _, c := range r.w.creators {
		c = strings.TrimSpace(c)
		if c == "" || isJunkContributor(c, "") {
			continue
		}
		out = append(out, codexbf.Contribution{
			Primary: len(out) == 0,
			Class:   "Person",
			Label:   lastFirst(c),
			Roles:   []codexbf.Role{{Term: "author"}},
		})
	}
	return out
}

// yearLed matches an agent entry that opens with a bare 4-digit year ("1999",
// "2011 EMI Records Ltd."), which is copyright-line debris, not an agent.
var yearLed = regexp.MustCompile(`^\d{4}\b`)

// maxContributorName bounds a single agent label in bytes. A "name" past this
// bound is a credit list that escaped splitting or a transcribed access point
// (a 158-byte conference heading, coll:32780), not an agent -- and downstream
// it becomes a contributor term slug that overflows the 255-byte filename
// limit when Hugo mints the term page. Mirrors the coll provider's policy
// (coll-support parse.go), so a feed flip does not grow contributions the old
// pipeline dropped (tasks/186).
const maxContributorName = 100

// isJunkContributor reports a "name" that is copyright-line debris or an
// overlong non-agent rather than a person: a © line, an "All rights
// reserved" fragment, a copyright-holder credit, or a year-led remnant.
func isJunkContributor(name, role string) bool {
	lower := strings.ToLower(name)
	return role == "copyright holder" ||
		len(name) > maxContributorName ||
		strings.Contains(name, "©") ||
		strings.Contains(lower, "all rights reserved") ||
		lower == "c" ||
		yearLed.MatchString(name)
}

// Instance returns this record's Instance: title, the format's RDA media
// type, the ISBNs and mapped identifiers, the source-tagged provider id
// (which MUST equal the SchemeID key so re-ingest round-trips ids for
// isbn-less works), and the publication provision.
func (r record) Instance() codexbf.Instance {
	var inst codexbf.Instance
	if r.w.title != "" {
		inst.Titles = append(inst.Titles, r.title())
	}
	if m, ok := rdaMediaTerm(r.w.format); ok {
		inst.Media = []codexbf.RDATerm{m}
	}
	for _, isbn := range r.w.isbns {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Isbn", Value: isbn})
	}
	for _, id := range r.w.idents {
		inst.Identifiers = append(inst.Identifiers,
			codexbf.Identifier{Class: id.class, Value: id.value, Source: id.source})
	}
	inst.Identifiers = append(inst.Identifiers,
		codexbf.Identifier{Class: "Identifier", Value: r.providerID(), Source: r.idScheme})
	if r.w.publisher != "" || r.w.issued != "" {
		inst.Provisions = []codexbf.Provision{{Class: "Publication", Publisher: r.w.publisher, Date: r.w.issued}}
	}
	return inst
}

// title is the record's transcribed title, shared by its Work and Instance.
func (r record) title() codexbf.Title {
	return codexbf.Title{MainTitle: r.w.title, Subtitle: r.w.subtitle}
}

// Extras carries the export's display extras (the mapping's extras-prefix
// statements, verbatim) and its provenance: source slugs under the mapping's
// extra key (the one the public-sources allowlist governs -- it wins that
// key on a collision), plus a lower-confidence marker for tentative-only
// works.
func (r record) Extras() map[string]string {
	e := map[string]string{}
	maps.Copy(e, r.w.extras)
	if len(r.w.sources) > 0 {
		e[r.m.Sources.ExtraKey] = strings.Join(dedupeSorted(r.w.sources), ", ")
		if !r.w.confident {
			e["tentative"] = "yes"
		}
	}
	if len(e) == 0 {
		return nil
	}
	return e
}

// ControlledSubjects returns the export's subject URIs with the prefLabels
// (per language) and broader edges the export carries; a URI the export left
// undescribed emits bare and the projector's corpus-wide indexes cover it if
// any other feed knows it.
func (r record) ControlledSubjects() []ingest.AuthoritySubject {
	var subs []ingest.AuthoritySubject
	for _, uri := range dedupeSorted(r.w.subjectURIs) {
		subs = append(subs, r.authoritySubject(uri))
	}
	return subs
}

// DescribedTerms returns the subjects' ancestor-chain descriptions
// (prefLabel per language + broader), emitted into the feed graph with no
// bf:subject link so subject trees keep labeled top levels (tasks/180/182).
// The walk is breadth-first over the harvested broader edges, cycle-safe,
// depth-capped, excludes the subjects themselves (ControlledSubjects already
// describes them), skips URIs the export says nothing about, and returns
// sorted by URI for deterministic grains.
func (r record) DescribedTerms() []ingest.AuthoritySubject {
	direct := map[string]bool{}
	frontier := make([]string, 0, len(r.w.subjectURIs))
	for _, uri := range dedupeSorted(r.w.subjectURIs) {
		direct[uri] = true
		frontier = append(frontier, uri)
	}
	seen := map[string]bool{}
	var out []ingest.AuthoritySubject
	for depth := 0; depth < ancestryDepthCap && len(frontier) > 0; depth++ {
		var next []string
		for _, uri := range frontier {
			for _, parent := range r.terms.broader[uri] {
				if seen[parent] {
					continue
				}
				seen[parent] = true
				next = append(next, parent)
				if !direct[parent] && (len(r.terms.labels[parent]) > 0 || len(r.terms.broader[parent]) > 0) {
					out = append(out, r.authoritySubject(parent))
				}
			}
		}
		frontier = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URI < out[j].URI })
	return out
}

// ancestryDepthCap bounds the broader-chain walk per record, mirroring
// libcat's projection cap so a vocabulary cycle cannot run away.
const ancestryDepthCap = 12

// authoritySubject assembles one concept's description from the harvested
// term maps: labels per language and broader edges (sorted, deduped).
func (r record) authoritySubject(uri string) ingest.AuthoritySubject {
	s := ingest.AuthoritySubject{URI: uri}
	if labels := r.terms.labels[uri]; len(labels) > 0 {
		s.Labels = labels
	}
	if parents := r.terms.broader[uri]; len(parents) > 0 {
		s.Broader = dedupeSorted(parents)
	}
	return s
}

// providerID backs both the SchemeID resolution key and the Instance's
// source-tagged identifier; the two must be the same string.
func (r record) providerID() string {
	return r.idScheme + ":" + r.w.id
}

// splitNameRole splits one contributor literal into the name and its
// lowercased trailing parenthesized role, e.g. "Doe, Jane (Illustrator)" ->
// ("Doe, Jane", "illustrator"). An entry without a role returns role "".
func splitNameRole(entry string) (string, string) {
	if !strings.HasSuffix(entry, ")") {
		return entry, ""
	}
	i := strings.LastIndex(entry, "(")
	if i < 0 {
		return entry, ""
	}
	return strings.TrimSpace(entry[:i]), strings.ToLower(strings.TrimSpace(entry[i+1 : len(entry)-1]))
}

// rdaMediaTerm maps a normalized format literal to its RDA media type (337
// -> bf:media), which the projector reads back into the format facet: audio
// for an audiobook, computer for an ebook, unmediated for a physical book.
// ok is false for an empty or unrecognized format, so no media statement is
// emitted.
func rdaMediaTerm(format string) (codexbf.RDATerm, bool) {
	switch format {
	case "audiobook":
		return codexbf.RDATerm{Code: "s", Label: "audio"}, true
	case "ebook":
		return codexbf.RDATerm{Code: "c", Label: "computer"}, true
	case "physical":
		return codexbf.RDATerm{Code: "n", Label: "unmediated"}, true
	default:
		return codexbf.RDATerm{}, false
	}
}

// lastFirst normalizes "First Middle Last" to "Last, First Middle" (already
// comma-formed names pass through), so shared works across feeds don't get
// double-listed contributors.
func lastFirst(name string) string {
	n := strings.TrimSpace(name)
	if n == "" || strings.Contains(n, ",") {
		return n
	}
	parts := strings.Fields(n)
	if len(parts) < 2 {
		return n
	}
	return parts[len(parts)-1] + ", " + strings.Join(parts[:len(parts)-1], " ")
}

// dedupeSorted returns the distinct values in sorted order.
func dedupeSorted(vals []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
