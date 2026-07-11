// Package csvmap is the generic CSV ingest provider: it reads a
// spreadsheet-shaped export -- the lowest-common-denominator dump every ILS
// and collection tool can produce -- into ingest records driven by a
// declarative TOML mapping of columns to fields, so a deployment sideloads a
// CSV with a profile, not code. Works sharing ISBNs (or a stable id column)
// with another feed merge in the shared clustering pipeline.
package csvmap

import (
	"context"
	stdcsv "encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/freeeve/libcat/ingest"
)

// ProviderName is the registry key and default provenance feed (feed:csv).
const ProviderName = "csv"

// Provider reads a mapped CSV file into ingest records, one per row.
type Provider struct {
	feed     string
	path     string
	m        *Mapping
	idScheme string
}

// New builds the provider from an ingest.Config: Source is the .csv path and
// Params["mapping"] the mapping TOML path; Feed overrides the provenance
// feed name.
func New(cfg ingest.Config) (ingest.Provider, error) {
	if cfg.Source == "" {
		return nil, fmt.Errorf("csv: Source (.csv path) is required")
	}
	mappingPath := cfg.Params["mapping"]
	if mappingPath == "" {
		return nil, fmt.Errorf("csv: Params[\"mapping\"] (mapping TOML path) is required")
	}
	m, err := LoadMapping(mappingPath)
	if err != nil {
		return nil, err
	}
	feed := cfg.Feed
	if feed == "" {
		feed = ProviderName
	}
	idScheme := m.IDScheme
	if idScheme == "" {
		idScheme = feed
	}
	return &Provider{feed: feed, path: cfg.Source, m: m, idScheme: idScheme}, nil
}

// Name is the provenance feed the run writes (feed:<name>).
func (p *Provider) Name() string { return p.feed }

// Role marks this an ingest-role provider.
func (p *Provider) Role() ingest.Role { return ingest.RoleIngest }

// Records reads every row, in file order (the file is the adopter's own
// export, so its order is already deterministic). Rows without a title are
// dropped with a warning tally, mirroring the nquads provider.
func (p *Provider) Records(ctx context.Context) ([]ingest.Record, error) {
	f, err := os.Open(p.path)
	if err != nil {
		return nil, fmt.Errorf("csv: open %s: %w", p.path, err)
	}
	defer f.Close()

	r := stdcsv.NewReader(f)
	r.Comma = p.m.delimiter()
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("csv: read header of %s: %w", p.path, err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.TrimSpace(name)] = i
	}
	if err := p.m.checkColumns(col); err != nil {
		return nil, fmt.Errorf("csv: %s: %w", p.path, err)
	}

	var recs []ingest.Record
	dropped, line := 0, 1
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("csv: %s line %d: %w", p.path, line, err)
		}
		rec := p.rowRecord(row, col, line)
		if rec.title == "" {
			dropped++
			continue
		}
		recs = append(recs, rec)
	}
	if dropped > 0 {
		fmt.Fprintf(os.Stderr, "csv: dropped %d untitled rows\n", dropped)
	}
	return recs, nil
}

// rowRecord maps one row through the column mapping.
func (p *Provider) rowRecord(row []string, col map[string]int, line int) record {
	cell := func(field string) string {
		name, ok := p.m.Columns[field]
		if !ok {
			return ""
		}
		i, ok := col[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	rec := record{m: p.m, idScheme: p.idScheme, line: line}
	rec.id = cell("id")
	rec.title = cell("title")
	rec.subtitle = cell("subtitle")
	rec.summary = cell("summary")
	rec.creators = p.m.split(cell("creator"))
	rec.isbns = p.m.split(cell("isbn"))
	rec.subjects = p.m.split(cell("subject"))
	if lang := cell("language"); lang != "" {
		rec.lang = p.m.language(lang)
	}
	for key, name := range p.m.Extras {
		i, ok := col[name]
		if !ok || i >= len(row) {
			continue
		}
		if v := strings.TrimSpace(row[i]); v != "" {
			if rec.extras == nil {
				rec.extras = map[string]string{}
			}
			rec.extras[key] = v
		}
	}
	return rec
}
