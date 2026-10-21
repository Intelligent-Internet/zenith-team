//go:build integration

package realtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestCompanyUserRedisPipelinePreservesPartialResultsAndScope(t *testing.T) {
	client := isolatedDiscussionRedis(t)
	ctx := context.Background()
	remoteHub := NewHub()
	recipient := attachRealtimeTestClient(remoteHub, "company-a", "a")
	outsider := attachRealtimeTestClient(remoteHub, "company-b", "a")
	relay := NewShardedStreamRelay(remoteHub, client, client, ShardedStreamRelayConfig{Shards: 2})
	firstUser := "a"
	secondUser := ""
	for n := range 100 {
		candidate := fmt.Sprintf("user-%d", n)
		if relay.shardFor(ScopeCompanyUser, candidate) != relay.shardFor(ScopeCompanyUser, firstUser) {
			secondUser = candidate
			break
		}
	}
	if secondUser == "" {
		t.Fatal("no distinct shard")
	}
	badStream := ShardedStreamKey(relay.shardFor(ScopeCompanyUser, secondUser))
	if err := client.Set(ctx, badStream, "wrong type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	frames := []CompanyUserRelayFrame{
		{
			CompanyID: "company-a",
			UserID:    firstUser,
			EventID:   "event-a",
			Frame: []byte(
				`{"type":"conversation.discussion_changed","companyId":"company-a","eventId":"event-a","payload":{"conversationId":"private-id","revision":2,"headSeq":1}}`,
			),
		},
		{
			CompanyID: "company-a",
			UserID:    secondUser,
			EventID:   "event-b",
			Frame:     []byte(`{"type":"conversation.discussion_changed","eventId":"event-b"}`),
		},
	}
	results := relay.PublishCompanyUserBatch(ctx, frames)
	if len(results) != 2 || results[0].Err != nil || results[1].Err == nil || results[0].EventID != "event-a" ||
		results[1].EventID != "event-b" {
		t.Fatalf("pipeline=%+v", results)
	}
	stored, err := client.XRange(ctx, ShardedStreamKey(relay.shardFor(ScopeCompanyUser, firstUser)), "-", "+").
		Result()
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%+v %v", stored, err)
	}
	remoteRelay := NewShardedStreamRelay(remoteHub, client, client, ShardedStreamRelayConfig{Shards: 2})
	remoteRelay.deliverMessage(stored[0])
	remoteRelay.deliverMessage(stored[0])
	received := receiveRealtimeFrame(t, recipient)
	if received.EventID != "event-a" {
		t.Fatalf("received=%+v", received)
	}
	select {
	case raw := <-recipient.send:
		t.Fatalf("duplicate=%s", raw)
	default:
	}
	select {
	case raw := <-outsider.send:
		t.Fatalf("foreign Company=%s", raw)
	default:
	}
	if err = client.Do(ctx, "CLIENT", "PAUSE", 1500, "ALL").Err(); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	results = relay.PublishCompanyUserBatch(bounded, frames[:1])
	if results[0].Err == nil || results[0].EventID != "event-a" || time.Since(start) > time.Second {
		t.Fatalf("bounded relay=%+v elapsed=%s", results, time.Since(start))
	}
}

func isolatedDiscussionRedis(t *testing.T) *redis.Client {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for the local Redis integration test")
	}
	// Unix sockets avoid touching a user's Redis port or database. macOS caps
	// socket paths at 104 bytes, so use a short dedicated temporary directory.
	directory, err := os.MkdirTemp("/tmp", "zcr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "r.sock")
	command := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--save", "", "--appendonly", "no")
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	client := redis.NewClient(
		&redis.Options{Network: "unix", Addr: socket, ContextTimeoutEnabled: true, MaxRetries: -1},
	)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if client.Ping(ctx).Err() == nil {
			return client
		}
		select {
		case <-ctx.Done():
			t.Fatal("isolated Redis did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
