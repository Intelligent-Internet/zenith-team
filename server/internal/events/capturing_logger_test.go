package events

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// capturingLogger records what the bus logs, so tests can assert that a
// refused or failed event is reported rather than dropped in silence.
type capturingLogger struct {
	mu    sync.Mutex
	lines []string
}

func newCapturingLogger() *capturingLogger { return &capturingLogger{} }

func (c *capturingLogger) logger() *slog.Logger { return slog.New(c) }

func (c *capturingLogger) Enabled(context.Context, slog.Level) bool { return true }

func (c *capturingLogger) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		line.WriteString(" " + attr.Key + "=" + attr.Value.String())
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line.String())
	return nil
}

func (c *capturingLogger) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capturingLogger) WithGroup(string) slog.Handler      { return c }

func (c *capturingLogger) contains(needle string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range c.lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func (c *capturingLogger) records() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}
