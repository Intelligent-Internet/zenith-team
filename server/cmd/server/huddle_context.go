package main

import (
	"context"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/task"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

// Wiring lives here because Task already depends on Conversation. Huddle owns
// session authority; the domain readers own their shared-data projections.
func readHuddleContext(ctx context.Context, q *generated.Queries, companyID uuid.UUID, in voice.ContextRequest) (voice.ContextResult, error) {
	switch in.Tool {
	case "get_company_context", "list_projects":
		return company.ReadVoiceContext(ctx, q, companyID, in)
	case "search_tasks", "get_task":
		return task.ReadVoiceContext(ctx, q, companyID, in)
	default:
		return voice.ContextResult{}, conversation.ErrHuddleInvalid
	}
}
