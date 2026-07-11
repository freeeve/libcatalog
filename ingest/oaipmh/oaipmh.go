// Package oaipmh is the OAI-PMH bibliographic harvest provider (feature
// ils-import): it pulls MARC records from an ILS's OAI-PMH endpoint and
// yields them through the same crosswalk + clustering pipeline as file MARC ingest,
// so onboarding an ILS that speaks OAI-PMH (Koha, Evergreen, and many others) is a
// Register call plus config, no libcat fork.
//
// It issues ListRecords over the metadata prefix (default marc21), follows
// resumptionToken pagination to the end, and supports the incremental window
// (from/until) and set selectors. Each record's MARCXML metadata is decoded to a
// codex.Record and crosswalked by the shared marc.FromCodexRecords path. Records
// the endpoint marks deleted are skipped -- a full harvest's withdrawal
// reconciliation removes anything no longer present; explicit
// incremental-deletion signalling is a follow-up.
package oaipmh

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/marcxml"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/marc"
)

// ProviderName is the registry key and default provenance feed graph. A deployment
// onboarding a specific ILS overrides the feed to name it (e.g. feed:koha).
const ProviderName = "oai"

// defaultPrefix is the OAI metadataPrefix libcat harvests -- MARC21 (slim) XML,
// which the libcodex marcxml reader decodes to a codex.Record.
const defaultPrefix = "marc21"

// maxPages bounds resumptionToken following, so a server that loops a token can
// never spin the harvest forever.
const maxPages = 1_000_000

// maxRetries is how many extra times a page fetch is attempted after a transient
// failure (a connection reset, timeout, or 5xx) before giving up. Sustained OAI
// harvests over thousands of records routinely hit a recycled worker or a brief
// network blip mid-run, and a whole harvest should not be lost to one; backoff
// starts at retryBase and doubles.
const (
	maxRetries = 5
	retryBase  = 500 * time.Millisecond
)

// Doer is the HTTP surface the provider needs; http.Client satisfies it, and a
// test injects a stub.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Provider harvests one OAI-PMH repository.
type Provider struct {
	feed   string
	base   string
	prefix string
	set    string
	from   string
	until  string
	client Doer
}

// New is the ingest.Factory for OAI-PMH. Config.Source is the OAI base URL;
// Config.Feed names the provenance graph (default "oai"). Params: "prefix"
// (metadataPrefix, default marc21), "set", "from", "until" (the incremental
// window, OAI datestamps).
func New(cfg ingest.Config) (ingest.Provider, error) {
	if cfg.Source == "" {
		return nil, fmt.Errorf("oaipmh: OAI base URL (Config.Source) is required")
	}
	feed := cfg.Feed
	if feed == "" {
		feed = ProviderName
	}
	prefix := cfg.Params["prefix"]
	if prefix == "" {
		prefix = defaultPrefix
	}
	return &Provider{
		feed:   feed,
		base:   cfg.Source,
		prefix: prefix,
		set:    cfg.Params["set"],
		from:   cfg.Params["from"],
		until:  cfg.Params["until"],
		client: http.DefaultClient,
	}, nil
}

// SetClient overrides the HTTP client (tests, or a client with custom timeouts).
func (p *Provider) SetClient(d Doer) { p.client = d }

// Name returns the provenance feed graph name.
func (p *Provider) Name() string { return p.feed }

// Role reports OAI-PMH as a bibliographic ingest source.
func (p *Provider) Role() ingest.Role { return ingest.RoleIngest }

// oaiResponse is the subset of the OAI-PMH envelope the harvest reads.
type oaiResponse struct {
	Error *struct {
		Code string `xml:"code,attr"`
		Msg  string `xml:",chardata"`
	} `xml:"error"`
	ListRecords struct {
		Records         []oaiRecord `xml:"record"`
		ResumptionToken string      `xml:"resumptionToken"`
	} `xml:"ListRecords"`
}

// oaiRecord is one harvested record: a header (which may flag a deletion) and the
// MARCXML metadata payload.
type oaiRecord struct {
	Header struct {
		Status     string `xml:"status,attr"`
		Identifier string `xml:"identifier"`
	} `xml:"header"`
	Metadata struct {
		Inner string `xml:",innerxml"`
	} `xml:"metadata"`
}

// Records harvests every page of the repository (following resumptionToken),
// decodes each non-deleted record's MARCXML, and crosswalks the lot through the
// shared MARC path. Honours ctx for cancellation between and within page fetches.
func (p *Provider) Records(ctx context.Context) ([]ingest.Record, error) {
	var recs []*codex.Record
	token := ""
	for page := 0; page < maxPages; page++ {
		resp, err := p.fetch(ctx, token)
		if err != nil {
			return nil, err
		}
		if resp.Error != nil {
			// noRecordsMatch is an empty result, not a failure (an incremental
			// window with nothing new, or an empty set).
			if resp.Error.Code == "noRecordsMatch" {
				return nil, nil
			}
			return nil, fmt.Errorf("oaipmh: %s: %s", resp.Error.Code, resp.Error.Msg)
		}
		for i := range resp.ListRecords.Records {
			r := &resp.ListRecords.Records[i]
			if r.Header.Status == "deleted" {
				continue
			}
			rec, err := marcxml.Decode([]byte(r.Metadata.Inner))
			if err != nil {
				return nil, fmt.Errorf("oaipmh: record %s: %w", r.Header.Identifier, err)
			}
			recs = append(recs, rec)
		}
		token = resp.ListRecords.ResumptionToken
		if token == "" {
			break
		}
	}
	return marc.FromCodexRecords(recs), nil
}

// fetch issues one page request, retrying a transient failure (a connection reset,
// timeout, or 5xx) with exponential backoff so a long harvest survives a recycled
// server worker or a brief blip. Honours ctx between attempts.
func (p *Provider) fetch(ctx context.Context, token string) (*oaiResponse, error) {
	var err error
	var resp *oaiResponse
	backoff := retryBase
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		resp, err = p.fetchOnce(ctx, token)
		if err == nil || ctx.Err() != nil {
			return resp, err
		}
	}
	return nil, fmt.Errorf("oaipmh: after %d retries: %w", maxRetries, err)
}

// fetchOnce issues one ListRecords request and decodes the envelope. The first page
// carries the selectors; subsequent pages carry only the resumptionToken (the OAI
// spec forbids repeating the selectors alongside a token).
func (p *Provider) fetchOnce(ctx context.Context, token string) (*oaiResponse, error) {
	q := url.Values{"verb": {"ListRecords"}}
	if token != "" {
		q.Set("resumptionToken", token)
	} else {
		q.Set("metadataPrefix", p.prefix)
		if p.set != "" {
			q.Set("set", p.set)
		}
		if p.from != "" {
			q.Set("from", p.from)
		}
		if p.until != "" {
			q.Set("until", p.until)
		}
	}
	sep := "?"
	if strings.Contains(p.base, "?") {
		sep = "&"
	}
	u := p.base + sep + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oaipmh: %s -> HTTP %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out oaiResponse
	if err := xml.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("oaipmh: parsing response: %w", err)
	}
	return &out, nil
}
