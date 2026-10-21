package daemonws

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
)

// recordingDaemon records the order of everything the consumer does, which is
// the only way to observe the one invariant this file exists to hold.
type recordingDaemon struct {
	calls []string
}

func (r *recordingDaemon) Bump(_ context.Context, runtimeID string) {
	r.calls = append(r.calls, "bump:"+runtimeID)
}

func (r *recordingDaemon) NotifyRunAvailable(runtimeID, runID string) {
	r.calls = append(r.calls, "wake:"+runtimeID+"/"+runID)
}

func (r *recordingDaemon) NotifyPendingWork(runtimeID, kind string) {
	r.calls = append(r.calls, "pending:"+runtimeID+"/"+kind)
}

func (r *recordingDaemon) NotifyCompaniesChanged(userID string) {
	r.calls = append(r.calls, "companies:"+userID)
}

func daemonBus(t *testing.T) (*events.Bus, *recordingDaemon) {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	daemon := &recordingDaemon{}
	if err := RegisterListeners(bus, daemon, daemon); err != nil {
		t.Fatalf("RegisterListeners() error = %v", err)
	}
	return bus, daemon
}

// The daemon plane routes by runtime and user, never by company, so every
// fact here is published into one company that none of the assertions read.
var testCompany = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func publish(t *testing.T, bus *events.Bus, topic, name string, payload any) {
	t.Helper()
	bus.Publish(context.Background(), topic, events.Event{
		EntityType: topic,
		Name:       name,
		CompanyID:  testCompany,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload:    payload,
	})
}

// The invariant the whole handler exists for: a claim already in flight may be
// about to write an "empty" verdict for this runtime. Bumping first makes that
// verdict stale on read, so the woken claim cannot return null for work that
// already exists. Waking first would leave the run waiting for the next poll.
func TestTheClaimCacheIsBumpedBeforeTheDaemonIsWoken(t *testing.T) {
	t.Parallel()
	bus, daemon := daemonBus(t)

	publish(t, bus, execution.TopicRuntime, execution.EventRuntimeWorkAvailable,
		execution.RuntimeWork{RuntimeID: "runtime-1", RunID: "00000000-0000-4000-8000-000000000042"})

	want := []string{"bump:runtime-1", "wake:runtime-1/00000000-0000-4000-8000-000000000042"}
	if len(daemon.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", daemon.calls, want)
	}
	for i := range want {
		if daemon.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v — the bump must precede the wakeup", daemon.calls, want)
		}
	}
}

// A runtime the fact cannot name addresses nobody, so neither half runs: a
// bump on an empty key would invalidate nothing and a wakeup would go nowhere.
func TestAnUnnamedRuntimeWakesNobody(t *testing.T) {
	t.Parallel()
	bus, daemon := daemonBus(t)

	publish(t, bus, execution.TopicRuntime, execution.EventRuntimeWorkAvailable,
		execution.RuntimeWork{RunID: "00000000-0000-4000-8000-000000000042"})

	if len(daemon.calls) != 0 {
		t.Fatalf("calls = %v, want none for a fact naming no runtime", daemon.calls)
	}
}

// The daemon plane reacts to three separate facts; each reaches its own arm.
func TestEachDaemonFactReachesItsOwnArm(t *testing.T) {
	t.Parallel()
	bus, daemon := daemonBus(t)

	publish(t, bus, agent.TopicRuntime, agent.EventRuntimePendingWork,
		agent.PendingWork{RuntimeID: "runtime-1", Kind: "model_discovery"})
	publish(t, bus, company.Topic, company.EventCompanyMembershipChanged,
		company.Membership{UserID: "user-1"})

	want := []string{"pending:runtime-1/model_discovery", "companies:user-1"}
	if len(daemon.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", daemon.calls, want)
	}
	for i := range want {
		if daemon.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", daemon.calls, want)
		}
	}
}

// company.Topic carries browser-facing facts alongside the wakeup. The daemon
// consumer must ignore everything that is not its own.
func TestTheDaemonConsumerIgnoresTheRestOfTheTopic(t *testing.T) {
	t.Parallel()
	bus, daemon := daemonBus(t)

	publish(t, bus, company.Topic, company.EventCompanyUpdated, company.Response{})

	if len(daemon.calls) != 0 {
		t.Fatalf("calls = %v, want none — company.updated is a browser frame", daemon.calls)
	}
}
