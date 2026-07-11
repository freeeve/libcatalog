// Package export runs catalog export jobs: a selected subset (or all) of the
// Works emitted as MARC, N-Quads, JSON-LD, or CSV, written to the blob store,
// and handed back as a time-limited download link (presigned when the store
// signs URLs, an HMAC-token route otherwise). Jobs are records in the
// document store; small selections run in-request, larger ones queue for the
// worker loop.
package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// Format selects the output serialization.
type Format string

const (
	FormatMARC   Format = "marc"   // ISO 2709 via the libcodex round-trip (lossy per docs/marc-fidelity.md)
	FormatNQuads Format = "nquads" // corpus-style merged canonical N-Quads
	FormatJSONLD Format = "jsonld" // JSON-LD array via the record path (MARC-fidelity-bounded)
	FormatCSV    Format = "csv"    // projected rows (id, title, contributors, subjects, ...)
)

// Extensions per format.
var extensions = map[Format]string{
	FormatMARC: "mrc", FormatNQuads: "nq", FormatJSONLD: "jsonld", FormatCSV: "csv",
}

// Content types per format, naming what the bytes are once decompressed.
var contentTypes = map[Format]string{
	FormatMARC: "application/marc", FormatNQuads: "application/n-quads",
	FormatJSONLD: "application/ld+json", FormatCSV: "text/csv",
}

// Every export is stored gzipped: a full-corpus N-Quads dump
// compresses ~20x, and the store, the wire and the librarian's disk each pay for
// the difference. Formats differ only in whether the compression is visible.
//
// CSV is the human-facing format -- it is opened in Excel and OpenRefine -- so it
// is delivered transparently: Content-Type text/csv, Content-Encoding gzip, and
// the browser saves an ordinary .csv. The machine formats are delivered as real
// .gz artifacts, because a 2GB N-Quads dump should stay 100MB after it lands.
//
// This distinction is metadata on the stored object, not a serving-time decision,
// because DownloadURL hands a presigned URL straight to the browser when the blob
// store signs: nothing of ours runs on that path.
func transparentGzip(f Format) bool { return f == FormatCSV }

// Delivery describes how a format's stored object is named and labelled.
type Delivery struct {
	Path            string // blob path, and the download's filename
	ContentType     string
	ContentEncoding string // "gzip" when the compression is transparent
}

// DeliveryFor is the single source of truth for an export's stored shape. Run
// writes it; the download handler reads it back off the job's OutputPath.
func DeliveryFor(id string, f Format) Delivery {
	if transparentGzip(f) {
		return Delivery{
			Path:            fmt.Sprintf("exports/%s.%s", id, extensions[f]),
			ContentType:     contentTypes[f],
			ContentEncoding: "gzip",
		}
	}
	return Delivery{
		Path:        fmt.Sprintf("exports/%s.%s.gz", id, extensions[f]),
		ContentType: "application/gzip",
	}
}

// Selection scopes an export: everything, or an explicit id list. Richer
// selections (saved queries, search results) compile down to id lists by the
// batch machinery.
type Selection struct {
	All     bool     `json:"all,omitempty"`
	WorkIDs []string `json:"workIds,omitempty"`
}

// Status is the job lifecycle.
type Status string

const (
	StatusQueued  Status = "QUEUED"
	StatusRunning Status = "RUNNING"
	StatusDone    Status = "DONE"
	StatusFailed  Status = "FAILED"
)

// Job is one export request.
type Job struct {
	ID        string    `json:"id"`
	Requester string    `json:"requester"`
	Format    Format    `json:"format"`
	Selection Selection `json:"selection"`
	// Authorities, when set, makes this an authority export:
	// the format renders terms instead of work grains.
	Authorities *AuthoritySelection `json:"authorities,omitempty"`
	Status      Status              `json:"status"`
	Records     int                 `json:"records,omitempty"`
	OutputPath  string              `json:"outputPath,omitempty"`
	Error       string              `json:"error,omitempty"`
	CreatedAt   time.Time           `json:"createdAt"`
	FinishedAt  time.Time           `json:"finishedAt,omitzero"`
	// ExpiresAt bounds the download's availability (bucket lifecycle rules
	// enforce the object side).
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

// ErrNotFound reports an unknown job (or one the requester may not see).
var ErrNotFound = errors.New("export: job not found")

const (
	// InRequestCutoff is the largest explicit selection run synchronously.
	InRequestCutoff = 200
	// downloadTTL bounds link and object availability.
	downloadTTL = 24 * time.Hour
	jobTTL      = 7 * 24 * time.Hour
)

// Service manages export jobs.
type Service struct {
	db   store.Store
	blob blob.Store
	// GrainPrefix roots the grain tree ("data/works/" under repo layout is
	// implied by bibframe.GrainPath; empty prefix = paths used as-is).
	GrainPrefix string
	// Provider names the feed graph the CSV projection reads.
	Provider string
	// Vocab, when set, enables authority exports over the loaded term index
	//.
	Vocab *vocab.Index
	// OrgCode is the deployment's MARC organization code; when set, MARC and
	// JSON-LD exports derive each record's 040 from graph facts at decode
	// time.
	OrgCode string
	// tokenSecret signs fallback download tokens.
	tokenSecret []byte
	now         func() time.Time
}

// New wires the service. secret signs fallback download tokens (>=16 bytes).
func New(db store.Store, bs blob.Store, provider string, secret []byte) (*Service, error) {
	if len(secret) < 16 {
		return nil, errors.New("export: token secret too short")
	}
	return &Service{db: db, blob: bs, Provider: provider, tokenSecret: secret, now: time.Now}, nil
}

func jobKey(id string) store.Key { return store.Key{PK: "JOB#EXPORT", SK: id} }
func userIdxKey(requester, ts, id string) store.Key {
	return store.Key{PK: "EXPORTIDX#" + requester, SK: ts + "#" + id}
}

// Create records a new job. Explicit selections at or under InRequestCutoff
// run synchronously before returning; everything else stays QUEUED for the
// worker.
func (s *Service) Create(ctx context.Context, requester string, format Format, sel Selection) (Job, error) {
	if _, ok := extensions[format]; !ok {
		return Job{}, fmt.Errorf("export: unknown format %q", format)
	}
	if !sel.All && len(sel.WorkIDs) == 0 {
		return Job{}, errors.New("export: empty selection")
	}
	if sel.All && len(sel.WorkIDs) > 0 {
		return Job{}, errors.New("export: all and workIds are mutually exclusive")
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return Job{}, err
	}
	now := s.now().UTC()
	job := Job{
		ID: hex.EncodeToString(suffix), Requester: requester,
		Format: format, Selection: sel, Status: StatusQueued, CreatedAt: now,
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
	if !sel.All && len(sel.WorkIDs) <= InRequestCutoff {
		if err := s.Run(ctx, job.ID); err != nil {
			return Job{}, err
		}
		return s.Get(ctx, requester, job.ID, true)
	}
	return job, nil
}

func (s *Service) put(ctx context.Context, job *Job, cond store.Cond) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	rec := store.Record{Key: jobKey(job.ID), Data: data, ExpireAt: s.now().Add(jobTTL)}
	if cond == store.CondIfVersion {
		// Caller must have loaded the version; re-read for simplicity.
		cur, err := s.db.Get(ctx, rec.Key)
		if err != nil {
			return err
		}
		rec.Version = cur.Version
	}
	_, err = s.db.Put(ctx, rec, cond)
	return err
}

// Get returns a job; non-admin callers see only their own.
func (s *Service) Get(ctx context.Context, requester, id string, admin bool) (Job, error) {
	rec, err := s.db.Get(ctx, jobKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err := json.Unmarshal(rec.Data, &job); err != nil {
		return Job{}, err
	}
	if !admin && job.Requester != requester {
		return Job{}, ErrNotFound
	}
	return job, nil
}

// List returns the requester's jobs, newest first.
func (s *Service) List(ctx context.Context, requester string) ([]Job, error) {
	jobs := []Job{}
	for rec, err := range s.db.Query(ctx, "EXPORTIDX#"+requester, "", store.QueryOpt{Descending: true, Limit: 100}) {
		if err != nil {
			return nil, err
		}
		job, err := s.Get(ctx, requester, string(rec.Data), false)
		if err != nil {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// DownloadURL returns a time-limited link for a DONE job: presigned when the
// blob store implements blob.Signer, otherwise the token-authenticated API
// route.
func (s *Service) DownloadURL(ctx context.Context, job Job) (string, error) {
	if job.Status != StatusDone {
		return "", fmt.Errorf("export: job %s is %s", job.ID, job.Status)
	}
	if s.now().After(job.ExpiresAt) {
		return "", fmt.Errorf("export: job %s expired", job.ID)
	}
	if signer, ok := s.blob.(blob.Signer); ok {
		ttl := min(time.Until(job.ExpiresAt), downloadTTL)
		return signer.SignedGetURL(ctx, job.OutputPath, ttl)
	}
	return fmt.Sprintf("/v1/exports/%s/download?token=%s", job.ID, s.Token(job)), nil
}

// Token signs the fallback download token for a job.
func (s *Service) Token(job Job) string {
	mac := hmac.New(sha256.New, s.tokenSecret)
	fmt.Fprintf(mac, "%s:%d", job.ID, job.ExpiresAt.Unix())
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyToken checks a fallback token against a job and its expiry.
func (s *Service) VerifyToken(job Job, token string) bool {
	if s.now().After(job.ExpiresAt) {
		return false
	}
	return hmac.Equal([]byte(s.Token(job)), []byte(token))
}

// Open returns a DONE job's output decompressed, whatever form it is stored in.
// Jobs written hold plain bytes and still read back correctly:
// the gzip magic number decides, not the path.
func (s *Service) Open(ctx context.Context, job Job) ([]byte, error) {
	data, gzipped, err := s.OpenStored(ctx, job)
	if err != nil || !gzipped {
		return data, err
	}
	return Gunzip(data)
}

// OpenStored returns a DONE job's output exactly as stored, reporting whether
// those bytes are a gzip stream. The download handler needs the compressed form
// so it can hand it to a client that accepts it without a decompress/recompress
// round trip (the fallback download route's read side).
func (s *Service) OpenStored(ctx context.Context, job Job) (data []byte, gzipped bool, err error) {
	data, _, err = s.blob.Get(ctx, job.OutputPath)
	if err != nil {
		return nil, false, err
	}
	return data, isGzip(data), nil
}

// isGzip sniffs the RFC 1952 magic number.
func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// Gunzip decompresses a gzip stream held in memory. Exported for the download
// handler, which must undo the at-rest compression for a client that refuses it.
func Gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
