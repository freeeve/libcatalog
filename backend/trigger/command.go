package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Command runs a shell command on every event -- the local dev loop: point
// it at "lcat serialize && lcat project" targeting a running `hugo server`'s
// data directory and published edits appear in the discovery site within
// seconds, no cloud, no CI (the hugo-watcher idea). The
// changed blob paths arrive newline-joined in $LCAT_CHANGED_PATHS.
type Command struct {
	// Shell command, run via sh -c.
	Cmd string
	// Dir is the working directory ("" = inherited).
	Dir string
	// Timeout bounds one run. Default 5 minutes.
	Timeout time.Duration
	// Logger receives run results. nil disables logging.
	Logger *slog.Logger

	mu sync.Mutex // serialize runs; a rebuild is not reentrant
}

// Notify implements Notifier.
func (c *Command) Notify(ctx context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", "-c", c.Cmd)
	cmd.Dir = c.Dir
	cmd.Env = append(cmd.Environ(),
		"LCAT_CHANGED_PATHS="+strings.Join(e.Paths, "\n"),
		"LCAT_EVENT_KIND="+e.Kind,
	)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if c.Logger != nil {
			c.Logger.Error("rebuild command failed", "err", err, "output", truncate(string(out), 2000))
		}
		return fmt.Errorf("trigger: rebuild command: %w", err)
	}
	if c.Logger != nil {
		c.Logger.Info("rebuild command ok", "duration", time.Since(start), "paths", len(e.Paths))
	}
	return nil
}

// Fanout delivers each event to every notifier, returning the first error
// after all have run (a failing webhook must not starve the local rebuild).
type Fanout []Notifier

// Notify implements Notifier.
func (f Fanout) Notify(ctx context.Context, e Event) error {
	var first error
	for _, n := range f {
		if err := n.Notify(ctx, e); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
