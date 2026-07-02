// Package enrich executes enrichment sources against the deployment's grain
// store in one of two modes: direct (auto-approve -- assertions land in the
// source's enrichment:<name> graph) or queue (candidates become
// PIPELINE-provenance suggestions for moderation). The mode is a per-source
// deployment decision; the enrichers themselves are mode-blind.
package enrich

import (
	"context"
	"fmt"

	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/suggest"
	"github.com/freeeve/libcatalog/backend/vocab"
)

// Mode selects how a source's results land.
type Mode string

const (
	// ModeQueue routes candidates through moderation (the approval gate).
	ModeQueue Mode = "queue"
	// ModeDirect writes the source's enrichment graph outright
	// (auto-approve on import).
	ModeDirect Mode = "direct"
)

// Source pairs an enricher with its deployment mode.
type Source struct {
	Enricher ingest.Enricher
	Mode     Mode
	// Scheme keys the queued TermRefs (e.g. "lcsh"); queue mode only.
	Scheme string
}

// Service runs configured sources.
type Service struct {
	Blob        blob.Store
	GrainPrefix string
	Queue       *suggest.Service
	Sources     map[string]Source
}

// Result summarizes one run.
type Result struct {
	Source string `json:"source"`
	Mode   Mode   `json:"mode"`
	// Works is the number of Works enriched (direct) or with candidates
	// queued (queue).
	Works int `json:"works"`
}

// Run executes one configured source by name.
func (s *Service) Run(ctx context.Context, name string) (Result, error) {
	src, ok := s.Sources[name]
	if !ok {
		return Result{}, fmt.Errorf("enrich: unknown source %q", name)
	}
	switch src.Mode {
	case ModeDirect:
		n, err := ingest.RunEnrich(ctx, s.Blob, s.GrainPrefix, src.Enricher)
		return Result{Source: name, Mode: src.Mode, Works: n}, err
	case ModeQueue:
		n, err := s.runQueued(ctx, src)
		return Result{Source: name, Mode: src.Mode, Works: n}, err
	}
	return Result{}, fmt.Errorf("enrich: source %q has invalid mode %q", name, src.Mode)
}

// Names lists the configured sources.
func (s *Service) Names() []string {
	names := make([]string, 0, len(s.Sources))
	for name := range s.Sources {
		names = append(names, name)
	}
	return names
}

func (s *Service) runQueued(ctx context.Context, src Source) (int, error) {
	if s.Queue == nil {
		return 0, fmt.Errorf("enrich: queue mode needs the suggestion service")
	}
	summaries, _, err := ingest.ScanSummaries(ctx, s.Blob, s.GrainPrefix)
	if err != nil {
		return 0, err
	}
	results, err := src.Enricher.Enrich(ctx, summaries)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, res := range results {
		landed := false
		for _, subj := range res.Subjects {
			label := subj.URI
			for _, lang := range []string{"en", ""} {
				if v, ok := subj.Labels[lang]; ok {
					label = v
					break
				}
			}
			term := vocab.TermRef{Scheme: src.Scheme, ID: subj.URI, Label: label}
			err := s.Queue.PipelineSuggest(ctx, res.WorkID, term, res.Confidence)
			if err != nil {
				return queued, err
			}
			landed = true
		}
		if landed {
			queued++
		}
	}
	return queued, nil
}
