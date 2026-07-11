package trigger

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Coalesce batches a burst of events into one downstream Notify carrying the
// union of changed paths: an editing session's stream of
// publishes becomes one incremental rebuild instead of one per save. The
// batch fires after Window of quiet, or MaxDelay after its first event,
// whichever comes first. Notify itself never blocks on the downstream --
// delivery runs on its own goroutine with its own deadline, and a failure is
// logged, not returned, because the publish that triggered it has already
// succeeded (scheduled full rebuilds stay the correctness backstop).
type Coalesce struct {
	Next Notifier
	// Window is the quiet period that closes a batch. Default 5s.
	Window time.Duration
	// MaxDelay bounds how long a busy stream can hold the batch open,
	// measured from its first event. Default 12x Window.
	MaxDelay time.Duration
	// Timeout bounds one downstream delivery. Default 10 minutes.
	Timeout time.Duration
	Logger  *slog.Logger

	mu      sync.Mutex
	timer   *time.Timer
	firstAt time.Time
	kind    string
	paths   []string
	seen    map[string]bool
}

func (c *Coalesce) window() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return 5 * time.Second
}

func (c *Coalesce) maxDelay() time.Duration {
	if c.MaxDelay > 0 {
		return c.MaxDelay
	}
	return 12 * c.window()
}

// Notify implements Notifier: it buffers the event and (re)arms the flush
// timer. Always returns nil; delivery is asynchronous.
func (c *Coalesce) Notify(_ context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	if len(c.paths) == 0 {
		c.firstAt = time.Now()
	}
	c.kind = e.Kind
	for _, p := range e.Paths {
		if !c.seen[p] {
			c.seen[p] = true
			c.paths = append(c.paths, p)
		}
	}
	delay := c.window()
	if rem := c.maxDelay() - time.Since(c.firstAt); rem < delay {
		delay = max(rem, 0)
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(delay, c.flush)
	return nil
}

// flush delivers the buffered batch downstream on the timer goroutine.
func (c *Coalesce) flush() {
	c.mu.Lock()
	if len(c.paths) == 0 {
		c.mu.Unlock()
		return
	}
	e := Event{Kind: c.kind, Paths: c.paths, At: time.Now()}
	c.paths, c.seen, c.timer = nil, nil, nil
	c.mu.Unlock()

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := c.Next.Notify(ctx, e); err != nil {
		if c.Logger != nil {
			c.Logger.Error("coalesced trigger delivery failed", "err", err, "paths", len(e.Paths))
		}
		return
	}
	if c.Logger != nil {
		c.Logger.Info("coalesced trigger delivered", "paths", len(e.Paths))
	}
}
