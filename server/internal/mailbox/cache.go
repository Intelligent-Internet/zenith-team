package mailbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxCachedDetailBytes = 16 << 20

type RedisDetailCache struct {
	client *redis.Client
	prefix string
}

func NewRedisDetailCache(client *redis.Client, prefix string) (*RedisDetailCache, error) {
	if client == nil {
		return nil, errors.New("Mailbox detail cache requires a Redis client")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || len(prefix) > 128 || strings.ContainsAny(prefix, " \t\r\n") {
		return nil, errors.New("Mailbox detail cache prefix is invalid")
	}
	return &RedisDetailCache{client: client, prefix: prefix}, nil
}

func (c *RedisDetailCache) Get(
	ctx context.Context,
	providerEmailID string,
) (DetailContent, bool, error) {
	if c == nil || c.client == nil {
		return DetailContent{}, false, errors.New("Mailbox detail cache is not configured")
	}
	payload, err := c.client.Get(ctx, c.key(providerEmailID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return DetailContent{}, false, nil
	}
	if err != nil {
		return DetailContent{}, false, fmt.Errorf("read Mailbox detail cache: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxCachedDetailBytes {
		return DetailContent{}, false, errors.New("Mailbox detail cache payload is invalid")
	}
	var content DetailContent
	if err := json.Unmarshal(payload, &content); err != nil {
		return DetailContent{}, false, errors.New("Mailbox detail cache payload is invalid")
	}
	content = normalizeDetailContent(content)
	content.HTML = sanitizeHTML(content.HTML)
	content.Text = sanitizeText(content.Text)
	return content, true, nil
}

func (c *RedisDetailCache) Set(
	ctx context.Context,
	providerEmailID string,
	content DetailContent,
	ttl time.Duration,
) error {
	if c == nil || c.client == nil || ttl < time.Second || ttl > time.Hour {
		return errors.New("Mailbox detail cache write is invalid")
	}
	content = normalizeDetailContent(content)
	content.HTML = sanitizeHTML(content.HTML)
	content.Text = sanitizeText(content.Text)
	payload, err := json.Marshal(content)
	if err != nil || len(payload) == 0 || len(payload) > maxCachedDetailBytes {
		return errors.New("Mailbox detail cache payload is invalid")
	}
	if err := c.client.Set(ctx, c.key(providerEmailID), payload, ttl).Err(); err != nil {
		return fmt.Errorf("write Mailbox detail cache: %w", err)
	}
	return nil
}

func (c *RedisDetailCache) Delete(ctx context.Context, providerEmailID string) error {
	if c == nil || c.client == nil {
		return errors.New("Mailbox detail cache is not configured")
	}
	if err := c.client.Del(ctx, c.key(providerEmailID)).Err(); err != nil {
		return fmt.Errorf("delete Mailbox detail cache: %w", err)
	}
	return nil
}

func (c *RedisDetailCache) IsExpired(ctx context.Context, providerEmailID string) (bool, error) {
	if c == nil || c.client == nil {
		return false, errors.New("Mailbox expiration cache is not configured")
	}
	exists, err := c.client.Exists(ctx, c.expiredKey(providerEmailID)).Result()
	if err != nil {
		return false, fmt.Errorf("read Mailbox expiration cache: %w", err)
	}
	return exists > 0, nil
}

func (c *RedisDetailCache) MarkExpired(
	ctx context.Context,
	providerEmailID string,
	ttl time.Duration,
) error {
	if c == nil || c.client == nil || ttl < time.Second || ttl > time.Hour {
		return errors.New("Mailbox expiration cache write is invalid")
	}
	if err := c.client.Set(ctx, c.expiredKey(providerEmailID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("write Mailbox expiration cache: %w", err)
	}
	return nil
}

func (c *RedisDetailCache) key(providerEmailID string) string {
	return c.hashedKey("detail:v1", providerEmailID)
}

func (c *RedisDetailCache) expiredKey(providerEmailID string) string {
	return c.hashedKey("expired:v1", providerEmailID)
}

func (c *RedisDetailCache) hashedKey(kind, providerEmailID string) string {
	digest := sha256.Sum256([]byte(providerEmailID))
	return c.prefix + ":" + kind + ":" + hex.EncodeToString(digest[:])
}

var (
	_ DetailCache         = (*RedisDetailCache)(nil)
	_ ProviderExpiryCache = (*RedisDetailCache)(nil)
)
