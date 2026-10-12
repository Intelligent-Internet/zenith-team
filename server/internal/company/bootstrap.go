package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Bootstrap is the owner-scoped opening snapshot. Every nested value is the
// canonical DTO served by the domain that owns it. The run counters use the
// one counting vocabulary: queued counts queued+deferred, active counts
// dispatched+waiting_local_directory+running — nothing else is served.
type Bootstrap struct {
	Company           Response        `json:"company"`
	InboxCounts       inbox.Counts    `json:"inboxCounts"`
	CEOAgent          *agent.Response `json:"ceoAgent"`
	CEOConversationID *uuid.UUID      `json:"ceoConversationId"`
	ActiveTaskCount   int64           `json:"activeTaskCount"`
	QueuedRunCount    int64           `json:"queuedRunCount"`
	ActiveRunCount    int64           `json:"activeRunCount"`
}

// Bootstrap reads the whole opening snapshot from one repeatable-read,
// read-only transaction. The owner middleware is the authorization fence;
// the service composes trusted owning-domain projections without reaching
// through their pool-owning services.
func (s *DefaultCompanyService) Bootstrap(
	ctx context.Context,
	principal auth.UserPrincipal,
) (Bootstrap, error) {
	var result Bootstrap
	err := s.store.WithinTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, func(q *generated.Queries) error {
		companyRow, err := companyViewRow(ctx, q, principal.CompanyID, principal.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read Bootstrap Company: %w", err)
		}
		organization, err := agent.ReadRosterProjectionInTx(ctx, q, principal)
		if err != nil {
			return fmt.Errorf("read Bootstrap Agent projection: %w", err)
		}
		counts, err := inbox.ReadCountsInTx(ctx, q, principal.CompanyID, principal.UserID)
		if err != nil {
			return err
		}
		aggregateCounts, err := q.GetCompanyBootstrapCounts(ctx, principal.CompanyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read Bootstrap counters: %w", err)
		}

		result = Bootstrap{
			Company:         companyDTOFromGetRow(companyRow),
			InboxCounts:     counts,
			ActiveTaskCount: aggregateCounts.ActiveTaskCount,
			QueuedRunCount:  aggregateCounts.QueuedRunCount,
			ActiveRunCount:  aggregateCounts.ActiveRunCount,
		}
		result.CEOAgent = rootAgent(organization.Agents)
		if result.CEOAgent != nil {
			conversationRow, err := q.GetDirectConversationByAgent(
				ctx,
				generated.GetDirectConversationByAgentParams{
					ActorUserID: principal.UserID,
					CompanyID:   principal.CompanyID,
					AgentID:     result.CEOAgent.ID,
				},
			)
			if err == nil {
				conversationID := conversationRow.ID
				result.CEOConversationID = &conversationID
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("read Bootstrap CEO Conversation: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Bootstrap{}, err
	}
	return result, nil
}

func rootAgent(agents []agent.Response) *agent.Response {
	for index := range agents {
		if agents[index].ReportsTo == nil && agents[index].ArchivedAt == nil {
			root := agents[index]
			return &root
		}
	}
	return nil
}
