// This file holds the topic, the event names, and the payloads that exist
// only to be published. A payload the REST surface also serves — RunSnapshot,
// the body of the REST read model — lives with the domain's other types: the
// event carries the domain's projection, it does not own it.
//
// Who may see an event lives in cmd/server/realtime_routing.go — a comment
// here says what fact an event records, never who receives it, because a rule
// written in two places is a rule that drifts.

package execution

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// Topic carries everything a run says about itself. The lifecycle names ride
// a RunSnapshot and the transition is the name — the status is in the payload
// so a client can cross-check the two on arrival. run.message rides a
// TranscriptItem instead: the same domain, a different fact with a different
// shape, so a subscriber switches on Event.Name before typing the payload.
const Topic = "run"

// The run.* wire names.
const (
	EventRunStudioCanvasAdded     = "run.studio_canvas_added"
	EventRunQueued                = "run.queued"
	EventRunDispatched            = "run.dispatched"
	EventRunAuthWaitChanged       = "run.auth_wait_changed"
	EventRunRunning               = "run.running"
	EventRunWaitingLocalDirectory = "run.waiting_local_directory"
	EventRunCompleted             = "run.completed"
	EventRunFailed                = "run.failed"
	EventRunCancelled             = "run.cancelled"

	// EventRunMessageCreated carries one durable transcript item, as it commits.
	EventRunMessageCreated = "run.message_created"
)

// TranscriptItem is one committed transcript item, plus the facts that decide who
// may see it. The wire half is protocol.RunMessagePayload unchanged.
type TranscriptItem struct {
	protocol.RunMessagePayload
	// CompanyID and the conversation route are process-local routing facts.
	CompanyID uuid.UUID `json:"-"`
	ConversationRoute
}

// TopicRuntime is what a runtime says about its own work queue. Nothing
// forwards it to a browser — that absence is what makes it internal.
const TopicRuntime = "runtime"

// EventRuntimeWorkAvailable says a runtime may have claimable work.
const EventRuntimeWorkAvailable = "runtime.work_available"

// RuntimeWork names the runtime that should look for work, and optionally the
// run that prompted it.
type RuntimeWork struct {
	RuntimeID string
	RunID     string
}

// PublishRuntimeWork says this run is there to be claimed, and names it so
// the daemon can go straight for it.
func PublishRuntimeWork(ctx context.Context, bus *events.Bus, run generated.Run) {
	publishRuntimeWork(ctx, bus, run, run.ID.String())
}

// PublishRuntimeUnblocked says this run reached a terminal state, so a queued
// successor on the same runtime may have become claimable. It deliberately
// names no run: the one that just finished is not there to claim, and naming
// it would send the daemon after work that no longer exists.
func PublishRuntimeUnblocked(ctx context.Context, bus *events.Bus, run generated.Run) {
	publishRuntimeWork(ctx, bus, run, "")
}

// publishRuntimeWork is the one place runtime.work_available is built, shared
// by every domain whose transaction moves a run, so the fact is said the same
// way everywhere. The run names the runtime to wake and the company it
// belongs to.
//
// A staging failure is logged and dropped rather than returned: the caller's
// transaction has already committed, so there is nothing left to abort, and
// the 30-second claim poll bounds how long a lost wakeup can stall the run.
func publishRuntimeWork(ctx context.Context, bus *events.Bus, run generated.Run, runID string) {
	if bus == nil || run.RuntimeID == uuid.Nil {
		return
	}
	bus.Publish(ctx, TopicRuntime, events.Event{
		EntityType:     TopicRuntime,
		Name:           EventRuntimeWorkAvailable,
		CompanyID:      run.CompanyID,
		CreatedAt:      time.Now().UTC(),
		RunID:          run.ID,
		TaskID:         util.NullUUIDToValue(run.TaskID),
		ConversationID: run.ConversationID,
		Actor:          events.SystemActor(),
		Payload:        RuntimeWork{RuntimeID: run.RuntimeID.String(), RunID: runID},
	})
}
