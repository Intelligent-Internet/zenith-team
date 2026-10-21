//go:build integration

package task

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// Files attached when a Task is created belong to the Task: they travel on a
// member message in its Conversation, so they list as the Task's sources and
// every later Run of the Task reads them, and the assignment Run that message
// belongs to freezes them before the message exists.
func TestCreateAttachesFilesToTheTaskAndItsAssignmentRun(t *testing.T) {
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
	dbtest.MustExec(t, ctx, pool,
		`UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	service, err := NewTaskService(store, newTestBus(t), nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	base := time.Date(2026, time.September, 25, 9, 0, 0, 0, time.UTC)
	seed := func(companyID, userID uuid.UUID, filename, status string, version int, offset time.Duration) uuid.UUID {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO attachments (
				id, company_id, uploaded_by_user_id, request_key, filename,
				content_type, size_bytes, status, version, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, 'application/pdf', 42, $6, $7, $8, $8)`,
			id, companyID, userID, dbtest.NewUUID(t), filename, status, version, base.Add(offset))
		return id
	}
	// A zero assignee is the caller (ADR 0014); executedBy nil means the
	// assignee does the work themself.
	create := func(
		key string, assignee ActorRef, executedBy *ActorRef, status Status, files ...uuid.UUID,
	) (Response, error) {
		result, err := service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA, Key: key, Title: "Review " + key,
			Status: status, MilestoneType: MilestoneTypeProduct,
			Assignee: assignee, ExecutedBy: executedBy, AttachmentIDs: files,
		})
		return result.Task, err
	}
	agent := &ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	self := ActorRef{}
	taskExists := func(key string) bool {
		t.Helper()
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM tasks WHERE company_id = $1 AND key = $2)`,
			fixture.CompanyA, key,
		).Scan(&exists); err != nil {
			t.Fatalf("probe Task %q: %v", key, err)
		}
		return exists
	}

	t.Run("the files ride a message on the assignment Run and freeze into it", func(t *testing.T) {
		contract := seed(fixture.CompanyA, fixture.UserA, "contract.pdf", "ready", 2, 0)
		annex := seed(fixture.CompanyA, fixture.UserA, "annex.pdf", "ready", 2, time.Minute)
		created, err := create("attach_on_create", self, agent, StatusTodo, annex, contract)
		if err != nil {
			t.Fatalf("Create(files) error = %v", err)
		}
		if created.Status != StatusInProgress {
			t.Fatalf("Create(files) status = %q, want %q", created.Status, StatusInProgress)
		}
		var runID uuid.UUID
		var runContext []byte
		if err := pool.QueryRow(ctx,
			`SELECT id, context FROM runs WHERE task_id = $1`, created.ID,
		).Scan(&runID, &runContext); err != nil {
			t.Fatalf("read assignment Run: %v", err)
		}
		var document struct {
			TaskAttachments []struct {
				ID string `json:"id"`
			} `json:"taskAttachments"`
		}
		if err := json.Unmarshal(runContext, &document); err != nil {
			t.Fatalf("decode run context: %v", err)
		}
		frozen := make([]string, 0, len(document.TaskAttachments))
		for _, file := range document.TaskAttachments {
			frozen = append(frozen, file.ID)
		}
		if want := []string{contract.String(), annex.String()}; !slices.Equal(frozen, want) {
			t.Fatalf("assignment Run froze %v, want %v in upload order", frozen, want)
		}

		rows, err := pool.Query(ctx, `
			SELECT conversation_items.run_id, conversation_items.content,
				message_attachments.attachment_id
			FROM conversation_items
			JOIN message_attachments ON message_attachments.message_id = conversation_items.id
			WHERE conversation_items.conversation_id = $1
			  AND conversation_items.type = 'user_message'
			  AND conversation_items.author_kind = 'user'
			  AND conversation_items.author_id = $2
			ORDER BY message_attachments."position"`,
			created.ConversationID, fixture.UserA)
		if err != nil {
			t.Fatalf("read Task files message: %v", err)
		}
		defer rows.Close()
		var linked []uuid.UUID
		for rows.Next() {
			var messageRunID, attachmentID uuid.UUID
			var content string
			if err := rows.Scan(&messageRunID, &content, &attachmentID); err != nil {
				t.Fatalf("scan Task files message: %v", err)
			}
			if messageRunID != runID || content != "Attached 2 files for this Task." {
				t.Fatalf(
					"files message run %s content %q, want run %s and the file count",
					messageRunID,
					content,
					runID,
				)
			}
			linked = append(linked, attachmentID)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate Task files message: %v", err)
		}
		if want := []uuid.UUID{annex, contract}; !slices.Equal(linked, want) {
			t.Fatalf("files message links %v, want %v in the order given", linked, want)
		}
	})

	t.Run("files need the assignment Run to read them", func(t *testing.T) {
		file := seed(fixture.CompanyA, fixture.UserA, "brief.pdf", "ready", 2, 2*time.Minute)
		colleague := seedTaskMember(t, ctx, pool, fixture, "files-colleague@example.com")
		for _, refused := range []struct {
			key        string
			assignee   ActorRef
			executedBy *ActorRef
			status     Status
		}{
			// The assignee does the work themself: no Run.
			{"files_for_a_person", self, &ActorRef{Kind: ActorUser, ID: fixture.UserA}, StatusTodo},
			{"files_without_executor", self, nil, StatusTodo},
			// Another member's Task waits for them to start it.
			{"files_for_a_colleague", ActorRef{Kind: ActorUser, ID: colleague}, agent, StatusTodo},
			// A closed Task admits no Run.
			{"files_for_a_closed_task", self, agent, StatusDone},
			{"files_for_a_cancelled_task", self, agent, StatusCancelled},
		} {
			if _, err := create(
				refused.key, refused.assignee, refused.executedBy, refused.status, file,
			); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Create(%s) error = %v, want %v", refused.key, err, ErrInvalidInput)
			}
			if taskExists(refused.key) {
				t.Fatalf("Create(%s) left its Task behind", refused.key)
			}
		}
	})

	t.Run("a file the creator cannot attach refuses the whole create", func(t *testing.T) {
		unfinished := seed(fixture.CompanyA, fixture.UserA, "partial.pdf", "pending", 1, 3*time.Minute)
		foreign := seed(fixture.CompanyB, fixture.UserB, "foreign.pdf", "ready", 2, 4*time.Minute)
		ready := seed(fixture.CompanyA, fixture.UserA, "fresh.pdf", "ready", 2, 5*time.Minute)
		for name, file := range map[string]uuid.UUID{"unfinished": unfinished, "foreign": foreign} {
			key := "refused_" + name
			if _, err := create(
				key,
				self,
				agent,
				StatusTodo,
				ready,
				file,
			); !errors.Is(
				err,
				ErrAttachmentUnavailable,
			) {
				t.Fatalf("Create(%s file) error = %v, want %v", name, err, ErrAttachmentUnavailable)
			}
			if taskExists(key) {
				t.Fatalf("Create(%s file) left Task %q behind", name, key)
			}
		}
		if _, err := create("reuse_first", self, agent, StatusTodo, ready); err != nil {
			t.Fatalf("Create(ready file) error = %v", err)
		}
		if _, err := create(
			"reuse_again",
			self,
			agent,
			StatusTodo,
			ready,
		); !errors.Is(
			err,
			ErrAttachmentUnavailable,
		) {
			t.Fatalf("Create(already attached file) error = %v, want %v", err, ErrAttachmentUnavailable)
		}
	})
}
