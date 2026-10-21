package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Wiring lives here because Task already depends on Conversation. Huddle owns
// session authority; the domain readers own their shared-data projections.
func readHuddleContext(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	in voice.ContextRequest,
) (voice.ContextResult, error) {
	switch in.Tool {
	case "get_company_context", "list_projects":
		return company.ReadVoiceContext(ctx, q, companyID, in)
	case "search_tasks", "get_task":
		return task.ReadVoiceContext(ctx, q, companyID, in)
	default:
		return voice.ContextResult{}, conversation.ErrHuddleInvalid
	}
}
