package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// RosterProjection is the Agent-owned organization snapshot a composite read
// can reuse inside its own transaction.
type RosterProjection struct {
	Agents []Response
}

// ReadRosterProjectionInTx loads the Company roster with one bulk query on a
// caller's transaction-bound query handle.
func ReadRosterProjectionInTx(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
) (RosterProjection, error) {
	rows, err := listProjectionRows(ctx, q, principal, projectionOptions{
		ListOptions: ListOptions{IncludeArchived: true},
	})
	if err != nil {
		return RosterProjection{}, err
	}
	agents := make([]Response, len(rows))
	for index, row := range rows {
		agents[index] = responseFromProjectionRow(row, principal)
	}
	if err := applyActiveRunCounts(ctx, q, principal, agents); err != nil {
		return RosterProjection{}, err
	}
	return RosterProjection{Agents: agents}, nil
}

// ListProjectionsForMember maps the canonical bulk Agent projection using a
// caller's query handle. Callers that own a snapshot transaction can compose
// it without reaching through a pool-owning service.
func ListProjectionsForMember(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	options ListOptions,
) ([]Response, error) {
	rows, err := listProjectionRows(ctx, q, principal, projectionOptions{
		ListOptions: options,
	})
	if err != nil {
		return nil, err
	}
	responses := make([]Response, len(rows))
	for index, row := range rows {
		responses[index] = responseFromProjectionRow(row, principal)
	}
	if err := applyActiveRunCounts(ctx, q, principal, responses); err != nil {
		return nil, err
	}
	return responses, nil
}

// RootProjectionForMember returns the active root Agent for a caller-owned
// snapshot, or nil until the Company has configured one.
func RootProjectionForMember(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
) (*Response, error) {
	rows, err := listProjectionRows(ctx, q, principal, projectionOptions{RootOnly: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	responses := []Response{responseFromProjectionRow(rows[0], principal)}
	if err := applyActiveRunCounts(ctx, q, principal, responses); err != nil {
		return nil, err
	}
	return &responses[0], nil
}

type projectionOptions struct {
	ListOptions
	AgentID  *uuid.UUID
	RootOnly bool
}

func listProjectionRows(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	options projectionOptions,
) ([]generated.ListAgentProjectionsForActorRow, error) {
	rows, err := q.ListAgentProjectionsForActor(ctx, generated.ListAgentProjectionsForActorParams{
		ActorUserID:      principal.UserID,
		CompanyID:        principal.CompanyID,
		IncludeArchived:  options.IncludeArchived,
		ReportsToAgentID: util.NullUUIDFromPtr(options.ReportsToAgentID),
		HasAgentID:       options.AgentID != nil,
		AgentID:          valueOrNilUUID(options.AgentID),
		RootOnly:         options.RootOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list canonical Agent projections: %w", err)
	}
	return rows, nil
}

func projectionForMember(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) (Response, error) {
	rows, err := listProjectionRows(ctx, q, principal, projectionOptions{
		ListOptions: ListOptions{IncludeArchived: true},
		AgentID:     &agentID,
	})
	if err != nil {
		return Response{}, err
	}
	if len(rows) == 0 {
		return Response{}, ErrNotFound
	}
	responses := []Response{responseFromProjectionRow(rows[0], principal)}
	if err := applyActiveRunCounts(ctx, q, principal, responses); err != nil {
		return Response{}, err
	}
	return responses[0], nil
}

// applyActiveRunCounts stamps ActiveRunCount onto already-mapped Responses:
// the viewer's binding runtime's count, since capacity is per binding
// (ADR 0014). One grouped query per projection read, whatever the roster
// size; the count's predicate is the claim gate's own — see
// CountActiveTaskLaneRunsByAgentForBinder — so the rendered number and the
// enforced cap agree. An agent absent from the result simply has no active
// task-lane runs on the viewer's machine, which the zero value already says.
func applyActiveRunCounts(
	ctx context.Context,
	q *generated.Queries,
	viewer auth.UserPrincipal,
	responses []Response,
) error {
	if len(responses) == 0 {
		return nil
	}
	rows, err := q.CountActiveTaskLaneRunsByAgentForBinder(
		ctx,
		generated.CountActiveTaskLaneRunsByAgentForBinderParams{
			UserID: viewer.UserID, CompanyID: viewer.CompanyID,
		},
	)
	if err != nil {
		return fmt.Errorf("count active task-lane Runs per Agent: %w", err)
	}
	counts := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		counts[row.AgentID] = row.ActiveRunCount
	}
	for index := range responses {
		responses[index].ActiveRunCount = counts[responses[index].ID]
	}
	return nil
}

// responseFromProjectionRow maps one canonical row for the viewer who asked:
// the binding, the availability and Access.Execute are that viewer's own
// setup of the Agent (ADR 0014) and Access.Configure their right to
// configure it (ADR 0013) — the facts on the row that are not shared by
// every reader.
func responseFromProjectionRow(row generated.ListAgentProjectionsForActorRow, viewer auth.UserPrincipal) Response {
	response := FromRow(row.Agent)
	if row.BindingID.Valid && row.RuntimeID.Valid {
		response.Binding = &BindingRef{
			ID:      row.BindingID.UUID,
			AgentID: row.Agent.ID,
			Runtime: RuntimeRef{
				ID:         row.RuntimeID.UUID,
				Name:       row.RuntimeName.String,
				CustomName: util.TextToPtr(row.RuntimeCustomName),
				Status:     row.RuntimeStatus.String,
				Provider:   row.Agent.Provider,
				SignIn: runtimeSignInRef(
					row.RuntimeSignInState,
					row.RuntimeSignInDetail,
					row.RuntimeSignInCheckedAt,
				),
				UnusableReason: runtimeUnusableReasonRef(
					row.RuntimeUnusableReasonCode,
					row.RuntimeUnusableReasonMessage,
				),
			},
			CreatedAt: row.BindingCreatedAt.Time.UTC(),
		}
	}
	response.Access = Access{
		Configure: canConfigureAgent(viewer.Role, row.Agent, viewer.UserID),
		Execute:   row.RuntimeAvailable,
	}
	if row.Agent.CreatedByUserID.Valid {
		response.CreatedBy = &OwnerRef{
			ID:          row.Agent.CreatedByUserID.UUID,
			Kind:        protocol.ActorKindUser,
			DisplayName: row.CreatorDisplayName.String,
			AvatarURL:   util.TextToPtr(row.CreatorAvatarUrl),
		}
	}
	if row.Agent.ReportsToAgentID.Valid {
		response.ReportsTo = &NamedRef{ID: row.ParentAgentID, Name: row.ParentAgentName}
	}
	response.RuntimeAvailable = row.RuntimeAvailable
	if row.UnavailableReason != "" {
		reason := row.UnavailableReason
		response.UnavailableReason = &reason
	}
	return response
}

// runtimeSignInRef maps the stored sign-in verdict onto a RuntimeRef; nil
// when no registration has carried one yet.
func runtimeSignInRef(state, detail pgtype.Text, checkedAt pgtype.Timestamptz) *RuntimeSignIn {
	if !state.Valid {
		return nil
	}
	return &RuntimeSignIn{State: state.String, Detail: detail.String, CheckedAt: checkedAt.Time.UTC()}
}

// runtimeUnusableReasonRef maps the stored unusable reason onto a
// RuntimeRef; nil for an ordinary row. The CHECK constraint keeps code and
// message together, so one column decides presence.
func runtimeUnusableReasonRef(code, message pgtype.Text) *protocol.RuntimeUnusableReason {
	if !code.Valid {
		return nil
	}
	return &protocol.RuntimeUnusableReason{Code: code.String, Message: message.String}
}

func valueOrNilUUID(value *uuid.UUID) uuid.UUID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}
