package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// TestIsUnreachable pins the connection-class classifier (task 469): the
// failures that mean "this host is misconfigured" -- DNS miss, refused,
// unroutable, timeout -- count toward the circuit break; a content/parse
// failure or an HTTP status does not.
func TestIsUnreachable(t *testing.T) {
	unreachable := []error{
		&net.DNSError{Err: "no such host", Name: "zzdead.example", IsNotFound: true},
		fmt.Errorf("dial: %w", syscall.ECONNREFUSED),
		fmt.Errorf("route: %w", syscall.EHOSTUNREACH),
		context.DeadlineExceeded,
		&net.OpError{Op: "dial", Err: &timeoutErr{}},
		// Wrapped through the enricher's own error, as the providers return it.
		fmt.Errorf("%w: %w", ingest.ErrEnricher, &net.DNSError{Err: "no such host", IsNotFound: true}),
	}
	for i, e := range unreachable {
		if !ingest.IsUnreachable(e) {
			t.Fatalf("unreachable[%d] = %v classified reachable", i, e)
		}
	}
	reachable := []error{
		nil,
		errors.New("parse rss: unexpected EOF"),
		fmt.Errorf("%w: HTTP 404", ingest.ErrEnricher),
		fmt.Errorf("%w: totalHits null (request schema rejected)", ingest.ErrEnricher),
	}
	for i, e := range reachable {
		if ingest.IsUnreachable(e) {
			t.Fatalf("reachable[%d] = %v classified unreachable", i, e)
		}
	}
}

// timeoutErr is a net.Error that reports a timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
