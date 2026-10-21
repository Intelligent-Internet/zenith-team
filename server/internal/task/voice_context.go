package task

import (
	"context"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ReadVoiceContext participates in the Huddle's authorized transaction. Only
// Task conversations are shared; creator-private conversations are excluded even
// when the invitation's granting member could personally read them.
func ReadVoiceContext(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	in voice.ContextRequest,
) (voice.ContextResult, error) {
	result := voice.ContextResult{Items: []voice.ContextItem{}}
	rows, err := q.ReadVoiceTasks(ctx, generated.ReadVoiceTasksParams{
		CompanyID:                 companyID,
		TaskID:                    in.TaskID,
		ProjectID:                 in.ProjectID,
		Status:                    in.Status,
		Query:                     in.Query,
		BeforeNumber:              in.BeforeNumber,
		ContextGrantedByUserID:    in.AuthorizerUserID,
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	})
	if err != nil {
		return result, err
	}
	result.HasMore = len(rows) > 20
	for _, row := range rows[:min(20, len(rows))] {
		item := voice.ContextItem{
			ID:             row.ID,
			Type:           "task",
			Key:            row.Key,
			Title:          row.Title,
			Description:    row.Description,
			Status:         row.Status,
			ProjectID:      &row.ProjectID,
			Assignee:       row.Assignee,
			BlockedByCount: row.BlockedByCount,
			UpdatedAt:      row.UpdatedAt.Time,
			Truncated:      row.Truncated.Bool,
		}
		if row.DueDate.Valid {
			item.DueDate = row.DueDate.Time.Format("2006-01-02")
		}
		result.Items = append(result.Items, item)
	}
	if result.HasMore {
		result.NextBeforeNumber = rows[19].Number
	}
	return result, nil
}
