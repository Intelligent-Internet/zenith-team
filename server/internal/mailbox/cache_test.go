package mailbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisDetailCacheStoresOnlySanitizedContentUnderHashedKey(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewRedisDetailCache(client, "boardly:test:mailbox")
	if err != nil {
		t.Fatalf("NewRedisDetailCache() error = %v", err)
	}
	providerEmailID := "provider-email-sensitive-id"
	want := DetailContent{
		From: "customer@example.com", To: []string{"sales@example.com"},
		HTML: "<p>Hello</p>", Text: "Hello",
	}
	if err := cache.Set(context.Background(), providerEmailID, want, 5*time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	keys := server.Keys()
	if len(keys) != 1 || strings.Contains(keys[0], providerEmailID) {
		t.Fatalf("cache keys = %#v", keys)
	}
	got, found, err := cache.Get(context.Background(), providerEmailID)
	if err != nil || !found || got.HTML != want.HTML || got.Text != want.Text {
		t.Fatalf("Get() = %#v, %t, %v", got, found, err)
	}
	for name, addresses := range map[string][]string{
		"bcc": got.BCC, "cc": got.CC, "replyTo": got.ReplyTo,
	} {
		if addresses == nil || len(addresses) != 0 {
			t.Fatalf("Get() %s = %#v, want non-nil empty list", name, addresses)
		}
	}
	if err := cache.Delete(context.Background(), providerEmailID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, found, err := cache.Get(context.Background(), providerEmailID); err != nil || found {
		t.Fatalf("Get(after delete) found = %t, error = %v", found, err)
	}
}

func TestRedisDetailCacheSanitizesContentAtItsBoundary(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewRedisDetailCache(client, "boardly:test:mailbox-boundary")
	if err != nil {
		t.Fatalf("NewRedisDetailCache() error = %v", err)
	}
	if err := cache.Set(context.Background(), "provider-email-1", DetailContent{
		HTML: `<p onclick="steal()">Hello</p><script>steal()</script>`,
		Text: "Hello\x00world",
	}, 5*time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	got, found, err := cache.Get(context.Background(), "provider-email-1")
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %t, %v", got, found, err)
	}
	if got.HTML != "<p>Hello</p>" || got.Text != "Hello\x00world" {
		t.Fatalf("cached detail = %#v", got)
	}
}

func TestRedisDetailCacheStoresExpirationsAsShortHashedTombstones(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewRedisDetailCache(client, "boardly:test:mailbox-expired")
	if err != nil {
		t.Fatalf("NewRedisDetailCache() error = %v", err)
	}
	providerEmailID := "provider-email-sensitive-expired-id"
	if err := cache.MarkExpired(
		context.Background(),
		providerEmailID,
		5*time.Minute,
	); err != nil {
		t.Fatalf("MarkExpired() error = %v", err)
	}
	expired, err := cache.IsExpired(context.Background(), providerEmailID)
	if err != nil || !expired {
		t.Fatalf("IsExpired() = %t, %v, want true", expired, err)
	}
	keys := server.Keys()
	if len(keys) != 1 || strings.Contains(keys[0], providerEmailID) {
		t.Fatalf("expiration cache keys = %#v", keys)
	}
	server.FastForward(5*time.Minute + time.Second)
	expired, err = cache.IsExpired(context.Background(), providerEmailID)
	if err != nil || expired {
		t.Fatalf("IsExpired(after TTL) = %t, %v, want false", expired, err)
	}
}
