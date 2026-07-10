package vocabsrc

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// Status is the download-job lifecycle (the tasks/038 export shape).
type Status string

const (
	StatusQueued  Status = "QUEUED"
	StatusRunning Status = "RUNNING"
	StatusDone    Status = "DONE"
	StatusFailed  Status = "FAILED"
)

// Job is one vocabulary download: fetch the source's SKOS dump, convert it to
// authority-tree N-Quads, install, and swap the index.
type Job struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Scheme     string    `json:"scheme"`
	Requester  string    `json:"requester"`
	Status     Status    `json:"status"`
	Terms      int       `json:"terms,omitempty"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

const jobTTL = 7 * 24 * time.Hour

func jobKey(id string) store.Key { return store.Key{PK: "JOB#VOCAB", SK: id} }

var errAlreadyClaimed = errors.New("vocabsrc: already claimed")

// CreateDownload queues a download job for a snapshot-capable source. The
// worker loop (RunQueued) picks it up; installing the same source again is
// the refresh path -- the snapshot is overwritten in place.
func (s *Service) CreateDownload(ctx context.Context, requester, sourceName string) (Job, error) {
	src, err := s.GetSource(ctx, sourceName)
	if err != nil {
		return Job{}, err
	}
	if !src.CanSnapshot() {
		return Job{}, fmt.Errorf("%w: source %q has no snapshot url", ErrValidation, sourceName)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return Job{}, err
	}
	job := Job{
		ID: hex.EncodeToString(suffix), Source: src.Name, Scheme: src.Scheme,
		Requester: requester, Status: StatusQueued, CreatedAt: s.clock().UTC(),
	}
	return job, s.putJob(ctx, &job, store.CondIfAbsent)
}

func (s *Service) putJob(ctx context.Context, job *Job, cond store.Cond) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	rec := store.Record{Key: jobKey(job.ID), Data: data, ExpireAt: s.clock().Add(jobTTL)}
	if cond == store.CondIfVersion {
		cur, err := s.DB.Get(ctx, rec.Key)
		if err != nil {
			return err
		}
		rec.Version = cur.Version
	}
	_, err = s.DB.Put(ctx, rec, cond)
	return err
}

// GetJob returns one job.
func (s *Service) GetJob(ctx context.Context, id string) (Job, error) {
	rec, err := s.DB.Get(ctx, jobKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	var job Job
	return job, json.Unmarshal(rec.Data, &job)
}

// Jobs lists the download jobs, newest first.
func (s *Service) Jobs(ctx context.Context) ([]Job, error) {
	out := []Job{}
	for rec, err := range s.DB.Query(ctx, "JOB#VOCAB", "", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var job Job
		if json.Unmarshal(rec.Data, &job) == nil {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// RunQueued drains QUEUED jobs once -- the worker-loop body.
func (s *Service) RunQueued(ctx context.Context) (int, error) {
	ran := 0
	for rec, err := range s.DB.Query(ctx, "JOB#VOCAB", "", store.QueryOpt{}) {
		if err != nil {
			return ran, err
		}
		var job Job
		if json.Unmarshal(rec.Data, &job) != nil || job.Status != StatusQueued {
			continue
		}
		if err := s.RunDownload(ctx, job.ID); err != nil {
			return ran, err
		}
		ran++
	}
	return ran, nil
}

// RunDownload executes a QUEUED job (claiming it RUNNING first, so concurrent
// workers cannot double-run): fetch, convert, install, reload. Failures land
// in the job's Error; RunDownload errors only on store problems.
func (s *Service) RunDownload(ctx context.Context, id string) error {
	job, err := s.claim(ctx, id)
	if errors.Is(err, errAlreadyClaimed) {
		return nil
	}
	if err != nil {
		return err
	}
	terms, runErr := s.install(ctx, *job)
	job.FinishedAt = s.clock().UTC()
	if runErr != nil {
		job.Status = StatusFailed
		job.Error = runErr.Error()
		if s.Logger != nil {
			s.Logger.Error("vocab download failed", "source", job.Source, "err", runErr)
		}
		return s.putJob(ctx, job, store.CondIfVersion)
	}
	job.Status = StatusDone
	job.Terms = terms
	if s.Logger != nil {
		s.Logger.Info("vocab snapshot installed", "source", job.Source, "terms", terms)
	}
	return s.putJob(ctx, job, store.CondIfVersion)
}

// claim flips QUEUED -> RUNNING under the job record's version.
func (s *Service) claim(ctx context.Context, id string) (*Job, error) {
	rec, err := s.DB.Get(ctx, jobKey(id))
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
	if _, err := s.DB.Put(ctx, rec, store.CondIfVersion); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return nil, errAlreadyClaimed
		}
		return nil, err
	}
	return &job, nil
}

// install fetches the source's dump, converts it, writes the snapshot and its
// sidecar, and swaps the index. Returns the concept count.
func (s *Service) install(ctx context.Context, job Job) (int, error) {
	src, err := s.GetSource(ctx, job.Source)
	if err != nil {
		return 0, err
	}
	if !src.CanSnapshot() {
		return 0, fmt.Errorf("%w: source %q lost its snapshot url", ErrValidation, src.Name)
	}
	body, err := s.fetchSnapshot(ctx, src.SnapshotURL)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	return s.installFrom(ctx, src, body, src.SnapshotURL)
}

// InstallUpload installs a hand-supplied dump for a registered source -- the
// escape hatch when the publisher's download URL is unreachable (or the
// source has none). Same converter, snapshot layout, and index swap as a
// download; the sidecar records "upload" as the provenance. Synchronous:
// the caller holds the bytes, no worker round-trip.
func (s *Service) InstallUpload(ctx context.Context, sourceName string, r io.Reader) (int, error) {
	src, err := s.GetSource(ctx, sourceName)
	if err != nil {
		return 0, err
	}
	return s.installFrom(ctx, src, r, "upload")
}

// errNoConcepts fails a conversion that parsed but yielded nothing usable,
// inside the pipe so the snapshot write never lands.
var errNoConcepts = errors.New("dump yielded no concepts -- not a SKOS N-Triples/N-Quads dump?")

// convertError marks an error as originating on the conversion side of the
// install pipe, so installFrom can tell a bad dump (validation) from a store
// write failure (500) after both travel through the same PutStream call.
type convertError struct{ err error }

func (e convertError) Error() string { return e.err.Error() }
func (e convertError) Unwrap() error { return e.err }

// installFrom converts a dump stream directly into the snapshot blob --
// peak memory is the converter's chunk, not the dump (tasks/110) -- then
// writes the sidecar and swaps the index; the shared back half of download
// and upload. A conversion failure (bad bytes, over-cap, zero concepts)
// aborts the pipe before the store commits anything, so a previously
// installed snapshot survives a failed refresh. Conversion failures are the
// dump's fault, so they surface as validation errors with the underlying
// reason rather than a generic 500.
func (s *Service) installFrom(ctx context.Context, src Source, r io.Reader, provenance string) (int, error) {
	pr, pw := io.Pipe()
	var terms int
	go func() {
		t, err := ConvertTo(pw, r, src.Scheme, int64(s.MaxSnapshotMB)<<20)
		if err == nil && t == 0 {
			err = errNoConcepts
		}
		if err != nil {
			err = convertError{err}
		}
		terms = t
		pw.CloseWithError(err)
	}()
	if _, err := blob.PutStream(ctx, s.Blob, s.snapshotPath(src.Name), pr, blob.PutOptions{ContentType: "application/n-quads"}); err != nil {
		// A store-side failure must also unblock the converter goroutine.
		pr.CloseWithError(err)
		var ce convertError
		if errors.As(err, &ce) {
			// Double-wrap: validation for the status code, the original
			// error kept typed so callers can tell an oversized body from
			// bad bytes.
			return 0, fmt.Errorf("%w: %s: %w", ErrValidation, provenance, ce.err)
		}
		return 0, err
	}
	info := InstallInfo{
		Source: src.Name, Scheme: src.Scheme, Terms: terms,
		InstalledAt: s.clock().UTC(), SnapshotURL: provenance,
	}
	meta, err := json.Marshal(info)
	if err != nil {
		return 0, err
	}
	if _, err := s.Blob.Put(ctx, s.metaPath(src.Name), meta, blob.PutOptions{ContentType: "application/json"}); err != nil {
		return 0, err
	}
	// Sidecar index artifacts (tasks/167): built per install so big schemes
	// serve range-fetched instead of as resident maps. A build failure keeps
	// the install -- the map path serves the scheme until the next rebuild.
	if m, err := vocab.BuildSidecar(ctx, s.Blob, s.prefix(), src.Scheme, s.snapshotPath(src.Name)); err != nil {
		slog.Warn("vocabsrc: sidecar index build failed; scheme serves from maps", "scheme", src.Scheme, "err", err)
	} else {
		slog.Info("vocabsrc: sidecar index built", "scheme", m.Scheme, "terms", m.Terms)
	}
	return terms, s.Reload(ctx)
}

const downloadTimeout = 15 * time.Minute

func (s *Service) fetchSnapshot(ctx context.Context, url string) (io.ReadCloser, error) {
	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", suggestUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vocabsrc: fetch %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("vocabsrc: fetch %s: status %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

// keepPredicates is the SKOS surface the vocab index reads (vocab.go) --
// everything else in a dump is dropped at conversion.
var keepPredicates = map[string]bool{
	"http://www.w3.org/2004/02/skos/core#prefLabel":  true,
	"http://www.w3.org/2004/02/skos/core#altLabel":   true,
	"http://www.w3.org/2004/02/skos/core#definition": true,
	"http://www.w3.org/2004/02/skos/core#broader":    true,
	"http://www.w3.org/2004/02/skos/core#narrower":   true,
	"http://www.w3.org/2004/02/skos/core#related":    true,
	"http://www.w3.org/2004/02/skos/core#exactMatch": true,
	"http://www.w3.org/2004/02/skos/core#closeMatch": true,
	"http://www.w3.org/2000/01/rdf-schema#label":     true,
}

const skosPrefLabel = "http://www.w3.org/2004/02/skos/core#prefLabel"

// Defensive ceilings on snapshot conversion (tasks/110). SnapshotURL is
// admin-set, so these bound mistakes and hostile endpoints rather than
// gatekeep: the size cap counts decompressed bytes (a gzip bomb hits it as
// it expands, and download and upload paths pass through here alike), the
// line cap catches a response that is not line-delimited RDF before its
// "line" grows without bound.
const (
	defaultMaxSnapshotMB = 4096
	maxDumpLine          = 4 << 20
)

// errDumpTooLarge and errLineTooLong classify the cap failures so callers
// can tell an oversized dump from bad bytes.
var (
	errDumpTooLarge = errors.New("dump exceeds the snapshot size cap")
	errLineTooLong  = errors.New("no newline within the line cap -- not line-delimited N-Triples/N-Quads?")
)

// Convert buffers ConvertTo -- for callers that want the converted bytes in
// hand; the install paths stream through ConvertTo into the blob store.
func Convert(r io.Reader, scheme string) ([]byte, int, error) {
	var out bytes.Buffer
	terms, err := ConvertTo(&out, r, scheme, 0)
	if err != nil {
		return nil, 0, err
	}
	return out.Bytes(), terms, nil
}

// ConvertTo streams a SKOS N-Triples/N-Quads dump (gzipped or plain) into
// authority-tree N-Quads under the authority:<scheme> graph, keeping only the
// predicates the index reads. Lines are independent in N-Quads, so the input
// parses and emits in bounded chunks -- peak memory is the chunk plus the
// concept-count set, not the dump (tasks/110). Common wrong-format uploads (zip
// archives, XML exports) are named outright -- publishers like OCLC FAST
// distribute both. maxBytes caps the decompressed input (0 = the 4GB default).
// Returns the distinct prefLabel-bearing concept count.
//
// A malformed line refuses the whole dump, naming the line (tasks/317). It used to
// be skipped, and that was not a decision -- it was whatever libcodex's parser did.
// The dumps that trip it are the ones you most want refused: a 5,242,880-byte
// homosaurus-v4.nt, cut mid-IRI at exactly 5MiB, converts cleanly under a lenient
// parser and installs a vocabulary silently missing every concept after the cut.
// The subject pages it labels are then wrong, and nothing anywhere says so. Five
// real LC/Homosaurus/FAST dumps were parsed strictly to check this; the only one
// that failed was that truncated download.
func ConvertTo(w io.Writer, r io.Reader, scheme string, maxBytes int64) (int, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxSnapshotMB << 20
	}
	br := bufio.NewReaderSize(r, 1<<20)
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return 0, fmt.Errorf("gunzip: %w", err)
		}
		defer gz.Close()
		br = bufio.NewReaderSize(gz, 1<<20)
	}
	if magic, err := br.Peek(64); err == nil || len(magic) > 4 {
		head := bytes.TrimLeft(magic, " \t\r\n")
		switch {
		case bytes.HasPrefix(magic, []byte("PK\x03\x04")):
			return 0, fmt.Errorf("this is a zip archive -- extract the .nt/.nq file and upload it (plain or gzipped)")
		// N-Triples subjects start "<http…", so only unmistakable XML
		// openings count.
		case bytes.HasPrefix(head, []byte("<?xml")), bytes.HasPrefix(head, []byte("<!DOCTYPE")), bytes.HasPrefix(head, []byte("<rdf")):
			return 0, fmt.Errorf("this looks like XML (MARCXML/RDF-XML?) -- the converter reads N-Triples/N-Quads only")
		}
	}
	graph := bibframe.AuthorityGraph(scheme)
	var enc rdf.Encoder
	var out []byte
	var consumed int64
	concepts := map[string]bool{}
	chunk := make([]byte, 0, 1<<20)
	// Lines fully parsed before the current chunk. A SyntaxError's Line is relative
	// to the bytes handed to the parser, and the parser sees one chunk at a time, so
	// without this an operator is sent to line 4,312 of a five-million-line dump.
	lineBase := 0
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		ds, err := rdf.ParseNQuads(chunk)
		if err != nil {
			var se *rdf.SyntaxError
			if errors.As(err, &se) {
				return fmt.Errorf("line %d is not a valid N-Triples/N-Quads statement (%q) -- the dump is truncated or corrupt; a partial download is the usual cause", lineBase+se.Line, se.Text)
			}
			return err
		}
		lineBase += bytes.Count(chunk, []byte("\n"))
		out = out[:0]
		for _, q := range ds.Quads {
			if !q.S.IsIRI() || !keepPredicates[q.P.Value] {
				continue
			}
			if q.P.Value == skosPrefLabel {
				concepts[q.S.Value] = true
			}
			out = enc.AppendQuad(out, rdf.Quad{S: q.S, P: q.P, O: q.O, G: graph})
		}
		chunk = chunk[:0]
		if len(out) == 0 {
			return nil
		}
		_, err = w.Write(out)
		return err
	}
	for {
		// ReadSlice (not ReadBytes) so a delimiter-less body accumulates in
		// buffer-sized pieces under our caps instead of growing one
		// unbounded line inside bufio.
		line, err := br.ReadSlice('\n')
		chunk = append(chunk, line...)
		if errors.Is(err, bufio.ErrBufferFull) {
			err = nil
		}
		if consumed += int64(len(line)); consumed > maxBytes {
			return 0, fmt.Errorf("%w (%d MB)", errDumpTooLarge, maxBytes>>20)
		}
		if len(chunk) >= 1<<20 || (err != nil && len(chunk) > 0) {
			if !bytes.HasSuffix(chunk, []byte("\n")) && err == nil {
				if len(chunk) > maxDumpLine {
					return 0, errLineTooLong
				}
				// A line longer than the chunk: keep appending until its end.
				continue
			}
			if ferr := flush(); ferr != nil {
				return 0, ferr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	return len(concepts), nil
}
