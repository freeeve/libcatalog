package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/iso2709"
	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/project"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/store"
)

// Run executes a QUEUED job (claiming it RUNNING first, so concurrent
// workers cannot double-run) and stores the output. Failures land in the
// job's Error with StatusFailed; Run itself errors only on store problems.
func (s *Service) Run(ctx context.Context, id string) error {
	job, err := s.claim(ctx, id)
	if errors.Is(err, errAlreadyClaimed) {
		return nil
	}
	if err != nil {
		return err
	}
	output, records, runErr := s.emit(ctx, *job)
	now := s.now().UTC()
	job.FinishedAt = now
	if runErr != nil {
		job.Status = StatusFailed
		job.Error = runErr.Error()
		return s.put(ctx, job, store.CondIfVersion)
	}
	job.OutputPath = fmt.Sprintf("exports/%s.%s", job.ID, extensions[job.Format])
	if _, err := s.blob.Put(ctx, job.OutputPath, output, blob.PutOptions{ContentType: contentTypes[job.Format]}); err != nil {
		job.Status = StatusFailed
		job.Error = err.Error()
		return s.put(ctx, job, store.CondIfVersion)
	}
	job.Status = StatusDone
	job.Records = records
	job.ExpiresAt = now.Add(downloadTTL)
	return s.put(ctx, job, store.CondIfVersion)
}

var contentTypes = map[Format]string{
	FormatMARC: "application/marc", FormatNQuads: "application/n-quads",
	FormatJSONLD: "application/ld+json", FormatCSV: "text/csv",
}

var errAlreadyClaimed = errors.New("export: already claimed")

// claim flips QUEUED -> RUNNING under the job record's version.
func (s *Service) claim(ctx context.Context, id string) (*Job, error) {
	rec, err := s.db.Get(ctx, jobKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(rec.Data, &job); err != nil {
		return nil, err
	}
	if job.Status != StatusQueued {
		return nil, errAlreadyClaimed
	}
	job.Status = StatusRunning
	data, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	rec.Data = data
	if _, err := s.db.Put(ctx, rec, store.CondIfVersion); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return nil, errAlreadyClaimed
		}
		return nil, err
	}
	return &job, nil
}

// selectionPaths resolves the job's selection to grain paths in Work-id
// order -- the same order SerializeGrains uses, so a full-selection N-Quads
// export is byte-identical to the corpus catalog.nq.
func (s *Service) selectionPaths(ctx context.Context, sel Selection) ([]string, error) {
	byID := map[string]string{}
	if !sel.All {
		for _, id := range sel.WorkIDs {
			byID[id] = s.GrainPrefix + bibframe.GrainPath(id)
		}
	} else {
		for entry, err := range s.blob.List(ctx, s.GrainPrefix+"data/works/") {
			if err != nil {
				return nil, err
			}
			base := path.Base(entry.Path)
			if strings.HasSuffix(base, ".nq") && base != "catalog.nq" {
				byID[strings.TrimSuffix(base, ".nq")] = entry.Path
			}
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	paths := make([]string, len(ids))
	for i, id := range ids {
		paths[i] = byID[id]
	}
	return paths, nil
}

// emit renders the selection in the job's format. Grains stream one at a
// time; only the encoded output accumulates.
func (s *Service) emit(ctx context.Context, job Job) ([]byte, int, error) {
	paths, err := s.selectionPaths(ctx, job.Selection)
	if err != nil {
		return nil, 0, err
	}
	if len(paths) == 0 {
		return nil, 0, errors.New("selection matched no works")
	}
	switch job.Format {
	case FormatNQuads:
		return s.emitNQuads(ctx, paths)
	case FormatMARC:
		return s.emitMARC(ctx, paths)
	case FormatJSONLD:
		return s.emitJSONLD(ctx, paths)
	case FormatCSV:
		return s.emitCSV(ctx, paths)
	}
	return nil, 0, fmt.Errorf("export: unknown format %q", job.Format)
}

// eachGrain streams the selected grains.
func (s *Service) eachGrain(ctx context.Context, paths []string, fn func(path string, grain []byte) error) error {
	for _, path := range paths {
		grain, _, err := s.blob.Get(ctx, path)
		if errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("no grain at %s", path)
		}
		if err != nil {
			return err
		}
		if err := fn(path, grain); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// emitNQuads merges the selection corpus-style: one shared encoder so
// blank-node labels stay unique across grains (the SerializeGrains
// discipline -- plain concatenation would collide labels).
func (s *Service) emitNQuads(ctx context.Context, paths []string) ([]byte, int, error) {
	var out bytes.Buffer
	var enc rdf.Encoder
	count := 0
	err := s.eachGrain(ctx, paths, func(_ string, grain []byte) error {
		ds, err := rdf.ParseNQuads(grain)
		if err != nil {
			return err
		}
		graphs := ds.Graphs()
		sort.Slice(graphs, func(i, j int) bool { return graphs[i].Value < graphs[j].Value })
		for _, gt := range graphs {
			out.Write(enc.AppendNQuads(nil, ds.Graph(gt), gt))
		}
		count++
		return nil
	})
	return out.Bytes(), count, err
}

// emitMARC round-trips each grain to MARC (lossiness measured in
// docs/marc-fidelity.md); the framework-aware decode honors editorial
// lcat:overrides shadows and re-attaches each record's lcat:marcVerbatim
// sidecar fields, so the crosswalk-lossy tags round-trip (tasks/049).
func (s *Service) emitMARC(ctx context.Context, paths []string) ([]byte, int, error) {
	var out bytes.Buffer
	w := iso2709.NewWriter(&out)
	count := 0
	err := s.eachGrain(ctx, paths, func(_ string, grain []byte) error {
		recs, err := bibframe.DecodeGrainMARC(grain)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if err := w.Write(rec); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return out.Bytes(), count, err
}

// emitJSONLD renders a JSON array of per-record BIBFRAME JSON-LD documents
// (via the record path, so bounded by the same fidelity table as MARC).
func (s *Service) emitJSONLD(ctx context.Context, paths []string) ([]byte, int, error) {
	docs := []json.RawMessage{}
	err := s.eachGrain(ctx, paths, func(_ string, grain []byte) error {
		recs, err := bibframe.DecodeGrainMARC(grain)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			doc, err := codexbf.EncodeJSONLD(rec)
			if err != nil {
				return err
			}
			docs = append(docs, doc)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out, err := json.MarshalIndent(docs, "", " ")
	return out, len(docs), err
}

// emitCSV serializes the selection in memory, projects it with the real
// projector, and writes one row per Work -- the Koha "export search results"
// analog over the projected shape.
func (s *Service) emitCSV(ctx context.Context, paths []string) ([]byte, int, error) {
	var merged bytes.Buffer
	var enc rdf.Encoder
	err := s.eachGrain(ctx, paths, func(_ string, grain []byte) error {
		ds, err := rdf.ParseNQuads(grain)
		if err != nil {
			return err
		}
		graphs := ds.Graphs()
		sort.Slice(graphs, func(i, j int) bool { return graphs[i].Value < graphs[j].Value })
		for _, gt := range graphs {
			merged.Write(enc.AppendNQuads(nil, ds.Graph(gt), gt))
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	catalog, err := project.Project(merged.Bytes(), s.Provider)
	if err != nil {
		return nil, 0, err
	}
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	_ = w.Write([]string{"id", "title", "subtitle", "contributors", "subjects", "tags", "languages", "formats", "classifications", "isbns"})
	for _, work := range catalog.Works {
		contributors := make([]string, 0, len(work.Contributors))
		for _, c := range work.Contributors {
			contributors = append(contributors, c.Name)
		}
		subjects := make([]string, 0, len(work.Subjects))
		for _, subj := range work.Subjects {
			label := subj.ID
			for _, l := range []string{"en", ""} {
				if v, ok := subj.Labels[l]; ok {
					label = v
					break
				}
			}
			subjects = append(subjects, label)
		}
		var isbns []string
		for _, inst := range work.Instances {
			isbns = append(isbns, inst.ISBNs...)
		}
		_ = w.Write([]string{
			work.ID, work.Title, work.Subtitle,
			strings.Join(contributors, "; "), strings.Join(subjects, "; "),
			strings.Join(work.Tags, "; "), strings.Join(work.Languages, "; "),
			strings.Join(work.Formats, "; "), strings.Join(work.Classifications, "; "),
			strings.Join(isbns, "; "),
		})
	}
	w.Flush()
	return out.Bytes(), len(catalog.Works), w.Error()
}

// RunQueued drains QUEUED jobs once -- the worker-loop body for container
// deployments (lcatd runs it on a ticker; a Lambda deployment invokes it
// asynchronously).
func (s *Service) RunQueued(ctx context.Context) (int, error) {
	ran := 0
	for rec, err := range s.db.Query(ctx, "JOB#EXPORT", "", store.QueryOpt{}) {
		if err != nil {
			return ran, err
		}
		var job Job
		if json.Unmarshal(rec.Data, &job) != nil || job.Status != StatusQueued {
			continue
		}
		if err := s.Run(ctx, job.ID); err != nil {
			return ran, err
		}
		ran++
	}
	return ran, nil
}
