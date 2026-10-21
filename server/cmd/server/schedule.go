package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	"github.com/Intelligent-Internet/zenith-team/server/internal/telemetry"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type scheduleComponents struct {
	service      *schedule.DefaultScheduleService
	firingWorker *schedule.FiringWorker
}

func newScheduleComponents(
	store *db.Store,
	bus *events.Bus,
	logger *slog.Logger,
	registry *telemetry.Registry,
	interval time.Duration,
) (scheduleComponents, error) {
	conversationUpdater := schedule.ConversationUpdaterFunc(func(
		ctx context.Context,
		queries *generated.Queries,
		pending *[]events.Event,
		input schedule.UpdateConversationInput,
	) error {
		return conversation.UpdateScheduleConversationInTx(
			ctx,
			queries,
			pending,
			conversation.UpdateScheduleConversationInput{
				ID: input.ID, ScheduleID: input.ScheduleID, CompanyID: input.CompanyID,
				CreatorUserID: input.CreatorUserID, AgentID: input.AgentID, Title: input.Title,
			},
		)
	})
	materializer := schedule.MaterializerFunc(func(
		ctx context.Context,
		queries *generated.Queries,
		pending *[]events.Event,
		input schedule.MaterializeInput,
	) (schedule.MaterializeResult, error) {
		result, err := conversation.MaterializeScheduleRunInTx(
			ctx,
			queries,
			pending,
			conversation.ScheduleRunInput{
				ScheduleID: input.ScheduleID, CompanyID: input.CompanyID,
				ConversationID: input.ConversationID, CreatorUserID: input.CreatorUserID,
				AgentID: input.AgentID, Provider: input.Provider,
				Instructions: input.Instructions,
				OccurrenceAt: input.OccurrenceAt,
			},
		)
		if errors.Is(err, conversation.ErrConflict) {
			// The Schedule's Conversation or Agent can no longer take a Run.
			return schedule.MaterializeResult{}, errors.Join(schedule.ErrConflict, err)
		}
		if err != nil {
			return schedule.MaterializeResult{}, err
		}
		return schedule.MaterializeResult{
			Disposition: schedule.FiringDispositionMaterialized,
			RunID:       result.RunID,
			MessageID:   result.MessageID,
		}, nil
	})
	superseder := schedule.QueuedRunSupersederFunc(func(
		ctx context.Context,
		queries *generated.Queries,
		pending *[]events.Event,
		input schedule.SupersedeQueuedRunInput,
	) error {
		return execution.SupersedeScheduleRunInTx(
			ctx,
			queries,
			pending,
			execution.SupersedeScheduleRunInput{
				RunID: input.RunID, FiringID: input.FiringID, ScheduleID: input.ScheduleID,
				CompanyID: input.CompanyID, ConversationID: input.ConversationID,
			},
		)
	})
	invalidator := schedule.QueuedRunInvalidatorFunc(func(
		ctx context.Context,
		queries *generated.Queries,
		pending *[]events.Event,
		input schedule.InvalidateQueuedRunInput,
	) error {
		return execution.InvalidateScheduleRunInTx(
			ctx,
			queries,
			pending,
			execution.ScheduleRunCancellationInput{
				RunID: input.RunID, FiringID: input.FiringID, ScheduleID: input.ScheduleID,
				CompanyID: input.CompanyID, ConversationID: input.ConversationID,
			},
		)
	})

	service, err := schedule.NewScheduleService(schedule.ServiceOptions{
		Store:               store,
		Bus:                 bus,
		ConversationUpdater: conversationUpdater,
		Materializer:        materializer,
		OnFired:             registry.ObserveScheduleFiring,
	})
	if err != nil {
		return scheduleComponents{}, fmt.Errorf("construct Schedule service: %w", err)
	}
	firingWorker, err := schedule.NewFiringWorker(
		schedule.FiringWorkerDependencies{
			Store: store, Bus: bus, Materializer: materializer,
			Superseder: superseder, Invalidator: invalidator,
		},
		schedule.FiringWorkerOptions{
			SweepInterval: interval, OnIssue: newScheduleFiringIssueHandler(logger),
			OnFired: registry.ObserveScheduleFiring, OnPass: registry.ObserveScheduleSweep,
		},
	)
	if err != nil {
		return scheduleComponents{}, fmt.Errorf("construct Schedule firing worker: %w", err)
	}
	return scheduleComponents{service: service, firingWorker: firingWorker}, nil
}
