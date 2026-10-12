// Package conversation owns Boardly's creator-private direct chats and shared
// immutable product Message stream.
package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// conversationWriteTxOptions is the isolation contract for Conversation mutations.
var conversationWriteTxOptions = pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}

// ConversationService owns the creator-private direct Conversations, the
// per-user state and Run queue each one carries, and the Message stream they
// append to.
type ConversationService interface { //nolint:revive // AGENTS.md mandates <Domain>Service; bare Service is forbidden.
	FetchModelSettings(context.Context, auth.UserPrincipal, uuid.UUID) (ModelSettings, error)
	UpdateModelSettings(context.Context, auth.UserPrincipal, uuid.UUID, ModelSettingsInput) (ModelSettings, error)
	CreateHumanDirect(context.Context, auth.UserPrincipal, CreateHumanDirectInput) (HumanDirectResult, error)
	ListHumanDirects(context.Context, auth.UserPrincipal, DiscussionListOptions) (DiscussionList, error)
	InviteHumanDirectMembers(context.Context, auth.UserPrincipal, uuid.UUID, InviteHumanDirectInput) (HumanDirectDetail, error)
	FetchHumanDirect(context.Context, auth.UserPrincipal, uuid.UUID) (HumanDirectDetail, error)
	CreateChannel(context.Context, auth.UserPrincipal, CreateChannelInput) (ChannelReceipt, error)
	ListChannels(context.Context, auth.UserPrincipal, DiscussionListOptions) (DiscussionList, error)
	ListChannelManagement(context.Context, auth.UserPrincipal, DiscussionListOptions) (ChannelManagementList, error)
	FetchChannel(context.Context, auth.UserPrincipal, uuid.UUID) (ChannelDetail, error)
	FetchChannelManagement(context.Context, auth.UserPrincipal, uuid.UUID) (ChannelManagement, error)
	UpdateChannel(context.Context, auth.UserPrincipal, uuid.UUID, ChannelUpdateInput) (ChannelManagement, error)
	SetChannelMember(context.Context, auth.UserPrincipal, uuid.UUID, ChannelMemberInput) (ChannelManagement, error)
	SetChannelAgent(context.Context, auth.UserPrincipal, uuid.UUID, ChannelAgentInput) (ChannelManagement, error)
	LeaveChannel(context.Context, auth.UserPrincipal, uuid.UUID, int64) error
	SetChannelArchived(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		ChannelArchiveInput,
	) (ChannelManagement, error)
	CreateDirect(context.Context, auth.UserPrincipal, CreateDirectInput) (CreateDirectResult, error)
	RenameDirect(context.Context, auth.UserPrincipal, uuid.UUID, RenameDirectInput) (Response, error)
	ListDirect(context.Context, auth.UserPrincipal, bool) (DirectPage, error)
	ListPins(context.Context, auth.UserPrincipal) ([]UserState, error)
	ListStates(context.Context, auth.UserPrincipal) ([]UserState, error)
	FetchDirect(context.Context, auth.UserPrincipal, uuid.UUID) (ListItem, error)
	UpdateState(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		UpdateUserStateInput,
	) (UserState, error)
	FetchState(context.Context, auth.UserPrincipal, uuid.UUID) (UserState, error)
	ListMessages(context.Context, auth.UserPrincipal, MessageTarget, MessageListOptions) (MessagePage, error)
	Transcript(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		TranscriptListOptions,
	) (TranscriptPage, error)
	Send(context.Context, auth.UserPrincipal, MessageTarget, SendInput) (SendResult, error)
	FetchMessageContext(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		int,
	) (MessageContextPage, error)
	FetchRunQueue(context.Context, auth.UserPrincipal, uuid.UUID) (RunQueueProjection, error)
	ReorderRunQueue(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		RunQueueOrderInput,
	) (RunQueueProjection, error)
	DeleteRunQueueItem(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		DeleteRunQueueItemInput,
	) (RunQueueProjection, error)
}

var _ ConversationService = (*DefaultConversationService)(nil)

// TaskMessageRunPreparer is the narrow Task-domain port used immediately
// before a Message materializes a fresh Task Run. The Task domain owns the
// lifecycle transition, dependency policy, outcome-report reconciliation,
// and task.updated event; Conversation owns the Message and Run queue that
// trigger it. When a colleague's Message is queued for the assignee
// instead, the Task domain also words the next step the assignee's nudge
// names, by the rule its own task_assigned copy follows.
type TaskMessageRunPreparer interface {
	CheckTaskMessageRunInTx(
		context.Context,
		*generated.Queries,
		generated.Task,
	) (bool, error)
	PrepareTaskMessageRunInTx(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		uuid.UUID,
		generated.Task,
	) (bool, error)
	TaskRunNextStepInTx(
		context.Context,
		*generated.Queries,
		generated.Task,
	) (string, error)
}

type TaskMessageRunPreparerFuncs struct {
	Check   func(context.Context, *generated.Queries, generated.Task) (bool, error)
	Prepare func(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		uuid.UUID,
		generated.Task,
	) (bool, error)
	NextStep func(context.Context, *generated.Queries, generated.Task) (string, error)
}

func (f TaskMessageRunPreparerFuncs) CheckTaskMessageRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	row generated.Task,
) (bool, error) {
	if f.Check == nil {
		return false, errors.New("Task Message Run check is not configured")
	}
	return f.Check(ctx, queries, row)
}

func (f TaskMessageRunPreparerFuncs) PrepareTaskMessageRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	actorUserID uuid.UUID,
	row generated.Task,
) (bool, error) {
	if f.Prepare == nil {
		return false, errors.New("Task Message Run preparation is not configured")
	}
	return f.Prepare(ctx, queries, pending, actorUserID, row)
}

func (f TaskMessageRunPreparerFuncs) TaskRunNextStepInTx(
	ctx context.Context,
	queries *generated.Queries,
	row generated.Task,
) (string, error) {
	if f.NextStep == nil {
		return "", errors.New("Task next step is not configured")
	}
	return f.NextStep(ctx, queries, row)
}

type ServiceOptions struct {
	Store           *db.Store
	Bus             *events.Bus
	TaskMessageRuns TaskMessageRunPreparer
}

type DefaultConversationService struct {
	store           *db.Store
	bus             *events.Bus
	taskMessageRuns TaskMessageRunPreparer
}

func NewConversationService(options ServiceOptions) (*DefaultConversationService, error) {
	if options.Store == nil {
		return nil, errors.New("Conversation store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("Conversation event bus is required")
	}
	if options.TaskMessageRuns == nil {
		return nil, errors.New("Conversation Task Message Run preparer is required")
	}
	return &DefaultConversationService{
		store:           options.Store,
		bus:             options.Bus,
		taskMessageRuns: options.TaskMessageRuns,
	}, nil
}

func (s *DefaultConversationService) CreateDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	input CreateDirectInput,
) (CreateDirectResult, error) {
	normalized, err := normalizeCreateDirectInput(input)
	if err != nil {
		return CreateDirectResult{}, err
	}
	result, err := s.createDirect(ctx, principal, normalized)
	if err != nil {
		return CreateDirectResult{}, fmt.Errorf("create direct Conversation: %w", err)
	}
	return result, nil
}

func (s *DefaultConversationService) createDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	input CreateDirectInput,
) (CreateDirectResult, error) {
	var result CreateDirectResult
	var pending []events.Event
	err := s.store.WithinTx(ctx, conversationWriteTxOptions, func(tx *generated.Queries) error {
		pending = pending[:0]
		if err := lockActiveMembership(ctx, tx, principal.CompanyID, principal.UserID); err != nil {
			return err
		}

		var value Response
		var err error
		if input.RequestKey != nil {
			row, readErr := tx.GetDirectConversationByRequestKey(
				ctx,
				generated.GetDirectConversationByRequestKeyParams{
					ActorUserID: principal.UserID,
					CompanyID:   principal.CompanyID,
					RequestKey:  *input.RequestKey,
				},
			)
			switch {
			case readErr == nil:
				if !row.AgentID.Valid || row.AgentID.UUID != input.AgentID {
					return ErrConflict
				}
				result = CreateDirectResult{Conversation: conversationFromRow(row)}
				return nil
			case !errors.Is(readErr, pgx.ErrNoRows):
				return fmt.Errorf("read direct Conversation creation receipt: %w", readErr)
			}
		} else {
			value, err = directConversationByAgent(
				ctx,
				tx,
				principal.UserID,
				principal.CompanyID,
				input.AgentID,
			)
			if err == nil {
				result = CreateDirectResult{Conversation: value}
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		value, err = insertDirectConversation(ctx, tx, principal.UserID, input, principal.CompanyID)
		if err != nil {
			return err
		}

		pending = append(pending, newConversationEvent(
			principal.UserID, nil, value, EventConversationCreated, nil,
		))
		result = CreateDirectResult{Conversation: value, Created: true}
		return activity.RecordInTx(
			ctx,
			tx,
			&pending,
			events.UserActor(principal.UserID.String()),
			EventConversationCreated,
			nil,
			Topic,
			value.ID,
		)
	})
	if err != nil {
		return CreateDirectResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultConversationService) RenameDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	input RenameDirectInput,
) (Response, error) {
	input, err := normalizeRenameDirectInput(input)
	if err != nil {
		return Response{}, err
	}
	var result Response
	var pending []events.Event
	err = s.store.WithinTx(ctx, conversationWriteTxOptions, func(tx *generated.Queries) error {
		pending = pending[:0]
		if err := lockActiveMembership(ctx, tx, principal.CompanyID, principal.UserID); err != nil {
			return err
		}
		current, err := tx.LockConversationForActiveUserMutation(ctx, generated.LockConversationForActiveUserMutationParams{
			ActorUserID: principal.UserID, ConversationID: conversationID, CompanyID: principal.CompanyID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock direct Conversation for rename: %w", err)
		}
		if Kind(current.Kind) != KindDirect {
			return ErrNotFound
		}
		if current.Version != input.ExpectedRevision {
			return ErrConflict
		}
		if current.Title.Valid && current.Title.String == input.Title {
			result = conversationFromRow(current)
			return nil
		}
		before, err := activity.ObserveInTx(ctx, tx, Topic, conversationID)
		if err != nil {
			return err
		}
		count, err := tx.SetDirectConversationTitle(ctx, generated.SetDirectConversationTitleParams{
			Title: input.Title, ConversationID: conversationID, CompanyID: principal.CompanyID,
			ActorUserID: principal.UserID, ExpectedVersion: input.ExpectedRevision,
		})
		if err != nil {
			return mapConversationMutationError("set direct Conversation title", err)
		}
		if count == 0 {
			return ErrConflict
		}
		result, err = bumpConversationProjection(ctx, tx, conversationID, principal.CompanyID,
			&pending, events.UserActor(principal.UserID.String()), before)
		if err != nil {
			return err
		}
		summary, err := conversationListSummary(ctx, tx, principal.UserID, conversationID, principal.CompanyID)
		if err != nil {
			return err
		}
		return stageInTx(ctx, tx, &pending,
			newConversationEvent(principal.UserID, nil, result, EventConversationUpdated, &summary))
	})
	if err != nil {
		return Response{}, fmt.Errorf("rename direct Conversation: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultConversationService) ListDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	includeArchived bool,
) (DirectPage, error) {
	page, err := s.listDirect(ctx, principal, includeArchived)
	if err != nil {
		return DirectPage{}, fmt.Errorf("list direct Conversations: %w", err)
	}
	if page.Conversations == nil {
		page.Conversations = []ListItem{}
	}
	return page, nil
}

func (s *DefaultConversationService) listDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	includeArchived bool,
) (DirectPage, error) {
	return directConversationPage(ctx, s.store.Queries(), generated.ListDirectConversationsForCreatorParams{
		ActorUserID: principal.UserID, CompanyID: principal.CompanyID, IncludeArchived: includeArchived,
	})
}

func (s *DefaultConversationService) FetchDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (ListItem, error) {
	value, err := s.fetchDirect(ctx, principal, conversationID)
	if err != nil {
		return ListItem{}, fmt.Errorf("get direct Conversation: %w", err)
	}
	return value, nil
}

func (s *DefaultConversationService) fetchDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (ListItem, error) {
	return directConversationItem(ctx, s.store.Queries(), principal.UserID, conversationID, principal.CompanyID)
}

// FetchDirectDetail is the combined read: one direct conversation, a bounded
// window of its messages, and the co-resident panel state, so the client opens
// a chat with a single request instead of a read followed by a message page.
//
// It composes the authorized reads rather than adding a new one: FetchDirect
// resolves the conversation and enforces creator-only visibility, ListMessages
// windows the messages and reports the watermark the window was cut against,
// FetchRunQueue reads the queue.
func (s *DefaultConversationService) FetchDirectDetail(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	window MessageWindow,
) (DirectDetail, error) {
	item, err := s.FetchDirect(ctx, principal, conversationID)
	if err != nil {
		return DirectDetail{}, err
	}

	options := MessageListOptions{Limit: window.Limit, ThroughSeq: window.ThroughSeq}
	if window.AfterSeq != nil {
		options.AfterSeq = *window.AfterSeq
	}
	// No floor is a page counted off the end of the timeline rather than
	// carved out of a seq range (see newestMessageViews): with no cursor at
	// all, a panel opening on the newest messages; with through_seq alone,
	// a scroll-back to the newest messages at or below the bound, as the
	// cursored message read answers the same query.
	options.FromNewest = window.AfterSeq == nil
	page, err := s.ListMessages(
		ctx, principal, MessageTarget{Kind: TargetConversation, ID: conversationID}, options,
	)
	if err != nil {
		return DirectDetail{}, err
	}
	queue, err := s.FetchRunQueue(ctx, principal, conversationID)
	if err != nil {
		return DirectDetail{}, err
	}

	return DirectDetail{
		Conversation:         item.Conversation,
		Agent:                item.Agent,
		Messages:             page.Items,
		EarliestAvailableSeq: page.EarliestAvailableSeq,
		ThroughSeq:           page.ThroughSeq,
		UserState:            item.UserState,
		Runs:                 item.Summary.Runs,
		RunQueue:             queue,
	}, nil
}

func (s *DefaultConversationService) UpdateState(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	input UpdateUserStateInput,
) (UserState, error) {
	if !util.ValidUUID(conversationID) {
		return UserState{}, ErrNotFound
	}
	normalized, err := normalizeUpdateUserStateInput(input)
	if err != nil {
		return UserState{}, err
	}
	state, err := s.updateUserState(ctx, principal, conversationID, normalized)
	if err != nil {
		return UserState{}, fmt.Errorf("update Conversation user state: %w", err)
	}
	return state, nil
}

func (s *DefaultConversationService) updateUserState(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	input UpdateUserStateInput,
) (UserState, error) {
	var result UserState
	var pending []events.Event
	err := s.store.WithinTx(ctx, conversationWriteTxOptions, func(tx *generated.Queries) error {
		pending = pending[:0]
		resolved, err := resolveConversationStateTarget(ctx, tx, principal.UserID, conversationID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return err
			}
			return fmt.Errorf("resolve Conversation user-state target: %w", err)
		}
		if err := lockActiveMembership(ctx, tx, resolved.CompanyID, principal.UserID); err != nil {
			return err
		}
		if resolved.Kind != KindTask && (resolved.Kind != KindDirect || !resolved.HasState) {
			return ErrNotFound
		}
		locked, err := tx.LockConversationForActiveUserMutation(
			ctx,
			generated.LockConversationForActiveUserMutationParams{
				ActorUserID:    principal.UserID,
				ConversationID: conversationID,
				CompanyID:      resolved.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Conversation for user-state update: %w", err)
		}
		if resolved.Kind == KindTask {
			var changed bool
			result, changed, err = updateTaskChatState(ctx, tx, principal.UserID, locked, input)
			if err != nil {
				return err
			}
			if changed {
				return stageUserStateEvent(&pending, principal.UserID, resolved.CompanyID, result)
			}
			return nil
		}
		current := userStateFromConversationRow(locked)
		targetPinned, targetArchived := current.PinnedAt != nil, current.ArchivedAt != nil
		if input.Pinned != nil {
			targetPinned = *input.Pinned
		}
		if input.Archived != nil {
			targetArchived = *input.Archived
		}
		if current.Revision != input.ExpectedRevision {
			if !isReplayOfUserStateMutation(
				current, input.ExpectedRevision, targetPinned, targetArchived,
			) {
				return ErrConflict
			}
			// Succeed on the stored snapshot without writing or re-emitting.
			result = current
			return nil
		}
		if !userStateChanged(current, targetPinned, targetArchived, input) {
			result = current
			return nil
		}
		beforeHistory, err := activity.ObserveInTx(ctx, tx, Topic, conversationID)
		if err != nil {
			return err
		}
		updated, err := tx.UpdateDirectConversationUserState(
			ctx,
			generated.UpdateDirectConversationUserStateParams{
				Pinned: targetPinned, Archived: targetArchived,
				ConversationID: conversationID, ActorUserID: principal.UserID,
				ExpectedVersion: current.Revision, CompanyID: resolved.CompanyID,
			},
		)
		if err != nil {
			return mapConversationMutationError("update Conversation user state", err)
		}
		result = userStateFromConversationRow(updated)
		if err := activity.RecordInTx(
			ctx,
			tx,
			&pending,
			events.UserActor(principal.UserID.String()),
			EventConversationRead,
			beforeHistory,
			Topic,
			conversationID,
		); err != nil {
			return err
		}
		event := newConversationUserStateEvent(principal.UserID, resolved.CompanyID, result)
		event.ActivityRecorded = true
		pending = append(pending, event)
		return nil
	})
	if err != nil {
		return UserState{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultConversationService) FetchState(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (UserState, error) {
	state, err := s.fetchUserState(ctx, principal, conversationID)
	if err != nil {
		return UserState{}, fmt.Errorf("get Conversation user state: %w", err)
	}
	return state, nil
}

func (s *DefaultConversationService) fetchUserState(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (UserState, error) {
	q := s.store.Queries()
	resolved, err := resolveConversationStateTarget(ctx, q, principal.UserID, conversationID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return UserState{}, err
		}
		return UserState{}, fmt.Errorf("resolve Conversation user-state read: %w", err)
	}
	if !resolved.HasState {
		// Task conversations (and non-creator readers) have no stored state;
		// report the synthesized default so reads stay total.
		return UserState{
			ConversationID: conversationID,
			UserID:         principal.UserID,
			CompanyID:      resolved.CompanyID,
			UpdatedAt:      resolved.CreatedAt.UTC(),
			Revision:       0,
		}, nil
	}
	return resolved.State, nil
}

// isReplayOfUserStateMutation reports whether a revision that does not match
// the stored one is the client's own write coming back, rather than someone
// else's.
//
// A retry after a lost response is indistinguishable from a stale write to the
// database: both arrive with a revision the row no longer carries, so the CAS
// in UpdateDirectConversationUserState fails either way and can only answer
// "conflict". Three facts together say the write this request is retrying is
// the one that landed: the row advanced by exactly one, and both flags already
// hold the value this request asks for. A racing writer that moved the row
// somewhere else fails at least one of them — and one that happened to ask for
// the same change has already produced the state this caller wants, so
// reporting success on the stored snapshot is the true answer either way.
//
// The bound belongs in here rather than at the call site, because
// expectedRevision+1 is the whole comparison and it only means anything below
// the ceiling. At protocol.MaxJSONSafeInteger the successor is a revision the
// wire cannot carry and no row can hold; at math.MaxInt64 the addition
// overflows to a negative number that a row conceivably could. Refusing the
// whole range at and above the ceiling makes the answer right for any int64
// this is handed, including the ones the `revision` binding rule would already
// have refused at the boundary.
func isReplayOfUserStateMutation(
	current UserState,
	expectedRevision int64,
	targetPinned, targetArchived bool,
) bool {
	if expectedRevision >= protocol.MaxJSONSafeInteger {
		return false
	}
	return current.Revision == expectedRevision+1 &&
		(current.PinnedAt != nil) == targetPinned &&
		(current.ArchivedAt != nil) == targetArchived
}

// userStateChanged gates the write and the user-state event: fields absent
// from the request never count as changes.
func userStateChanged(
	current UserState,
	targetPinned, targetArchived bool,
	input UpdateUserStateInput,
) bool {
	return input.Pinned != nil && (current.PinnedAt != nil) != targetPinned ||
		input.Archived != nil && (current.ArchivedAt != nil) != targetArchived
}

// stageUserStateEvent appends the conversation.read watermark event to the
// caller's per-transaction slice; the transaction owner publishes it on the
// Bus only after commit. A staging failure aborts that transaction: an event
// that cannot be event describes a fact that must not commit.
func stageUserStateEvent(
	pending *[]events.Event,
	actorID, companyID uuid.UUID,
	state UserState,
) error {
	*pending = append(*pending, newConversationUserStateEvent(actorID, companyID, state))
	return nil
}
