package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// companyViewRow reads the member-scoped Company view. The error is returned
// raw: the onboarding callers wrap it with their own operation and treat
// absence in-tx as an invariant violation, while Get maps it to ErrNotFound.
func companyViewRow(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
) (generated.GetCompanyForMemberRow, error) {
	return q.GetCompanyForMember(ctx, generated.GetCompanyForMemberParams{
		CompanyID: companyID,
		UserID:    userID,
	})
}

func memberResponse(row generated.CompanyMember) MemberResponse {
	return MemberResponse{
		CompanyID:           row.CompanyID,
		UserID:              row.UserID,
		Role:                auth.CompanyRole(row.Role),
		Status:              MembershipStatus(row.Status),
		RevokedAt:           util.TimestampToTime(row.RevokedAt),
		RevokedByUserID:     optionalUUID(row.RevokedByUserID),
		ReactivatedAt:       util.TimestampToTime(row.ReactivatedAt),
		ReactivatedByUserID: optionalUUID(row.ReactivatedByUserID),
		CreatedAt:           row.CreatedAt.Time.UTC(),
		UpdatedAt:           row.UpdatedAt.Time.UTC(),
		Revision:            row.Version,
	}
}

func companyDTOFromGetRow(row generated.GetCompanyForMemberRow) Response {
	return Response{
		ID:                    row.ID,
		Name:                  row.Name,
		AvatarAttachmentID:    util.NullUUIDPtr(row.AvatarAttachmentID),
		Slug:                  row.Slug,
		TaskPrefix:            row.TaskPrefix,
		BusinessContext:       row.BusinessContext,
		InitializationPending: row.InitializationPending,
		WebsiteURL:            util.TextToPtr(row.WebsiteUrl),
		ContactEmail:          util.TextToPtr(row.ContactEmail),
		PhoneCountryISO2:      util.TextToPtr(row.PhoneCountryIso2),
		PhoneNumber:           util.TextToPtr(row.PhoneNumber),
		Role:                  auth.CompanyRole(row.MemberRole),
		Permissions:           auth.PermissionsOf(auth.CompanyRole(row.MemberRole)),
		Revision:              row.Version,
		CreatedAt:             row.CreatedAt.Time.UTC(),
		UpdatedAt:             row.UpdatedAt.Time.UTC(),
		AgentSetupCompletedAt: util.TimestampToTime(row.AgentSetupCompletedAt),
		FirstPlanAppliedAt:    util.TimestampToTime(row.FirstPlanAppliedAt),
	}
}

func companyDTOFromListRow(row generated.ListCompaniesForUserRow) Response {
	return Response{
		ID:                    row.ID,
		Name:                  row.Name,
		AvatarAttachmentID:    util.NullUUIDPtr(row.AvatarAttachmentID),
		Slug:                  row.Slug,
		TaskPrefix:            row.TaskPrefix,
		BusinessContext:       row.BusinessContext,
		InitializationPending: row.InitializationPending,
		WebsiteURL:            util.TextToPtr(row.WebsiteUrl),
		ContactEmail:          util.TextToPtr(row.ContactEmail),
		PhoneCountryISO2:      util.TextToPtr(row.PhoneCountryIso2),
		PhoneNumber:           util.TextToPtr(row.PhoneNumber),
		Role:                  auth.CompanyRole(row.MemberRole),
		Permissions:           auth.PermissionsOf(auth.CompanyRole(row.MemberRole)),
		Revision:              row.Version,
		CreatedAt:             row.CreatedAt.Time.UTC(),
		UpdatedAt:             row.UpdatedAt.Time.UTC(),
		AgentSetupCompletedAt: util.TimestampToTime(row.AgentSetupCompletedAt),
		FirstPlanAppliedAt:    util.TimestampToTime(row.FirstPlanAppliedAt),
	}
}

func mapCreateError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func optionalUUID(value uuid.NullUUID) *uuid.UUID {
	return util.NullUUIDPtr(value)
}
