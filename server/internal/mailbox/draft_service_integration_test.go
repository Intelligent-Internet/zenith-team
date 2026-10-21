//go:build integration

package mailbox_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/email"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mailbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestAgentMailCreateBindsLiveRunReplaysAndPreservesConversationVisibility(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	service := newDraftService(t, test)
	trusted := mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}
	input := []mailbox.DraftInput{{
		Subject: "Lead follow-up", Text: "Hello from Zenith Team",
		Deliveries: []mailbox.DraftDeliveryInput{
			{To: []string{"lead-a@example.com"}},
			{To: []string{"lead-b@example.com"}, CC: []string{"owner@example.com"}},
		},
	}}

	created, err := service.Create(test.ctx, test.ownerA, trusted, input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.SourceRunID != test.fixture.RunA || created.ConversationID != test.fixture.ConversationA ||
		len(created.Drafts) != 1 || created.Drafts[0].RecipientCount != 3 ||
		created.Drafts[0].FromAddress != "agent-mail@"+mailboxDomain ||
		created.Drafts[0].FromName != "Schema A" {
		t.Fatalf("created group = %#v", created)
	}
	// The Run writes the drafts for its member, so the frame,
	// and the Activity row it becomes, names the Agent, the member and the
	// Run.
	drafted := events.RunActor(
		test.fixture.AgentA.String(),
		test.fixture.RunA.String(),
		test.fixture.UserA.String(),
	)
	test.mailboxEvents.mu.Lock()
	staged := append([]events.Event(nil), test.mailboxEvents.events...)
	test.mailboxEvents.mu.Unlock()
	draftFrames := 0
	for _, event := range staged {
		if event.Name != mailbox.EventDraftChanged {
			continue
		}
		draftFrames++
		if event.Actor != drafted {
			t.Fatalf("mailbox.draft_changed actor = %+v, want %+v", event.Actor, drafted)
		}
	}
	if draftFrames != 1 {
		t.Fatalf("mailbox.draft_changed frames = %d, want 1", draftFrames)
	}
	replayed, err := service.Create(test.ctx, test.ownerA, trusted, input)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("replayed Create() = %#v, %v", replayed, err)
	}
	changed := append([]mailbox.DraftInput(nil), input...)
	changed[0].Text = "Changed payload"
	if _, err := service.Create(
		test.ctx,
		test.ownerA,
		trusted,
		changed,
	); !errors.Is(
		err,
		mailbox.ErrAgentMailDraftConflict,
	) {
		t.Fatalf("changed replay error = %v, want conflict", err)
	}

	// Another owner still needs the chat's Project access.
	dbtest.MustExec(
		t,
		test.ctx,
		test.pool,
		`UPDATE company_members SET role='owner' WHERE company_id=$1 AND user_id=$2`,
		test.fixture.CompanyA,
		test.fixture.UserB,
	)
	test.memberA.Role = auth.CompanyRoleOwner
	groups, err := service.ListGroups(
		test.ctx, test.memberA, test.fixture.ConversationA, []uuid.UUID{test.fixture.RunA},
	)
	if err != nil || len(groups) != 1 || groups[0].ID != created.ID {
		t.Fatalf("ListGroups(task owner) = %#v, %v", groups, err)
	}
	if _, err := service.ListGroups(
		test.ctx, test.ownerB, test.fixture.ConversationA, []uuid.UUID{test.fixture.RunA},
	); !errors.Is(err, mailbox.ErrAgentMailDraftNotFound) {
		t.Fatalf("cross-tenant ListGroups() error = %v", err)
	}
	// Editing, deleting or sending a Task chat's drafts is posting there
	// (ADR 0017): an owner who only reads the Project sees them but may not
	// change or send them.
	dbtest.MustExec(t, test.ctx, test.pool,
		`DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2 AND permission <> 'project.read'`,
		test.fixture.ProjectA, test.fixture.UserB)
	draft := created.Drafts[0]
	if _, err := service.UpdateDraft(
		test.ctx,
		test.memberA,
		test.fixture.ConversationA,
		draft.ID,
		mailbox.UpdateDraftInput{
			ExpectedRevision: draft.Revision,
			Subject:          "Changed",
			Text:             "Changed",
			Deliveries:       input[0].Deliveries,
		},
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("UpdateDraft(reader) error = %v, want ErrProjectPermissionRequired", err)
	}
	if err := service.DeleteDraft(
		test.ctx, test.memberA, test.fixture.ConversationA, draft.ID, draft.Revision,
	); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("DeleteDraft(reader) error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := service.QueueDraft(
		test.ctx, test.memberA, test.fixture.ConversationA, draft.ID, draft.Revision,
	); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("QueueDraft(reader) error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := service.QueueGroup(
		test.ctx, test.memberA, test.fixture.ConversationA, created.ID,
		[]mailbox.DraftRevision{{ID: draft.ID, Revision: draft.Revision}},
	); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("QueueGroup(reader) error = %v, want ErrProjectPermissionRequired", err)
	}
	// A Task chat's drafts follow its Project (ADR 0017): an owner who stops
	// reading the Project no longer finds them.
	dbtest.MustExec(t, test.ctx, test.pool,
		`DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		test.fixture.ProjectA, test.fixture.UserB)
	if _, err := service.ListGroups(
		test.ctx, test.memberA, test.fixture.ConversationA, []uuid.UUID{test.fixture.RunA},
	); !errors.Is(err, mailbox.ErrAgentMailDraftNotFound) {
		t.Fatalf("ListGroups(member outside the Project) error = %v, want not found", err)
	}
}

func TestAgentMailCreateLocksConversationBeforeRun(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail-order")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	service := newDraftService(t, test)

	holder, err := test.pool.Begin(test.ctx)
	if err != nil {
		t.Fatalf("begin Conversation lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		test.ctx,
		`SELECT id FROM conversations WHERE id = $1 FOR UPDATE`,
		test.fixture.ConversationA,
	); err != nil {
		t.Fatalf("hold Conversation lock: %v", err)
	}

	type createResult struct {
		value mailbox.DraftCreateResult
		err   error
	}
	result := make(chan createResult, 1)
	go func() {
		created, createErr := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
			CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
			RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
		}, []mailbox.DraftInput{{
			Subject: "Lock order", Text: "Conversation before Run",
			Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"lead@example.com"}}},
		}})
		result <- createResult{value: created, err: createErr}
	}()
	waitForBlockedMailboxQuery(t, test.ctx, test.pool, "LockAgentMailConversationForMutation")

	checker, err := test.pool.Begin(test.ctx)
	if err != nil {
		t.Fatalf("begin Run lock checker: %v", err)
	}
	var checkedRunID uuid.UUID
	checkErr := checker.QueryRow(
		test.ctx,
		`SELECT id FROM runs WHERE id = $1 FOR UPDATE NOWAIT`,
		test.fixture.RunA,
	).Scan(&checkedRunID)
	_ = checker.Rollback(context.Background())
	if checkErr != nil {
		t.Fatalf("Run locked before the canonical Conversation lock completed: %v", checkErr)
	}
	if checkedRunID != test.fixture.RunA {
		t.Fatalf("checked Run id = %d, want %d", checkedRunID, test.fixture.RunA)
	}

	if err := holder.Commit(test.ctx); err != nil {
		t.Fatalf("release Conversation lock: %v", err)
	}
	select {
	case created := <-result:
		if created.err != nil || !created.value.Created {
			t.Fatalf("Create() = %#v, %v", created.value, created.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Create() did not resume after Conversation lock release")
	}
}

func TestAgentMailListGroupsIsBoundedToRequestedSourceRuns(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	service := newDraftService(t, test)

	makeLiveDraftRun(t, test, test.fixture.RunA)
	first, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, []mailbox.DraftInput{{
		Subject: "First", Text: "Body",
		Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"first@example.com"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	finishDraftRun(t, test, test.fixture.RunA)
	secondRunID := cloneDraftRun(t, test)
	makeLiveDraftRun(t, test, secondRunID)
	second, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: secondRunID, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, []mailbox.DraftInput{{
		Subject: "Second", Text: "Body",
		Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"second@example.com"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	groups, err := service.ListGroups(
		test.ctx, test.ownerA, test.fixture.ConversationA, []uuid.UUID{secondRunID},
	)
	if err != nil || len(groups) != 1 || groups[0].ID != second.ID || groups[0].ID == first.ID {
		t.Fatalf("ListGroups(second Run) = %#v, %v", groups, err)
	}
	if _, err := service.ListGroups(
		test.ctx, test.ownerA, test.fixture.ConversationA, nil,
	); !errors.Is(err, mailbox.ErrAgentMailInvalidInput) {
		t.Fatalf("ListGroups(without source Runs) error = %v", err)
	}
	tooManySourceRuns := make([]uuid.UUID, 101)
	for index := range tooManySourceRuns {
		tooManySourceRuns[index] = uuid.New()
	}
	if _, err := service.ListGroups(
		test.ctx, test.ownerA, test.fixture.ConversationA, tooManySourceRuns,
	); !errors.Is(err, mailbox.ErrAgentMailInvalidInput) {
		t.Fatalf("ListGroups(above source Run limit) error = %v", err)
	}
}

func TestAgentMailCreateRequiresCompanyEmailLiveRunAndInteractiveConversationAccess(t *testing.T) {
	test := newMailboxTest(t)
	service := newDraftService(t, test)
	input := []mailbox.DraftInput{{
		Subject: "Review me", Text: "Body",
		Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"lead@example.com"}}},
	}}
	makeLiveDraftRun(t, test, test.fixture.RunA)
	trusted := mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}
	if _, err := service.Create(
		test.ctx,
		test.ownerA,
		trusted,
		input,
	); !errors.Is(
		err,
		mailbox.ErrAgentMailCompanyEmailRequired,
	) {
		t.Fatalf("Create(without Company Email) error = %v", err)
	}
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	finishDraftRun(t, test, test.fixture.RunA)
	if _, err := service.Create(
		test.ctx,
		test.ownerA,
		trusted,
		input,
	); !errors.Is(
		err,
		mailbox.ErrAgentMailForbidden,
	) {
		t.Fatalf("Create(finished Run) error = %v", err)
	}

	directRunID := cloneDraftRun(t, test)
	if _, err := test.pool.Exec(test.ctx, `
		UPDATE runs
		SET task_id=NULL, conversation_id=$2, updated_at=clock_timestamp()
		WHERE id=$1`, directRunID, test.fixture.DirectConversationA); err != nil {
		t.Fatalf("make direct Run: %v", err)
	}
	makeLiveDraftRun(t, test, directRunID)
	directTrusted := mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.DirectConversationA,
		RunID: directRunID, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}
	if _, err := service.Create(
		test.ctx,
		test.memberA,
		directTrusted,
		input,
	); !errors.Is(
		err,
		mailbox.ErrAgentMailForbidden,
	) {
		t.Fatalf("Create(other user's direct chat) error = %v", err)
	}
	created, err := service.Create(test.ctx, test.ownerA, directTrusted, input)
	if err != nil || created.ConversationID != test.fixture.DirectConversationA {
		t.Fatalf("Create(direct owner) = %#v, %v", created, err)
	}
	if _, err := service.ListGroups(
		test.ctx, test.memberA, test.fixture.DirectConversationA, []uuid.UUID{directRunID},
	); !errors.Is(err, mailbox.ErrAgentMailForbidden) {
		t.Fatalf("ListGroups(other user's direct chat) error = %v", err)
	}

	scheduledRunID, scheduleConversationID := seedScheduledDraftRun(t, test)
	makeLiveDraftRun(t, test, scheduledRunID)
	if _, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: scheduleConversationID,
		RunID: scheduledRunID, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, input); !errors.Is(err, mailbox.ErrAgentMailForbidden) {
		t.Fatalf("Create(scheduled Run) error = %v", err)
	}
}

func TestAgentMailEditUsesOptimisticVersionAndQueueSendsPrivateBatch(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	provider := newFakeOutboundProvider()
	service := newDraftService(t, test)
	trusted := mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}
	group, err := service.Create(test.ctx, test.ownerA, trusted, []mailbox.DraftInput{{
		Subject: "Initial", Text: "Body",
		Deliveries: []mailbox.DraftDeliveryInput{
			{To: []string{"lead-a@example.com"}},
			{To: []string{"lead-b@example.com"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	draft := group.Drafts[0]
	updated, err := service.UpdateDraft(
		test.ctx,
		test.ownerA,
		test.fixture.ConversationA,
		draft.ID,
		mailbox.UpdateDraftInput{
			Subject: "Updated", Text: "Updated body", Deliveries: []mailbox.DraftDeliveryInput{
				{To: []string{"lead-a@example.com"}},
				{To: []string{"lead-b@example.com"}},
			}, ExpectedRevision: draft.Revision,
		},
	)
	if err != nil || updated.Revision != draft.Revision+1 || updated.Subject != "Updated" {
		t.Fatalf("UpdateDraft() = %#v, %v", updated, err)
	}
	if _, err := service.UpdateDraft(
		test.ctx,
		test.ownerA,
		test.fixture.ConversationA,
		draft.ID,
		mailbox.UpdateDraftInput{
			Subject: "Stale", Text: "Body", Deliveries: updatedDeliveryInputs(updated),
			ExpectedRevision: draft.Revision,
		},
	); !errors.Is(
		err,
		mailbox.ErrAgentMailDraftConflict,
	) {
		t.Fatalf("stale UpdateDraft() error = %v", err)
	}

	queued, err := service.QueueDraft(test.ctx, test.ownerA, test.fixture.ConversationA, draft.ID, updated.Revision)
	if err != nil || queued.Status != mailbox.DraftStatusSending || queued.SendGeneration != 1 {
		t.Fatalf("QueueDraft() = %#v, %v", queued, err)
	}
	worker := newDraftWorker(t, test, provider)
	processed, err := worker.ProcessOnce(test.ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessOnce() = %t, %v", processed, err)
	}
	requests := provider.take(t, 1)
	if len(requests[0].messages) != 2 || requests[0].idempotencyKey != "agent-mail/"+draft.ID.String()+"/1" {
		t.Fatalf("provider request = %#v", requests[0])
	}
	if len(requests[0].messages[0].To) != 1 || len(requests[0].messages[1].To) != 1 ||
		requests[0].messages[0].To[0] == requests[0].messages[1].To[0] {
		t.Fatalf("independent deliveries were not private: %#v", requests[0].messages)
	}
	groups, err := service.ListGroups(
		test.ctx, test.ownerA, test.fixture.ConversationA, []uuid.UUID{test.fixture.RunA},
	)
	if err != nil || groups[0].Drafts[0].Status != mailbox.DraftStatusSent ||
		groups[0].Drafts[0].SentAt == nil {
		t.Fatalf("sent group = %#v, %v", groups, err)
	}
}

func TestAgentMailWorkerRejectsInvalidProviderDeliveryIDBeforeWrite(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		deliveryID string
	}{
		{name: "surrounding whitespace", deliveryID: " provider-id"},
		{name: "embedded NUL", deliveryID: "provider\x00id"},
		{name: "invalid UTF-8", deliveryID: string([]byte{0xff})},
		{name: "more than 512 bytes", deliveryID: strings.Repeat("x", 513)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			test := newMailboxTest(t)
			mustCreateMailbox(t, test, test.ownerA, "agent-mail-provider-id")
			makeLiveDraftRun(t, test, test.fixture.RunA)
			provider := newFakeOutboundProvider()
			provider.setDeliveryID(testCase.deliveryID)
			service := newDraftService(t, test)
			group, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
				CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
				RunID: test.fixture.RunA, AgentID: test.fixture.AgentA,
				RequestKey: uuid.Must(uuid.NewV7()),
			}, []mailbox.DraftInput{{
				Subject: "Provider contract", Text: "Reject an invalid provider delivery ID",
				Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"lead@example.com"}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			draft := group.Drafts[0]
			if _, err := service.QueueDraft(
				test.ctx, test.ownerA, test.fixture.ConversationA, draft.ID, draft.Revision,
			); err != nil {
				t.Fatalf("QueueDraft() error = %v", err)
			}

			processed, err := newDraftWorker(t, test, provider).ProcessOnce(test.ctx)
			if !processed || err == nil || !strings.Contains(err.Error(), "invalid delivery ID") {
				t.Fatalf("ProcessOnce() = %t, %v; want provider delivery ID rejection", processed, err)
			}
			var providerIDMissing, acceptedAtMissing bool
			if err := test.pool.QueryRow(test.ctx, `
				SELECT provider_email_id IS NULL, accepted_at IS NULL
				FROM mailbox_draft_deliveries
				WHERE draft_id = $1`, draft.ID).Scan(&providerIDMissing, &acceptedAtMissing); err != nil {
				t.Fatalf("read rejected Agent Mail delivery: %v", err)
			}
			if !providerIDMissing || !acceptedAtMissing {
				t.Fatalf(
					"rejected delivery persisted provider ID = %v, accepted at = %v",
					!providerIDMissing,
					!acceptedAtMissing,
				)
			}
		})
	}
}

func TestAgentMailQueueFencesConcurrentMembershipRevocationAtCompany(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail-account-race")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	service := newDraftService(t, test)
	group, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, []mailbox.DraftInput{{
		Subject: "Do not send", Text: "The member is being revoked",
		Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"lead@example.com"}}},
	}})
	if err != nil {
		t.Fatalf("create Agent Mail draft: %v", err)
	}
	draft := group.Drafts[0]

	holder, err := test.pool.Begin(test.ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		test.ctx,
		`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
		test.fixture.CompanyA,
	); err != nil {
		t.Fatalf("hold Company lock: %v", err)
	}

	queueResult := make(chan error, 1)
	go func() {
		_, queueErr := service.QueueDraft(
			test.ctx,
			test.ownerA,
			test.fixture.ConversationA,
			draft.ID,
			draft.Revision,
		)
		queueResult <- queueErr
	}()
	waitForBlockedMailboxQuery(t, test.ctx, test.pool, "LockCompanyForMembershipMutation")

	if _, err := holder.Exec(
		test.ctx, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(),
			updated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2 AND status = 'active'`,
		test.fixture.CompanyA,
		test.fixture.UserA,
	); err != nil {
		t.Fatalf("revoke Agent Mail sender membership: %v", err)
	}
	if err := holder.Commit(test.ctx); err != nil {
		t.Fatalf("commit membership revocation: %v", err)
	}

	select {
	case err := <-queueResult:
		if !errors.Is(err, mailbox.ErrAgentMailForbidden) {
			t.Fatalf("QueueDraft() error = %v, want ErrAgentMailForbidden", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("QueueDraft() did not resume after Company lock release")
	}
	var status string
	if err := test.pool.QueryRow(
		test.ctx, `
		SELECT status FROM mailbox_drafts WHERE id = $1`, draft.ID,
	).Scan(&status); err != nil {
		t.Fatalf("read post-revocation draft: %v", err)
	}
	if status != string(mailbox.DraftStatusDraft) {
		t.Fatalf("post-revocation draft status = %q, want draft", status)
	}
}

func TestAgentMailCompanyDailyQuotaIsAtomicAcrossConcurrentQueues(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	service := newDraftService(t, test)

	groups := make([]mailbox.DraftGroup, 2)
	for index := range groups {
		runID := test.fixture.RunA
		if index == 1 {
			runID = cloneDraftRun(t, test)
		}
		makeLiveDraftRun(t, test, runID)
		created, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
			CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
			RunID: runID, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
		}, []mailbox.DraftInput{{
			Subject: "Quota", Text: "Body", Deliveries: quotaDeliveries(60, index),
		}})
		if err != nil {
			t.Fatalf("Create(%d) error = %v", index, err)
		}
		groups[index] = created.DraftGroup
		finishDraftRun(t, test, runID)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, group := range groups {
		go func() {
			<-start
			_, queueErr := service.QueueDraft(
				test.ctx, test.ownerA, test.fixture.ConversationA,
				group.Drafts[0].ID, group.Drafts[0].Revision,
			)
			errs <- queueErr
		}()
	}
	close(start)
	successes, limited := 0, 0
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			successes++
		case errors.Is(err, mailbox.ErrAgentMailSendLimitExceeded):
			limited++
		default:
			t.Fatalf("QueueDraft() error = %v", err)
		}
	}
	if successes != 1 || limited != 1 {
		t.Fatalf("concurrent quota results = %d success, %d limited", successes, limited)
	}
}

func TestAgentMailGroupRecipientLimitIsAtomicAcrossConcurrentEdits(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	service := newDraftService(t, test)
	group, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, []mailbox.DraftInput{
		{
			Subject:    "First",
			Text:       "Body",
			Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"first@example.com"}}},
		},
		{
			Subject:    "Second",
			Text:       "Body",
			Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"second@example.com"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, len(group.Drafts))
	for index, draft := range group.Drafts {
		go func() {
			<-start
			_, updateErr := service.UpdateDraft(
				test.ctx, test.ownerA, test.fixture.ConversationA, draft.ID,
				mailbox.UpdateDraftInput{
					Subject: draft.Subject, Text: draft.Text,
					Deliveries: quotaDeliveries(60, index), ExpectedRevision: draft.Revision,
				},
			)
			errs <- updateErr
		}()
	}
	close(start)
	successes, limited := 0, 0
	for range len(group.Drafts) {
		switch updateErr := <-errs; {
		case updateErr == nil:
			successes++
		case errors.Is(updateErr, mailbox.ErrAgentMailSendLimitExceeded):
			limited++
		default:
			t.Fatalf("UpdateDraft() error = %v", updateErr)
		}
	}
	if successes != 1 || limited != 1 {
		t.Fatalf("concurrent group limit results = %d success, %d limited", successes, limited)
	}
}

func TestAgentMailSendAllKeepsEachDraftOutcomeIndependent(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	provider := newFakeOutboundProvider()
	service := newDraftService(t, test)
	group, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, []mailbox.DraftInput{
		{
			Subject:    "First",
			Text:       "First body",
			Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"one@example.com"}}},
		},
		{
			Subject:    "Second",
			Text:       "Second body",
			Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"two@example.com"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	versions := make([]mailbox.DraftRevision, len(group.Drafts))
	for index, draft := range group.Drafts {
		versions[index] = mailbox.DraftRevision{ID: draft.ID, Revision: draft.Revision}
	}
	queued, err := service.QueueGroup(
		test.ctx, test.ownerA, test.fixture.ConversationA, group.ID, versions,
	)
	if err != nil {
		t.Fatalf("QueueGroup() error = %v", err)
	}
	for _, draft := range queued.Drafts {
		if draft.Status != mailbox.DraftStatusSending {
			t.Fatalf("queued draft status = %q", draft.Status)
		}
	}

	provider.setFailures(nil, errors.New("provider rejected one draft"))
	worker := newDraftWorker(t, test, provider)
	for range 2 {
		processed, processErr := worker.ProcessOnce(test.ctx)
		if processErr != nil || !processed {
			t.Fatalf("ProcessOnce() = %t, %v", processed, processErr)
		}
	}
	groups, err := service.ListGroups(
		test.ctx, test.ownerA, test.fixture.ConversationA, []uuid.UUID{test.fixture.RunA},
	)
	if err != nil || len(groups) != 1 || len(groups[0].Drafts) != 2 {
		t.Fatalf("ListGroups() = %#v, %v", groups, err)
	}
	statuses := map[mailbox.DraftStatus]int{}
	for _, draft := range groups[0].Drafts {
		statuses[draft.Status]++
	}
	if statuses[mailbox.DraftStatusSent] != 1 || statuses[mailbox.DraftStatusFailed] != 1 {
		t.Fatalf("independent draft statuses = %#v", statuses)
	}
}

func TestAgentMailWorkerRetriesAfterRestartWithStableIdempotencyKey(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	provider := newFakeOutboundProvider()
	service := newDraftService(t, test)
	group, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, []mailbox.DraftInput{{
		Subject: "Retry", Text: "Body",
		Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"retry@example.com"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	draft := group.Drafts[0]
	if _, err := service.QueueDraft(
		test.ctx, test.ownerA, test.fixture.ConversationA, draft.ID, draft.Revision,
	); err != nil {
		t.Fatalf("QueueDraft() error = %v", err)
	}
	provider.setFailures(&email.RateLimitError{RetryAfter: time.Second}, nil)
	firstWorker := newDraftWorker(t, test, provider)
	if processed, err := firstWorker.ProcessOnce(test.ctx); err != nil || !processed {
		t.Fatalf("first ProcessOnce() = %t, %v", processed, err)
	}
	test.clock.set(test.now.Add(time.Second))
	restartedWorker := newDraftWorker(t, test, provider)
	if processed, err := restartedWorker.ProcessOnce(test.ctx); err != nil || !processed {
		t.Fatalf("restarted ProcessOnce() = %t, %v", processed, err)
	}
	requests := provider.take(t, 2)
	wantKey := "agent-mail/" + draft.ID.String() + "/1"
	if requests[0].idempotencyKey != wantKey || requests[1].idempotencyKey != wantKey {
		t.Fatalf(
			"retry idempotency keys = %q, %q; want %q",
			requests[0].idempotencyKey,
			requests[1].idempotencyKey,
			wantKey,
		)
	}
	groups, err := service.ListGroups(
		test.ctx, test.ownerA, test.fixture.ConversationA, []uuid.UUID{test.fixture.RunA},
	)
	if err != nil || groups[0].Drafts[0].Status != mailbox.DraftStatusSent {
		t.Fatalf("retried group = %#v, %v", groups, err)
	}
}

func TestAgentMailWorkerDoesNotSendAfterIdempotencyWindowExpires(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "agent-mail")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	provider := newFakeOutboundProvider()
	service := newDraftService(t, test)
	group, err := service.Create(test.ctx, test.ownerA, mailbox.TrustedDraftContext{
		CompanyID: test.fixture.CompanyA, ConversationID: test.fixture.ConversationA,
		RunID: test.fixture.RunA, AgentID: test.fixture.AgentA, RequestKey: uuid.Must(uuid.NewV7()),
	}, []mailbox.DraftInput{{
		Subject: "Expired retry", Text: "Body",
		Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"retry@example.com"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	draft := group.Drafts[0]
	if _, err := service.QueueDraft(
		test.ctx, test.ownerA, test.fixture.ConversationA, draft.ID, draft.Revision,
	); err != nil {
		t.Fatalf("QueueDraft() error = %v", err)
	}

	test.clock.set(test.now.Add(24 * time.Hour))
	worker := newDraftWorker(t, test, provider)
	if processed, err := worker.ProcessOnce(test.ctx); err != nil || !processed {
		t.Fatalf("ProcessOnce() = %t, %v", processed, err)
	}
	provider.take(t, 0)
	groups, err := service.ListGroups(
		test.ctx, test.ownerA, test.fixture.ConversationA, []uuid.UUID{test.fixture.RunA},
	)
	if err != nil || len(groups) != 1 || len(groups[0].Drafts) != 1 {
		t.Fatalf("ListGroups() = %#v, %v", groups, err)
	}
	failed := groups[0].Drafts[0]
	if failed.Status != mailbox.DraftStatusFailed || failed.FailureReason == nil ||
		*failed.FailureReason != "send_expired" {
		t.Fatalf("expired draft = %#v", failed)
	}
}

func newDraftService(t *testing.T, test *mailboxTest) *mailbox.DefaultDraftService {
	t.Helper()
	service, err := mailbox.NewDraftService(mailbox.DraftServiceOptions{
		Store: test.store, Bus: test.bus, Domain: mailboxDomain, Now: test.clock.read,
	})
	if err != nil {
		t.Fatalf("mailbox.NewDraftService() error = %v", err)
	}
	return service
}

func newDraftWorker(t *testing.T, test *mailboxTest, provider email.OutboundProvider) *mailbox.DraftWorker {
	t.Helper()
	worker, err := mailbox.NewDraftWorker(mailbox.DraftWorkerOptions{
		Store: test.store, Provider: provider, Bus: test.bus, Now: test.clock.read,
		LeaseDuration: time.Minute, PollInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("mailbox.NewDraftWorker() error = %v", err)
	}
	return worker
}

func makeLiveDraftRun(t *testing.T, test *mailboxTest, runID uuid.UUID) {
	t.Helper()
	_, err := test.pool.Exec(test.ctx, `
		UPDATE runs
		SET status='running', prepare_lease_expires_at=NULL,
		    updated_at=clock_timestamp()
		WHERE id=$1`, runID)
	if err != nil {
		t.Fatalf("make Run live: %v", err)
	}
}

func cloneDraftRun(t *testing.T, test *mailboxTest) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	err := test.pool.QueryRow(test.ctx, `
		INSERT INTO runs (
			company_id, task_id, conversation_id, agent_id, runtime_id, run_type, revision, context
		)
		SELECT company_id, task_id, conversation_id, agent_id, runtime_id, run_type, revision, context
		FROM runs WHERE id=$1
		RETURNING id`, test.fixture.RunA).Scan(&runID)
	if err != nil {
		t.Fatalf("clone Run: %v", err)
	}
	return runID
}

func seedScheduledDraftRun(t *testing.T, test *mailboxTest) (uuid.UUID, uuid.UUID) {
	t.Helper()
	runID := cloneDraftRun(t, test)
	scheduleID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	firingID := uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	tx, err := test.pool.Begin(test.ctx)
	if err != nil {
		t.Fatalf("begin Schedule fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(test.ctx, `
		INSERT INTO schedules (
			id, company_id, conversation_id, creator_user_id, request_key, agent_id,
			reviewed_provider, name, instructions, trigger_type, run_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			'codex', 'Scheduled mail', 'Draft mail', 'once', clock_timestamp()
		)`, scheduleID, test.fixture.CompanyA, conversationID, test.fixture.UserA,
		uuid.Must(uuid.NewV7()), test.fixture.AgentA); err != nil {
		t.Fatalf("insert Schedule fixture: %v", err)
	}
	if _, err := tx.Exec(test.ctx, `
		INSERT INTO conversations (
			id, company_id, kind, schedule_id, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'schedule', $3, $4, $5, 'Scheduled mail')`,
		conversationID, test.fixture.CompanyA, scheduleID,
		test.fixture.AgentA, test.fixture.UserA); err != nil {
		t.Fatalf("insert Schedule Conversation fixture: %v", err)
	}
	if _, err := tx.Exec(test.ctx, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type,
			author_kind, author_id, content
		) VALUES ($1, $2, $3, $4, 1, 'user_message', 'user', $5, 'Draft mail')`,
		messageID, test.fixture.CompanyA, conversationID, runID,
		test.fixture.UserA); err != nil {
		t.Fatalf("insert Schedule Message fixture: %v", err)
	}
	if _, err := tx.Exec(test.ctx, `
		INSERT INTO schedule_firings (
			id, company_id, schedule_id, schedule_version, conversation_id,
			message_id, run_id, occurrence_at, trigger, disposition
		) VALUES (
			$1, $2, $3, 1, $4, $5, $6,
			clock_timestamp(), 'scheduled', 'materialized'
		)`, firingID, test.fixture.CompanyA, scheduleID, conversationID,
		messageID, runID); err != nil {
		t.Fatalf("insert Schedule firing fixture: %v", err)
	}
	if _, err := tx.Exec(test.ctx, `
		UPDATE runs
		SET task_id=NULL, conversation_id=$2,
			schedule_firing_id=$3,
			connector_selection_mode='exact',
			updated_at=clock_timestamp()
		WHERE id=$1`, runID, conversationID, firingID); err != nil {
		t.Fatalf("make Run scheduled: %v", err)
	}
	if err := tx.Commit(test.ctx); err != nil {
		t.Fatalf("commit Schedule fixture: %v", err)
	}
	return runID, conversationID
}

func finishDraftRun(t *testing.T, test *mailboxTest, runID uuid.UUID) {
	t.Helper()
	if _, err := test.pool.Exec(test.ctx, `
		UPDATE runs
		SET status='completed', updated_at=clock_timestamp()
		WHERE id=$1`, runID); err != nil {
		t.Fatalf("finish Run: %v", err)
	}
}

func updatedDeliveryInputs(draft mailbox.Draft) []mailbox.DraftDeliveryInput {
	result := make([]mailbox.DraftDeliveryInput, len(draft.Deliveries))
	for index, delivery := range draft.Deliveries {
		result[index] = mailbox.DraftDeliveryInput{To: delivery.To, CC: delivery.CC}
	}
	return result
}

func quotaDeliveries(count, suffix int) []mailbox.DraftDeliveryInput {
	deliveries := make([]mailbox.DraftDeliveryInput, count)
	for index := range deliveries {
		deliveries[index] = mailbox.DraftDeliveryInput{To: []string{
			"lead-" + string(rune('a'+suffix)) + "-" + string(rune('a'+index%26)) +
				string(rune('a'+index/26)) + "@example.com",
		}}
	}
	return deliveries
}

type outboundRequest struct {
	messages       []email.OutboundMessage
	idempotencyKey string
}

type fakeOutboundProvider struct {
	mu               sync.Mutex
	requests         []outboundRequest
	err              error
	failures         []error
	singleDeliveryID string
}

func newFakeOutboundProvider() *fakeOutboundProvider {
	return &fakeOutboundProvider{singleDeliveryID: "single-provider-id"}
}

func (p *fakeOutboundProvider) Send(
	_ context.Context,
	message email.OutboundMessage,
	idempotencyKey string,
) (email.Delivery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, outboundRequest{[]email.OutboundMessage{message}, idempotencyKey})
	if err := p.nextFailure(); err != nil {
		return email.Delivery{}, err
	}
	return email.Delivery{ID: p.singleDeliveryID}, nil
}

func (p *fakeOutboundProvider) SendBatch(
	_ context.Context,
	messages []email.OutboundMessage,
	idempotencyKey string,
) ([]email.Delivery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(
		p.requests,
		outboundRequest{append([]email.OutboundMessage(nil), messages...), idempotencyKey},
	)
	if err := p.nextFailure(); err != nil {
		return nil, err
	}
	deliveries := make([]email.Delivery, len(messages))
	for index := range deliveries {
		deliveries[index] = email.Delivery{ID: "batch-provider-" + string(rune('a'+index))}
	}
	return deliveries, nil
}

func (p *fakeOutboundProvider) nextFailure() error {
	if len(p.failures) == 0 {
		return p.err
	}
	err := p.failures[0]
	p.failures = p.failures[1:]
	return err
}

func (p *fakeOutboundProvider) setFailures(failures ...error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = append([]error(nil), failures...)
}

func (p *fakeOutboundProvider) setDeliveryID(deliveryID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.singleDeliveryID = deliveryID
}

func (p *fakeOutboundProvider) take(t *testing.T, want int) []outboundRequest {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != want {
		t.Fatalf("outbound requests = %d, want %d", len(p.requests), want)
	}
	return append([]outboundRequest(nil), p.requests...)
}

func TestAgentMailRequiresOwnerIncludingStaleRunPrincipal(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "restricted-drafts")
	makeLiveDraftRun(t, test, test.fixture.RunA)
	service := newDraftService(t, test)
	trusted := mailbox.TrustedDraftContext{
		CompanyID:      test.fixture.CompanyA,
		ConversationID: test.fixture.ConversationA,
		RunID:          test.fixture.RunA,
		AgentID:        test.fixture.AgentA,
		RequestKey:     uuid.New(),
	}
	input := []mailbox.DraftInput{
		{
			Subject:    "Confidential",
			Text:       "Private body",
			Deliveries: []mailbox.DraftDeliveryInput{{To: []string{"owner@example.com"}}},
		},
	}
	group, err := service.Create(test.ctx, test.ownerA, trusted, input)
	if err != nil {
		t.Fatal(err)
	}
	draft := group.Drafts[0]
	changes := test.mailboxEvents.take(t, 2)
	payload, err := events.PayloadOf[mailbox.DraftChangedPayload](changes[1])
	if err != nil || payload.Audience == nil || payload.Audience.CompanyWide ||
		!slices.Equal(payload.Audience.UserIDs, []uuid.UUID{test.ownerA.UserID}) {
		t.Fatalf("Agent Mail audience = %+v, %v, want owner alone", payload.Audience, err)
	}
	for _, role := range []auth.CompanyRole{auth.CompanyRoleAdmin, auth.CompanyRoleMember} {
		dbtest.MustExec(
			t,
			test.ctx,
			test.pool,
			`UPDATE company_members SET role=$3 WHERE company_id=$1 AND user_id=$2`,
			test.fixture.CompanyA,
			test.fixture.UserA,
			string(role),
		)
		for _, stale := range []bool{false, true} {
			principal := test.ownerA
			principal.AuthKind = auth.TokenKindBearer
			if !stale {
				principal.Role = role
			}
			for name, call := range map[string]func() error{
				"create": func() error { _, err := service.Create(test.ctx, principal, trusted, input); return err },
				"list": func() error {
					_, err := service.ListGroups(test.ctx, principal, trusted.ConversationID, []uuid.UUID{trusted.RunID})
					return err
				},
				"update": func() error {
					_, err := service.UpdateDraft(test.ctx, principal, trusted.ConversationID, draft.ID, mailbox.UpdateDraftInput{Subject: "Changed", Text: "Changed", Deliveries: input[0].Deliveries, ExpectedRevision: draft.Revision})
					return err
				},
				"delete": func() error {
					return service.DeleteDraft(test.ctx, principal, trusted.ConversationID, draft.ID, draft.Revision)
				},
				"send": func() error {
					_, err := service.QueueDraft(test.ctx, principal, trusted.ConversationID, draft.ID, draft.Revision)
					return err
				},
				"send group": func() error {
					_, err := service.QueueGroup(test.ctx, principal, trusted.ConversationID, group.ID, []mailbox.DraftRevision{{ID: draft.ID, Revision: draft.Revision}})
					return err
				},
			} {
				want := mailbox.ErrAgentMailForbidden
				if name == "list" && stale {
					want = mailbox.ErrAgentMailDraftNotFound
				}
				if err := call(); !errors.Is(err, want) {
					t.Errorf("%s(%s, stale=%t) error = %v, want %v", name, role, stale, err, want)
				}
			}
		}
	}
}
