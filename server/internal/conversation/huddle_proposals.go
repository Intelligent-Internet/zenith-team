package conversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	ErrHuddleProposalConflict = errors.New("huddle proposal changed or is no longer editable")
	ErrHuddleProposalLimit    = errors.New("huddle proposal limit reached")
)

// HuddleProposal is a shared draft, never an execution authorization.
// The creating Agent is attribution; no speaker or responsible member is inferred.
type HuddleProposal struct {
	Work             *voice.WorkProgress `json:"work,omitempty"`
	ID               uuid.UUID           `json:"id"`
	AgentID          uuid.UUID           `json:"agentId"`
	AgentName        string              `json:"agentName"`
	ProjectID        *uuid.UUID          `json:"projectId"`
	ProjectTitle     string              `json:"projectTitle"`
	TaskID           *uuid.UUID          `json:"taskId"`
	TaskKey          string              `json:"taskKey"`
	Title            string              `json:"title"`
	Brief            string              `json:"brief"`
	State            string              `json:"state"`
	Revision         int64               `json:"revision"`
	CanEdit          bool                `json:"canEdit"`
	SubmittedTaskID  *uuid.UUID          `json:"submittedTaskId"`
	SubmittedTaskKey string              `json:"submittedTaskKey"`
	UpdatedByUserID  *uuid.UUID          `json:"updatedByUserId"`
	ExpiresAt        time.Time           `json:"expiresAt"`
	CreatedAt        time.Time           `json:"createdAt"`
}

type HuddleProposalInput struct {
	RequestKey string
	ProjectID  uuid.UUID
	TaskID     uuid.NullUUID
	Title      string
	Brief      string
}

type UpdateHuddleProposalInput struct {
	ProposalID       uuid.UUID
	ExpectedRevision int64
	Action           string
	Title            string
	Brief            string
}

func normalizeHuddleProposalInput(in HuddleProposalInput) (HuddleProposalInput, error) {
	in.Title, in.Brief = strings.TrimSpace(in.Title), strings.TrimSpace(in.Brief)
	if in.RequestKey == "" || len(in.RequestKey) > 128 || in.ProjectID == uuid.Nil {
		return in, ErrHuddleInvalid
	}
	if !validHuddleProposalText(in.Title, in.Brief) {
		return in, ErrHuddleInvalid
	}
	return in, nil
}

func validHuddleProposalText(title, brief string) bool {
	return title != "" && brief != "" && utf8.RuneCountInString(title) <= 200 &&
		utf8.RuneCountInString(brief) <= 4000
}

func (s *DefaultHuddleAIService) ProposeWork(
	ctx context.Context,
	attemptID uuid.UUID,
	token string,
	in HuddleProposalInput,
) (HuddleProposal, error) {
	in, err := normalizeHuddleProposalInput(in)
	if err != nil {
		return HuddleProposal{}, err
	}
	encoded, err := json.Marshal(
		map[string]any{
			"projectId": in.ProjectID,
			"taskId":    util.NullUUIDPtr(in.TaskID),
			"title":     in.Title,
			"brief":     in.Brief,
		},
	)
	if err != nil {
		return HuddleProposal{}, err
	}
	hash := sha256.Sum256(encoded)
	var result HuddleProposal
	err = s.withAttempt(
		ctx,
		attemptID,
		func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
			if err := checkHuddleAICapability(a, token); err != nil {
				return err
			}
			if err := s.authorized(ctx, q, h, state, a); err != nil {
				return err
			}
			if !state.AllowCompanyContext {
				return ErrHuddleForbidden
			}
			previous, err := q.GetHuddleProposalReplay(ctx, generated.GetHuddleProposalReplayParams{
				CompanyID:          h.CompanyID,
				HuddleID:           h.ID,
				InvitationRevision: state.Revision,
				RequestKey:         in.RequestKey,
			})
			if err == nil {
				if !bytes.Equal(previous.RequestHash, hash[:]) {
					return ErrHuddleProposalConflict
				}
				result = huddleProposalProjection(previous, h, state.Revision, state.Disabled, false)
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			count, err := q.CountHuddleProposals(
				ctx,
				generated.CountHuddleProposalsParams{CompanyID: h.CompanyID, HuddleID: h.ID},
			)
			if err != nil {
				return err
			}
			if count >= 20 {
				return ErrHuddleProposalLimit
			}
			target, err := q.GetHuddleProposalTarget(
				ctx,
				generated.GetHuddleProposalTargetParams{
					CompanyID: h.CompanyID,
					ProjectID: in.ProjectID,
					TaskID:    in.TaskID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrHuddleNotFound
			}
			if err != nil {
				return err
			}
			var profile voice.Profile
			if err = json.Unmarshal(state.AgentProfile, &profile); err != nil {
				return err
			}
			expires := time.Now().Add(time.Hour)
			if h.DeadlineAt.Time.Before(expires) {
				expires = h.DeadlineAt.Time
			}
			row, err := q.CreateHuddleProposal(ctx, generated.CreateHuddleProposalParams{
				CompanyID:          h.CompanyID,
				HuddleID:           h.ID,
				AttemptID:          uuid.NullUUID{UUID: a.ID, Valid: true},
				InvitationRevision: state.Revision,
				CreatedByAgentID:   state.AgentID.UUID,
				AgentName:          profile.Name,
				ProjectID:          uuid.NullUUID{UUID: in.ProjectID, Valid: true},
				ProjectTitle:       target.ProjectTitle,
				TaskID:             in.TaskID,
				TaskKey:            target.TaskKey,
				RequestKey:         in.RequestKey,
				RequestHash:        hash[:],
				Title:              in.Title,
				Brief:              in.Brief,
				ExpiresAt:          pgtype.Timestamptz{Time: expires, Valid: true},
			})
			if err != nil {
				return err
			}
			result = huddleProposalProjection(row, h, state.Revision, state.Disabled, false)
			return nil
		},
	)
	if err == nil {
		s.signalAttempt(ctx, attemptID)
	}
	return result, err
}

func (s *DefaultHuddleAIService) UpdateWorkProposal(
	ctx context.Context,
	p auth.UserPrincipal,
	huddleID uuid.UUID,
	in UpdateHuddleProposalInput,
) (HuddleProposal, error) {
	in.Title, in.Brief = strings.TrimSpace(in.Title), strings.TrimSpace(in.Brief)
	if in.ExpectedRevision < 1 {
		return HuddleProposal{}, ErrHuddleInvalid
	}
	switch in.Action {
	case "edit":
		if !validHuddleProposalText(in.Title, in.Brief) {
			return HuddleProposal{}, ErrHuddleInvalid
		}
	case "reject":
		if in.Title != "" || in.Brief != "" {
			return HuddleProposal{}, ErrHuddleInvalid
		}
	default:
		return HuddleProposal{}, ErrHuddleInvalid
	}
	var result HuddleProposal
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, role, err := lockHuddleAIActor(ctx, q, p, huddleID)
		if err != nil {
			return err
		}
		if h.StartedByUserID != p.UserID && !role.Can(auth.PermissionCompanyProfileUpdate) {
			return ErrHuddleForbidden
		}
		state, err := q.LockHuddleAIState(
			ctx,
			generated.LockHuddleAIStateParams{CompanyID: h.CompanyID, HuddleID: h.ID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleNotFound
		}
		if err != nil {
			return err
		}
		row, err := q.GetHuddleProposal(
			ctx,
			generated.GetHuddleProposalParams{CompanyID: h.CompanyID, HuddleID: h.ID, ID: in.ProposalID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleNotFound
		}
		if err != nil {
			return err
		}
		current := huddleProposalProjection(row, h, state.Revision, state.Disabled, true)
		if !current.CanEdit || row.Revision != in.ExpectedRevision {
			return ErrHuddleProposalConflict
		}
		if in.Action == "reject" {
			row.State = "rejected"
		} else {
			row.Title, row.Brief = in.Title, in.Brief
		}
		row, err = q.UpdateHuddleProposal(ctx, generated.UpdateHuddleProposalParams{
			CompanyID: h.CompanyID, HuddleID: h.ID, ID: row.ID, Title: row.Title, Brief: row.Brief,
			State: row.State, UpdatedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
		})
		if err != nil {
			return err
		}
		result = huddleProposalProjection(row, h, state.Revision, state.Disabled, true)
		return nil
	})
	if err == nil {
		s.signalStatus(ctx, p.CompanyID, huddleID)
	}
	return result, err
}

func huddleProposalProjection(
	row generated.HuddleWorkProposal,
	h generated.Huddle,
	_ int64,
	_, moderate bool,
) HuddleProposal {
	state := row.State
	if state == "pending" {
		if h.State != "open" || !h.DeadlineAt.Time.After(time.Now()) || !row.ExpiresAt.Time.After(time.Now()) {
			state = "expired"
		}
	}
	return HuddleProposal{
		ID:               row.ID,
		AgentID:          row.CreatedByAgentID,
		AgentName:        row.AgentName,
		ProjectID:        util.NullUUIDPtr(row.ProjectID),
		ProjectTitle:     row.ProjectTitle,
		TaskID:           util.NullUUIDPtr(row.TaskID),
		TaskKey:          row.TaskKey,
		Title:            row.Title,
		Brief:            row.Brief,
		State:            state,
		Revision:         row.Revision,
		CanEdit:          moderate && state == "pending",
		SubmittedTaskID:  util.NullUUIDPtr(row.SubmittedTaskID),
		SubmittedTaskKey: row.SubmittedTaskKey.String,
		UpdatedByUserID: util.NullUUIDPtr(
			row.UpdatedByUserID,
		),
		ExpiresAt: row.ExpiresAt.Time,
		CreatedAt: row.CreatedAt.Time,
	}
}
