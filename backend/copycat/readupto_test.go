package copycat

import (
	"errors"
	"io"
	"strings"
	"testing"

	codex "github.com/freeeve/libcodex"
)

func rec(id string) *codex.Record {
	r := codex.NewRecord()
	r.AddField(codex.NewControlField("001", id))
	return r
}

func recs(n int) []*codex.Record {
	out := make([]*codex.Record, n)
	for i := range out {
		out[i] = rec(string(rune('1' + i)))
	}
	return out
}

// stream replays a scripted stream: records, then a terminal error that repeats,
// because libcodex's readers make an error sticky. It reports no total, standing
// in for a reader over a file or a pipe -- a source with no result set to size.
type stream struct {
	recs     []*codex.Record
	i        int
	terminal error
}

func (s *stream) Read() (*codex.Record, error) {
	if s.i < len(s.recs) {
		s.i++
		return s.recs[s.i-1], nil
	}
	return nil, s.terminal
}

// counted is a stream whose target announces the size of the result set, the way
// an SRU or Z39.50 reader does (codex.RecordCounter).
type counted struct {
	stream
	total int
}

func (c *counted) Total() int { return c.total }

var _ codex.RecordReader = (*stream)(nil)
var _ codex.RecordCounter = (*counted)(nil)

func reads(rs []*codex.Record, terminal error) *stream {
	return &stream{recs: rs, terminal: terminal}
}

func counts(rs []*codex.Record, terminal error, total int) *counted {
	return &counted{stream: stream{recs: rs, terminal: terminal}, total: total}
}

// A complete stream is complete: no records lost, nothing to report.
func TestReadUpToCompleteStream(t *testing.T) {
	got, err := readUpTo(reads(recs(2), io.EOF), 20)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("records = %d, want 2", len(got))
	}
}

// An error before any record is a failure, as it always was.
func TestReadUpToImmediateErrorIsAFailure(t *testing.T) {
	boom := errors.New("sru: parse response: XML syntax error")
	got, err := readUpTo(reads(nil, boom), 20)
	if got != nil {
		t.Fatalf("records = %v, want none", got)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the stream's error", err)
	}
	var partial *PartialError
	if errors.As(err, &partial) {
		t.Fatal("an immediate error must not be reported as partial results")
	}
}

// the same error one record later used to be swallowed whole. The
// records are still returned -- partial results beat none -- but the caller is
// now told the set is incomplete, and why.
func TestReadUpToMidStreamErrorReportsPartialResults(t *testing.T) {
	boom := errors.New("sru: parse response: XML syntax error")
	got, err := readUpTo(reads(recs(1), boom), 20)
	if len(got) != 1 {
		t.Fatalf("records = %d, want the one that arrived before the break", len(got))
	}
	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want a PartialError", err)
	}
	if partial.Got != 1 {
		t.Fatalf("partial.Got = %d, want 1", partial.Got)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("the underlying stream error was lost: %v", err)
	}
}

// a broken stream from a counting target says how much was missed.
// "1 of 9" is the sentence the report asked for; "1" is not.
func TestReadUpToMidStreamErrorNamesTheAdvertisedTotal(t *testing.T) {
	boom := errors.New("sru: unexpected HTTP status 500")
	_, err := readUpTo(counts(recs(1), boom, 9), 20)

	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want a PartialError", err)
	}
	if partial.Total != 9 {
		t.Fatalf("partial.Total = %d, want the target's advertised 9", partial.Total)
	}
	if !strings.Contains(err.Error(), "1 of 9") {
		t.Fatalf("message = %q, want it to name both counts", err.Error())
	}
}

// A target that never says how many it holds must not have a count invented for
// it. -1 is "unknown", and the message falls back to the bare count.
func TestReadUpToMidStreamErrorWithoutATotalSaysOnlyWhatItKnows(t *testing.T) {
	boom := errors.New("connection reset by peer")
	_, err := readUpTo(counts(recs(2), boom, unknownTotal), 20)

	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want a PartialError", err)
	}
	if partial.Total != unknownTotal {
		t.Fatalf("partial.Total = %d, want unknownTotal", partial.Total)
	}
	if strings.Contains(err.Error(), " of ") {
		t.Fatalf("message = %q invents a total the target never gave", err.Error())
	}
}

// the point of it: a result set of exactly limit records is a
// complete answer. It used to warn, on the commonest path there is.
func TestReadUpToFullPageThatIsTheWholeResultSetIsNotCapped(t *testing.T) {
	got, err := readUpTo(counts(recs(2), io.EOF, 2), 2)
	if err != nil {
		t.Fatalf("err = %v, want nil: the target holds exactly these 2", err)
	}
	if len(got) != 2 {
		t.Fatalf("records = %d, want 2", len(got))
	}
}

// A genuinely truncated set says how much it is hiding.
func TestReadUpToCapNamesTheAdvertisedTotal(t *testing.T) {
	got, err := readUpTo(counts(recs(3), io.EOF, 4113), 2)
	if len(got) != 2 {
		t.Fatalf("records = %d, want the limit", len(got))
	}
	if !errors.Is(err, ErrCapped) {
		t.Fatalf("err = %v, want ErrCapped", err)
	}
	if !strings.Contains(err.Error(), "2 of 4113") {
		t.Fatalf("message = %q, want the counts a cataloger can act on", err.Error())
	}
	var partial *PartialError
	if errors.As(err, &partial) {
		t.Fatal("the cap is not a stream failure")
	}
}

// A target that reports no total still gets the old, honest warning: we filled
// the page and cannot say whether more exists.
func TestReadUpToCapWithoutATotalKeepsTheOldWarning(t *testing.T) {
	_, err := readUpTo(reads(recs(3), io.EOF), 2)
	if !errors.Is(err, ErrCapped) {
		t.Fatalf("err = %v, want ErrCapped", err)
	}
	if !strings.Contains(err.Error(), "first 2") {
		t.Fatalf("message = %q", err.Error())
	}
}

// Zero is a real answer and must never read as "unknown": a target saying it
// holds nothing is not a target declining to say. The two used to unmarshal the
// same in libcodex, which is why this is pinned here too.
func TestReadUpToTreatsZeroTotalAsAnAnswerNotAsUnknown(t *testing.T) {
	got, err := readUpTo(counts(nil, io.EOF, 0), 20)
	if err != nil {
		t.Fatalf("err = %v, want nil: an empty result set is complete", err)
	}
	if len(got) != 0 {
		t.Fatalf("records = %d, want none", len(got))
	}
}

// cappedError decides, on every full page, whether the cataloger is warned. The
// boundaries are where it earns its keep, and readUpTo cannot reach some of them
// (a stream that ends at EOF never asks). Test the decision directly.
func TestCappedErrorBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		total, got int
		want       string // "" means no warning
	}{
		{"the result set is bigger than the page", 4113, 20, "20 of 4113"},
		{"one more record exists", 21, 20, "20 of 21"},
		{"the page is the whole result set", 20, 20, ""},
		{"the target reported no total", unknownTotal, 20, "first 20"},
		{"the target contradicts its own stream", 19, 20, "first 20"},
		{"an empty result set, reported as such", 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cappedError(tc.total, tc.got, 20)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want no warning", err)
				}
				return
			}
			if !errors.Is(err, ErrCapped) {
				t.Fatalf("err = %v, want ErrCapped", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("message = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// A server whose advertised count contradicts its own stream (fewer than it
// sent) gets the honest fallback rather than a nonsense "3 of 2".
func TestReadUpToCapWithAContradictoryTotalFallsBack(t *testing.T) {
	_, err := readUpTo(counts(recs(3), io.EOF, 1), 2)
	if !errors.Is(err, ErrCapped) {
		t.Fatalf("err = %v, want ErrCapped", err)
	}
	if strings.Contains(err.Error(), "of 1") {
		t.Fatalf("message = %q repeats the server's contradiction", err.Error())
	}
}

// advertisedTotal must not claim a total for a reader that implements no
// counter: a file or a pipe has no result set to size.
func TestAdvertisedTotalOfANonCountingReaderIsUnknown(t *testing.T) {
	if got := advertisedTotal(reads(nil, io.EOF)); got != unknownTotal {
		t.Fatalf("advertisedTotal = %d, want unknownTotal", got)
	}
	if got := advertisedTotal(counts(nil, io.EOF, 7)); got != 7 {
		t.Fatalf("advertisedTotal = %d, want 7", got)
	}
}
