package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// ModelSettings are the next Run's choices, independent of the Agent defaults.
// Null delegates to the provider. An unstarted Task chat reads the Agent defaults until its first Run or edit.
type ModelSettings struct {
	AgentID       uuid.UUID     `json:"agentId"`
	Provider      string        `json:"provider"`
	Model         *string       `json:"model"`
	ThinkingLevel *string       `json:"thinkingLevel"`
	Revision      int64         `json:"revision"`
	CanEdit       bool          `json:"canEdit"`
	Changes       []ModelChange `json:"changes"`
}

// ModelChange is a committed settings change anchored in the Conversation stream.
type ModelChange struct {
	ID            uuid.UUID `json:"id"`
	AgentID       uuid.UUID `json:"agentId"`
	PreviousModel *string   `json:"previousModel"`
	Model         *string   `json:"model"`
	AfterSeq      int64     `json:"afterSeq"`
	CreatedAt     time.Time `json:"createdAt"`
}

type ModelSettingsInput struct {
	AgentID          uuid.UUID
	ExpectedRevision int64
	Model            *string
	ThinkingLevel    *string
}

func normalizeModelSettings(in ModelSettingsInput, provider string) (ModelSettingsInput, error) {
	if in.AgentID == uuid.Nil || in.ExpectedRevision < 1 || in.ExpectedRevision > protocol.MaxJSONSafeInteger {
		return ModelSettingsInput{}, ErrInvalidInput
	}
	for _, field := range []struct {
		value **string
		limit int
	}{{&in.Model, 256}, {&in.ThinkingLevel, 64}} {
		if *field.value == nil {
			continue
		}
		value := strings.TrimSpace(**field.value)
		if value == "" || utf8.RuneCountInString(value) > field.limit {
			return ModelSettingsInput{}, ErrInvalidInput
		}
		*field.value = &value
	}
	if in.ThinkingLevel != nil && !provideragent.KnownThinkingLevel(provider, *in.ThinkingLevel) {
		return ModelSettingsInput{}, ErrInvalidInput
	}
	return in, nil
}

func (s *DefaultConversationService) FetchModelSettings(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (ModelSettings, error) {
	target, err := resolveMessageTarget(
		ctx,
		s.store.Queries(),
		p.UserID,
		MessageTarget{Kind: TargetConversation, ID: id},
	)
	if err != nil {
		return ModelSettings{}, err
	}
	return readModelSettings(ctx, s.store.Queries(), p.UserID, target)
}

func readModelSettings(
	ctx context.Context,
	q *generated.Queries,
	userID uuid.UUID,
	target resolvedConversationTarget,
) (ModelSettings, error) {
	if target.AgentID == nil {
		return ModelSettings{}, ErrNotFound
	}
	row, err := q.GetConversationModelSettings(ctx, generated.GetConversationModelSettingsParams{
		CompanyID: target.CompanyID, ConversationID: target.ConversationID, AgentID: *target.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelSettings{}, ErrNotFound
	}
	if err != nil {
		return ModelSettings{}, fmt.Errorf("read chat model settings: %w", err)
	}
	canEdit := target.TaskID == nil
	if target.TaskID != nil && target.AssigneeUserID != nil && *target.AssigneeUserID == userID &&
		target.ProjectID != nil {
		canEdit, err = auth.HoldsProjectPermission(
			ctx,
			q,
			target.CompanyID,
			*target.ProjectID,
			userID,
			auth.ProjectPermissionTasksExecute,
		)
		if err != nil {
			return ModelSettings{}, err
		}
	}
	rows, err := q.ListConversationModelChanges(ctx, generated.ListConversationModelChangesParams{
		CompanyID: target.CompanyID, ConversationID: target.ConversationID,
	})
	if err != nil {
		return ModelSettings{}, fmt.Errorf("read chat model changes: %w", err)
	}
	changes := make([]ModelChange, 0, len(rows))
	for _, change := range rows {
		changes = append(changes, ModelChange{
			ID: change.ID, AgentID: change.AgentID,
			PreviousModel: util.TextToPtr(change.PreviousModel), Model: util.TextToPtr(change.Model),
			AfterSeq: change.AfterSeq, CreatedAt: change.CreatedAt.Time,
		})
	}
	return ModelSettings{
		AgentID:       row.AgentID,
		Provider:      row.Provider,
		Model:         util.TextToPtr(util.StrToText(row.Model)),
		ThinkingLevel: util.TextToPtr(util.StrToText(row.ThinkingLevel)),
		Revision:      row.Revision,
		CanEdit:       canEdit,
		Changes:       changes,
	}, nil
}

func (s *DefaultConversationService) UpdateModelSettings(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	in ModelSettingsInput,
) (ModelSettings, error) {
	var result ModelSettings
	var pending []events.Event
	err := s.store.WithinTx(ctx, conversationWriteTxOptions, func(q *generated.Queries) error {
		pending = nil
		target, err := resolveMessageTarget(ctx, q, p.UserID, MessageTarget{Kind: TargetConversation, ID: id})
		if err != nil {
			return err
		}
		if err := lockActiveMembership(ctx, q, target.CompanyID, p.UserID); err != nil {
			return err
		}
		if target.ProjectID != nil {
			if _, err := q.LockProjectForAccessRead(
				ctx,
				generated.LockProjectForAccessReadParams{
					CompanyID: target.CompanyID,
					ProjectID: *target.ProjectID,
				},
			); err != nil {
				return err
			}
		}
		if err := lockConversation(ctx, q, p.UserID, id, target.CompanyID); err != nil {
			return err
		}
		if target.TaskID != nil {
			if _, err := q.GetTaskForCompanyByIDForUpdate(
				ctx,
				generated.GetTaskForCompanyByIDForUpdateParams{
					CompanyID: target.CompanyID,
					TaskID:    *target.TaskID,
				},
			); err != nil {
				return err
			}
		}
		// Re-resolve after locking: a Task may have been handed to another member/Agent.
		target, err = resolveMessageTarget(ctx, q, p.UserID, MessageTarget{Kind: TargetConversation, ID: id})
		if err != nil {
			return err
		}
		current, err := readModelSettings(ctx, q, p.UserID, target)
		if err != nil {
			return err
		}
		if !current.CanEdit {
			return ErrModelSettingsForbidden
		}
		if current.AgentID != in.AgentID || current.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		in, err = normalizeModelSettings(in, current.Provider)
		if err != nil {
			return err
		}
		if err := q.InitializeConversationModelSettings(
			ctx,
			generated.InitializeConversationModelSettingsParams{ConversationID: id, AgentID: in.AgentID},
		); err != nil {
			return err
		}
		count, err := q.UpdateConversationModelSettings(ctx, generated.UpdateConversationModelSettingsParams{
			ConversationID: id,
			AgentID:        in.AgentID,
			Model:          util.PtrToText(in.Model),
			ThinkingLevel:  util.PtrToText(in.ThinkingLevel),
		})
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrConflict
		}
		if util.PtrToText(current.Model) != util.PtrToText(in.Model) {
			count, err := q.InsertConversationModelChange(
				ctx,
				generated.InsertConversationModelChangeParams{
					CompanyID: target.CompanyID, ConversationID: id, AgentID: in.AgentID,
					ChangedByUserID: p.UserID,
					PreviousModel:   util.PtrToText(current.Model), Model: util.PtrToText(in.Model),
				},
			)
			if err != nil {
				return fmt.Errorf("record chat model change: %w", err)
			}
			if count == 0 {
				return ErrNotFound
			}
		}
		value, err := bumpConversationProjection(
			ctx,
			q,
			id,
			target.CompanyID,
			&pending,
			events.UserActor(p.UserID.String()),
		)
		if err != nil {
			return err
		}
		summary, err := conversationListSummary(ctx, q, p.UserID, id, target.CompanyID)
		if err != nil {
			return err
		}
		if err := stageInTx(
			ctx,
			q,
			&pending,
			newConversationEvent(p.UserID, target.TaskID, value, EventConversationUpdated, &summary),
		); err != nil {
			return err
		}
		result, err = readModelSettings(ctx, q, p.UserID, target)
		return err
	})
	if err != nil {
		return ModelSettings{}, fmt.Errorf("update chat model settings: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}
