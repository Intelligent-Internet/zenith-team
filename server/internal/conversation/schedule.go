package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const maxScheduleConversationTitleRunes = 240

// UpdateScheduleConversationInput keeps the private Schedule conversation in
// step with its reviewed definition. The Schedule domain owns the transaction.
type UpdateScheduleConversationInput struct {
	ID            uuid.UUID
	ScheduleID    uuid.UUID
	CompanyID     uuid.UUID
	CreatorUserID uuid.UUID
	AgentID       uuid.UUID
	Title         string
}

// UpdateScheduleConversationInTx is the cross-domain Schedule write seam.
func UpdateScheduleConversationInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input UpdateScheduleConversationInput,
) error {
	if queries == nil || !validUpdateScheduleConversationInput(input) {
		return ErrInvalidInput
	}
	if _, err := queries.LockConversationForActiveUserMutation(
		ctx,
		generated.LockConversationForActiveUserMutationParams{
			ActorUserID: input.CreatorUserID, CompanyID: input.CompanyID, ConversationID: input.ID,
		},
	); err != nil {
		return fmt.Errorf("lock Schedule Conversation history: %w", err)
	}
	before, err := activity.ObserveInTx(ctx, queries, Topic, input.ID)
	if err != nil {
		return err
	}
	_, err = queries.UpdateScheduleConversationDefinition(
		ctx,
		generated.UpdateScheduleConversationDefinitionParams{
			// A Schedule conversation follows its creator, and the creator must
			// still hold schedules.manage for the definition to move.
			ActorRoles: auth.RolesWith(auth.PermissionSchedulesManage),
			AgentID:    input.AgentID, Title: input.Title, ActorUserID: input.CreatorUserID,
			ConversationID: input.ID, CompanyID: input.CompanyID, ScheduleID: input.ScheduleID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("update Schedule Conversation definition: %w", err)
	}
	return activity.RecordInTx(
		ctx,
		queries,
		pending,
		events.UserActor(input.CreatorUserID.String()),
		EventConversationUpdated,
		before,
		Topic,
		input.ID,
	)
}

// ScheduleRunInput is the frozen input used to materialize one occurrence into
// an ordinary queued Run without creating a product Task.
type ScheduleRunInput struct {
	ScheduleID     uuid.UUID
	CompanyID      uuid.UUID
	ConversationID uuid.UUID
	CreatorUserID  uuid.UUID
	AgentID        uuid.UUID
	Provider       string
	Instructions   string
	OccurrenceAt   time.Time
}

// ScheduleRunResult contains identities PostgreSQL allocated for the new turn.
type ScheduleRunResult struct {
	RunID     uuid.UUID
	MessageID uuid.UUID
}

// MaterializeScheduleRunInTx inserts a queued Run, its prompt item, and the
// conversation projections inside the caller's firing transaction. The
// Schedule domain links the returned Run to the database-generated firing ID.
func MaterializeScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input ScheduleRunInput,
) (ScheduleRunResult, error) {
	if queries == nil || pending == nil || !validScheduleRunInput(input) {
		return ScheduleRunResult{}, ErrInvalidInput
	}
	conversationRow, err := queries.LockConversationForActiveUserMutation(
		ctx,
		generated.LockConversationForActiveUserMutationParams{
			ActorUserID: input.CreatorUserID, ConversationID: input.ConversationID,
			CompanyID: input.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduleRunResult{}, ErrConflict
	}
	if err != nil {
		return ScheduleRunResult{}, fmt.Errorf("lock Schedule Conversation: %w", err)
	}
	if conversationRow.Kind != string(KindSchedule) || !conversationRow.ScheduleID.Valid ||
		conversationRow.ScheduleID.UUID != input.ScheduleID || !conversationRow.AgentID.Valid ||
		conversationRow.AgentID.UUID != input.AgentID || !conversationRow.CreatedByUserID.Valid ||
		conversationRow.CreatedByUserID.UUID != input.CreatorUserID {
		return ScheduleRunResult{}, ErrConflict
	}

	// The firing runs on the creator's own binding of the Agent (ADR 0014).
	agent, err := queries.GetActiveScheduleAgent(ctx, generated.GetActiveScheduleAgentParams{
		AgentID: input.AgentID, CompanyID: input.CompanyID, CreatorUserID: input.CreatorUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A binding the creator removed is the enqueue's own refusal, which
		// the caller can name as a setup step; anything else is a conflict.
		needsBinding, classifyErr := queries.ScheduleAgentNeedsCreatorBinding(
			ctx,
			generated.ScheduleAgentNeedsCreatorBindingParams{
				CreatorUserID: input.CreatorUserID, AgentID: input.AgentID, CompanyID: input.CompanyID,
			},
		)
		if classifyErr != nil {
			return ScheduleRunResult{}, fmt.Errorf("classify unavailable Schedule Agent: %w", classifyErr)
		}
		if needsBinding {
			return ScheduleRunResult{}, execution.ErrRunAgentNotBound
		}
		return ScheduleRunResult{}, ErrConflict
	}
	if err != nil {
		return ScheduleRunResult{}, fmt.Errorf("load Schedule Agent: %w", err)
	}
	if agent.Provider != input.Provider {
		return ScheduleRunResult{}, ErrConflict
	}

	run, err := execution.EnqueueRunInTx(ctx, queries, pending, execution.RunTrigger{
		CompanyID: input.CompanyID, ConversationID: input.ConversationID,
		AgentID: input.AgentID, TriggerPrompt: input.Instructions,
		Source: execution.RunSourceSchedule, ActorUserID: input.CreatorUserID,
		AuthorizedByUserID: input.CreatorUserID,
		ForceFreshSession:  true,
	})
	if err != nil {
		return ScheduleRunResult{}, fmt.Errorf("enqueue Schedule Run: %w", err)
	}
	messageRow, err := queries.InsertUserMessageAfterConversationLock(
		ctx,
		generated.InsertUserMessageAfterConversationLockParams{
			RunID: run.ID, Content: input.Instructions, ActorUserID: input.CreatorUserID,
			ConversationID: input.ConversationID, CompanyID: input.CompanyID,
		},
	)
	if err != nil {
		return ScheduleRunResult{}, fmt.Errorf("insert Schedule prompt: %w", err)
	}
	bumped, err := bumpConversationProjection(
		ctx,
		queries,
		input.ConversationID,
		input.CompanyID,
		pending,
		events.SystemActor(),
	)
	if err != nil {
		return ScheduleRunResult{}, err
	}
	message := messageFromRow(messageRow)
	view, err := messageViewWithAttachments(ctx, queries, input.CreatorUserID, input.CompanyID, message)
	if err != nil {
		return ScheduleRunResult{}, err
	}
	summary, err := conversationListSummary(
		ctx, queries, input.CreatorUserID, input.ConversationID, input.CompanyID,
	)
	if err != nil {
		return ScheduleRunResult{}, err
	}
	*pending = append(
		*pending,
		newMessageCreatedEvent(input.CreatorUserID, bumped, view),
		newConversationEvent(input.CreatorUserID, nil, bumped, EventConversationUpdated, &summary),
	)
	return ScheduleRunResult{RunID: run.ID, MessageID: messageRow.ID}, nil
}

func validUpdateScheduleConversationInput(input UpdateScheduleConversationInput) bool {
	return util.ValidUUID(input.ID) && util.ValidUUID(input.ScheduleID) &&
		util.ValidUUID(input.CompanyID) && util.ValidUUID(input.CreatorUserID) &&
		util.ValidUUID(input.AgentID) && validScheduleConversationTitle(input.Title)
}

func validScheduleConversationTitle(value string) bool {
	return value != "" && utf8.ValidString(value) && protocol.TrimContractWhitespace(value) == value &&
		utf8.RuneCountInString(value) <= maxScheduleConversationTitleRunes
}

func validScheduleRunInput(input ScheduleRunInput) bool {
	return util.ValidUUID(input.ScheduleID) && util.ValidUUID(input.CompanyID) &&
		util.ValidUUID(input.ConversationID) && util.ValidUUID(input.CreatorUserID) &&
		util.ValidUUID(input.AgentID) && input.Provider != "" && input.Instructions != "" &&
		!input.OccurrenceAt.IsZero()
}
