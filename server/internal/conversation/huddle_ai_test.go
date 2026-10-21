package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

func TestHuddleAIRefusesInvitationsWhileVoiceAgentsAreOff(t *testing.T) {
	// The flag check comes before any database work, so no store is needed.
	service := &DefaultHuddleAIService{options: HuddleAIServiceOptions{Enabled: false}}
	_, err := service.SetEnabled(context.Background(), auth.UserPrincipal{}, uuid.New(), SetHuddleAIInput{
		Enabled: true, AgentID: uuid.New(), ExpectedRevision: 1, RequestKey: uuid.New(),
	})
	if !errors.Is(err, ErrHuddleAIDisabled) {
		t.Fatalf("SetEnabled() error = %v, want ErrHuddleAIDisabled", err)
	}
}
