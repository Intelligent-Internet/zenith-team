package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/events"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// lockActiveMembership serializes the acting member against concurrent
// membership mutations and fails with ErrNotFound unless the member is still
// active inside the caller's transaction.
func lockActiveMembership(
	ctx context.Context,
	q *generated.Queries,
	companyID, actorID uuid.UUID,
) error {
	if _, err := q.LockCompanyForMembershipMutation(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock Company for Conversation mutation: %w", err)
	}
	membership, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: companyID, UserID: actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock acting Company member for Conversation mutation: %w", err)
	}
	return nil
}

// directConversationByAgent resolves the persistent chat boundary for one
// creator and Agent. CreateDirect locks that creator's membership row before
// calling this helper, serializing concurrent opens before either can insert.
func directConversationByAgent(
	ctx context.Context,
	q *generated.Queries,
	actorID, companyID, agentID uuid.UUID,
) (Response, error) {
	row, err := q.GetDirectConversationByAgent(ctx, generated.GetDirectConversationByAgentParams{
		ActorUserID: actorID, CompanyID: companyID, AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, ErrNotFound
	}
	if err != nil {
		return Response{}, fmt.Errorf("look up direct Conversation by Agent: %w", err)
	}
	return conversationFromRow(row), nil
}

func insertDirectConversation(
	ctx context.Context,
	q *generated.Queries,
	actorID uuid.UUID,
	input CreateDirectInput,
	companyID uuid.UUID,
) (Response, error) {
	row, err := q.InsertDirectConversation(ctx, generated.InsertDirectConversationParams{
		ActorUserID: actorID, AgentID: input.AgentID, CompanyID: companyID,
		Title: util.PtrToText(input.Title), RequestKey: util.NullUUIDFromPtr(input.RequestKey),
	})
	if err != nil {
		return Response{}, mapConversationMutationError("insert direct Conversation", err)
	}
	if err := q.InitializeConversationModelSettings(
		ctx,
		generated.InitializeConversationModelSettingsParams{ConversationID: row.ID, AgentID: input.AgentID},
	); err != nil {
		return Response{}, fmt.Errorf("initialize chat model settings: %w", err)
	}
	return conversationFromRow(row), nil
}

func directConversationPage(
	ctx context.Context,
	q *generated.Queries,
	params generated.ListDirectConversationsForCreatorParams,
) (DirectPage, error) {
	rows, err := q.ListDirectConversationsForCreator(ctx, params)
	if err != nil {
		return DirectPage{}, fmt.Errorf("list direct Conversations: %w", err)
	}
	values, err := buildDirectListItems(ctx, q, params.ActorUserID, params.CompanyID, rows)
	if err != nil {
		return DirectPage{}, fmt.Errorf("build direct Conversation page: %w", err)
	}
	return DirectPage{Conversations: values}, nil
}

func buildDirectListItems(
	ctx context.Context,
	q *generated.Queries,
	actorID, companyID uuid.UUID,
	rows []generated.Conversation,
) ([]ListItem, error) {
	if len(rows) == 0 {
		return make([]ListItem, 0), nil
	}
	conversationIDs := make([]uuid.UUID, len(rows))
	for index, row := range rows {
		conversationIDs[index] = row.ID
	}
	bases, err := q.ListDirectConversationSummaryBasesBulk(
		ctx,
		generated.ListDirectConversationSummaryBasesBulkParams{
			ActorUserID:     actorID,
			ConversationIds: conversationIDs,
			CompanyID:       companyID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list direct Conversation summaries in bulk: %w", err)
	}
	messages, err := q.ListDirectConversationLatestMessagesBulk(
		ctx,
		generated.ListDirectConversationLatestMessagesBulkParams{
			ActorUserID:     actorID,
			ConversationIds: conversationIDs,
			CompanyID:       companyID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list latest direct Conversation Messages in bulk: %w", err)
	}
	latestRuns, err := q.ListDirectConversationLatestRunsBulk(
		ctx,
		generated.ListDirectConversationLatestRunsBulkParams{
			ActorUserID:     actorID,
			ConversationIds: conversationIDs,
			CompanyID:       companyID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list latest direct Conversation Runs in bulk: %w", err)
	}
	agentRefs, err := q.ListDirectConversationAgentRefsBulk(
		ctx,
		generated.ListDirectConversationAgentRefsBulkParams{
			ActorUserID:     actorID,
			ConversationIds: conversationIDs,
			CompanyID:       companyID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list direct Conversation agent refs in bulk: %w", err)
	}
	return assembleDirectListItems(actorID, rows, bases, messages, latestRuns, agentRefs), nil
}

// agentRefFromBulkRow projects the agent columns of a bulk row. A
// conversation whose agent was deleted has a null agent_id, and there the
// ref is nil rather than a zero-valued agent — the column is SET NULL
// precisely so the row can still list. The query already answered for the
// viewer who asked (ADR 0014): `bound` is whether they have set the agent up
// on one of their own runtimes, `runtime_available` whether that runtime is
// usable now.
func agentRefFromBulkRow(row generated.ListDirectConversationAgentRefsBulkRow) *AgentRef {
	if !row.AgentID.Valid {
		return nil
	}
	return &AgentRef{
		ID:               row.AgentID.UUID,
		Name:             row.AgentName.String,
		Title:            row.AgentTitle.String,
		RuntimeAvailable: row.RuntimeAvailable,
		RuntimeAccess:    protocol.RuntimeAccess{Execute: row.Bound},
	}
}

// assembleDirectListItems merges the five bulk projections into per-user list
// items without touching the database, so the fixed bulk-query count of
// directConversationPage stays structural rather than asserted.
func assembleDirectListItems(
	actorID uuid.UUID,
	rows []generated.Conversation,
	bases []generated.ListDirectConversationSummaryBasesBulkRow,
	messages []generated.ListDirectConversationLatestMessagesBulkRow,
	latestRuns []generated.ListDirectConversationLatestRunsBulkRow,
	agentRefs []generated.ListDirectConversationAgentRefsBulkRow,
) []ListItem {
	agentByConversation := make(map[uuid.UUID]*AgentRef, len(agentRefs))
	for _, ref := range agentRefs {
		agentByConversation[ref.ConversationID] = agentRefFromBulkRow(ref)
	}
	items := make([]ListItem, len(rows))
	summaryByConversation := make(map[uuid.UUID]ListSummary, len(bases))
	for _, base := range bases {
		summaryByConversation[base.ConversationID] = ListSummary{
			UpdatedAt: base.UpdatedAt.Time.UTC(),
			Runs:      RunsSummary{QueuedCount: base.QueuedRunCount, ActiveCount: base.ActiveRunCount},
		}
	}
	for _, message := range messages {
		conversationID := message.ConversationID
		summary := summaryByConversation[conversationID]
		summary.LastMessage = newLastMessageSummary(
			message.Seq, message.ContentPreview, message.ContentTruncated,
			message.CreatedAt, message.Type, message.AuthorKind,
		)
		summaryByConversation[conversationID] = summary
	}
	for _, run := range latestRuns {
		conversationID := run.ConversationID
		summary := summaryByConversation[conversationID]
		summary.Runs.Latest = newLatestRunSummary(
			run.RunID, run.AgentID, run.Status, run.CreatedAt,
			run.FailureReason,
			run.Attempt,
		)
		summaryByConversation[conversationID] = summary
	}

	for index, row := range rows {
		conversation := conversationFromRow(row)
		summary := summaryByConversation[conversation.ID]
		state := userStateFromConversationRow(row)
		items[index] = ListItem{
			Conversation: conversation, Agent: agentByConversation[conversation.ID],
			Summary: summary, UserState: state,
		}
	}
	return items
}

func directConversationItem(
	ctx context.Context,
	q *generated.Queries,
	actorID, conversationID, companyID uuid.UUID,
) (ListItem, error) {
	row, err := q.GetDirectConversationForCreator(ctx, generated.GetDirectConversationForCreatorParams{
		ActorUserID: actorID, ConversationID: conversationID,
		CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ListItem{}, ErrNotFound
	}
	if err != nil {
		return ListItem{}, fmt.Errorf("get direct Conversation: %w", err)
	}
	value := conversationFromRow(row)
	summary, err := conversationListSummary(ctx, q, actorID, conversationID, companyID)
	if err != nil {
		return ListItem{}, err
	}
	// Creator read/pin/archive state lives on the conversation row itself.
	state := userStateFromConversationRow(row)
	// The agent ref reuses the bulk projection with a single id, so the one
	// runtime-availability predicate lives in exactly one query.
	agentRefs, err := q.ListDirectConversationAgentRefsBulk(
		ctx,
		generated.ListDirectConversationAgentRefsBulkParams{
			ActorUserID: actorID, ConversationIds: []uuid.UUID{conversationID}, CompanyID: companyID,
		},
	)
	if err != nil {
		return ListItem{}, fmt.Errorf("read direct Conversation agent ref: %w", err)
	}
	var agent *AgentRef
	if len(agentRefs) > 0 {
		agent = agentRefFromBulkRow(agentRefs[0])
	}
	return ListItem{Conversation: value, Agent: agent, Summary: summary, UserState: state}, nil
}

func conversationListSummary(
	ctx context.Context,
	q *generated.Queries,
	actorID, conversationID, companyID uuid.UUID,
) (ListSummary, error) {
	params := generated.GetConversationListSummaryBaseParams{
		ActorUserID: actorID, ConversationID: conversationID,
		CompanyID: companyID,
	}
	base, err := q.GetConversationListSummaryBase(ctx, params)
	if err != nil {
		return ListSummary{}, fmt.Errorf("read Conversation summary base: %w", err)
	}
	summary := ListSummary{
		UpdatedAt: base.UpdatedAt.Time.UTC(),
		Runs:      RunsSummary{QueuedCount: base.QueuedRunCount, ActiveCount: base.ActiveRunCount},
	}
	message, err := q.GetLatestConversationMessageForSummary(
		ctx, generated.GetLatestConversationMessageForSummaryParams(params),
	)
	if err == nil {
		summary.LastMessage = newLastMessageSummary(
			message.Seq, message.ContentPreview, message.ContentTruncated,
			message.CreatedAt, message.Type, message.AuthorKind,
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ListSummary{}, fmt.Errorf("read latest Conversation Message: %w", err)
	}
	latest, err := q.GetLatestConversationRunForSummary(
		ctx, generated.GetLatestConversationRunForSummaryParams(params),
	)
	if err == nil {
		summary.Runs.Latest = newLatestRunSummary(
			latest.RunID, latest.AgentID, latest.Status, latest.CreatedAt,
			latest.FailureReason,
			latest.Attempt,
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ListSummary{}, fmt.Errorf("read latest Conversation Run: %w", err)
	}
	return summary, nil
}

// newLastMessageSummary builds the conversation preview line. Three surfaces
// publish this same wire type — the list projection, the single-conversation
// read, and the run-transition event — so they share one constructor: a
// producer that quietly omitted the preview once blanked the row a client had
// just rendered from the list.
func newLastMessageSummary(
	seq int64,
	contentPreview string,
	contentTruncated bool,
	createdAt pgtype.Timestamptz,
	itemType, authorKind string,
) *LastMessageSummary {
	return &LastMessageSummary{
		Seq: seq, Content: contentPreview, ContentTruncated: contentTruncated,
		CreatedAt: createdAt.Time.UTC(), Type: ItemType(itemType),
		AuthorKind: ActorKind(authorKind),
	}
}

func newLatestRunSummary(
	runID uuid.UUID,
	agentID uuid.UUID,
	status string,
	createdAt pgtype.Timestamptz,
	failureReason pgtype.Text,
	attempt int32,
) *LatestRunSummary {
	return &LatestRunSummary{
		RunID: runID, AgentID: agentID, Status: RunStatus(status),
		CreatedAt: createdAt.Time.UTC(), FailureReason: util.TextToPtr(failureReason),
		Attempt: attempt, MaxAttempts: execution.MaxAttemptsFor(failureReason.String, attempt),
	}
}

// conversationStateTarget is the access-gated resolution of a Conversation for
// per-user state reads and mutations. Creator read/pin/archive state lives on
// direct conversation rows; task conversations carry no user state.
type conversationStateTarget struct {
	CompanyID uuid.UUID
	CreatedAt time.Time
	Kind      Kind
	CreatorID *uuid.UUID
	State     UserState
	HasState  bool
}

// resolveConversationStateTarget maps pgx.ErrNoRows to ErrNotFound and returns
// every other error unwrapped; each of its callers applies its own wrap text.
func resolveConversationStateTarget(
	ctx context.Context,
	q *generated.Queries,
	actorID, conversationID uuid.UUID,
) (conversationStateTarget, error) {
	resolved, err := q.ResolveConversationStateForActiveUser(
		ctx,
		generated.ResolveConversationStateForActiveUserParams{
			ActorUserID: actorID, ConversationID: conversationID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationStateTarget{}, ErrNotFound
	}
	if err != nil {
		return conversationStateTarget{}, err
	}
	target := conversationStateTarget{
		CompanyID: resolved.CompanyID, CreatedAt: resolved.CreatedAt.Time,
		Kind:      Kind(resolved.Kind),
		CreatorID: util.NullUUIDPtr(resolved.CreatedByUserID),
	}
	if target.Kind == KindDirect && target.CreatorID != nil && *target.CreatorID == actorID {
		state := userStateFromConversationRow(generated.Conversation{
			ID: resolved.ID, CompanyID: resolved.CompanyID, Kind: resolved.Kind,
			CreatedByUserID: resolved.CreatedByUserID,
			PinnedAt:        resolved.PinnedAt, ArchivedAt: resolved.ArchivedAt,
			CreatedAt: resolved.CreatedAt, UpdatedAt: resolved.UpdatedAt, Version: resolved.Version,
		})
		target.State = state
		target.HasState = true
	}
	if target.Kind == KindTask {
		row, err := q.GetTaskConversationUserState(ctx, generated.GetTaskConversationUserStateParams{ActorUserID: actorID, CompanyID: target.CompanyID, ConversationID: conversationID})
		if err == nil {
			target.State = taskChatUserState(row)
			target.HasState = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return conversationStateTarget{}, fmt.Errorf("read task chat state: %w", err)
		}
	}
	return target, nil
}

// lockConversation fences the Conversation row for the rest of the caller's
// transaction. It maps pgx.ErrNoRows to ErrNotFound and returns every other
// error unwrapped; its callers apply their own wrap texts.
func lockConversation(
	ctx context.Context,
	q *generated.Queries,
	actorID, conversationID, companyID uuid.UUID,
) error {
	_, err := q.LockConversationForActiveUserMutation(ctx, generated.LockConversationForActiveUserMutationParams{
		ActorUserID: actorID, ConversationID: conversationID,
		CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func bumpConversationProjection(
	ctx context.Context, q *generated.Queries, conversationID, companyID uuid.UUID,
	pending *[]events.Event, actor events.Actor, observed ...*activity.Snapshot,
) (Response, error) {
	var before *activity.Snapshot
	if len(observed) != 0 {
		before = observed[0]
	}
	if before == nil {
		var err error
		before, err = activity.ObserveInTx(ctx, q, Topic, conversationID)
		if err != nil {
			return Response{}, err
		}
	}
	row, err := q.BumpConversationProjection(ctx, generated.BumpConversationProjectionParams{
		ConversationID: conversationID, CompanyID: companyID,
	})
	if err != nil {
		return Response{}, mapConversationMutationError("bump Conversation projection", err)
	}
	if err := activity.RecordInTx(ctx, q, pending, actor, EventConversationUpdated, before, Topic, row.ID); err != nil {
		return Response{}, err
	}
	return conversationFromRow(row), nil
}

func conversationFromRow(row generated.Conversation) Response {
	return Response{
		ID: row.ID, CompanyID: row.CompanyID, Kind: Kind(row.Kind),
		TaskID: util.NullUUIDPtr(row.TaskID),
		Title:  util.TextToPtr(row.Title), AgentID: util.NullUUIDPtr(row.AgentID),
		CreatedByUserID: util.NullUUIDPtr(row.CreatedByUserID),
		CreatedAt:       row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
}

// ResponseFromRow projects a trusted sqlc Conversation for a transaction
// owned by another domain. Database constraints make this mapping total.
func ResponseFromRow(row generated.Conversation) Response {
	return conversationFromRow(row)
}

// userStateFromConversationRow projects the creator-scoped pin/archive state
// carried by a direct conversation row.
func userStateFromConversationRow(row generated.Conversation) UserState {
	return UserState{
		ConversationID: row.ID, UserID: row.CreatedByUserID.UUID, CompanyID: row.CompanyID,
		PinnedAt: util.TimestampToTime(row.PinnedAt), ArchivedAt: util.TimestampToTime(row.ArchivedAt),
		UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
}

func mapConversationMutationError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23505", "23514":
			return fmt.Errorf("%s: %w", operation, ErrConflict)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
