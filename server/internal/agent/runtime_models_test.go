package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

func TestInMemoryModelDiscoveryStoreCoalescesAndClaimsAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runtimeID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	store := NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{})

	const callers = 32
	requests := make(chan ModelDiscoveryRequest, callers)
	var enqueueWait sync.WaitGroup
	for range callers {
		enqueueWait.Add(1)
		go func() {
			defer enqueueWait.Done()
			request, _, err := store.Enqueue(ctx, ModelDiscoverySeed{
				RuntimeID: runtimeID, CompanyID: companyID, Provider: "codex",
			})
			if err != nil {
				t.Errorf("Enqueue() error = %v", err)
				return
			}
			requests <- request
		}()
	}
	enqueueWait.Wait()
	close(requests)

	var requestID uuid.UUID
	for request := range requests {
		if requestID == uuid.Nil {
			requestID = request.ID
		}
		if request.ID != requestID {
			t.Fatalf("coalesced request ID = %s, want %s", request.ID, requestID)
		}
	}

	claims := make(chan *ModelDiscoveryRequest, callers)
	var claimWait sync.WaitGroup
	for range callers {
		claimWait.Add(1)
		go func() {
			defer claimWait.Done()
			claim, err := store.Claim(ctx, runtimeID)
			if err != nil {
				t.Errorf("Claim() error = %v", err)
				return
			}
			claims <- claim
		}()
	}
	claimWait.Wait()
	close(claims)

	winners := 0
	for claim := range claims {
		if claim != nil {
			winners++
			if claim.ID != requestID || claim.Status != ModelDiscoveryRunning {
				t.Fatalf("Claim() = %+v", claim)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d, want 1", winners)
	}
}

func TestInMemoryModelDiscoveryStoreTimeoutsAndRetention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store := NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{
		Now: func() time.Time { return now },
	})
	runtimeID := uuid.Must(uuid.NewV7())
	seed := ModelDiscoverySeed{
		RuntimeID: runtimeID, CompanyID: uuid.Must(uuid.NewV7()), Provider: "codex",
	}

	pending, _, err := store.Enqueue(ctx, seed)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	now = now.Add(modelDiscoveryPendingTimeout + time.Nanosecond)
	got, err := store.Get(ctx, pending.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != ModelDiscoveryTimeout || got.Error == nil || got.Error.Code != "pending_timeout" {
		t.Fatalf("pending timeout = %+v", got)
	}

	running, _, err := store.Enqueue(ctx, seed)
	if err != nil {
		t.Fatalf("Enqueue(second) error = %v", err)
	}
	if _, err := store.Claim(ctx, runtimeID); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	now = now.Add(modelDiscoveryRunningTimeout + time.Nanosecond)
	got, err = store.Get(ctx, running.ID)
	if err != nil {
		t.Fatalf("Get(running) error = %v", err)
	}
	if got.Status != ModelDiscoveryTimeout || got.Error == nil || got.Error.Code != "running_timeout" {
		t.Fatalf("running timeout = %+v", got)
	}

	now = now.Add(modelDiscoveryRetention + time.Nanosecond)
	_, err = store.Get(ctx, running.ID)
	if !errors.Is(err, ErrModelDiscoveryNotFound) {
		t.Fatalf("Get(expired) error = %v, want ErrModelDiscoveryNotFound", err)
	}
}

func TestInMemoryModelDiscoveryStoreRetainsUnobservedTimeoutForTerminalWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name    string
		claim   bool
		advance time.Duration
	}{
		{
			name:    "pending",
			advance: modelDiscoveryPendingTimeout + modelDiscoveryRetention - time.Nanosecond,
		},
		{
			name:    "running",
			claim:   true,
			advance: modelDiscoveryRunningTimeout + modelDiscoveryRetention - time.Nanosecond,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			now := start
			store := NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{
				Now: func() time.Time { return now },
			})
			seed := ModelDiscoverySeed{
				RuntimeID: uuid.Must(uuid.NewV7()),
				CompanyID: uuid.Must(uuid.NewV7()),
				Provider:  "codex",
			}
			request, _, err := store.Enqueue(ctx, seed)
			if err != nil {
				t.Fatalf("Enqueue() error = %v", err)
			}
			if test.claim {
				if _, err := store.Claim(ctx, seed.RuntimeID); err != nil {
					t.Fatalf("Claim() error = %v", err)
				}
			}
			now = now.Add(test.advance)
			got, err := store.Get(ctx, request.ID)
			if err != nil {
				t.Fatalf("Get() before terminal retention elapsed error = %v", err)
			}
			if got.Status != ModelDiscoveryTimeout {
				t.Fatalf("Get() status = %q, want %q", got.Status, ModelDiscoveryTimeout)
			}
		})
	}
}

func TestInMemoryModelDiscoveryStoreTerminalDuplicateIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runtimeID := uuid.Must(uuid.NewV7())
	store := NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{})
	request, _, err := store.Enqueue(ctx, ModelDiscoverySeed{
		RuntimeID: runtimeID, CompanyID: uuid.Must(uuid.NewV7()), Provider: "codex",
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := store.Claim(ctx, runtimeID); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	first := ModelDiscoveryResult{
		Status: ModelDiscoveryCompleted,
		Models: []provideragent.RuntimeModel{{ID: "gpt-first", Label: "GPT First"}},
	}
	completed, err := store.Finish(ctx, runtimeID, request.ID, first)
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	duplicate, err := store.Finish(ctx, runtimeID, request.ID, ModelDiscoveryResult{
		Status: ModelDiscoveryFailed,
		Error:  &ModelDiscoveryError{Code: "discovery_failed", Message: "later"},
	})
	if err != nil {
		t.Fatalf("Finish(duplicate) error = %v", err)
	}
	if duplicate.Status != ModelDiscoveryCompleted || len(duplicate.Models) != 1 ||
		duplicate.Models[0].ID != completed.Models[0].ID {
		t.Fatalf("duplicate changed terminal request: %+v", duplicate)
	}
}
