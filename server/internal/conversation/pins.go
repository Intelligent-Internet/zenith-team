package conversation

import (
	"context"
	"fmt"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

func (s *DefaultConversationService) ListPins(ctx context.Context, principal auth.UserPrincipal) ([]UserState, error) {
	rows, err := s.store.Queries().ListPinnedConversationStatesForActiveMember(ctx, generated.ListPinnedConversationStatesForActiveMemberParams{ActorUserID: principal.UserID, CompanyID: principal.CompanyID})
	if err != nil {
		return nil, fmt.Errorf("list personal chat pins: %w", err)
	}
	states := make([]UserState, len(rows))
	for index, row := range rows {
		states[index] = UserState{ConversationID: row.ConversationID, CompanyID: row.CompanyID, UserID: principal.UserID, PinnedAt: util.TimestampToTime(row.PinnedAt), UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version}
	}
	return states, nil
}

func taskChatUserState(row generated.ConversationUserState) UserState {
	return UserState{
		ConversationID: row.ConversationID, CompanyID: row.CompanyID, UserID: row.UserID,
		PinnedAt: util.TimestampToTime(row.PinnedAt), ArchivedAt: util.TimestampToTime(row.ArchivedAt),
		UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
}

// Caller holds the company/member and Conversation mutation locks.
func updateTaskChatState(ctx context.Context, q *generated.Queries, actorID uuid.UUID, conversation generated.Conversation, input UpdateUserStateInput) (UserState, bool, error) {
	params := generated.GetTaskConversationUserStateParams{ActorUserID: actorID, CompanyID: conversation.CompanyID, ConversationID: conversation.ID}
	if err := q.EnsureTaskConversationUserState(ctx, generated.EnsureTaskConversationUserStateParams{
		ActorUserID: actorID, CompanyID: conversation.CompanyID, ConversationID: conversation.ID,
	}); err != nil {
		return UserState{}, false, fmt.Errorf("initialize task chat state: %w", err)
	}
	row, err := q.GetTaskConversationUserState(ctx, params)
	if err != nil {
		return UserState{}, false, fmt.Errorf("read task chat state for update: %w", err)
	}
	current := taskChatUserState(row)
	pinned, archived := current.PinnedAt != nil, current.ArchivedAt != nil
	if input.Pinned != nil {
		pinned = *input.Pinned
	}
	if input.Archived != nil {
		archived = *input.Archived
	}
	if current.Revision != input.ExpectedRevision {
		if isReplayOfUserStateMutation(current, input.ExpectedRevision, pinned, archived) {
			return current, false, nil
		}
		return UserState{}, false, ErrConflict
	}
	if !userStateChanged(current, pinned, archived, input) {
		return current, false, nil
	}
	updated, err := q.UpdateTaskConversationUserState(ctx, generated.UpdateTaskConversationUserStateParams{
		CompanyID: conversation.CompanyID, ConversationID: conversation.ID, ActorUserID: actorID,
		ExpectedVersion: current.Revision, Pinned: pinned, Archived: archived,
	})
	if err != nil {
		return UserState{}, false, mapConversationMutationError("update task chat state", err)
	}
	return taskChatUserState(updated), true, nil
}

// ListStates returns saved personal filing state; absent chats have neither flag set.
func (s *DefaultConversationService) ListStates(ctx context.Context, principal auth.UserPrincipal) ([]UserState, error) {
	rows, err := s.store.Queries().ListConversationStatesForActiveMember(ctx, generated.ListConversationStatesForActiveMemberParams{
		ActorUserID: principal.UserID, CompanyID: principal.CompanyID,
	})
	if err != nil {
		return nil, fmt.Errorf("list personal chat states: %w", err)
	}
	states := make([]UserState, len(rows))
	for index, row := range rows {
		states[index] = UserState{
			ConversationID: row.ConversationID, CompanyID: row.CompanyID, UserID: principal.UserID,
			PinnedAt: util.TimestampToTime(row.PinnedAt), ArchivedAt: util.TimestampToTime(row.ArchivedAt),
			UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
		}
	}
	return states, nil
}
