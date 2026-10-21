package company

import (
	"context"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ReadVoiceContext participates in the Huddle's authorized transaction. It
// exposes only shared profile/project fields, never contacts or private resources.
func ReadVoiceContext(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	in voice.ContextRequest,
) (voice.ContextResult, error) {
	result := voice.ContextResult{Items: []voice.ContextItem{}}
	if in.Tool == "get_company_context" {
		row, err := q.GetVoiceCompanyContext(ctx, companyID)
		if err != nil {
			return result, err
		}
		result.Items = append(
			result.Items,
			voice.ContextItem{
				ID:          row.ID,
				Type:        "company",
				Title:       row.Name,
				Description: row.Description,
				Truncated:   row.Truncated.Bool,
				UpdatedAt:   row.UpdatedAt.Time,
			},
		)
		return result, nil
	}
	rows, err := q.ListVoiceProjects(ctx, generated.ListVoiceProjectsParams{
		CompanyID: companyID, AfterID: in.AfterID,
		ContextGrantedByUserID:    in.AuthorizerUserID,
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	})
	if err != nil {
		return result, err
	}
	result.HasMore = len(rows) > 20
	for _, row := range rows[:min(20, len(rows))] {
		result.Items = append(
			result.Items,
			voice.ContextItem{
				ID:          row.ID,
				Type:        "project",
				Key:         row.Key,
				Title:       row.Title,
				Description: row.Description,
				Truncated:   row.Truncated.Bool,
				UpdatedAt:   row.UpdatedAt.Time,
			},
		)
	}
	if result.HasMore {
		result.NextAfterID = &result.Items[len(result.Items)-1].ID
	}
	return result, nil
}
