package export

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/iso2709"
	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// AuthoritySelection scopes an authority export: everything, a
// vocabulary subset, and/or a label-prefix filter.
type AuthoritySelection struct {
	All    bool     `json:"all,omitempty"`
	Vocabs []string `json:"vocabs,omitempty"` // scheme keys, e.g. ["local", "lcgft"]
	Label  string   `json:"label,omitempty"`  // label-prefix filter
}

// labelSearchLimit bounds a label-filtered export per scheme.
const labelSearchLimit = 1000

// CreateAuthorities records an authority export job. Label-filtered
// selections run in-request (bounded); full-vocabulary dumps queue for the
// worker. CSV has no authority shape and is refused.
func (s *Service) CreateAuthorities(ctx context.Context, requester string, format Format, sel AuthoritySelection) (Job, error) {
	if _, ok := extensions[format]; !ok || format == FormatCSV {
		return Job{}, fmt.Errorf("export: authorities export as marc, nquads, or jsonld, not %q", format)
	}
	if s.Vocab == nil {
		return Job{}, errors.New("export: no vocabulary index on this deployment")
	}
	if !sel.All && len(sel.Vocabs) == 0 && sel.Label == "" {
		return Job{}, errors.New("export: empty authority selection")
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return Job{}, err
	}
	now := s.now().UTC()
	job := Job{
		ID: hex.EncodeToString(suffix), Requester: requester,
		Format: format, Authorities: &sel, Status: StatusQueued, CreatedAt: now,
	}
	if err := s.put(ctx, &job, store.CondIfAbsent); err != nil {
		return Job{}, err
	}
	if _, err := s.db.Put(ctx, store.Record{
		Key:      userIdxKey(requester, now.Format(time.RFC3339), job.ID),
		Data:     []byte(job.ID),
		ExpireAt: now.Add(jobTTL),
	}, store.CondNone); err != nil {
		return Job{}, err
	}
	// A label filter is bounded (labelSearchLimit per scheme); run now.
	if sel.Label != "" {
		if err := s.Run(ctx, job.ID); err != nil {
			return Job{}, err
		}
		return s.Get(ctx, requester, job.ID, true)
	}
	return job, nil
}

// resolveTerms materializes the selection from the index, deterministic
// scheme-then-label order, retired (merged) terms excluded.
func (s *Service) resolveTerms(sel AuthoritySelection) ([]*vocab.Term, error) {
	schemes := sel.Vocabs
	if len(schemes) == 0 {
		schemes = s.Vocab.Schemes()
	}
	sort.Strings(schemes)
	var out []*vocab.Term
	for _, scheme := range schemes {
		var terms []*vocab.Term
		if sel.Label != "" {
			terms = s.Vocab.Search(scheme, sel.Label, labelSearchLimit)
		} else {
			terms = s.Vocab.Terms(scheme)
		}
		for _, t := range terms {
			if t.MergedInto == "" {
				out = append(out, t)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("selection matched no authority terms")
	}
	return out, nil
}

// emitAuthorities renders the authority selection in the job's format.
func (s *Service) emitAuthorities(_ context.Context, job Job) ([]byte, int, error) {
	terms, err := s.resolveTerms(*job.Authorities)
	if err != nil {
		return nil, 0, err
	}
	switch job.Format {
	case FormatMARC:
		return emitAuthorityMARC(s.Vocab, terms)
	case FormatNQuads:
		return emitAuthorityNQuads(terms)
	case FormatJSONLD:
		return emitAuthorityJSONLD(terms)
	}
	return nil, 0, fmt.Errorf("export: unknown authority format %q", job.Format)
}

// authorityLeader marks a MARC 21 Authority record (type z).
const authorityLeader = codex.Leader("00000nz  a2200000o  4500")

// emitAuthorityMARC hand-builds one MARC authority record per term: 150
// heading, 450 used-for references, 550 broader/narrower/related (resolved
// to labels through the index), 750 $0 exact-match URIs, 680 scope note.
func emitAuthorityMARC(ix *vocab.Index, terms []*vocab.Term) ([]byte, int, error) {
	var out bytes.Buffer
	w := iso2709.NewWriter(&out)
	for _, t := range terms {
		rec := codex.NewRecord()
		rec.SetLeader(authorityLeader)
		rec.AddField(codex.NewControlField("001", localName(t.ID)))
		rec.AddField(codex.NewControlField("008", "000000"+strings.Repeat("|", 34)))
		rec.AddField(codex.NewDataField("024", '7', ' ',
			codex.NewSubfield('a', t.ID), codex.NewSubfield('2', "uri")))
		rec.AddField(codex.NewDataField("150", ' ', ' ', codex.NewSubfield('a', t.Label("en"))))
		for _, lang := range sortedLangs(t.AltLabels) {
			for _, alt := range t.AltLabels[lang] {
				rec.AddField(codex.NewDataField("450", ' ', ' ', codex.NewSubfield('a', alt)))
			}
		}
		addSeeAlso := func(uris []string, control string) {
			for _, uri := range uris {
				ref, ok := ix.Lookup(t.Scheme, uri)
				if !ok {
					continue
				}
				subs := []codex.Subfield{}
				if control != "" {
					subs = append(subs, codex.NewSubfield('w', control))
				}
				subs = append(subs, codex.NewSubfield('a', ref.Label("en")))
				rec.AddField(codex.NewDataField("550", ' ', ' ', subs...))
			}
		}
		addSeeAlso(t.Broader, "g")
		addSeeAlso(t.Narrower, "h")
		addSeeAlso(t.Related, "")
		for _, uri := range t.ExactMatch {
			rec.AddField(codex.NewDataField("750", ' ', '7', codex.NewSubfield('0', uri)))
		}
		if def := vocab.PickLabel(t.Definition); def != "" {
			rec.AddField(codex.NewDataField("680", ' ', ' ', codex.NewSubfield('i', def)))
		}
		if err := w.Write(rec); err != nil {
			return nil, 0, err
		}
	}
	return out.Bytes(), len(terms), nil
}

// emitAuthorityNQuads re-serializes the terms' SKOS statements under their
// authority:<scheme> graphs -- the same shape the authorities tree stores.
func emitAuthorityNQuads(terms []*vocab.Term) ([]byte, int, error) {
	var enc rdf.Encoder
	var out []byte
	for _, t := range terms {
		at := termToAuthority(t)
		quads, err := at.Quads()
		if err != nil {
			return nil, 0, err
		}
		graph := bibframe.AuthorityGraph(t.Scheme)
		for _, q := range quads {
			q.G = graph
			out = enc.AppendQuad(out, q)
		}
	}
	return out, len(terms), nil
}

// emitAuthorityJSONLD renders a SKOS JSON-LD graph of the terms.
func emitAuthorityJSONLD(terms []*vocab.Term) ([]byte, int, error) {
	graph := make([]map[string]any, 0, len(terms))
	for _, t := range terms {
		doc := map[string]any{"@id": t.ID, "@type": "skos:Concept", "lcat:scheme": t.Scheme}
		doc["skos:prefLabel"] = langValues(t.Labels)
		if alts := langListValues(t.AltLabels); len(alts) > 0 {
			doc["skos:altLabel"] = alts
		}
		if defs := langValues(t.Definition); len(defs) > 0 {
			doc["skos:definition"] = defs
		}
		for key, uris := range map[string][]string{
			"skos:broader": t.Broader, "skos:narrower": t.Narrower,
			"skos:related": t.Related, "skos:exactMatch": t.ExactMatch,
		} {
			if len(uris) > 0 {
				refs := make([]map[string]string, len(uris))
				for i, uri := range uris {
					refs[i] = map[string]string{"@id": uri}
				}
				doc[key] = refs
			}
		}
		graph = append(graph, doc)
	}
	out, err := json.MarshalIndent(map[string]any{
		"@context": map[string]string{
			"skos": "http://www.w3.org/2004/02/skos/core#",
			"lcat": bibframe.LcatNS,
		},
		"@graph": graph,
	}, "", " ")
	return out, len(terms), err
}

// termToAuthority converts an index term back to the grain-facing shape.
func termToAuthority(t *vocab.Term) bibframe.AuthorityTerm {
	return bibframe.AuthorityTerm{
		URI: t.ID, PrefLabel: t.Labels, AltLabel: t.AltLabels,
		Definition: t.Definition, Broader: t.Broader, Narrower: t.Narrower,
		Related: t.Related, ExactMatch: t.ExactMatch,
	}
}

func localName(uri string) string {
	if i := strings.LastIndexAny(uri, "/#"); i >= 0 && i < len(uri)-1 {
		return uri[i+1:]
	}
	return uri
}

func sortedLangs[V any](m map[string]V) []string {
	langs := make([]string, 0, len(m))
	for k := range m {
		langs = append(langs, k)
	}
	sort.Strings(langs)
	return langs
}

func langValues(byLang map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(byLang))
	for _, lang := range sortedLangs(byLang) {
		v := map[string]string{"@value": byLang[lang]}
		if lang != "" {
			v["@language"] = lang
		}
		out = append(out, v)
	}
	return out
}

func langListValues(byLang map[string][]string) []map[string]string {
	var out []map[string]string
	for _, lang := range sortedLangs(byLang) {
		for _, val := range byLang[lang] {
			v := map[string]string{"@value": val}
			if lang != "" {
				v["@language"] = lang
			}
			out = append(out, v)
		}
	}
	return out
}
