package main

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// registerTaskReadiness keeps every member's view of whether a Task can
// start current. That is read live off the assignee's setup of the
// executing Agent and the computer it names (ADR 0014), so setting an Agent
// up, moving or removing it, and a computer coming online, going offline or
// signing out move no Task row and publish no task frame of their own. The
// agent frames that record them reach that member alone, so the member's
// open Tasks those Agents execute are published again, each to its
// Project's readers (ADR 0017).
func registerTaskReadiness(bus *events.Bus, queries *generated.Queries) error {
	return errors.Join(
		bus.Subscribe(agent.EventAgentBindingChanged, func(ctx context.Context, e events.Event) error {
			change, err := events.PayloadOf[agent.BindingChange](e)
			if err != nil {
				return err
			}
			return task.PublishReadinessChanged(
				ctx, queries, bus, change.CompanyID, change.UserID, []uuid.UUID{change.AgentID},
			)
		}),
		bus.Subscribe(agent.EventAgentStatusChanged, func(ctx context.Context, e events.Event) error {
			change, err := events.PayloadOf[agent.AvailabilityChange](e)
			if err != nil {
				return err
			}
			agentIDs := make([]uuid.UUID, len(change.Agents))
			for index, availability := range change.Agents {
				agentIDs[index] = availability.AgentID
			}
			return task.PublishReadinessChanged(
				ctx,
				queries,
				bus,
				change.CompanyID,
				change.UserID,
				agentIDs,
			)
		}),
	)
}
