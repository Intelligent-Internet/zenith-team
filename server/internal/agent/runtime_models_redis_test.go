package agent

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisModelDiscoveryStoreCoalescesAcrossInstancesAndClaimsOnce(t *testing.T) {
	client := newAgentRedisTestClient(t)
	ctx := context.Background()
	first, err := NewRedisModelDiscoveryStore(client, ModelDiscoveryStoreOptions{})
	if err != nil {
		t.Fatalf("NewRedisModelDiscoveryStore() error = %v", err)
	}
	second, err := NewRedisModelDiscoveryStore(client, ModelDiscoveryStoreOptions{})
	if err != nil {
		t.Fatalf("NewRedisModelDiscoveryStore() error = %v", err)
	}
	runtimeID := uuid.Must(uuid.NewV7())
	seed := ModelDiscoverySeed{
		RuntimeID: runtimeID, CompanyID: uuid.Must(uuid.NewV7()), Provider: "codex",
	}
	requestA, createdA, err := first.Enqueue(ctx, seed)
	if err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	requestB, createdB, err := second.Enqueue(ctx, seed)
	if err != nil {
		t.Fatalf("second Enqueue() error = %v", err)
	}
	if !createdA || createdB || requestA.ID != requestB.ID {
		t.Fatalf("coalesce = (%t, %s) / (%t, %s)", createdA, requestA.ID, createdB, requestB.ID)
	}
	assertRedisModelDiscoveryTTL(
		t,
		client,
		redisModelDiscoveryRequestKey(requestA.ID),
		modelDiscoveryPendingTimeout+modelDiscoveryRetention,
	)

	stores := []*RedisModelDiscoveryStore{first, second}
	claims := make(chan *ModelDiscoveryRequest, len(stores))
	var wait sync.WaitGroup
	for _, store := range stores {

		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, claimErr := store.Claim(ctx, runtimeID)
			if claimErr != nil {
				t.Errorf("Claim() error = %v", claimErr)
				return
			}
			claims <- claim
		}()
	}
	wait.Wait()
	close(claims)
	winners := 0
	for claim := range claims {
		if claim != nil {
			winners++
			if claim.ID != requestA.ID || claim.Status != ModelDiscoveryRunning {
				t.Fatalf("Claim() = %+v", claim)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d, want 1", winners)
	}
	assertRedisModelDiscoveryTTL(
		t,
		client,
		redisModelDiscoveryRequestKey(requestA.ID),
		modelDiscoveryRunningTimeout+modelDiscoveryRetention,
	)
}

func assertRedisModelDiscoveryTTL(
	t *testing.T,
	client *redis.Client,
	key string,
	want time.Duration,
) {
	t.Helper()
	got, err := client.PTTL(t.Context(), key).Result()
	if err != nil {
		t.Fatalf("PTTL(%q) error = %v", key, err)
	}
	if got < want-time.Second || got > want {
		t.Fatalf("PTTL(%q) = %s, want approximately %s", key, got, want)
	}
}

func newAgentRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("REDIS_TEST_URL")
	if rawURL == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse REDIS_TEST_URL: %v", err)
	}
	client := redis.NewClient(options)
	if err := client.Ping(t.Context()).Err(); err != nil {
		client.Close()
		t.Skipf("REDIS_TEST_URL unreachable: %v", err)
	}
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		client.Close()
		t.Fatalf("flush Redis test database: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Redis test client: %v", err)
		}
	})
	return client
}
