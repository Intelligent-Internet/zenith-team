package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ArtifactAccess describes authority over live work. A delivery, snapshot or
// placement never enters this decision. Wiki callers still apply publication
// rules when choosing which document state a reader may see.
type ArtifactAccess struct {
	Read, Edit, Manage bool
}

func ResolveArtifactAccess(
	ctx context.Context,
	q *generated.Queries,
	p UserPrincipal,
	id uuid.UUID,
) (ArtifactAccess, error) {
	row, err := q.ReadArtifactAccess(ctx, generated.ReadArtifactAccessParams{
		CompanyID: p.CompanyID, ArtifactID: id, ActorUserID: p.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactAccess{}, nil
	}
	if err != nil {
		return ArtifactAccess{}, err
	}
	access := ArtifactAccess{Read: true}
	if row.Artifact.ArchivedAt.Valid || row.ProjectArchived {
		return access, nil
	}
	if row.Artifact.Type == "wiki" {
		access.Edit = CompanyRole(row.Role).Can(PermissionWikiPublish)
		access.Manage = access.Edit
		return access, nil
	}
	if row.Artifact.ProjectID.Valid {
		project, err := ResolveProjectAccess(ctx, q, p, row.Artifact.ProjectID.UUID)
		if errors.Is(err, ErrProjectNotVisible) {
			return ArtifactAccess{}, nil
		}
		if err != nil {
			return ArtifactAccess{}, err
		}
		access.Read = project.Can(ProjectPermissionRead)
		permission := ProjectPermissionTasksWrite
		if row.Artifact.Type == "file" || row.Artifact.Type == "media" || row.Artifact.Type == "app" {
			permission = ProjectPermissionFilesManage
		}
		access.Edit = access.Read && project.Can(permission)
		access.Manage = access.Read && project.Can(ProjectPermissionGrantsManage)
		return access, nil
	}
	access.Edit = row.AccessLevel >= 2
	access.Manage = row.AccessLevel >= 3
	return access, nil
}

// LockArtifactLifecycle takes the Company, membership and Project fences before
// callers lock Conversations or Runs. It grants no authority: callers must
// subsequently use LockArtifactAccess before changing the Artifact. Missing or
// inactive membership does not prevent terminal Run transitions.
func LockArtifactLifecycle(ctx context.Context, q *generated.Queries, p UserPrincipal, id uuid.UUID) error {
	if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err := q.GetCompanyMemberForShare(ctx, generated.GetCompanyMemberForShareParams{
		CompanyID: p.CompanyID, UserID: p.UserID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	identity, err := q.GetArtifactIdentity(ctx, generated.GetArtifactIdentityParams{CompanyID: p.CompanyID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if identity.ProjectID.Valid {
		_, err = q.LockProjectForAccessRead(ctx, generated.LockProjectForAccessReadParams{
			CompanyID: p.CompanyID, ProjectID: identity.ProjectID.UUID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

// LockArtifactAccess fences writes against membership, Project and sharing
// changes, in that order. Every grant mutation holds the same Artifact lock.
func LockArtifactAccess(
	ctx context.Context,
	q *generated.Queries,
	p UserPrincipal,
	id uuid.UUID,
) (ArtifactAccess, error) {
	if err := LockArtifactLifecycle(ctx, q, p, id); err != nil {
		return ArtifactAccess{}, err
	}
	if _, err := q.LockArtifactIdentity(
		ctx,
		generated.LockArtifactIdentityParams{CompanyID: p.CompanyID, ID: id},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ArtifactAccess{}, nil
		}
		return ArtifactAccess{}, err
	}
	return ResolveArtifactAccess(ctx, q, p, id)
}
