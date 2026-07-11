package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/iso2709"
	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/project"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// Run executes a QUEUED job (claiming it RUNNING first, so concurrent
// workers cannot double-run) and streams the output into the blob store:
// the emitters write per grain through a pipe into PutStream, so a
// full-corpus export's peak memory is the per-grain working set, not the
// output. An emit failure aborts the pipe before the store
// commits anything. Failures land in the job's Error with StatusFailed; Run
// itself errors only on store problems.
func (s *Service) Run(ctx context.Context, id string) error {
	job, err := s.claim(ctx, id)
	if errors.Is(err, errAlreadyClaimed) {
		return nil
	}
	if err != nil {
		return err
	}
	del := DeliveryFor(job.ID, job.Format)
	pr, pw := io.Pipe()
	var records int
	go func() {
		// The gzip writer sits inside the pipe, so the emitters still stream per
		// grain and the compressor's window is the only added memory.
		//
		// Close flushes the compressor and writes the trailer; without it the
		// object is a truncated gzip stream. On an aborted emit it is skipped and
		// the emit error goes to the pipe instead -- PutStream then fails and
		// stores nothing, so there is no object to have a trailer.
		gz := gzip.NewWriter(pw)
		n, err := s.emitTo(ctx, gz, *job)
		records = n
		if err == nil {
			err = gz.Close()
		}
		pw.CloseWithError(err)
	}()
	_, runErr := blob.PutStream(ctx, s.blob, del.Path, pr, blob.PutOptions{
		ContentType:     del.ContentType,
		ContentEncoding: del.ContentEncoding,
	})
	now := s.now().UTC()
	job.FinishedAt = now
	if runErr != nil {
		// A store-side failure must also unblock the emitter goroutine.
		pr.CloseWithError(runErr)
		job.Status = StatusFailed
		job.Error = runErr.Error()
		return s.put(ctx, job, store.CondIfVersion)
	}
	job.OutputPath = del.Path
	job.Status = StatusDone
	job.Records = records
	job.ExpiresAt = now.Add(downloadTTL)
	return s.put(ctx, job, store.CondIfVersion)
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

// emitTo renders the selection in the job's format onto w. Grains stream one
// at a time and each format emits as it goes, so nothing output-sized
// accumulates. Authority jobs render from the term index instead
// -- still buffered, bounded by the loaded vocabulary rather than the corpus.
func (s *Service) emitTo(ctx context.Context, w io.Writer, job Job) (int, error) {
	if job.Authorities != nil {
		out, n, err := s.emitAuthorities(ctx, job)
		if err != nil {
			return 0, err
		}
		_, err = w.Write(out)
		return n, err
	}
	paths, err := s.selectionPaths(ctx, job.Selection)
	if err != nil {
		return 0, err
	}
	if len(paths) == 0 {
		return 0, errors.New("selection matched no works")
	}
	switch job.Format {
	case FormatNQuads:
		return s.emitNQuads(ctx, w, paths)
	case FormatMARC:
		return s.emitMARC(ctx, w, paths)
	case FormatJSONLD:
		return s.emitJSONLD(ctx, w, paths)
	case FormatCSV:
		return s.emitCSV(ctx, w, paths)
	}
	return 0, fmt.Errorf("export: unknown format %q", job.Format)
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

// emitNQuads merges the selection corpus-style, the same way SerializeGrains
// does -- each grain's own canonical bytes, with its blank-node labels
// namespaced by grain id so they stay unique across the corpus (plain
// concatenation would collide them). The two writers must agree byte for byte;
// TestExportNQuadsMatchesSerialize holds them to it.
func (s *Service) emitNQuads(ctx context.Context, w io.Writer, paths []string) (int, error) {
	count := 0
	err := s.eachGrain(ctx, paths, func(grainPath string, grain []byte) error {
		// Parse to reject a grain the store accepted but no parser will, which is
		// the error contract this export has always had. The bytes we emit are the
		// grain's own, not a re-serialization: a grain is already canonical
		// N-Quads, and re-encoding it is a second chance to differ.
		if _, err := rdf.ParseNQuads(grain); err != nil {
			return err
		}
		out := bibframe.RelabelGrainBlanks(grain, bibframe.GrainBlankPrefix(grainPath))
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		if _, err := w.Write(out); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

// emitMARC round-trips each grain to MARC (lossiness measured in
// docs/marc-fidelity.md); the framework-aware decode honors editorial
// lcat:overrides shadows and re-attaches each record's lcat:marcVerbatim
// sidecar fields, so the crosswalk-lossy tags round-trip.
func (s *Service) emitMARC(ctx context.Context, w io.Writer, paths []string) (int, error) {
	mw := iso2709.NewWriter(w)
	count := 0
	err := s.eachGrain(ctx, paths, func(_ string, grain []byte) error {
		recs, err := bibframe.DecodeGrainMARCSource(grain, s.OrgCode)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if err := mw.Write(rec); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

// emitJSONLD renders a JSON array of per-record BIBFRAME JSON-LD documents
// (via the record path, so bounded by the same fidelity table as MARC),
// emitted per record in the json.MarshalIndent([], "", " ") shape.
func (s *Service) emitJSONLD(ctx context.Context, w io.Writer, paths []string) (int, error) {
	count := 0
	var buf bytes.Buffer
	err := s.eachGrain(ctx, paths, func(_ string, grain []byte) error {
		recs, err := bibframe.DecodeGrainMARCSource(grain, s.OrgCode)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			doc, err := codexbf.EncodeJSONLD(rec)
			if err != nil {
				return err
			}
			buf.Reset()
			if count == 0 {
				buf.WriteString("[\n ")
			} else {
				buf.WriteString(",\n ")
			}
			if err := json.Indent(&buf, doc, " ", " "); err != nil {
				return err
			}
			if _, err := w.Write(buf.Bytes()); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if count == 0 {
		_, err = w.Write([]byte("[]"))
		return 0, err
	}
	_, err = w.Write([]byte("\n]"))
	return count, err
}

// subjectLabel renders one controlled subject for the human-facing CSV
// . The grain's own skos:prefLabel wins; otherwise the loaded term
// index resolves the authority IRI, which is why a subject reads as a word
// whether or not its authority terms happen to have been appended to that
// particular grain. The IRI is the last resort, not the default: a term no
// index can resolve stays visible in the column rather than vanishing from
// the row, and a deployment with no index loaded exports exactly as before.
func (s *Service) subjectLabel(subj project.Subject) string {
	if l := vocab.PickLabel(subj.Labels); l != "" {
		return l
	}
	if s.Vocab != nil {
		if t, ok := s.Vocab.Resolve(subj.ID); ok {
			if l := vocab.PickLabel(t.Labels); l != "" {
				return l
			}
		}
	}
	return subj.ID
}

// workHoldings summarizes a Work's items across all its instances for the CSV
// : how many holdings it has, and the distinct call numbers,
// shelving locations and barcodes among them. Distinct rather than positional
// -- a spreadsheet sorts and pivots on these, and two copies on one shelf
// should read as one location, not two. Item notes are deliberately absent:
// they are free text, they would carry the "; " separator, and they are not a
// dimension anyone sorts by. The record remains the place to read them.
func workHoldings(w project.Work) (count int, callNumbers, locations, barcodes []string) {
	seenCall, seenLoc := map[string]bool{}, map[string]bool{}
	add := func(v string, seen map[string]bool, out *[]string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		*out = append(*out, v)
	}
	for _, inst := range w.Instances {
		for _, it := range inst.Items {
			count++
			add(it.CallNumber, seenCall, &callNumbers)
			add(it.Location, seenLoc, &locations)
			if it.Barcode != "" {
				barcodes = append(barcodes, it.Barcode)
			}
		}
	}
	return count, callNumbers, locations, barcodes
}

// emitCSV projects each grain with the real projector and writes one row per
// Work -- the Koha "export search results" analog over the projected shape.
// Projection is per grain: a grain carries all of its works'
// statements, so rows match the whole-corpus projection except that a
// subject label asserted only in a different work's grain no longer applies
// -- the old three-copies-in-RAM behavior priced that in.
func (s *Service) emitCSV(ctx context.Context, w io.Writer, paths []string) (int, error) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"id", "title", "subtitle", "contributors", "subjects", "tags", "languages",
		"formats", "classifications", "isbns",
		"itemCount", "callNumbers", "locations", "barcodes",
	})
	count := 0
	err := s.eachGrain(ctx, paths, func(_ string, grain []byte) error {
		catalog, err := project.Project(grain, s.Provider)
		if err != nil {
			return err
		}
		for _, work := range catalog.Works {
			contributors := make([]string, 0, len(work.Contributors))
			for _, c := range work.Contributors {
				contributors = append(contributors, c.Name)
			}
			subjects := make([]string, 0, len(work.Subjects))
			for _, subj := range work.Subjects {
				subjects = append(subjects, s.subjectLabel(subj))
			}
			classifications := make([]string, 0, len(work.Classifications))
			for _, cl := range work.Classifications {
				if cl.Label != "" {
					classifications = append(classifications, cl.Label)
				} else {
					classifications = append(classifications, cl.Value)
				}
			}
			var isbns []string
			for _, inst := range work.Instances {
				isbns = append(isbns, inst.ISBNs...)
			}
			itemCount, callNumbers, locations, barcodes := workHoldings(work)
			_ = cw.Write([]string{
				work.ID, work.Title, work.Subtitle,
				strings.Join(contributors, "; "), strings.Join(subjects, "; "),
				strings.Join(work.Tags, "; "), strings.Join(work.Languages, "; "),
				strings.Join(work.Formats, "; "), strings.Join(classifications, "; "),
				strings.Join(isbns, "; "),
				strconv.Itoa(itemCount), strings.Join(callNumbers, "; "),
				strings.Join(locations, "; "), strings.Join(barcodes, "; "),
			})
			count++
		}
		cw.Flush()
		return cw.Error()
	})
	if err != nil {
		return 0, err
	}
	cw.Flush()
	return count, cw.Error()
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
