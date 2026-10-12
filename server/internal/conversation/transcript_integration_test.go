//go:build integration

package conversation

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"github.com/google/uuid"
)

func TestTranscriptReadsOneAuthorizedConversationAcrossRunsInSequenceOrder(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service := newTestConversationService(t, store, bus)

	firstRunID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "completed")
	secondRunID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "completed")
	dbtest.MustExec(
		t, ctx, pool, `
		INSERT INTO conversation_items (
			company_id, conversation_id, run_id, seq, type, author_kind, author_id,
			item_key, content, parts, content_sha256, created_at
		)
		VALUES
			($1, $2, $3, 1, 'user_message', 'user', $5,
			 NULL, 'start', NULL, NULL, '2026-08-26T00:00:00Z'),
			($1, $2, $3, 2, 'agent_text', 'agent', $6,
			 'first-1', 'first', '[{"type":"text","text":"first"}]',
			 decode(repeat('11', 32), 'hex'), '2026-08-26T00:00:03Z'),
			($1, $2, $4, 3, 'user_message', 'user', $5,
			 NULL, 'continue', NULL, NULL, '2026-08-26T00:00:04Z'),
			($1, $2, $4, 4, 'reasoning', 'agent', $6,
			 'second-1', 'second', '[{"type":"reasoning","text":"second"}]',
			 decode(repeat('22', 32), 'hex'), '2026-08-26T00:00:01Z'),
			($1, $2, $3, 5, 'tool_result', 'agent', $6,
			 'first-2', 'third',
			 '[{"type":"tool_result","callId":"call-1","output":"third","status":"completed"}]',
			 decode(repeat('33', 32), 'hex'), '2026-08-26T00:00:02Z')`,
		fixture.CompanyA,
		fixture.DirectConversationA,
		firstRunID,
		secondRunID,
		fixture.UserA,
		fixture.AgentA,
	)

	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	firstPage, err := service.Transcript(
		ctx,
		principal,
		fixture.DirectConversationA,
		TranscriptListOptions{AfterSeq: 0, Limit: 2},
	)
	if err != nil {
		t.Fatalf("Transcript(first page) error = %v", err)
	}
	if got, want := transcriptSeqs(firstPage.Items), []int64{2, 4}; !slices.Equal(got, want) {
		t.Fatalf("first page seqs = %v, want %v", got, want)
	}
	if got, want := transcriptRunIDs(firstPage.Items), []uuid.UUID{firstRunID, secondRunID}; !slices.Equal(got, want) {
		t.Fatalf("first page Run ids = %v, want %v", got, want)
	}
	if firstPage.ThroughSeq != 5 || !firstPage.HasMore {
		t.Fatalf("first page bounds = through %d, hasMore %t, want 5/true",
			firstPage.ThroughSeq, firstPage.HasMore)
	}

	secondPage, err := service.Transcript(
		ctx,
		principal,
		fixture.DirectConversationA,
		TranscriptListOptions{AfterSeq: 4, Limit: 2},
	)
	if err != nil {
		t.Fatalf("Transcript(second page) error = %v", err)
	}
	if got, want := transcriptSeqs(secondPage.Items), []int64{5}; !slices.Equal(got, want) {
		t.Fatalf("second page seqs = %v, want %v", got, want)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].RunID != firstRunID ||
		secondPage.ThroughSeq != 5 || secondPage.HasMore {
		t.Fatalf("second page = %+v", secondPage)
	}

	for name, invisiblePrincipal := range map[string]auth.UserPrincipal{
		"different direct-conversation creator": {
			UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
		},
		"different company": {
			UserID: fixture.UserB, CompanyID: fixture.CompanyB, Role: auth.CompanyRoleOwner,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Transcript(
				ctx,
				invisiblePrincipal,
				fixture.DirectConversationA,
				TranscriptListOptions{Limit: 2},
			)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("Transcript(invisible) error = %v, want ErrNotFound", err)
			}
		})
	}
}

func transcriptSeqs(items []TranscriptEntry) []int64 {
	result := make([]int64, len(items))
	for index, item := range items {
		result[index] = item.Seq
	}
	return result
}

func transcriptRunIDs(items []TranscriptEntry) []uuid.UUID {
	result := make([]uuid.UUID, len(items))
	for index, item := range items {
		result[index] = item.RunID
	}
	return result
}
