package email

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLocalRequestGateLimitsToOneReservationPerInterval(t *testing.T) {
	t.Parallel()

	gate, err := NewLocalRequestGate(20)
	if err != nil {
		t.Fatalf("NewLocalRequestGate() error = %v", err)
	}
	started := time.Now()
	for range 3 {
		if err := gate.Wait(context.Background()); err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	}
	if elapsed := time.Since(started); elapsed < 90*time.Millisecond {
		t.Fatalf("three reservations completed in %s, want at least 90ms", elapsed)
	}
}

func TestRedisRequestGateCoordinatesClients(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: server.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})
	gateA, err := NewRedisRequestGate(clientA, "mailbox-test", 20)
	if err != nil {
		t.Fatalf("NewRedisRequestGate(A) error = %v", err)
	}
	gateB, err := NewRedisRequestGate(clientB, "mailbox-test", 20)
	if err != nil {
		t.Fatalf("NewRedisRequestGate(B) error = %v", err)
	}

	started := time.Now()
	if err := gateA.Wait(context.Background()); err != nil {
		t.Fatalf("gate A Wait() error = %v", err)
	}
	if err := gateB.Wait(context.Background()); err != nil {
		t.Fatalf("gate B Wait() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("cross-client reservations completed in %s, want at least 40ms", elapsed)
	}
}

func TestRequestGateHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	gate, err := NewLocalRequestGate(1)
	if err != nil {
		t.Fatalf("NewLocalRequestGate() error = %v", err)
	}
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); err == nil {
		t.Fatal("second Wait() error = nil, want context cancellation")
	}
}
