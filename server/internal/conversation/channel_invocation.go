package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type channelAdmission struct {
	Principal  auth.UserPrincipal
	ChannelID  uuid.UUID
	MessageID  uuid.UUID
	References []discussionReference
}

func admitChannelInvocations(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	input channelAdmission,
) error {
	seen := make(map[uuid.UUID]bool)
	for _, ref := range input.References {
		if ref.AgentID == nil || seen[*ref.AgentID] {
			continue
		}
		agentID := *ref.AgentID
		seen[agentID] = true
		p := input.Principal
		if err := execution.AuthorizeAgentInvocationInTx(
			ctx,
			q,
			execution.RunTrigger{CompanyID: p.CompanyID, AgentID: agentID, AuthorizedByUserID: p.UserID},
		); err != nil {
			return err
		}
		conv, err := q.EnsureChannelAgentConversation(
			ctx,
			db.EnsureChannelAgentConversationParams{
				CompanyID: p.CompanyID,
				ChannelID: input.ChannelID,
				AgentID:   agentID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDiscussionForbidden
		}
		if err != nil {
			return err
		}
		_, err = q.InsertChannelInvocation(
			ctx,
			db.InsertChannelInvocationParams{
				CompanyID:      p.CompanyID,
				ChannelID:      input.ChannelID,
				ConversationID: conv.ID,
				AgentID:        agentID,
				CallerUserID:   p.UserID,
				MessageID:      input.MessageID,
			},
		)
		if err != nil {
			return err
		}
		if _, err = promoteChannelInvocation(ctx, q, pending, conv.ID); err != nil {
			return err
		}
	}
	return nil
}

func promoteChannelInvocation(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	conversationID uuid.UUID,
) (QueuePromotion, error) {
	if _, err := q.GetActiveChannelInvocation(ctx, conversationID); err == nil {
		return QueuePromotion{}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return QueuePromotion{}, err
	}
	for {
		invocation, err := q.NextChannelInvocation(ctx, conversationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return QueuePromotion{}, nil
		}
		if err != nil {
			return QueuePromotion{}, err
		}
		allowed, err := q.ChannelInvocationAuthorized(
			ctx,
			db.ChannelInvocationAuthorizedParams{
				InvocationID: invocation.ID,
				ActorRoles:   auth.RolesWith(auth.PermissionConversationsDiscuss),
			},
		)
		if err != nil {
			return QueuePromotion{}, err
		}
		if !allowed {
			if err = setChannelInvocation(
				ctx,
				q,
				pending,
				invocation.ID,
				"cancelled",
				"blocked",
				"channel_access_revoked",
			); err != nil {
				return QueuePromotion{}, err
			}
			continue
		}
		consent := execution.AuthorizeAgentInvocationInTx(
			ctx,
			q,
			execution.RunTrigger{
				CompanyID:          invocation.CompanyID,
				AgentID:            invocation.AgentID,
				AuthorizedByUserID: invocation.CallerUserID,
			},
		)
		if errors.Is(consent, execution.ErrRunAgentNotBound) {
			if err = setChannelInvocation(
				ctx,
				q,
				pending,
				invocation.ID,
				"cancelled",
				"blocked",
				execution.RunReasonAgentUnbound,
			); err != nil {
				return QueuePromotion{}, err
			}
			continue
		}
		if consent != nil {
			return QueuePromotion{}, consent
		}
		if err = setChannelInvocation(ctx, q, pending, invocation.ID, "active", "pending", ""); err != nil {
			return QueuePromotion{}, err
		}
		run, err := execution.EnqueueRunInTx(ctx, q, pending, execution.RunTrigger{
			CompanyID:      invocation.CompanyID,
			ConversationID: invocation.ConversationID,
			AgentID:        invocation.AgentID,
			InvocationID: uuid.NullUUID{
				UUID:  invocation.ID,
				Valid: true,
			},
			TriggerPrompt:      invocation.TriggerContent,
			ActorUserID:        invocation.CallerUserID,
			AuthorizedByUserID: invocation.CallerUserID,
			Source:             execution.RunSourceMessage,
		})
		if err != nil {
			return QueuePromotion{}, err
		}
		if err = q.InsertChannelInvocationPrompt(
			ctx,
			db.InsertChannelInvocationPromptParams{RunID: run.ID, InvocationID: invocation.ID},
		); err != nil {
			return QueuePromotion{}, err
		}
		return QueuePromotion{RunID: &run.ID}, nil
	}
}

func setChannelInvocation(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	id uuid.UUID,
	status, publication, reason string,
) error {
	row, err := q.SetChannelInvocationState(
		ctx,
		db.SetChannelInvocationStateParams{
			InvocationID:      id,
			Status:            status,
			PublicationStatus: publication,
			FailureReason:     pgtype.Text{String: reason, Valid: reason != ""},
		},
	)
	if err == nil {
		*pending = append(*pending, channelAgentsChangedEvent(row))
	}
	return err
}

// CloseCancelledChannelTagsInTx closes the channel or group-DM tag each
// cancelled Run served and promotes the next tag queued behind it, the step a
// single cancel takes through FinalizeRunConversationInTx. A tag holds its
// channel-Agent Conversation's one active slot, so a bulk cancel that skipped
// it — an unbind or a runtime removal (ADR 0014) — would stall every later tag of that Agent there. The caller
// holds the cancelled Runs' Conversation locks. Runs that served no tag are
// skipped. It returns the ids of the Runs promotion enqueued, for the caller
// to wake after commit.
func CloseCancelledChannelTagsInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	runs []db.Run,
) ([]uuid.UUID, error) {
	var promoted []uuid.UUID
	for _, run := range runs {
		if !run.InvocationID.Valid || run.Status != "cancelled" {
			continue
		}
		promotion, err := finalizeChannelInvocation(ctx, q, pending, run)
		if err != nil {
			return nil, err
		}
		if promotion.RunID != nil {
			promoted = append(promoted, *promotion.RunID)
		}
	}
	return promoted, nil
}

func finalizeChannelInvocation(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	run db.Run,
) (QueuePromotion, error) {
	invocation, err := q.GetChannelInvocation(
		ctx,
		db.GetChannelInvocationParams{InvocationID: run.InvocationID.UUID, CompanyID: run.CompanyID},
	)
	if err != nil {
		return QueuePromotion{}, err
	}
	allowed, err := q.ChannelInvocationAuthorized(
		ctx,
		db.ChannelInvocationAuthorizedParams{
			InvocationID: invocation.ID,
			ActorRoles:   auth.RolesWith(auth.PermissionConversationsDiscuss),
		},
	)
	if err != nil {
		return QueuePromotion{}, err
	}
	if !allowed {
		if err = setChannelInvocation(
			ctx,
			q,
			pending,
			invocation.ID,
			"cancelled",
			"blocked",
			"channel_access_revoked",
		); err != nil {
			return QueuePromotion{}, err
		}
	} else {
		active, err := q.ListActiveInvocationRuns(ctx, uuid.NullUUID{UUID: invocation.ID, Valid: true})
		if err != nil {
			return QueuePromotion{}, err
		}
		if len(active) > 0 {
			return QueuePromotion{}, nil
		}
		waiting, err := q.ChannelInvocationHasPendingInput(ctx, uuid.NullUUID{UUID: invocation.ID, Valid: true})
		if err != nil {
			return QueuePromotion{}, err
		}
		if waiting {
			return QueuePromotion{}, setChannelInvocation(
				ctx,
				q,
				pending,
				invocation.ID,
				"waiting_input",
				"pending",
				"",
			)
		}
		switch run.Status {
		case "completed":
			if invocation.PublicationStatus == "published" {
				break
			}
			answer, err := q.GetChannelFinalAnswer(ctx, run.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				if err = setChannelInvocation(
					ctx,
					q,
					pending,
					invocation.ID,
					"completed",
					"no_output",
					"",
				); err != nil {
					return QueuePromotion{}, err
				}
				break
			}
			if err != nil {
				return QueuePromotion{}, err
			}
			// The feed carries content and Markdown together. Reserve the
			// same envelope budget as human messages rather than publishing
			// an item that would make its thread unreadable.
			encoded, err := json.Marshal(answer.Content)
			if err != nil {
				return QueuePromotion{}, err
			}
			if len(encoded)*2 > maxDiscussionItemBytes-4096 {
				if err := setChannelInvocation(
					ctx,
					q,
					pending,
					invocation.ID,
					"completed",
					"blocked",
					"final_answer_too_large",
				); err != nil {
					return QueuePromotion{}, err
				}
				break
			}
			seq, err := q.AllocateDiscussionSequence(
				ctx,
				db.AllocateDiscussionSequenceParams{
					CompanyID:      run.CompanyID,
					ConversationID: invocation.ChannelID,
				},
			)
			if err != nil {
				return QueuePromotion{}, err
			}
			_, err = q.InsertChannelAnswer(
				ctx,
				db.InsertChannelAnswerParams{
					InvocationID: invocation.ID,
					RunID:        run.ID,
					Seq:          seq,
					Content:      answer.Content,
					FinalItemKey: answer.ItemKey.String,
				},
			)
			if err != nil {
				return QueuePromotion{}, err
			}
			if err = setChannelInvocation(
				ctx,
				q,
				pending,
				invocation.ID,
				"completed",
				"published",
				"",
			); err != nil {
				return QueuePromotion{}, err
			}
			if err = applyDiscussionUnreadDelta(
				ctx,
				q,
				discussionUnreadDelta{
					ConversationID: invocation.ChannelID,
					RootID:         uuid.NullUUID{UUID: invocation.ThreadRootID, Valid: true},
					AuthorID:       invocation.AgentID,
					Seq:            seq,
					Delta:          1,
				},
			); err != nil {
				return QueuePromotion{}, err
			}
		case "failed":
			if err = setChannelInvocation(
				ctx,
				q,
				pending,
				invocation.ID,
				"failed",
				"no_output",
				run.FailureReason.String,
			); err != nil {
				return QueuePromotion{}, err
			}
		case "cancelled":
			if err = setChannelInvocation(
				ctx,
				q,
				pending,
				invocation.ID,
				"cancelled",
				"blocked",
				"execution_cancelled",
			); err != nil {
				return QueuePromotion{}, err
			}
		default:
			return QueuePromotion{}, nil
		}
	}
	row, err := q.BumpDiscussionConversation(
		ctx,
		db.BumpDiscussionConversationParams{CompanyID: run.CompanyID, ConversationID: invocation.ChannelID},
	)
	if err != nil {
		return QueuePromotion{}, err
	}
	*pending = append(*pending, discussionChangedEvent(row))
	return promoteChannelInvocation(ctx, q, pending, run.ConversationID)
}

func invalidateChannelInvocations(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	channelID uuid.UUID,
) error {
	conversations, err := q.ListChannelInvocationConversations(ctx, uuid.NullUUID{UUID: channelID, Valid: true})
	if err != nil {
		return err
	}
	invocations, err := q.ListUnfinishedChannelInvocations(ctx, channelID)
	if err != nil {
		return err
	}
	for _, invocation := range invocations {
		allowed, err := q.ChannelInvocationAuthorized(
			ctx,
			db.ChannelInvocationAuthorizedParams{
				InvocationID: invocation.ID,
				ActorRoles:   auth.RolesWith(auth.PermissionConversationsDiscuss),
			},
		)
		if err != nil {
			return err
		}
		if allowed {
			continue
		}
		if err = setChannelInvocation(
			ctx,
			q,
			pending,
			invocation.ID,
			"cancelled",
			"blocked",
			"channel_access_revoked",
		); err != nil {
			return err
		}
		if err = execution.CancelChannelInvocationRunsInTx(ctx, q, pending, invocation); err != nil {
			return err
		}
	}
	for _, conv := range conversations {
		if _, err := promoteChannelInvocation(ctx, q, pending, conv.ID); err != nil {
			return fmt.Errorf("promote channel invocation after revocation: %w", err)
		}
	}
	return nil
}

// AbandonChannelContinuationInTx releases a channel queue when a human-input
// receipt can no longer continue its invocation. The caller holds the queue lock.
func AbandonChannelContinuationInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	originRunID uuid.UUID,
) (*db.Run, error) {
	origin, err := q.GetRun(ctx, originRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !origin.InvocationID.Valid {
		return nil, nil
	}
	invocation, err := q.GetChannelInvocation(
		ctx,
		db.GetChannelInvocationParams{CompanyID: origin.CompanyID, InvocationID: origin.InvocationID.UUID},
	)
	if err != nil {
		return nil, err
	}
	if invocation.Status != "waiting_input" {
		return nil, nil
	}
	waiting, err := q.ChannelInvocationHasPendingInput(ctx, origin.InvocationID)
	if err != nil || waiting {
		return nil, err
	}
	if err := setChannelInvocation(
		ctx,
		q,
		pending,
		invocation.ID,
		"cancelled",
		"blocked",
		"continuation_unavailable",
	); err != nil {
		return nil, err
	}
	row, err := q.BumpDiscussionConversation(
		ctx,
		db.BumpDiscussionConversationParams{CompanyID: origin.CompanyID, ConversationID: invocation.ChannelID},
	)
	if err != nil {
		return nil, err
	}
	*pending = append(*pending, discussionChangedEvent(row))
	next, err := promoteChannelInvocation(ctx, q, pending, invocation.ConversationID)
	if err != nil || next.RunID == nil {
		return nil, err
	}
	run, err := q.GetRun(ctx, *next.RunID)
	return &run, err
}

func channelAgentsChangedEvent(row db.ChannelInvocation) events.Event {
	return events.Event{
		EntityType: Topic, Name: EventChannelAgentsChanged, CompanyID: row.CompanyID,
		ConversationID: row.ChannelID, CreatedAt: row.UpdatedAt.Time, Actor: events.SystemActor(),
		Payload: ChannelAgentsChanged{ConversationID: row.ChannelID},
	}
}
