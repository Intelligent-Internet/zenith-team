// Package redis constructs the process's Redis clients.
//
// The API configuration requires Redis for the Schedule beat and relays.
// The constructor keeps a nil option for isolated consumers and tests.
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
