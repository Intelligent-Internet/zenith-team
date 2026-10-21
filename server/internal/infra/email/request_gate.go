package email

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxProviderRequestsPerSecond = 1000

type LocalRequestGate struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func NewLocalRequestGate(requestsPerSecond int) (*LocalRequestGate, error) {
	interval, err := requestInterval(requestsPerSecond)
	if err != nil {
		return nil, err
	}
	return &LocalRequestGate{interval: interval}, nil
}

func (g *LocalRequestGate) Wait(ctx context.Context) error {
	if g == nil || g.interval <= 0 {
		return errors.New("email provider request gate is not configured")
	}
	now := time.Now()
	g.mu.Lock()
	reservedAt := now
	if g.next.After(reservedAt) {
		reservedAt = g.next
	}
	g.next = reservedAt.Add(g.interval)
	g.mu.Unlock()
	return waitForReservation(ctx, reservedAt.Sub(now))
}

var redisRequestGateReserve = redis.NewScript(`
local current_time = redis.call('TIME')
local now_ms = (tonumber(current_time[1]) * 1000) + math.floor(tonumber(current_time[2]) / 1000)
local interval_ms = tonumber(ARGV[1])
local next_ms = tonumber(redis.call('GET', KEYS[1]) or '0')
local reserved_ms = now_ms
if next_ms > reserved_ms then
  reserved_ms = next_ms
end
local new_next_ms = reserved_ms + interval_ms
local ttl_ms = (new_next_ms - now_ms) + (interval_ms * 2)
redis.call('SET', KEYS[1], new_next_ms, 'PX', ttl_ms)
return reserved_ms - now_ms
`)

type RedisRequestGate struct {
	client         *redis.Client
	key            string
	intervalMillis int64
}

func NewRedisRequestGate(
	client *redis.Client,
	key string,
	requestsPerSecond int,
) (*RedisRequestGate, error) {
	if client == nil {
		return nil, errors.New("email provider Redis request gate requires a client")
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 || strings.ContainsAny(key, " \t\r\n") {
		return nil, errors.New("email provider Redis request gate key is invalid")
	}
	interval, err := requestInterval(requestsPerSecond)
	if err != nil {
		return nil, err
	}
	return &RedisRequestGate{
		client: client, key: "zenith-team:email:request-gate:" + key,
		intervalMillis: max(interval.Milliseconds(), 1),
	}, nil
}

func (g *RedisRequestGate) Wait(ctx context.Context) error {
	if g == nil || g.client == nil || g.intervalMillis < 1 {
		return errors.New("email provider Redis request gate is not configured")
	}
	delayMillis, err := redisRequestGateReserve.Run(
		ctx,
		g.client,
		[]string{g.key},
		g.intervalMillis,
	).Int64()
	if err != nil {
		return fmt.Errorf("reserve email provider request capacity: %w", err)
	}
	if delayMillis < 0 || delayMillis > int64((time.Hour)/time.Millisecond) {
		return errors.New("email provider request gate returned an invalid delay")
	}
	return waitForReservation(ctx, time.Duration(delayMillis)*time.Millisecond)
}

func requestInterval(requestsPerSecond int) (time.Duration, error) {
	if requestsPerSecond < 1 || requestsPerSecond > maxProviderRequestsPerSecond {
		return 0, errors.New("email provider request rate is invalid")
	}
	return time.Second / time.Duration(requestsPerSecond), nil
}

func waitForReservation(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var (
	_ RequestGate = (*LocalRequestGate)(nil)
	_ RequestGate = (*RedisRequestGate)(nil)
)
