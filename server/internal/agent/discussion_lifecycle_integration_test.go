//go:build integration

package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestArchiveAgentRevokesHistoryAndRestoreDoesNotRegrant(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := context.Background()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, _ := mustAgentService(t, store)
	id := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Discussion Agent")
	channelID := dbtest.SeedDiscussionChannel(t, ctx, pool, fixture.companyID, fixture.userID, []uuid.UUID{id})
	archived, err := service.Archive(ctx, fixture.principal, id, RevisionInput{ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Restore(
		ctx,
		fixture.principal,
		id,
		RevisionInput{ExpectedRevision: archived.Revision},
	); err != nil {
		t.Fatal(err)
	}
	var status string
	var generation, revision int64
	if err = pool.QueryRow(ctx, `SELECT status,generation,version FROM conversation_agents WHERE conversation_id=$1 AND agent_id=$2`, channelID, id).
		Scan(&status, &generation, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "revoked" || generation != 1 || revision != 2 {
		t.Fatalf("restored Agent grant=%s/%d/%d", status, generation, revision)
	}
}
