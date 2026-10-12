package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

type ChannelInvocationService interface {
	List(context.Context, auth.UserPrincipal, uuid.UUID) (ChannelInvocationPage, error)
	Activity(context.Context, auth.UserPrincipal, ChannelAgentTarget) (ChannelAgentActivity, error)
	Retry(context.Context, auth.UserPrincipal, ChannelInvocationRetry) (ChannelInvocationPage, error)
}

var _ ChannelInvocationService = (*DefaultChannelInvocationService)(nil)

type DefaultChannelInvocationService struct {
	store *db.Store
	bus   *events.Bus
}

func NewChannelInvocationService(store *db.Store, bus *events.Bus) *DefaultChannelInvocationService {
	return &DefaultChannelInvocationService{store: store, bus: bus}
}

type (
	ChannelAgentTarget     struct{ ChannelID, AgentID uuid.UUID }
	ChannelInvocationRetry struct{ ChannelID, InvocationID, RequestKey uuid.UUID }
	ChannelAgentStatus     struct {
		AgentID       uuid.UUID  `json:"agentId"`
		Name          string     `json:"name"`
		ManagerUserID *uuid.UUID `json:"managerUserId"`
		ManagerName   string     `json:"managerName"`
		CanInvoke     bool       `json:"canInvoke"`
		CanViewDetail bool       `json:"canViewDetail"`
		RuntimeStatus string     `json:"runtimeStatus"`
	}
)

type ChannelInvocation struct {
	InputRequestIDs   []uuid.UUID `json:"inputRequestIds"`
	ID                uuid.UUID   `json:"id"`
	AgentID           uuid.UUID   `json:"agentId"`
	CallerUserID      uuid.UUID   `json:"callerUserId"`
	TriggerMessageID  uuid.UUID   `json:"triggerMessageId"`
	ThreadRootID      uuid.UUID   `json:"threadRootId"`
	Status            string      `json:"status"`
	PublicationStatus string      `json:"publicationStatus"`
	FailureReason     *string     `json:"failureReason"`
	RunStatus         *string     `json:"runStatus"`
	RuntimeStatus     *string     `json:"runtimeStatus"`
	CanRetry          bool        `json:"canRetry"`
	CreatedAt         time.Time   `json:"createdAt"`
}
type ChannelInvocationPage struct {
	Agents []ChannelAgentStatus `json:"agents"`
	Items  []ChannelInvocation  `json:"items"`
}
type ChannelAgentActivityItem struct {
	Parts        []protocol.RunTranscriptPart `json:"parts"`
	ID           uuid.UUID                    `json:"id"`
	RunID        uuid.UUID                    `json:"runId"`
	InvocationID uuid.UUID                    `json:"invocationId"`
	Type         string                       `json:"type"`
	Content      string                       `json:"content"`
	CreatedAt    time.Time                    `json:"createdAt"`
}
type ChannelAgentActivity struct {
	Items []ChannelAgentActivityItem `json:"items"`
}

func (s *DefaultChannelInvocationService) List(
	ctx context.Context,
	p auth.UserPrincipal,
	channelID uuid.UUID,
) (ChannelInvocationPage, error) {
	result := ChannelInvocationPage{Agents: []ChannelAgentStatus{}, Items: []ChannelInvocation{}}
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			access, err := currentDiscussion(ctx, q, p, channelID, false)
			if err != nil {
				return err
			}
			agents, err := q.ListChannelAgentRoster(
				ctx,
				generated.ListChannelAgentRosterParams{ChannelID: channelID, ViewerUserID: p.UserID},
			)
			if err != nil {
				return err
			}
			for _, a := range agents {
				// A tag runs on the tagger's own computer (ADR 0014), so both
				// answers are the viewer's: with no setup of the Agent they cannot
				// tag it, and no computer of theirs is there to run it.
				runtimeStatus := "offline"
				if a.ViewerBound && a.RuntimeStatus.Valid {
					runtimeStatus = a.RuntimeStatus.String
				}
				result.Agents = append(result.Agents, ChannelAgentStatus{
					AgentID:       a.ID,
					Name:          a.Name,
					ManagerUserID: util.NullUUIDPtr(a.CreatedByUserID),
					ManagerName:   a.ManagerName,
					CanInvoke: discussionOpen(access) &&
						auth.CompanyRole(access.CompanyRole).
							Can(auth.PermissionConversationsDiscuss) &&
						a.ViewerBound,
					// The activity view lists the viewer's own Runs (Access
					// TECH B11), so every member who sees the chat may open it.
					CanViewDetail: true,
					RuntimeStatus: runtimeStatus,
				})
			}
			rows, err := q.ListChannelInvocations(
				ctx,
				generated.ListChannelInvocationsParams{
					CompanyID:   p.CompanyID,
					ChannelID:   channelID,
					ActorUserID: p.UserID,
				},
			)
			if err != nil {
				return err
			}
			for _, v := range rows {
				allowed, err := q.ChannelInvocationAuthorized(
					ctx,
					generated.ChannelInvocationAuthorizedParams{
						InvocationID: v.ID,
						ActorRoles:   auth.RolesWith(auth.PermissionConversationsDiscuss),
					},
				)
				if err != nil {
					return err
				}
				canRetry := allowed && v.CallerUserID == p.UserID && v.Status == "failed" &&
					discussionOpen(access) &&
					auth.CompanyRole(access.CompanyRole).Can(auth.PermissionConversationsDiscuss)
				result.Items = append(result.Items, ChannelInvocation{
					InputRequestIDs:   v.InputRequestIds,
					ID:                v.ID,
					AgentID:           v.AgentID,
					CallerUserID:      v.CallerUserID,
					TriggerMessageID:  v.TriggerMessageID,
					ThreadRootID:      v.ThreadRootID,
					Status:            v.Status,
					PublicationStatus: v.PublicationStatus,
					FailureReason: util.TextToPtr(
						v.FailureReason,
					),
					RunStatus:     discussionOptionalString(v.RunStatus),
					RuntimeStatus: util.TextToPtr(v.RuntimeStatus),
					CanRetry:      canRetry,
					CreatedAt:     v.CreatedAt.Time.UTC(),
				})
			}
			return nil
		},
	)
	return result, err
}

func (s *DefaultChannelInvocationService) Activity(
	ctx context.Context,
	p auth.UserPrincipal,
	target ChannelAgentTarget,
) (ChannelAgentActivity, error) {
	result := ChannelAgentActivity{Items: []ChannelAgentActivityItem{}}
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			if _, err := currentDiscussion(ctx, q, p, target.ChannelID, false); err != nil {
				return err
			}
			_, err := q.GetAgentForCompany(
				ctx,
				generated.GetAgentForCompanyParams{CompanyID: p.CompanyID, AgentID: target.AgentID},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDiscussionNotFound
			}
			if err != nil {
				return err
			}
			// Every member who sees the chat may look, at their own Runs only:
			// a tagged Run works for its tagger (D31), so the Agent's creator
			// reads none of another person's.
			rows, err := q.ListChannelAgentActivity(
				ctx,
				generated.ListChannelAgentActivityParams{
					CompanyID:    p.CompanyID,
					ChannelID:    target.ChannelID,
					AgentID:      target.AgentID,
					ViewerUserID: p.UserID,
				},
			)
			if err != nil {
				return err
			}
			for i := len(rows) - 1; i >= 0; i-- {
				v := rows[i]
				parts, err := execution.ActivityParts(v.Parts)
				if err != nil {
					return err
				}
				result.Items = append(
					result.Items,
					ChannelAgentActivityItem{
						Parts:        parts,
						ID:           v.ID,
						RunID:        v.RunID.UUID,
						InvocationID: v.InvocationID.UUID,
						Type:         v.Type,
						Content:      v.Content,
						CreatedAt:    v.CreatedAt.Time.UTC(),
					},
				)
			}
			return nil
		},
	)
	return result, err
}

func (s *DefaultChannelInvocationService) Retry(
	ctx context.Context,
	p auth.UserPrincipal,
	input ChannelInvocationRetry,
) (ChannelInvocationPage, error) {
	if input.RequestKey == uuid.Nil {
		return ChannelInvocationPage{}, ErrDiscussionInvalid
	}
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := lockDiscussionMembership(ctx, q, p); err != nil {
			return err
		}
		if err := lockDiscussion(ctx, q, p, input.ChannelID); err != nil {
			return err
		}
		access, err := currentDiscussion(ctx, q, p, input.ChannelID, false)
		if err != nil {
			return err
		}
		if !discussionOpen(access) ||
			!auth.CompanyRole(access.CompanyRole).Can(auth.PermissionConversationsDiscuss) {
			return ErrDiscussionForbidden
		}
		v, err := q.GetChannelInvocation(
			ctx,
			generated.GetChannelInvocationParams{CompanyID: p.CompanyID, InvocationID: input.InvocationID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDiscussionNotFound
		}
		if err != nil {
			return err
		}
		if v.ChannelID != input.ChannelID || v.CallerUserID != p.UserID {
			return ErrDiscussionForbidden
		}
		if _, err = q.LockConversationForSystemRunQueueMutation(
			ctx,
			generated.LockConversationForSystemRunQueueMutationParams{
				CompanyID:      p.CompanyID,
				ConversationID: v.ConversationID,
			},
		); err != nil {
			return err
		}
		allowed, err := q.ChannelInvocationAuthorized(
			ctx,
			generated.ChannelInvocationAuthorizedParams{
				InvocationID: v.ID,
				ActorRoles:   auth.RolesWith(auth.PermissionConversationsDiscuss),
			},
		)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrDiscussionForbidden
		}
		if v.RequestKey.Valid && v.RequestKey.UUID == input.RequestKey {
			return nil
		}
		if v.Status != "failed" || v.PublicationStatus == "published" {
			return ErrDiscussionRevisionConflict
		}
		if err = execution.AuthorizeAgentInvocationInTx(
			ctx,
			q,
			execution.RunTrigger{CompanyID: p.CompanyID, AgentID: v.AgentID, AuthorizedByUserID: p.UserID},
		); err != nil {
			return err
		}
		v, err = q.RetryChannelInvocation(
			ctx,
			generated.RetryChannelInvocationParams{InvocationID: v.ID, RequestKey: input.RequestKey},
		)
		if err != nil {
			return err
		}
		pending = append(pending, channelAgentsChangedEvent(v))
		_, err = promoteChannelInvocation(ctx, q, &pending, v.ConversationID)
		return err
	})
	if err != nil {
		return ChannelInvocationPage{}, err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return s.List(ctx, p, input.ChannelID)
}
