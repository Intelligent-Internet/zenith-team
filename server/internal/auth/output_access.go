package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// OutputAccessInTx fences content operations against membership and sharing
// changes. The caller owns the transaction. Visibility only grants reading;
// level 2 is an explicit editor, and level 3 is an owner or manager.
func OutputAccessInTx(ctx context.Context, q *generated.Queries, p UserPrincipal, output uuid.UUID) (int32, error) {
	if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	member, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if member.Status != "active" {
		return 0, nil
	}
	if _, err = q.LockOutputForAccessChange(
		ctx,
		generated.LockOutputForAccessChangeParams{CompanyID: p.CompanyID, OutputID: output},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return q.GetOutputAccessForManage(
		ctx,
		generated.GetOutputAccessForManageParams{
			CompanyID:   p.CompanyID,
			OutputID:    output,
			ActorUserID: p.UserID,
		},
	)
}

// OutputDocumentAccess separates source content authority from its placement.
// A Project placement never upgrades a grant on a standalone Output.
type OutputDocumentAccess struct{ Read, Edit bool }

func OutputDocumentAccessInTx(
	ctx context.Context,
	q *generated.Queries,
	p UserPrincipal,
	output uuid.UUID,
) (OutputDocumentAccess, error) {
	level, err := OutputAccessInTx(ctx, q, p, output)
	if err != nil || level == 0 {
		return OutputDocumentAccess{}, err
	}
	project, err := q.OutputDocumentProject(
		ctx,
		generated.OutputDocumentProjectParams{CompanyID: p.CompanyID, ID: output},
	)
	if err != nil {
		return OutputDocumentAccess{}, err
	}
	if !project.Valid {
		return OutputDocumentAccess{Read: true, Edit: level >= 2}, nil
	}
	row, err := q.GetProjectForActiveMember(
		ctx,
		generated.GetProjectForActiveMemberParams{
			CompanyID:       p.CompanyID,
			ProjectID:       project.UUID,
			ActorUserID:     p.UserID,
			ManagementRoles: []string{},
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutputDocumentAccess{}, nil
	}
	if err != nil {
		return OutputDocumentAccess{}, err
	}
	read, err := HoldsProjectPermission(ctx, q, p.CompanyID, project.UUID, p.UserID, ProjectPermissionRead)
	if err != nil || !read {
		return OutputDocumentAccess{}, err
	}
	edit, err := HoldsProjectPermission(ctx, q, p.CompanyID, project.UUID, p.UserID, ProjectPermissionTasksWrite)
	return OutputDocumentAccess{Read: true, Edit: edit && !row.Project.ArchivedAt.Valid}, err
}
