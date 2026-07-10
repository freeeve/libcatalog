package bibframe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/freeeve/libcat/storage"
	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/iso2709"
)

// BuildStats reports what a corpus build produced.
type BuildStats struct {
	Records int // records read from the source
	Grains  int // per-Work grain files written
}

// WorkID returns a stable, filesystem-safe id for a record's grain, taken from
// the control number (MARC 001) and falling back to a hash of the record when
// absent. A record with no 001 whose encoding fails is an error: hashing the
// nil fallback would give every such record the same id, silently overwriting
// grains (tasks/115). Phase 0 only: ARCHITECTURE §4's identity model replaces
// this with a minted, provider-independent id in identity/ (Phase 1), which
// also changes the grain's subject IRIs and filename.
func WorkID(rec *codex.Record) (string, error) {
	if id := strings.TrimSpace(rec.ControlField("001")); id != "" {
		return sanitize(id), nil
	}
	b, err := iso2709.Encode(rec)
	if err != nil {
		return "", fmt.Errorf("bibframe: no 001 and record not encodable for the hash fallback: %w", err)
	}
	return "x" + hashID(b)[:16], nil
}

// GrainPath returns the sink-relative path for a work id:
// data/works/<xx>/<id>.nq, sharded by a hash prefix so no directory holds an
// unbounded number of files (ARCHITECTURE §3). Paths use forward slashes; the
// Sink maps them onto its backend.
func GrainPath(id string) string {
	shard := hashID([]byte(id))[:2]
	return path.Join("data", "works", shard, id+".nq")
}

// ReadMARC reads every record from an ISO 2709 (.mrc) stream.
func ReadMARC(r io.Reader) ([]*codex.Record, error) {
	rd := iso2709.NewReader(r)
	var recs []*codex.Record
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			return recs, nil
		}
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
}

// BuildMARC reads an ISO 2709 MARC stream -- e.g. an OverDrive Marketplace MARC
// Express export -- and builds the corpus into sink.
func BuildMARC(sink storage.Sink, marc io.Reader, provider string) (BuildStats, error) {
	recs, err := ReadMARC(marc)
	if err != nil {
		return BuildStats{}, fmt.Errorf("read marc: %w", err)
	}
	return BuildCorpus(sink, recs, provider)
}

// BuildCorpus writes one canonical N-Quads grain per record into sink (at
// GrainPath) in the provider's feed graph, plus a bulk catalog.nq. Because it
// writes through the Sink, the same build runs against a local directory, cloud
// object storage, or a git tree unchanged.
//
// catalog.nq is not a byte-concatenation of the grain files: each grain
// canonicalizes its blanks to _:c14nN independently, so a plain concatenation
// would merge two grains' _:c14n0 into one node. It is the grains' own bytes with
// each grain's labels namespaced by work id -- byte-identical to what
// SerializeGrains produces from the same grains, which is the point: catalog.nq
// means one thing whichever writer wrote it last (tasks/298). All grains are held
// in memory for the sorted bulk write; at large scale (ARCHITECTURE §3) that
// becomes an out-of-core / fan-out concern.
func BuildCorpus(sink storage.Sink, records []*codex.Record, provider string) (BuildStats, error) {
	feed := FeedGraph(provider)
	stats := BuildStats{Records: len(records)}

	entries := make([]grainEntry, 0, len(records))
	for _, rec := range records {
		id, err := WorkID(rec)
		if err != nil {
			return stats, err
		}
		grain, err := Grain(rec, feed)
		if err != nil {
			return stats, fmt.Errorf("grain %s: %w", id, err)
		}
		if err := writeSink(sink, GrainPath(id), grain); err != nil {
			return stats, err
		}
		stats.Grains++
		entries = append(entries, grainEntry{id, grain})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	if err := writeCatalog(sink, entries); err != nil {
		return stats, err
	}
	return stats, nil
}

// writeCatalog writes the bulk catalog.nq as the merge of the grains just
// written: each grain's own canonical bytes, blank labels namespaced by work id.
// Re-encoding the records a second time is what made ingest's catalog.nq differ
// from serialize's (tasks/298); the grains are the source of truth and this file
// is derived from them, here as everywhere else.
func writeCatalog(sink storage.Sink, entries []grainEntry) error {
	w, err := sink.Create("catalog.nq")
	if err != nil {
		return fmt.Errorf("create catalog.nq: %w", err)
	}
	for _, e := range entries {
		if err := WriteMergedGrain(w, e.id, e.grain); err != nil {
			w.Close()
			return fmt.Errorf("write catalog.nq: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close catalog.nq: %w", err)
	}
	return nil
}

// writeSink writes data to path through the sink, closing the writer.
func writeSink(sink storage.Sink, p string, data []byte) error {
	w, err := sink.Create(p)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// hashID is the hex SHA-256 of b, used for shard prefixes and id fallbacks.
func hashID(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sanitize maps a raw id to a filesystem-safe token, replacing anything outside
// [A-Za-z0-9._-] with an underscore.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}
