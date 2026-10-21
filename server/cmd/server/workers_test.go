package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
)

func TestHumanInputLifecycleWorkerWakesAfterResolution(t *testing.T) {
	t.Parallel()
	for _, name := range []string{approval.EventApprovalResolved, approval.EventQuestionResolved} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				bus, err := events.NewBus(events.BusOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int64
				worker, err := newHumanInputLifecycleWorker(bus, func(context.Context) { calls.Add(1) })
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- worker.run(ctx) }()
				synctest.Wait()
				if calls.Load() != 1 {
					t.Fatalf("startup passes = %d, want 1", calls.Load())
				}

				// A committed decision must still wake the supervised worker
				// after the HTTP request has ended, without waiting for its timer.
				requestCtx, requestCancel := context.WithCancel(ctx)
				requestCancel()
				topic, _, _ := strings.Cut(name, ".")
				bus.Publish(requestCtx, topic, events.Event{
					EntityType: topic, Name: name, CreatedAt: time.Now(),
					Actor: events.SystemActor(), Payload: struct{}{},
				})
				synctest.Wait()
				if calls.Load() != 2 {
					t.Fatalf("passes after resolution = %d, want 2 before the timer", calls.Load())
				}

				time.Sleep(humanInputInterval)
				synctest.Wait()
				if calls.Load() != 3 {
					t.Fatalf("passes after fallback timer = %d, want 3", calls.Load())
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("worker exit = %v, want context cancellation", err)
				}
			})
		})
	}
}

func TestHumanInputLifecycleWorkerCoalescesDecisionsDuringAPass(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		bus, err := events.NewBus(events.BusOptions{})
		if err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		var calls atomic.Int64
		worker, err := newHumanInputLifecycleWorker(bus, func(ctx context.Context) {
			if calls.Add(1) == 1 {
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.run(ctx) }()
		synctest.Wait()
		for range 20 {
			bus.Publish(ctx, approval.Topic, events.Event{
				EntityType: approval.Topic, Name: approval.EventApprovalResolved,
				CreatedAt: time.Now(), Actor: events.SystemActor(), Payload: struct{}{},
			})
		}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("concurrent passes = %d, want only the in-flight pass", calls.Load())
		}
		close(release)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("passes after decision burst = %d, want one follow-up pass", calls.Load())
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("worker exit = %v, want context cancellation", err)
		}
	})
}

func TestPublicationIssueHandlerLogsWorkIdentityAndCause(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	publicationID := uuid.Must(uuid.NewV7())
	newPublicationIssueHandler(logger)(execution.PublicationIssueReport{
		Issue:         execution.PublicationIssuePublishFailed,
		PublicationID: publicationID,
		Attempt:       2,
		ProviderType:  execution.PublicationProviderTypeCloudRun,
		Err:           errors.New("provider offline"),
	})
	got := output.String()
	for _, want := range []string{
		"error_code=publish_failed",
		"publication_id=" + publicationID.String(),
		"attempt=2",
		"provider_type=cloud_run",
		`error="provider offline"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("publication issue log = %q, want %q", got, want)
		}
	}
}
