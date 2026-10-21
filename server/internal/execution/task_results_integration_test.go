//go:build integration

package execution

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestTaskResultsPreserveOutputVersionsAndSourceScope(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	var newestRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `INSERT INTO runs
		(company_id, agent_id, task_id, conversation_id, runtime_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 )) RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA, fixture.ConversationA, fixture.RuntimeA,
	).Scan(&newestRunID); err != nil {
		t.Fatal(err)
	}
	outputIDs := make(map[uuid.UUID]uuid.UUID)
	for position, runID := range []uuid.UUID{fixture.RunA, newestRunID} {
		attachmentID := insertReadyDeliverableAttachment(
			t,
			ctx,
			pool,
			fixture.CompanyA,
			fixture.UserA,
			"result.pdf",
		)
		dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'running' WHERE id = $1`, runID)
		presented, err := service.Present(ctx, principal, RunOrigin{
			RunID: runID, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
			TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA,
		}, DeliverablePresentRequest{
			Title:   "Result",
			Summary: "A preserved version.",
			Items: []DeliverablePresentItemRequest{
				{Role: DeliverableItemRolePrimary, Label: "Report", AttachmentID: &attachmentID},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		outputIDs[runID] = presented.OutputID
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE outputs SET created_at = '2026-09-01'::timestamptz + $2 * interval '1 second' WHERE id = $1`,
			presented.OutputID,
			position,
		)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE runs SET status = 'completed', created_at = '2026-09-01'::timestamptz + $2 * interval '1 second' WHERE id = $1`,
			runID,
			position,
		)
	}
	for position := range 2 {
		attachmentID := insertReadyDeliverableAttachment(
			t,
			ctx,
			pool,
			fixture.CompanyA,
			fixture.UserA,
			"source.pdf",
		)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE attachments SET created_at = '2026-09-01' WHERE id = $1`,
			attachmentID,
		)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
			VALUES ($1, $2, $3, $4)`,
			fixture.CompanyA,
			fixture.MessageA,
			attachmentID,
			position,
		)
	}
	// An agent transcript attachment is not a user-provided source.
	agentMessageID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO conversation_items
		(id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
		VALUES ($1, $2, $3, $4, 2, 'agent_text', 'agent', $5, 'Generated output')`,
		agentMessageID, fixture.CompanyA, fixture.ConversationA, newestRunID, fixture.AgentA)
	agentAttachmentID := insertReadyDeliverableAttachment(
		t,
		ctx,
		pool,
		fixture.CompanyA,
		fixture.UserA,
		"not-source.pdf",
	)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`, fixture.CompanyA, agentMessageID, agentAttachmentID)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO deliverables (company_id, output_id, status, submitted_at)
		VALUES ($1, $2, 'approved', now())`, fixture.CompanyA, outputIDs[newestRunID])
	dbtest.MustExec(t, ctx, pool, `INSERT INTO output_publications
		(company_id, output_id, output_item_id, requested_by_user_id, status, service_name,
		 provider_type, provider_binding_id, public_url, published_at)
		SELECT $1, output_id, id, $3, 'published', 'result-app', 'cloud_run', 'test-provider',
		 'https://result.example.test', now() FROM output_items WHERE output_id = $2`,
		fixture.CompanyA, outputIDs[newestRunID], fixture.UserA)

	page, err := service.ListTaskResults(ctx, principal, fixture.TaskA, TaskResultListOptions{Limit: 1})
	if err != nil || len(page.Items) != 1 || len(page.Sources) != 1 || page.Next == nil || page.NextSource == nil {
		t.Fatalf("ListTaskResults() = %+v, %v", page, err)
	}
	result := page.Items[0]
	if (result.RunID == nil || *result.RunID != newestRunID) || result.Output.ID != outputIDs[newestRunID] ||
		len(result.Output.Items) != 1 {
		t.Fatalf("newest result = %+v", result)
	}
	if result.Publication == nil || result.Publication.URL == nil ||
		*result.Publication.URL != "https://result.example.test" || result.Publication.OutputID != result.Output.ID ||
		result.Deliverable == nil || result.Deliverable.Status != DeliverableStatusApproved {
		t.Fatalf("canonical review and publication = %+v", result)
	}
	item := result.Output.Items[0]
	if item.ID == uuid.Nil || item.AssetVersionID == uuid.Nil || item.ContentType != "application/pdf" ||
		item.SizeBytes != 4 {
		t.Fatalf("output file metadata = %+v", item)
	}
	if page.Sources[0].MessageID != fixture.MessageA {
		t.Fatalf("source message = %s, want %s", page.Sources[0].MessageID, fixture.MessageA)
	}
	older, err := service.ListTaskResults(ctx, principal, fixture.TaskA, TaskResultListOptions{
		Limit: 1, Before: page.Next, SourceBefore: page.NextSource,
	})
	if err != nil || len(older.Items) != 1 || len(older.Sources) != 1 {
		t.Fatalf("older results = %+v, %v", older, err)
	}
	if older.Items[0].RunID == nil || *older.Items[0].RunID != fixture.RunA || older.Next != nil ||
		older.NextSource != nil || older.Sources[0].Attachment.ID == page.Sources[0].Attachment.ID {
		t.Fatalf("older result provenance and pagination = %+v", older)
	}
	if older.Items[0].Output.Items[0].AssetVersionID == item.AssetVersionID {
		t.Fatal("different Run snapshots were collapsed by filename")
	}
	artifacts, err := service.ListConversationArtifacts(
		ctx,
		principal,
		fixture.ConversationA,
		ArtifactListOptions{Limit: 1},
	)
	if err != nil || len(artifacts.Items) != 1 || artifacts.Next == nil ||
		artifacts.Items[0].Artifact.ID != outputIDs[newestRunID] {
		t.Fatalf("ListConversationArtifacts() = %+v, %v", artifacts, err)
	}
	olderArtifacts, err := service.ListConversationArtifacts(
		ctx,
		principal,
		fixture.ConversationA,
		ArtifactListOptions{Limit: 1, Before: artifacts.Next},
	)
	if err != nil || len(olderArtifacts.Items) != 1 || olderArtifacts.Next != nil ||
		(olderArtifacts.Items[0].RunID == nil || *olderArtifacts.Items[0].RunID != fixture.RunA) {
		t.Fatalf("older artifacts = %+v, %v", olderArtifacts, err)
	}
	for _, runID := range []uuid.UUID{fixture.RunA, newestRunID, uuid.New()} {
		filtered, err := service.ListConversationArtifacts(ctx, principal, fixture.ConversationA,
			ArtifactListOptions{Limit: 1, RunID: runID})
		if err != nil || filtered.Next != nil {
			t.Fatalf("filtered artifacts = %+v, %v", filtered, err)
		}
		wantID, exists := outputIDs[runID]
		if exists && (len(filtered.Items) != 1 || filtered.Items[0].Artifact.ID != wantID) {
			t.Fatalf("filtered artifacts = %+v, want output %s", filtered, wantID)
		}
		if !exists && len(filtered.Items) != 0 {
			t.Fatalf("unknown Run returned artifacts: %+v", filtered)
		}
	}

	sourcesOnly, err := service.ListTaskResults(ctx, principal, fixture.TaskA, TaskResultListOptions{
		Limit: 1, SkipResults: true, SourceBefore: page.NextSource,
	})
	if err != nil || len(sourcesOnly.Items) != 0 || sourcesOnly.Items == nil || len(sourcesOnly.Sources) != 1 {
		t.Fatalf("sources-only continuation = %+v, %v", sourcesOnly, err)
	}
	if _, err := service.ListTaskResults(
		ctx,
		principal,
		fixture.TaskB,
		TaskResultListOptions{Limit: 1},
	); !errors.Is(
		err,
		ErrDeliverableNotFound,
	) {
		t.Fatalf("other company task error = %v, want not found", err)
	}
	if _, err := service.ListConversationArtifacts(
		ctx,
		principal,
		fixture.ConversationB,
		ArtifactListOptions{Limit: 1, RunID: fixture.RunA},
	); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("other company Conversation error = %v, want not found", err)
	}
	// A Run's own pagination includes every output and never pulls in another Run.
	attachmentID := insertReadyDeliverableAttachment(t, ctx, pool, fixture.CompanyA, fixture.UserA, "extra.pdf")
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'running' WHERE id = $1`, fixture.RunA)
	extra, err := service.Present(ctx, principal, RunOrigin{
		RunID: fixture.RunA, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA,
	}, DeliverablePresentRequest{Title: "Additional output", Summary: "Additional report", Items: []DeliverablePresentItemRequest{
		{Role: DeliverableItemRolePrimary, Label: "Additional report", AttachmentID: &attachmentID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, fixture.RunA)
	first, err := service.ListConversationArtifacts(
		ctx,
		principal,
		fixture.ConversationA,
		ArtifactListOptions{Limit: 1, RunID: fixture.RunA},
	)
	if err != nil || len(first.Items) != 1 || first.Items[0].Artifact.ID != extra.OutputID || first.Next == nil {
		t.Fatalf("first filtered page = %+v, %v", first, err)
	}
	second, err := service.ListConversationArtifacts(
		ctx,
		principal,
		fixture.ConversationA,
		ArtifactListOptions{Limit: 1, RunID: fixture.RunA, Before: first.Next},
	)
	if err != nil || len(second.Items) != 1 || second.Items[0].Artifact.ID != outputIDs[fixture.RunA] ||
		second.Next != nil {
		t.Fatalf("second filtered page = %+v, %v", second, err)
	}

	// The machine a result ran on is named only to its registrant (ADR 0014).
	// UserA registered it and reads its id; on a row whose owner is someone
	// else, UserA reads none on the same results.
	runtimeIDs := func() []*uuid.UUID {
		t.Helper()
		results, err := service.ListTaskResults(ctx, principal, fixture.TaskA, TaskResultListOptions{Limit: 1})
		if err != nil || len(results.Items) != 1 {
			t.Fatalf("ListTaskResults() = %+v, %v", results, err)
		}
		artifacts, err := service.ListConversationArtifacts(
			ctx,
			principal,
			fixture.ConversationA,
			ArtifactListOptions{Limit: 1},
		)
		if err != nil || len(artifacts.Items) != 1 {
			t.Fatalf("ListConversationArtifacts() = %+v, %v", artifacts, err)
		}
		run, err := service.FetchRunResult(ctx, principal, newestRunID)
		if err != nil {
			t.Fatalf("FetchRunResult() error = %v", err)
		}
		return []*uuid.UUID{results.Items[0].RuntimeID, artifacts.Items[0].RuntimeID, run.RuntimeID}
	}
	for _, got := range runtimeIDs() {
		if got == nil || *got != fixture.RuntimeA {
			t.Fatalf("registrant's result runtimeId = %v, want %s", got, fixture.RuntimeA)
		}
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
		fixture.RuntimeA,
		fixture.UserB,
	)
	for _, got := range runtimeIDs() {
		if got != nil {
			t.Fatalf("result names another member's runtime %s", *got)
		}
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status = 'revoked' WHERE company_id = $1 AND user_id = $2`,
		fixture.CompanyA,
		fixture.UserA,
	)
	if _, err := service.ListTaskResults(
		ctx,
		principal,
		fixture.TaskA,
		TaskResultListOptions{Limit: 1},
	); !errors.Is(
		err,
		ErrDeliverableNotFound,
	) {
		t.Fatalf("revoked member error = %v, want not found", err)
	}
	if _, err := service.ListConversationArtifacts(
		ctx,
		principal,
		fixture.ConversationA,
		ArtifactListOptions{Limit: 1, RunID: fixture.RunA},
	); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("revoked member Artifact error = %v, want not found", err)
	}
}
