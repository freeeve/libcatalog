package vega

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// dnsDoer fails every request as an unresolvable host.
type dnsDoer struct {
	mu    sync.Mutex
	calls int
}

func (d *dnsDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return nil, &net.DNSError{Err: "no such host", Name: req.URL.Hostname(), IsNotFound: true}
}

func manyTerms(n int) []Term {
	terms := make([]Term, n)
	for i := range terms {
		terms[i] = Term{URI: fmt.Sprintf("https://homosaurus.org/v5/u%d", i), Labels: map[string]string{"en": "x"}, Query: fmt.Sprintf("q%d", i)}
	}
	return terms
}

// TestVegaCircuitBreaksOnUnreachable pins the fast-fail (task 469): an
// unresolvable region host aborts after a bounded number of consecutive
// connection failures, naming the tenant, not after every driver term.
func TestVegaCircuitBreaksOnUnreachable(t *testing.T) {
	doer := &dnsDoer{}
	e := New([]Tenant{{SiteCode: "zzdead", Region: "na2"}}, manyTerms(200), WithClient(doer), WithDelay(0))
	_, err := e.Enrich(context.Background(), nil)
	if !errors.Is(err, ingest.ErrPeerUnreachable) {
		t.Fatalf("err = %v, want ErrPeerUnreachable", err)
	}
	if !strings.Contains(err.Error(), "zzdead.na2") {
		t.Fatalf("err = %v, want the tenant named", err)
	}
	if doer.calls > ingest.UnreachableAbortAfter+2 {
		t.Fatalf("calls = %d, want ~%d (aborted early)", doer.calls, ingest.UnreachableAbortAfter)
	}
}
