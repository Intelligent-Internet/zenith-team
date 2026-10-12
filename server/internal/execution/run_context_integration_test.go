//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// runContextGoldenPath is the document internal/daemon parses in
// TestLocalDirectoryForReadsTheServerDocument. Both sides read this one file
// because the structs that describe it are deliberately separate types.
const runContextGoldenPath = "testdata/run_context.golden.json"

// goldenResourceID is the id the golden names.
func goldenResourceID(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := id.Scan("0197f5b0-4000-7000-8000-000000000006"); err != nil {
		t.Fatalf("parse the golden resource id: %v", err)
	}
	return id
}

// goldenConversationID is the Conversation the golden names, so the
// snapshot's conversationId is stable across databases.
func goldenConversationID(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := id.Scan("0197f5b0-4000-7000-8000-000000000007"); err != nil {
		t.Fatalf("parse the golden conversation id: %v", err)
	}
	return id
}

// The run context is the only channel through which a company's local
// directory reaches a daemon. If it is empty the daemon prepares a fresh
// worktree, the agent edits the wrong files, and nothing errors — so these
// assert the document's shape rather than trusting it.
func TestBuildRunContextCarriesLocalDirectories(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE companies
		SET business_context = CASE id
			WHEN $1 THEN 'Company A context'
			WHEN $2 THEN 'Company B context'
		END
		WHERE id IN ($1, $2)`, fixture.CompanyA, fixture.CompanyB)
	// An empty direct Conversation with the id the golden names: its snapshot
	// is the explicit empty history every fresh chat starts from. The request
	// key keeps it clear of the one keyless direct chat the fixture already
	// holds for this member and Agent.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title, request_key)
		VALUES ($1, $2, 'direct', $3, $4, 'Golden', $1)`,
		goldenConversationID(t), fixture.CompanyA, fixture.AgentA, fixture.UserA)
	trigger := func(companyID, agentID, conversationID uuid.UUID, callerContext []byte, prompt string) RunTrigger {
		// Each fixture company's owner registered its runtime, so the owner
		// is the authorizer the consent gate admits.
		authorizer := fixture.UserA
		if companyID == fixture.CompanyB {
			authorizer = fixture.UserB
		}
		return RunTrigger{
			CompanyID: companyID, AgentID: agentID, ConversationID: conversationID,
			Context: callerContext, TriggerPrompt: prompt, Source: RunSourceMessage,
			AuthorizedByUserID: authorizer,
		}
	}
	// The enqueue kernel resolves the runtime from the authorizer's binding
	// before it builds the context; here the fixture binding is spelled out.
	build := func(in RunTrigger) ([]byte, error) {
		runtimeID := fixture.RuntimeA
		if in.CompanyID == fixture.CompanyB {
			runtimeID = fixture.RuntimeB
		}
		return buildRunContext(ctx, queries, in, runtimeID)
	}

	t.Run("a company with no resources still freezes its business and agent context", func(t *testing.T) {
		document, err := build(trigger(fixture.CompanyA, fixture.AgentA, goldenConversationID(t), nil, ""))
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatalf("decode run context %s: %v", document, err)
		}
		if _, ok := decoded["resources"]; ok {
			t.Fatalf("context %s carries resources, want none for a company with no resources", document)
		}
		if _, ok := decoded["agent"]; !ok {
			t.Fatalf(
				"context %s carries no agent brief; the daemon would execute with provider defaults",
				document,
			)
		}
		var company struct {
			Name            string `json:"name"`
			BusinessContext string `json:"businessContext"`
		}
		if err := json.Unmarshal(decoded["company"], &company); err != nil {
			t.Fatalf("decode Company context from %s: %v", document, err)
		}
		if company.Name != "Schema A" || company.BusinessContext != "Company A context" {
			t.Fatalf("Company context = %+v, want Schema A business context", company)
		}
	})

	t.Run("a Board chat's tagged Runs know their Board", func(t *testing.T) {
		var board uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO boards (company_id, title, created_by) VALUES ($1, 'Roadmap', $2) RETURNING id`,
			fixture.CompanyA, fixture.UserA).
			Scan(&board); err != nil {
			t.Fatal(err)
		}
		agentChat := func(channel uuid.UUID) uuid.UUID {
			chat, err := queries.EnsureChannelAgentConversation(
				ctx,
				generated.EnsureChannelAgentConversationParams{
					ChannelID: channel, AgentID: fixture.AgentA, CompanyID: fixture.CompanyA,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			return chat.ID
		}
		profile := func(conversation uuid.UUID) runExecutionProfile {
			raw, err := runContextExecutionProfile(ctx, queries, RunTrigger{
				CompanyID: fixture.CompanyA, ConversationID: conversation, Source: RunSourceMessage,
			})
			if err != nil {
				t.Fatal(err)
			}
			var decoded runExecutionProfile
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			return decoded
		}
		onBoard := agentChat(dbtest.SeedBoardChannel(t, ctx, pool, fixture.CompanyA, board, fixture.UserA,
			[]uuid.UUID{fixture.AgentA}))
		if w := profile(onBoard).Whiteboard; w == nil || w.ID != board.String() || w.Title != "Roadmap" {
			t.Fatalf("a tag in the Board's chat works on %+v, want the Board", w)
		}
		elsewhere := agentChat(dbtest.SeedDiscussionChannel(t, ctx, pool, fixture.CompanyA, fixture.UserA,
			[]uuid.UUID{fixture.AgentA}))
		for name, chat := range map[string]uuid.UUID{"a channel's tag": elsewhere, "a direct chat": goldenConversationID(t)} {
			if w := profile(chat).Whiteboard; w != nil {
				t.Fatalf("%s works on Board %+v", name, w)
			}
		}
	})

	// The same id the golden document names, so the produced bytes can be
	// compared to it exactly.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_resources
			(id, company_id, resource_type, daemon_id, local_path, label, position, created_by_user_id)
		VALUES ($1, $2, 'local_directory', 'daemon-a', '/Users/dana/code/acme', 'Acme', 0, $3)`,
		goldenResourceID(t), fixture.CompanyA, fixture.UserA)

	t.Run("the produced document matches the golden the daemon parses", func(t *testing.T) {
		document, err := build(trigger(fixture.CompanyA, fixture.AgentA, goldenConversationID(t), nil, ""))
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}
		want, err := os.ReadFile(filepath.Clean(runContextGoldenPath))
		if err != nil {
			t.Fatalf("read the golden: %v", err)
		}
		if got, expected := strings.TrimSpace(
			string(document),
		), strings.TrimSpace(
			string(want),
		); got != expected {
			t.Fatalf(
				"run context drifted from the golden the daemon reads.\n got: %s\nwant: %s\n\n"+
					"internal/daemon parses this exact file; changing one side alone means the "+
					"daemon silently prepares a worktree and the agent edits the wrong files.",
				got, expected,
			)
		}
	})

	t.Run("an empty Conversation freezes an explicit empty snapshot", func(t *testing.T) {
		document, err := build(trigger(fixture.CompanyA, fixture.AgentA, goldenConversationID(t), nil, ""))
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}
		var decoded struct {
			Snapshot struct {
				SchemaVersion  int               `json:"schemaVersion"`
				Kind           string            `json:"kind"`
				ConversationID string            `json:"conversationId"`
				ThroughSeq     int64             `json:"throughSeq"`
				Truncated      bool              `json:"truncated"`
				Items          []json.RawMessage `json:"items"`
			} `json:"conversationSnapshot"`
			Trigger json.RawMessage `json:"trigger"`
		}
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatalf("decode run context %s: %v", document, err)
		}
		snapshot := decoded.Snapshot
		if snapshot.SchemaVersion != 1 || snapshot.Kind != "direct" ||
			snapshot.ConversationID != goldenConversationID(t).String() ||
			snapshot.ThroughSeq != 0 || snapshot.Truncated || snapshot.Items == nil || len(snapshot.Items) != 0 {
			t.Fatalf("empty Conversation snapshot = %+v, want schema 1, kind direct, watermark 0 and an explicit empty item list", snapshot)
		}
		if decoded.Trigger != nil {
			t.Fatalf("context carries trigger %s, want none without an acting member", decoded.Trigger)
		}
	})

	t.Run("a local directory reaches the daemon under the agreed keys", func(t *testing.T) {
		document, err := build(trigger(fixture.CompanyA, fixture.AgentA, goldenConversationID(t), nil, ""))
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}

		// Decoded through the shape the daemon actually reads, not through
		// this package's own struct — the two are separate types on purpose,
		// so only a round trip proves they still agree.
		var decoded struct {
			Resources []struct {
				ID            string `json:"id"`
				ResourceType  string `json:"resourceType"`
				LocalDaemonID string `json:"localDaemonId"`
				LocalPath     string `json:"localPath"`
				Label         string `json:"label"`
			} `json:"resources"`
		}
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatalf("decode run context %s: %v", document, err)
		}
		if len(decoded.Resources) != 1 {
			t.Fatalf("run context carries %d resources, want 1: %s", len(decoded.Resources), document)
		}
		got := decoded.Resources[0]
		if got.ResourceType != localDirectoryResourceType {
			t.Errorf("resourceType = %q, want %q", got.ResourceType, localDirectoryResourceType)
		}
		if got.LocalDaemonID != "daemon-a" {
			t.Errorf("localDaemonId = %q, want %q", got.LocalDaemonID, "daemon-a")
		}
		if got.LocalPath != "/Users/dana/code/acme" {
			t.Errorf("localPath = %q, want %q", got.LocalPath, "/Users/dana/code/acme")
		}
		if got.Label != "Acme" {
			t.Errorf("label = %q, want %q", got.Label, "Acme")
		}
	})

	t.Run("another company's directory never leaks in", func(t *testing.T) {
		document, err := build(trigger(fixture.CompanyB, fixture.AgentB, fixture.UnownedConversationB, nil, ""))
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatalf("decode run context %s: %v", document, err)
		}
		if _, ok := decoded["resources"]; ok {
			t.Fatalf("company B context = %s, want no resources", document)
		}
		var company struct {
			Name            string `json:"name"`
			BusinessContext string `json:"businessContext"`
		}
		if err := json.Unmarshal(decoded["company"], &company); err != nil {
			t.Fatalf("decode Company B context from %s: %v", document, err)
		}
		if company.Name != "Schema B" || company.BusinessContext != "Company B context" {
			t.Fatalf("Company B context = %+v, want only Company B data", company)
		}
	})

	t.Run("a caller document keeps its own keys", func(t *testing.T) {
		document, err := build(trigger(
			fixture.CompanyA, fixture.AgentA, goldenConversationID(t), []byte(`{"prompt":"do the thing"}`), "",
		))
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}
		var decoded struct {
			Prompt    string            `json:"prompt"`
			Agent     json.RawMessage   `json:"agent"`
			Resources []json.RawMessage `json:"resources"`
		}
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatalf("decode merged context %s: %v", document, err)
		}
		if decoded.Prompt != "do the thing" {
			t.Errorf("prompt = %q, want it preserved", decoded.Prompt)
		}
		if len(decoded.Resources) != 1 {
			t.Errorf("merged context carries %d resources, want 1", len(decoded.Resources))
		}
		if len(decoded.Agent) == 0 {
			t.Error("merged context carries no agent brief")
		}
	})

	t.Run("a Schedule profile never receives Company resources", func(t *testing.T) {
		input := trigger(fixture.CompanyA, fixture.AgentA, goldenConversationID(t), nil, "summarize the reviewed sources")
		input.Source = RunSourceSchedule
		document, err := build(input)
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}
		var decoded struct {
			Resources []json.RawMessage `json:"resources"`
			Company   json.RawMessage   `json:"company"`
			Snapshot  json.RawMessage   `json:"conversationSnapshot"`
			Trigger   json.RawMessage   `json:"trigger"`
			Profile   struct {
				Source RunEnqueueSource `json:"source"`
			} `json:"executionProfile"`
		}
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatalf("decode Schedule context %s: %v", document, err)
		}
		if decoded.Resources != nil || decoded.Company != nil ||
			decoded.Snapshot != nil || decoded.Trigger != nil ||
			decoded.Profile.Source != RunSourceSchedule {
			t.Fatalf("Schedule context = %s, want source without Company context, resources or chat history", document)
		}
	})

	prerequisiteTaskID := dbtest.NewUUID(t)
	prerequisiteConversationID := dbtest.NewUUID(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin prerequisite fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
		VALUES ($1, $2, 'task', $3, 'Completed prerequisite', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
		prerequisiteConversationID, fixture.CompanyA, prerequisiteTaskID,
	); err != nil {
		t.Fatalf("insert prerequisite conversation: %v", err)
	}
	if _, err := tx.Exec(ctx, `
				INSERT INTO tasks (project_id,
			id, company_id, conversation_id,
			milestone_type, number, key, title, description,
			status, completed_at, executed_by_agent_id, assignee_user_id, creator_user_id
		) VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
			$1, $2, $3,
			'product', 2, 'prerequisite_a', 'Completed prerequisite',
			'Frozen dependency result', 'done', '2026-08-25T01:02:03Z', $4, $5, $5
		)`,
		prerequisiteTaskID, fixture.CompanyA, prerequisiteConversationID,
		fixture.AgentA, fixture.UserA,
	); err != nil {
		t.Fatalf("insert prerequisite Task: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_dependencies (project_id,
			company_id, dependent_task_id, prerequisite_task_id, created_by_user_id
		) VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`,
		fixture.CompanyA, fixture.TaskA, prerequisiteTaskID, fixture.UserA,
	); err != nil {
		t.Fatalf("insert prerequisite edge: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit prerequisite fixture: %v", err)
	}

	t.Run("a Task profile freezes the execution contract", func(t *testing.T) {
		input := trigger(fixture.CompanyA, fixture.AgentA, goldenConversationID(t), nil, "")
		input.Source = RunSourceAssign
		input.TaskID = uuid.NullUUID{UUID: fixture.TaskA, Valid: true}
		input.ConversationID = fixture.ConversationA
		document, err := build(input)
		if err != nil {
			t.Fatalf("buildRunContext() error = %v", err)
		}
		var decoded struct {
			Profile struct {
				Project runProjectBrief  `json:"project"`
				Source  RunEnqueueSource `json:"source"`
				Task    struct {
					Key            string `json:"key"`
					Title          string `json:"title"`
					ConversationID string `json:"conversationId"`
					Dependencies   []struct {
						ID             string     `json:"id"`
						ConversationID string     `json:"conversationId"`
						Key            string     `json:"key"`
						Title          string     `json:"title"`
						Description    string     `json:"description"`
						Status         string     `json:"status"`
						CompletedAt    *time.Time `json:"completedAt"`
					} `json:"dependencies"`
				} `json:"task"`
			} `json:"executionProfile"`
		}
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatalf("decode Task context %s: %v", document, err)
		}
		if decoded.Profile.Project.ID != fixture.ProjectA.String() || decoded.Profile.Project.Key != "work" || decoded.Profile.Project.Revision != 1 {
			t.Fatalf("frozen Project = %+v", decoded.Profile.Project)
		}
		if decoded.Profile.Source != RunSourceAssign || decoded.Profile.Task.Key != "task_a" ||
			decoded.Profile.Task.Title != "Task A" ||
			decoded.Profile.Task.ConversationID != fixture.ConversationA.String() ||
			len(decoded.Profile.Task.Dependencies) != 1 {
			t.Fatalf("Task execution profile = %+v", decoded.Profile)
		}
		dependency := decoded.Profile.Task.Dependencies[0]
		if dependency.ID != prerequisiteTaskID.String() ||
			dependency.ConversationID != prerequisiteConversationID.String() ||
			dependency.Key != "prerequisite_a" || dependency.Title != "Completed prerequisite" ||
			dependency.Description != "Frozen dependency result" || dependency.Status != "done" ||
			dependency.CompletedAt == nil {
			t.Fatalf("frozen Task prerequisite = %+v", dependency)
		}
	})
}

func TestBuildRunContextFreezesOnlyTaskProjectDocuments(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	q := generated.New(pool)
	var file uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status,version) VALUES ($1,$2,gen_random_uuid(),'requirements.md','text/markdown',12,'ready',2) RETURNING id`, fixture.CompanyA, fixture.UserA).Scan(&file); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO project_files(company_id,project_id,attachment_id,file_type) VALUES ($1,$2,$3,'document')`, fixture.CompanyA, fixture.ProjectA, file)
	input := RunTrigger{CompanyID: fixture.CompanyA, AgentID: fixture.AgentA, ConversationID: fixture.ConversationA, TaskID: uuid.NullUUID{UUID: fixture.TaskA, Valid: true}, Source: RunSourceAssign, AuthorizedByUserID: fixture.UserA}
	frozen, err := buildRunContext(ctx, q, input, fixture.RuntimeA)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Files []runAttachedFileBrief `json:"projectDocuments"`
	}
	if err := json.Unmarshal(frozen, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Files) != 1 || doc.Files[0].ID != file.String() {
		t.Fatalf("frozen documents = %+v", doc.Files)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE project_files SET removed_at=clock_timestamp() WHERE attachment_id=$1`, file)
	next, err := buildRunContext(ctx, q, input, fixture.RuntimeA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(next), "projectDocuments") {
		t.Fatal("removed document leaked into new Run")
	}
	if !strings.Contains(string(frozen), file.String()) {
		t.Fatal("original snapshot changed")
	}
	input.TaskID = uuid.NullUUID{}
	input.Source = RunSourceMessage
	input.Context = frozen
	direct, err := buildRunContext(ctx, q, input, fixture.RuntimeA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(direct), "projectDocuments") {
		t.Fatal("caller injected Project documents into a non-Task Run")
	}
}
