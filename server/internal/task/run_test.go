package task

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// The assignee is always the member column; executedBy is the executing
// Agent when the row names one, else the assignee again (ADR 0014).
func TestTaskFromRowMapsAssigneeAndExecutedBy(t *testing.T) {
	t.Parallel()

	row := validGeneratedTask()
	value := taskFromRow(row)
	if value.ID != row.ID ||
		value.Assignee != (ActorRef{Kind: ActorUser, ID: row.AssigneeUserID}) ||
		value.ExecutedBy != (ActorRef{Kind: ActorAgent, ID: row.ExecutedByAgentID.UUID}) ||
		value.Creator != (ActorRef{Kind: ActorUser, ID: row.CreatorUserID.UUID}) ||
		value.ExecutionGeneration != row.ExecutionGeneration {
		t.Fatalf("taskFromRow() = %+v", value)
	}
	row.ExecutedByAgentID = uuid.NullUUID{}
	if human := taskFromRow(row); human.ExecutedBy != human.Assignee {
		t.Fatalf("taskFromRow(no agent).ExecutedBy = %+v, want the assignee %+v", human.ExecutedBy, human.Assignee)
	}
}

// taskEvent is one recorded task.* event with its payload already re-typed:
// the envelope on the bus carries an untyped payload, so the subscription
// types it once and the assertions read it typed.
type taskEvent struct {
	Name           string
	Actor          events.Actor
	Payload        Response
	TaskID         uuid.UUID
	ConversationID uuid.UUID
	RunID          uuid.UUID
}

// A published event is opaque, so these read the event where everything else
// reads it: at a subscriber, after publication.
func recordTaskEvents(t *testing.T, name string) (*events.Bus, *[]taskEvent) {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	received := &[]taskEvent{}
	if err := bus.Subscribe(Topic,
		func(_ context.Context, event events.Event) error {
			if event.Name != name {
				return nil
			}
			payload, err := events.PayloadOf[Response](event)
			if err != nil {
				return err
			}
			*received = append(*received, taskEvent{
				Name: event.Name, Actor: event.Actor, Payload: payload,
				TaskID:         event.TaskID,
				ConversationID: event.ConversationID,
				RunID:          event.RunID,
			})
			return nil
		}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	return bus, received
}

// The event carries the committed Task and nothing else — the same bytes the
// REST surface serves. What changed is not on the payload: the audit log
// stores each committed state, so a diff is two rows apart rather than
// something every producer has to compute and carry.
func TestTaskEventCarriesTheCommittedResponse(t *testing.T) {
	t.Parallel()

	actorID := uuid.Must(uuid.NewV7())
	value := taskFromRow(validGeneratedTask())

	bus, created := recordTaskEvents(t, EventCreated)
	built := newTaskEvent(actorID, EventCreated, value)
	bus.Publish(context.Background(), built.EntityType, built)

	if len(*created) != 1 {
		t.Fatalf("task.created events = %d, want 1", len(*created))
	}
	event := (*created)[0]
	if event.Name != "task.created" || event.Actor != events.UserActor(actorID.String()) {
		t.Fatalf("task.created envelope = %+v", event)
	}
	if !reflect.DeepEqual(event.Payload, value) {
		t.Fatalf("task.created payload = %+v, want the canonical response", event.Payload)
	}
	// A task fact is about the task and the conversation the task owns, and
	// the envelope says so — a consumer does not have to know the shape of a
	// task projection to find out which rows moved.
	if event.TaskID != value.ID {
		t.Fatalf("TaskID = %v, want the task %v", event.TaskID, value.ID)
	}
	if event.ConversationID != value.ConversationID {
		t.Fatalf("ConversationID = %v, want %v", event.ConversationID, value.ConversationID)
	}
	if event.RunID != uuid.Nil {
		t.Fatalf("RunID = %v, want absent — creating a task runs nothing", event.RunID)
	}

	value.Revision = 2
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	value.Status = StatusInProgress

	updatedBus, updated := recordTaskEvents(t, EventUpdated)
	built = newTaskEvent(actorID, EventUpdated, value)
	updatedBus.Publish(context.Background(), built.EntityType, built)

	if len(*updated) != 1 {
		t.Fatalf("task.updated events = %d, want 1", len(*updated))
	}
	if !reflect.DeepEqual((*updated)[0].Payload, value) {
		t.Fatalf("task.updated payload = %+v, want the committed task", (*updated)[0].Payload)
	}
}

func validGeneratedTask() generated.Task {
	now := pgtype.Timestamptz{Time: time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC), Valid: true}
	return generated.Task{
		ID:                  uuid.Must(uuid.NewV7()),
		Key:                 "repair_release",
		CompanyID:           uuid.Must(uuid.NewV7()),
		ConversationID:      uuid.Must(uuid.NewV7()),
		Number:              42,
		Title:               "Repair release",
		Description:         "Inspect integration tests",
		Status:              "todo",
		Priority:            "high",
		AssigneeUserID:      uuid.Must(uuid.NewV7()),
		ExecutedByAgentID:   uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true},
		CreatorUserID:       uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true},
		ExecutionGeneration: 3,
		CreatedAt:           now,
		UpdatedAt:           now,
		Version:             1,
	}
}
