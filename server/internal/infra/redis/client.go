// Package redis constructs the process's Redis clients.
//
// Redis is optional: a single-replica deployment runs without it and every
// consumer degrades to a local-only path. That is why the constructor returns
// a nil client rather than an error for an empty URL — "not configured" is a
// supported deployment, not a misconfiguration.
package redis

import (
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// NewClient builds a client from a Redis URL, or returns nil when the URL is
// empty. Callers branch on nil to choose their local-only path.
func NewClient(rawURL string) (*goredis.Client, error) {
	if rawURL == "" {
		return nil, nil
	}
	options, err := goredis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	// go-redis otherwise replaces command contexts with context.Background for
	// socket I/O, defeating Relay and readiness deadlines on a blackholed peer.
	options.ContextTimeoutEnabled = true
	return goredis.NewClient(options), nil
}
