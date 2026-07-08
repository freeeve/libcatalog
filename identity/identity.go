// Package identity assigns libcat's opaque, two-tier Work/Instance ids
// (ARCHITECTURE §4). Ids are minted once and never derived from a provider id;
// on re-ingest a provider record resolves back to its previously minted id via
// the persisted identity map, so unchanged records keep stable ids -- and
// therefore stable public URLs.
//
// This file holds the provider-independent primitives: minting opaque ids and
// computing the clustering key. The persistence and resolve layer (reading the
// committed map, mint-or-resolve, clobber-safe re-ingest) builds on top.
package identity

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"unicode"
)

// Id prefixes distinguish the two tiers in a bare id string and in the public
// URL space (/works/w..., an Instance's i...). AuthorityPrefix marks locally
// minted authority terms (tasks/046) -- not a bibliographic tier, but minted
// through the same opaque-id discipline so local headings get stable IRIs.
const (
	WorkPrefix      = "w"
	InstancePrefix  = "i"
	AuthorityPrefix = "a"
)

// idBytes is the entropy per minted id: 8 bytes (64 bits) encodes to 13 base32
// characters. Collision probability stays negligible into the tens of millions
// of records, and the resolver mints-and-checks to rule it out entirely.
const idBytes = 8

// lowerBase32 encodes ids as lowercase alphanumerics (base32hex alphabet, no
// padding), giving short, URL- and filename-safe, opaque ids like "w7g3k9…".
var lowerBase32 = base32.NewEncoding("0123456789abcdefghijklmnopqrstuv").WithPadding(base32.NoPadding)

// Mint returns a fresh opaque id with the given tier prefix (WorkPrefix or
// InstancePrefix), drawn from crypto/rand so it is provider-independent and
// unguessable. Callers persist the id on first mint and never regenerate it; use
// a resolver to avoid re-minting for an already-mapped record.
func Mint(prefix string) string {
	var b [idBytes]byte
	// crypto/rand.Read never returns an error on the platforms we target; a short
	// read would still yield a valid (if lower-entropy) id, and the resolver's
	// uniqueness check is the real collision guard.
	_, _ = rand.Read(b[:])
	return prefix + lowerBase32.EncodeToString(b[:])
}

// keySep separates the fields of a clustering key. It is the ASCII unit
// separator so it cannot appear in a normalized field and collide.
const keySep = "\x1f"

// WorkKey builds the computed clustering key from the primary author, the title,
// and the original language -- the MARC 1XX+240 access-point key (ARCHITECTURE
// §4). Two Instances with the same key cluster into one Work unless an external
// work id or an editorial merge/split decision says otherwise. The title is the
// main title only (not the subtitle), so editions that vary a subtitle still
// cluster.
//
// A record with no main title has no usable access point: clustering title-less
// records by author (or by nothing) would merge unrelated books, so WorkKey
// returns "" and the caller must mint instead of clustering (tasks/101).
func WorkKey(author, title, lang string) string {
	t := NormalizeKey(title)
	if t == "" {
		return ""
	}
	return NormalizeKey(author) + keySep + t + keySep +
		strings.ToLower(strings.TrimSpace(lang))
}

// NormalizeKey folds a string to its clustering-key form: Unicode-lowercased,
// with every run of non-alphanumeric characters collapsed to a single space and
// the result trimmed. It deliberately does not fold diacritics -- that needs a
// Unicode-normalization dependency this framework avoids -- so accent and
// transliteration variants under-merge and are corrected in the editorial
// overlay (tasks/001) rather than guessed at here.
func NormalizeKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingSpace {
				b.WriteByte(' ')
				pendingSpace = false
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		// Defer the separator so leading/trailing runs produce no space.
		if b.Len() > 0 {
			pendingSpace = true
		}
	}
	return b.String()
}
