package execution

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// WaitForStudioAuth projects a live Studio command waiting for sign-in.
// The provider and CLI stay alive; authentication resumes that exact command.
func (s *DefaultRunService) WaitForStudioAuth(ctx context.Context, runID uuid.UUID, waiting bool) (*db.Run, error) {
	run, err := s.changeRun(ctx, runID, EventRunAuthWaitChanged, func(q *db.Queries) (db.Run, error) {
		return q.WaitForStudioAuth(ctx, db.WaitForStudioAuthParams{RunID: runID, Waiting: waiting})
	})
	if err != nil {
		return nil, fmt.Errorf("wait for Studio authentication: %w", err)
	}
	return &run, nil
}
