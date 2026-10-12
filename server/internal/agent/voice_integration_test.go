//go:build integration

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/agent/voice"
)

func TestAgentVoiceConfiguration(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	foreign := seedAgentCompany(t, ctx, pool, "owner")
	service, _ := mustAgentService(t, store)
	before, err := service.Get(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil {
		t.Fatal(err)
	}
	settings := voice.Settings{Enabled: true, Voice: "stone", Greet: false, Participation: "natural"}
	input := UpdateInput{ExpectedRevision: before.Revision, Voice: &settings}
	updated, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, input)
	if err != nil || updated.Voice != settings || updated.Revision != before.Revision+1 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if !sameCapabilitySettings(before.Capabilities, updated.Capabilities) {
		t.Fatal("Voice changed runtime capabilities")
	}
	replayed, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, input)
	if err != nil || replayed.Revision != updated.Revision {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	if _, err := service.Update(ctx, foreign.principal, fixture.rootAgentID, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company update = %v", err)
	}
	settings.Greet = true
	if _, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, input); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("changed replay = %v", err)
	}
	title := "Meeting secretary"
	retained, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, UpdateInput{ExpectedRevision: updated.Revision, Title: &title})
	if err != nil || retained.Voice != updated.Voice {
		t.Fatalf("unrelated update lost Voice = %+v, %v", retained, err)
	}
}
