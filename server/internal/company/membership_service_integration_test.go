//go:build integration

package company

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestListMembersReturnsResolvedActiveRoster(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	disabledUserID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name, avatar_url, disabled_at)
		VALUES ($1, 'disabled-member@example.com', 'Disabled Member',
			'https://cdn.example.com/disabled.png', now())`, disabledUserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, disabledUserID)

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewCompanyService() error = %v", err)
	}
	members, err := service.ListMembers(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	})
	if err != nil {
		t.Fatalf("ListMembers() error = %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("ListMembers() count = %d, want 2", len(members))
	}

	var disabled ActiveMemberResponse
	for _, member := range members {
		if member.Actor.ID == disabledUserID {
			disabled = member
		}
	}
	if disabled.Actor.ID != disabledUserID || disabled.Actor.Kind != ActorTypeUser ||
		disabled.Actor.DisplayName != "Disabled Member" || disabled.Actor.AvatarURL == nil ||
		*disabled.Actor.AvatarURL != "https://cdn.example.com/disabled.png" ||
		disabled.Email != "disabled-member@example.com" || !disabled.AccountDisabled ||
		disabled.CompanyID != fixture.CompanyA || disabled.Role != auth.CompanyRoleMember ||
		disabled.Revision != 1 {
		t.Fatalf("disabled member projection = %+v", disabled)
	}
}

func TestMembershipRemovalAndLeave(t *testing.T) {
	t.Parallel()
	for _, leave := range []bool{false, true} {
		name := "remove"
		if leave {
			name = "leave"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invites, pool, owner, recipient, sender := invitationFixture(t)
			_, token := createInvitationToken(t, invites, owner, sender)
			if _, err := invites.Accept(context.Background(), recipient, token); err != nil {
				t.Fatal(err)
			}
			svc := &DefaultCompanyService{store: invites.store, bus: invites.bus}
			var companySyncUsers []string
			if err := invites.bus.Subscribe(EventCompanyMembershipChanged,
				func(_ context.Context, event events.Event) error {
					membership, payloadErr := events.PayloadOf[Membership](event)
					if payloadErr != nil {
						return payloadErr
					}
					companySyncUsers = append(companySyncUsers, membership.UserID)
					return nil
				}); err != nil {
				t.Fatalf("subscribe company membership changes: %v", err)
			}
			recipient.CompanyID = owner.CompanyID
			recipient.Role = auth.CompanyRoleMember
			members, err := svc.ListMembers(context.Background(), owner)
			if err != nil {
				t.Fatal(err)
			}
			var revision int64
			for _, member := range members {
				if member.Actor.ID == recipient.UserID {
					revision = member.Revision
				}
			}
			if err := svc.RemoveMember(
				context.Background(),
				recipient,
				MembershipMutation{UserID: owner.UserID, ExpectedRevision: 1},
			); !errors.Is(
				err,
				ErrForbidden,
			) {
				t.Fatalf("member removal = %v", err)
			}
			if err := svc.Leave(context.Background(), owner, 1); !errors.Is(err, ErrLastOwner) {
				t.Fatalf("last owner = %v", err)
			}
			if err := svc.RemoveMember(
				context.Background(),
				owner,
				MembershipMutation{UserID: recipient.UserID, ExpectedRevision: revision + 1},
			); !errors.Is(
				err,
				ErrMemberRevisionConflict,
			) {
				t.Fatalf("stale revision = %v", err)
			}

			// A departing machine owner takes queued execution offline atomically.
			dbtest.MustExec(
				t,
				context.Background(),
				pool,
				`UPDATE runtimes SET owner_user_id=$2 WHERE company_id=$1`,
				owner.CompanyID,
				recipient.UserID,
			)
			runState := "running"
			if leave {
				runState = "queued"
			}
			var runID string
			if err = pool.QueryRow(context.Background(), `
                WITH c AS (
                  INSERT INTO conversations (company_id,kind,agent_id,created_by_user_id,title)
                  SELECT company_id,'direct',id,$2,'Membership test' FROM agents WHERE company_id=$1 LIMIT 1
                  RETURNING id,company_id,agent_id
                )
                INSERT INTO runs (
                  company_id,agent_id,conversation_id,runtime_id,
                  execution_authorized_by_user_id,status,run_type
                )
                SELECT c.company_id,c.agent_id,c.id,
                       (SELECT runtimes.id FROM runtimes WHERE runtimes.company_id=c.company_id LIMIT 1),
                       $2,$3,'direct_chat'
                FROM c
                RETURNING id`, owner.CompanyID, recipient.UserID, runState).Scan(&runID); err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(
				t,
				context.Background(),
				pool,
				`INSERT INTO run_tokens (run_id,token_hash,expires_at) VALUES ($1,$2,now()+interval '1 hour')`,
				runID,
				[]byte("membership-token"),
			)
			if leave {
				err = svc.Leave(context.Background(), recipient, revision)
			} else {
				err = svc.RemoveMember(
					context.Background(),
					owner,
					MembershipMutation{UserID: recipient.UserID, ExpectedRevision: revision},
				)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(companySyncUsers) != 1 || companySyncUsers[0] != recipient.UserID.String() {
				t.Fatalf("company sync users = %v, want [%s]", companySyncUsers, recipient.UserID)
			}

			var runStatus string
			if err := pool.QueryRow(context.Background(), `SELECT status FROM runs WHERE id=$1`, runID).
				Scan(&runStatus); err != nil ||
				runStatus != "cancelled" {
				t.Fatalf("run status = %s, %v", runStatus, err)
			}
			var tokens int
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM run_tokens WHERE run_id=$1`, runID).
				Scan(&tokens); err != nil ||
				tokens != 0 {
				t.Fatalf("remaining tokens = %d, %v", tokens, err)
			}
			if _, err := invites.Accept(
				context.Background(),
				recipient,
				token,
			); !errors.Is(
				err,
				ErrInvitationInactive,
			) {
				t.Fatalf("old invite rejoined = %v", err)
			}
			if _, err := svc.Authorize(
				context.Background(),
				recipient,
				owner.CompanyID,
			); !errors.Is(
				err,
				ErrNotFound,
			) {
				t.Fatalf("removed access = %v", err)
			}
			members, err = svc.ListMembers(context.Background(), owner)
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range members {
				if member.Actor.ID == recipient.UserID {
					t.Fatal("removed member still in roster")
				}
			}
		})
	}
}

func TestConcurrentOwnersCannotBothLeave(t *testing.T) {
	t.Parallel()
	invites, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	_, token := createInvitationToken(t, invites, owner, sender)
	if _, err := invites.Accept(ctx, recipient, token); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='owner' WHERE company_id=$1 AND user_id=$2`,
		owner.CompanyID,
		recipient.UserID,
	)
	recipient.CompanyID = owner.CompanyID
	recipient.Role = auth.CompanyRoleOwner
	svc := &DefaultCompanyService{store: invites.store, bus: invites.bus}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, principal := range []auth.UserPrincipal{owner, recipient} {
		go func() {
			<-start
			results <- svc.Leave(ctx, principal, 1)
		}()
	}
	close(start)
	var succeeded, blocked int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrLastOwner):
			blocked++
		default:
			t.Fatalf("concurrent leave = %v", err)
		}
	}
	if succeeded != 1 || blocked != 1 {
		t.Fatalf("succeeded %d, blocked %d", succeeded, blocked)
	}
}

// A colleague's Message in a Task chat waits in the queue for the assignee's
// next Run (ADR 0014), and the Message can only be written while its author
// is a member. Removing the colleague removes their queued words and closes
// the assignee's nudge about them, so the assignee can still start the
// Task, send in it and finish its Runs. A queued item whose author stopped
// being active without that cleanup is skipped, not written.
func TestRevokedColleaguesQueuedTaskMessageDoesNotBlockTheAssignee(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	companies, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewCompanyService() error = %v", err)
	}
	taskMessageRuns := conversation.TaskMessageRunPreparerFuncs{
		Check:    task.CheckMessageRunInTx,
		Prepare:  task.PrepareMessageRunInTx,
		NextStep: task.RunNextStepInTx,
	}
	conversations, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store, Bus: bus, TaskMessageRuns: taskMessageRuns,
	})
	if err != nil {
		t.Fatalf("NewConversationService() error = %v", err)
	}
	tasks, err := task.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewTaskService() error = %v", err)
	}
	runs := execution.NewRunService(execution.RunServiceOptions{
		Queries: store.Queries(), Transactor: store, Bus: bus,
		RouteResolver: conversation.ResolveConversationRoute,
	})
	runs.SetRunQueuePromoter(execution.RunQueuePromoterFunc(func(
		ctx context.Context, q *generated.Queries, pending *[]events.Event, run generated.Run, output string,
	) (*uuid.UUID, error) {
		promotion, err := conversation.FinalizeRunConversationInTx(
			ctx, q, pending, taskMessageRuns, run, output,
		)
		return promotion.RunID, err
	}))

	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	target := conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA}
	member := func(email string) uuid.UUID {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`, id, email)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			fixture.CompanyA, id)
		// Writing in the Task's chat needs project.tasks.write (ADR 0017).
		dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, id)
		return id
	}
	send := func(author uuid.UUID, content string) conversation.SendResult {
		t.Helper()
		sent, err := conversations.Send(ctx, auth.UserPrincipal{UserID: author}, target,
			conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: content})
		if err != nil {
			t.Fatalf("Send(%q) error = %v", content, err)
		}
		return sent
	}
	queueState := func(itemID uuid.UUID) (string, string) {
		t.Helper()
		var state, reason string
		if err := pool.QueryRow(ctx, `
			SELECT state, COALESCE(removal_reason, '')
			FROM conversation_run_queue_items WHERE id = $1`, itemID,
		).Scan(&state, &reason); err != nil {
			t.Fatalf("read queue item %s: %v", itemID, err)
		}
		return state, reason
	}
	complete := func(runID uuid.UUID) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'running' WHERE id = $1`, runID)
		receipt, err := runs.AppendTranscriptItem(ctx, fixture.RuntimeA, runID, execution.TranscriptItemInput{
			ItemKey: "answer",
			Role:    protocol.RunItemRoleAgent,
			Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "done"}},
		})
		if err != nil {
			t.Fatalf("AppendTranscriptItem(%s) error = %v", runID, err)
		}
		if _, err := runs.CompleteRun(ctx, runID, execution.TerminalTranscriptProof{
			ThroughSeq: receipt.ThroughSeq, FinalItemKey: receipt.ItemKey,
		}, "", false, ""); err != nil {
			t.Fatalf("CompleteRun(%s) error = %v", runID, err)
		}
	}

	// Two colleagues write in the owner's Task. One is then removed; the
	// other stops being active behind the service's back, as a row an older
	// revocation left queued would.
	departing := member("departing@example.com")
	stale := member("stale@example.com")
	departed := send(departing, "From the colleague who leaves.")
	orphaned := send(stale, "From a colleague revoked without cleanup.")
	if departed.QueueItem == nil || orphaned.QueueItem == nil {
		t.Fatalf("colleague sends = %+v / %+v, want queued items", departed, orphaned)
	}
	if err := companies.RemoveMember(
		ctx, owner, MembershipMutation{UserID: departing, ExpectedRevision: 1},
	); err != nil {
		t.Fatalf("RemoveMember() error = %v", err)
	}
	if state, reason := queueState(departed.QueueItem.ID); state != "removed" || reason != "authorization_revoked" {
		t.Fatalf("departed colleague's item = %s/%s, want removed/authorization_revoked", state, reason)
	}
	var nudgeOpen bool
	if err := pool.QueryRow(ctx, `
		SELECT archived_at IS NULL FROM inbox_items
		WHERE recipient_user_id = $1 AND kind = 'task_run_requested' AND entity_id = $2`,
		fixture.UserA, departed.QueueItem.MessageID.String(),
	).Scan(&nudgeOpen); err != nil || nudgeOpen {
		t.Fatalf("nudge about the removed Message open = %v, %v; want closed", nudgeOpen, err)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members SET status = 'revoked', revoked_at = now()
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, stale)

	// The assignee starts the Task.
	current, err := tasks.Get(ctx, owner, fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get(Task A) error = %v", err)
	}
	started, err := tasks.StartExecution(ctx, owner, fixture.TaskA, task.StartExecutionInput{
		ExpectedRevision: current.Revision,
	})
	if err != nil || started.RunID == nil {
		t.Fatalf("StartExecution(assignee) = %+v, %v", started, err)
	}
	// A prompt sent while that Run is live waits behind it, and finishing
	// the Run promotes it.
	deferred := send(fixture.UserA, "A follow-up from the assignee.")
	if deferred.Disposition != conversation.SendDispositionQueued || deferred.QueueItem == nil {
		t.Fatalf("assignee's send behind a live Run = %+v, want queued", deferred)
	}
	complete(*started.RunID)
	if state, _ := queueState(deferred.QueueItem.ID); state != "materialized" {
		t.Fatalf("assignee's deferred prompt = %s, want materialized by promotion", state)
	}
	var promoted uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT run_id FROM conversation_items WHERE id = $1`,
		deferred.QueueItem.MessageID).Scan(&promoted); err != nil {
		t.Fatalf("read the promoted Run: %v", err)
	}
	complete(promoted)
	// With nothing live, the assignee's message starts a Run at once.
	direct := send(fixture.UserA, "One more from the assignee.")
	if direct.Disposition != conversation.SendDispositionMessage || direct.RunID == nil {
		t.Fatalf("assignee's send with nothing live = %+v, want a message and a Run", direct)
	}
	// The stale colleague's words were never written: every Run skipped them.
	if state, _ := queueState(orphaned.QueueItem.ID); state != "queued" {
		t.Fatalf("stale colleague's item = %s, want still queued", state)
	}
	var written bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conversation_items WHERE id = $1)`,
		orphaned.QueueItem.MessageID).Scan(&written); err != nil || written {
		t.Fatalf("stale colleague's Message written = %v, %v; want not written", written, err)
	}
}

// Board grants go with the membership like Project grants (ADR 0017): a
// rejoined member reaches only what is shared with them again.
func TestMembershipRemovalDeletesBoardGrants(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	companies, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	boards := canvas.NewBoardService(store, bus)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		fixture.CompanyA, fixture.UserB)
	owner := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserA, Role: auth.CompanyRoleOwner}
	member := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserB, Role: auth.CompanyRoleMember}
	board, err := boards.Create(ctx, owner, canvas.CreateBoardInput{RequestKey: uuid.New(), Title: "Plans"})
	if err != nil {
		t.Fatal(err)
	}
	if err = boards.SetAccess(ctx, owner, board.ID, canvas.BoardAccessInput{
		ExpectedRevision: board.Revision,
		Actor:            protocol.ActorRef{ID: member.UserID, Kind: protocol.ActorKindUser},
		Access:           "manage",
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = pool.QueryRow(ctx, `SELECT version FROM company_members WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA, member.UserID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = companies.RemoveMember(ctx, owner, MembershipMutation{UserID: member.UserID, ExpectedRevision: version}); err != nil {
		t.Fatal(err)
	}
	var grants int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM board_member_access WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA, member.UserID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("Board grants left after removal = %d, %v", grants, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET status='active', reactivated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND user_id=$2`, fixture.CompanyA, member.UserID)
	if _, err = boards.Snapshot(ctx, member, canvas.BoardScope{BoardID: board.ID}, 0); !errors.Is(err, canvas.ErrBoardNotFound) {
		t.Fatalf("rejoined member reads the private Board: %v", err)
	}
}
