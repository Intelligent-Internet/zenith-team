package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A read avoids taking the Agent graph lock after the Huddle lock. Claim and
// every lease renewal independently fence archive and Voice revocation.
func huddleVoiceEligible(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	id uuid.NullUUID,
) (bool, error) {
	if !id.Valid {
		return false, nil
	}
	enabled, err := q.HuddleVoiceAgentEnabled(ctx, generated.HuddleVoiceAgentEnabledParams{CompanyID: companyID, ID: id.UUID})
	if err != nil {
		return false, fmt.Errorf("read Huddle voice Agent: %w", err)
	}
	return enabled, nil
}

func huddleVoiceSnapshot(ctx context.Context, q *generated.Queries, companyID, agentID uuid.UUID) ([]byte, error) {
	row, err := q.GetHuddleVoiceAgent(ctx, generated.GetHuddleVoiceAgentParams{CompanyID: companyID, ID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHuddleUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("read invited Agent profile: %w", err)
	}
	profile, err := json.Marshal(voice.Profile{
		Identity: voice.Identity{
			ID: row.ID, Name: row.Name, Title: row.Title,
			AvatarKey: util.TextToPtr(row.AvatarKey), AccentColor: util.TextToPtr(row.AccentColor),
		},
		Revision: row.Version, Instructions: row.Instructions, Settings: voice.FromSettings(row.Settings),
	})
	if err != nil {
		return nil, fmt.Errorf("encode invited Agent profile: %w", err)
	}
	return profile, nil
}
