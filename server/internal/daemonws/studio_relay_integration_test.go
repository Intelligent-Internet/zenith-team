//go:build integration

package daemonws

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Exercise the real Redis transport and WebSocket boundary on separate hubs.
// In particular, the caller never has the daemon in its local runtime index.
func TestStudioDistributedRelay(t *testing.T) {
	ctx := t.Context()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "redis:7-alpine", ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp"),
		}, Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	address, err := container.Endpoint(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: address, ContextTimeoutEnabled: true, MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	exerciseStudioRelay(t, rdb)
}
