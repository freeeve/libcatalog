package suggest

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/vocab"
)

// Key builders. Term IDs (authority URIs) may themselves contain "#", so
// every sk shape puts the ID last and parsers split with a fixed field count.
func workPK(workID string) string { return "WORK#" + workID }

func suggSK(term vocab.TermRef, t SuggType) string {
	return "SUGG#" + term.Scheme + "#" + string(t) + "#" + term.ID
}

func suppSK(term vocab.TermRef, t SuggType, hash string) string {
	return "SUPP#" + term.Scheme + "#" + string(t) + "#" + hash + "#" + term.ID
}

func tombstoneSK(term vocab.TermRef) string {
	return "REJ#" + term.Scheme + "#" + term.ID
}

func folkKey(norm string) store.Key {
	return store.Key{PK: "FOLK#" + norm, SK: "TERM"}
}

// statusIndexKey mirrors an aggregate into its status partition so the queue
// is a single-partition query. The aggregate is the source of truth: a stale
// index item (status flipped between index write and read) is skipped and
// deleted on hydration, so the index is self-healing without transactions.
func statusIndexKey(status Status, aggKey store.Key) store.Key {
	return store.Key{PK: "STATUS#" + string(status), SK: aggKey.PK + "|" + aggKey.SK}
}

func aggKeyFromIndexSK(sk string) (store.Key, error) {
	pk, rest, ok := strings.Cut(sk, "|")
	if !ok {
		return store.Key{}, fmt.Errorf("suggest: malformed index sk %q", sk)
	}
	return store.Key{PK: pk, SK: rest}, nil
}

func marshalSuggestion(s Suggestion) ([]byte, error) { return json.Marshal(s) }

func unmarshalSuggestion(data []byte) (Suggestion, error) {
	var s Suggestion
	err := json.Unmarshal(data, &s)
	return s, err
}
