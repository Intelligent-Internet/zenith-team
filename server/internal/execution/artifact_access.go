package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type ArtifactPermission string

const (
	ArtifactPermissionView   ArtifactPermission = "view"
	ArtifactPermissionEdit   ArtifactPermission = "edit"
	ArtifactPermissionManage ArtifactPermission = "manage"
)

type ArtifactGrant struct {
	UserID      uuid.UUID          `json:"userId"`
	DisplayName string             `json:"displayName"`
	Permission  ArtifactPermission `json:"permission"`
	GrantedAt   time.Time          `json:"grantedAt"`
}

type ArtifactAccess struct {
	Visibility         string                      `json:"visibility"`
	PublicToken        *uuid.UUID                  `json:"publicToken,omitempty"`
	ConversationGrants []ArtifactConversationGrant `json:"conversationGrants"`
	OwnerUserID        *uuid.UUID                  `json:"ownerUserId"`
	OwnerDisplayName   *string                     `json:"ownerDisplayName"`
	OwnerActive        bool                        `json:"ownerActive"`
	Permission         ArtifactPermission          `json:"permission"`
	Revision           int64                       `json:"revision"`
	Grants             []ArtifactGrant             `json:"grants"`
}

type ArtifactSource struct {
	ConversationID uuid.UUID  `json:"conversationId"`
	Type           string     `json:"type"`
	TaskID         *uuid.UUID `json:"taskId,omitempty"`
	AgentID        *uuid.UUID `json:"agentId,omitempty"`
}

type ArtifactDetail struct {
	Source *ArtifactSource      `json:"source,omitempty"`
	RunID  *uuid.UUID           `json:"runId"`
	Result DeliverableRunResult `json:"result"`
	Access ArtifactAccess       `json:"access"`
	URL    string               `json:"url"`
}

type UnownedArtifact struct {
	ID        uuid.UUID  `json:"id"`
	RunID     *uuid.UUID `json:"runId"`
	TaskID    *uuid.UUID `json:"taskId,omitempty"`
	Revision  int64      `json:"revision"`
	CreatedAt time.Time  `json:"createdAt"`
}

func (s *DefaultDeliverableService) ListUnownedArtifacts(
	ctx context.Context, principal auth.UserPrincipal,
) ([]UnownedArtifact, error) {
	membership, err := s.store.Queries().GetActiveCompanyMembership(ctx,
		dbgen.GetActiveCompanyMembershipParams{CompanyID: principal.CompanyID, UserID: principal.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeliverableNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Artifact recovery membership: %w", err)
	}
	if !auth.CompanyRole(membership.Role).Can(auth.PermissionResultsReview) {
		return nil, ErrDeliverableForbidden
	}
	rows, err := s.store.Queries().ListUnownedOutputs(ctx, principal.CompanyID)
	if err != nil {
		return nil, fmt.Errorf("list unowned Artifacts: %w", err)
	}
	result := make([]UnownedArtifact, 0, len(rows))
	for _, row := range rows {
		item := UnownedArtifact{
			ID: row.ID, RunID: util.NullUUIDPtr(row.RunID),
			Revision: row.AccessVersion, CreatedAt: row.CreatedAt.Time,
		}
		if row.TaskID.Valid {
			item.TaskID = &row.TaskID.UUID
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *DefaultDeliverableService) ClaimUnownedArtifact(
	ctx context.Context, principal auth.UserPrincipal, outputID uuid.UUID, expectedRevision int64,
) (ArtifactDetail, error) {
	if !util.ValidUUID(outputID) || expectedRevision < 1 {
		return ArtifactDetail{}, ErrDeliverableInvalidInput
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		role, active, err := lockActiveMember(ctx, q, principal.CompanyID, principal.UserID)
		if err != nil {
			return err
		}
		if !active || !role.Can(auth.PermissionResultsReview) {
			return ErrDeliverableForbidden
		}
		_, err = q.ClaimUnownedOutput(ctx, dbgen.ClaimUnownedOutputParams{
			CompanyID: principal.CompanyID, OutputID: outputID,
			UserID: principal.UserID, ExpectedRevision: expectedRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeliverableConflict
		}
		if err != nil {
			return fmt.Errorf("claim unowned Artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		return ArtifactDetail{}, err
	}
	s.bus.Publish(ctx, TopicArtifact, events.Event{
		EntityType: TopicArtifact, Name: EventArtifactAccessChanged,
		CompanyID: principal.CompanyID, CreatedAt: time.Now().UTC(),
		Actor: events.SystemActor(),
		Payload: ArtifactAccessChanged{
			RecipientUserIDs: []uuid.UUID{principal.UserID},
		},
	})
	return s.GetArtifact(ctx, principal, outputID)
}

func (s *DefaultDeliverableService) GetArtifact(
	ctx context.Context, principal auth.UserPrincipal, outputID uuid.UUID,
) (ArtifactDetail, error) {
	access, err := s.getArtifactAccess(ctx, principal, outputID)
	if err != nil {
		return ArtifactDetail{}, err
	}
	row, err := s.store.Queries().GetOutputAccess(ctx, dbgen.GetOutputAccessParams{
		CompanyID: principal.CompanyID, OutputID: outputID, ActorUserID: principal.UserID,
	})
	if err != nil {
		return ArtifactDetail{}, artifactReadError(err)
	}
	var result DeliverableRunResult
	if row.RunID.Valid {
		result, err = s.FetchRunArtifactResult(ctx, principal, row.RunID.UUID, outputID)
	} else {
		result, err = s.fetchRetainedArtifactResult(ctx, principal, outputID)
	}
	if err != nil {
		return ArtifactDetail{}, err
	}
	detail := ArtifactDetail{RunID: util.NullUUIDPtr(row.RunID), Result: result, Access: access}
	if !row.ConversationID.Valid {
		return detail, nil
	}
	conversationID := row.ConversationID.UUID
	parent, err := s.store.Queries().GetChannelAgentParent(ctx, dbgen.GetChannelAgentParentParams{
		CompanyID: principal.CompanyID, ConversationID: conversationID,
	})
	if err == nil {
		conversationID = parent
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ArtifactDetail{}, fmt.Errorf("read artifact source channel: %w", err)
	}
	source, err := s.store.Queries().GetConversationForArtifactList(ctx, dbgen.GetConversationForArtifactListParams{
		ConversationID: conversationID, CompanyID: principal.CompanyID, ActorUserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return detail, nil
	}
	if err != nil {
		return ArtifactDetail{}, fmt.Errorf("read artifact source conversation: %w", err)
	}
	detail.Source = &ArtifactSource{ConversationID: source.ID, Type: source.Kind}
	if source.TaskID.Valid {
		detail.Source.TaskID = &source.TaskID.UUID
	}
	if source.AgentID.Valid {
		detail.Source.AgentID = &source.AgentID.UUID
	}
	return detail, nil
}

// fetchRetainedArtifactResult reads delivery evidence without an execution row.
func (s *DefaultDeliverableService) fetchRetainedArtifactResult(
	ctx context.Context,
	principal auth.UserPrincipal,
	outputID uuid.UUID,
) (DeliverableRunResult, error) {
	q := s.store.Queries()
	if err := canvas.PresentationOutputAccessInTx(ctx, q, principal, outputID); err != nil {
		return DeliverableRunResult{}, mapPresentationError(err)
	}
	rows, err := q.ListArtifactsByID(
		ctx,
		dbgen.ListArtifactsByIDParams{
			CompanyID:   principal.CompanyID,
			ActorUserID: principal.UserID,
			OutputIds:   []uuid.UUID{outputID},
		},
	)
	if err != nil {
		return DeliverableRunResult{}, err
	}
	projected := make([]dbgen.ListConversationArtifactsRow, len(rows))
	for i, row := range rows {
		projected[i] = dbgen.ListConversationArtifactsRow(row)
	}
	results, err := artifactRunResults(ctx, q, principal.CompanyID, principal.UserID, projected)
	if err != nil {
		return DeliverableRunResult{}, err
	}
	if len(results) == 0 {
		return DeliverableRunResult{}, ErrDeliverableNotFound
	}
	item := results[0]
	result := DeliverableRunResult{
		RuntimeID:   item.RuntimeID,
		RunStatus:   item.RunStatus,
		Output:      &item.Artifact,
		Deliverable: item.Deliverable,
		Publication: item.Publication,
	}
	source, err := q.GetOutputStudioCanvas(
		ctx,
		dbgen.GetOutputStudioCanvasParams{CompanyID: principal.CompanyID, OutputID: outputID},
	)
	if err == nil {
		saved := SavedStudioCanvas{AddedAt: source.CreatedAt.Time}
		if err = json.Unmarshal(source.Canvas, &saved.Canvas); err != nil {
			return DeliverableRunResult{}, fmt.Errorf("decode retained Studio source: %w", err)
		}
		result.StudioCanvas = &saved
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return DeliverableRunResult{}, fmt.Errorf("read retained Studio source: %w", err)
	}
	return result, nil
}

func (s *DefaultDeliverableService) getArtifactAccess(
	ctx context.Context, principal auth.UserPrincipal, outputID uuid.UUID,
) (ArtifactAccess, error) {
	row, err := s.store.Queries().GetOutputAccess(ctx, dbgen.GetOutputAccessParams{
		CompanyID: principal.CompanyID, OutputID: outputID, ActorUserID: principal.UserID,
	})
	if err != nil {
		return ArtifactAccess{}, artifactReadError(err)
	}
	grants, err := s.store.Queries().ListOutputGrants(ctx, dbgen.ListOutputGrantsParams{
		CompanyID: principal.CompanyID, OutputID: outputID,
	})
	if err != nil {
		return ArtifactAccess{}, fmt.Errorf("list Artifact grants: %w", err)
	}
	groupGrants, err := s.store.Queries().
		ListOutputConversationGrants(ctx, dbgen.ListOutputConversationGrantsParams{CompanyID: principal.CompanyID, OutputID: outputID, ActorUserID: principal.UserID})
	if err != nil {
		return ArtifactAccess{}, err
	}
	view := ArtifactAccess{
		Visibility:         row.Visibility,
		ConversationGrants: []ArtifactConversationGrant{},
		Permission:         artifactPermissionFromLevel(row.AccessLevel), Revision: row.AccessVersion,
		OwnerActive: row.OwnerActive,
		Grants:      make([]ArtifactGrant, 0, len(grants)),
	}
	if row.AccessLevel >= 3 && row.PublicToken.Valid {
		view.PublicToken = &row.PublicToken.UUID
	}
	for _, grant := range groupGrants {
		view.ConversationGrants = append(
			view.ConversationGrants,
			ArtifactConversationGrant{
				ConversationID: grant.ConversationID,
				Title:          grant.Title,
				GrantedAt:      grant.CreatedAt.Time,
			},
		)
	}
	if row.OwnerUserID.Valid {
		view.OwnerUserID = &row.OwnerUserID.UUID
	}
	if row.OwnerDisplayName.Valid {
		view.OwnerDisplayName = &row.OwnerDisplayName.String
	}
	for _, grant := range grants {
		view.Grants = append(view.Grants, ArtifactGrant{
			UserID: grant.UserID, DisplayName: grant.DisplayName,
			Permission: ArtifactPermission(grant.Permission), GrantedAt: grant.CreatedAt.Time,
		})
	}
	return view, nil
}

func artifactPermissionFromLevel(level int32) ArtifactPermission {
	switch level {
	case 3:
		return ArtifactPermissionManage
	case 2:
		return ArtifactPermissionEdit
	default:
		return ArtifactPermissionView
	}
}

func artifactReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeliverableNotFound
	}
	return fmt.Errorf("read Artifact: %w", err)
}

func (s *DefaultDeliverableService) ChangeArtifactGrant(
	ctx context.Context, principal auth.UserPrincipal, outputID, recipientID uuid.UUID,
	permission *ArtifactPermission, expectedRevision int64,
) (ArtifactAccess, error) {
	if !util.ValidUUID(outputID) || !util.ValidUUID(recipientID) ||
		recipientID == principal.UserID || expectedRevision < 1 ||
		permission != nil && *permission != ArtifactPermissionView &&
			*permission != ArtifactPermissionManage && *permission != ArtifactPermissionEdit {
		return ArtifactAccess{}, ErrDeliverableInvalidInput
	}
	var ownerID uuid.UUID
	var changed bool
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		_, active, err := lockActiveMember(ctx, q, principal.CompanyID, principal.UserID)
		if err != nil {
			return err
		}
		if !active {
			return ErrDeliverableNotFound
		}
		output, err := q.LockOutputForAccessChange(ctx, dbgen.LockOutputForAccessChangeParams{
			CompanyID: principal.CompanyID, OutputID: outputID,
		})
		if err != nil {
			return artifactReadError(err)
		}
		level, err := q.GetOutputAccessForManage(ctx, dbgen.GetOutputAccessForManageParams{
			CompanyID: principal.CompanyID, OutputID: outputID, ActorUserID: principal.UserID,
		})
		if err != nil {
			return fmt.Errorf("authorize Artifact manager: %w", err)
		}
		if level == 0 {
			return ErrDeliverableNotFound
		}
		if level < 3 || output.OwnerUserID.Valid && output.OwnerUserID.UUID == recipientID {
			return ErrDeliverableForbidden
		}
		if output.AccessVersion != expectedRevision {
			return ErrDeliverableConflict
		}
		if output.OwnerUserID.Valid {
			ownerID = output.OwnerUserID.UUID
		}
		if permission == nil {
			removed, err := q.DeleteOutputGrant(
				ctx,
				dbgen.DeleteOutputGrantParams{
					CompanyID: principal.CompanyID,
					OutputID:  outputID,
					UserID:    recipientID,
				},
			)
			if err != nil {
				return fmt.Errorf("revoke Artifact grant: %w", err)
			}
			if removed == 0 {
				return nil
			}

		} else {
			_, err := q.UpsertOutputGrant(ctx, dbgen.UpsertOutputGrantParams{
				CompanyID: principal.CompanyID, OutputID: outputID,
				UserID: recipientID, Permission: string(*permission), ActorUserID: principal.UserID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDeliverableInvalidInput
			}
			if err != nil {
				return fmt.Errorf("grant Artifact access: %w", err)
			}
		}
		_, err = q.BumpOutputAccessVersion(ctx, dbgen.BumpOutputAccessVersionParams{
			CompanyID: principal.CompanyID, OutputID: outputID,
		})
		if err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return ArtifactAccess{}, err
	}
	if changed {
		s.bus.Publish(ctx, TopicArtifact, events.Event{
			EntityType: TopicArtifact, Name: EventArtifactAccessChanged,
			CompanyID: principal.CompanyID, CreatedAt: time.Now().UTC(),
			Actor: events.SystemActor(),
			Payload: ArtifactAccessChanged{
				RecipientUserIDs: []uuid.UUID{ownerID, principal.UserID, recipientID},
			},
		})
	}
	return s.getArtifactAccess(ctx, principal, outputID)
}
