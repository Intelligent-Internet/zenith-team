//go:build integration

package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type planActionParticipantStub struct {
	prepareCalls int
	executeCalls int
	executeErr   error
	// prepared and executed record the actor of the last call.
	prepared CompanyPlanActor
	executed CompanyPlanActor
}

func (s *planActionParticipantStub) LockCompanyPlanScope(
	context.Context,
	*generated.Queries,
	CompanyPlanActor,
) error {
	return nil
}

func (s *planActionParticipantStub) PrepareCompanyPlanApply(
	_ context.Context,
	_ *generated.Queries,
	actor CompanyPlanActor,
	_ json.RawMessage,
) (PreparedCompanyPlanAction, error) {
	s.prepareCalls++
	s.prepared = actor
	summary := "One authoritative change"
	return PreparedCompanyPlanAction{
		CanonicalAction: json.RawMessage(`{"artifactVersion":1}`),
		Presentation:    json.RawMessage(`{"kind":"company_plan","plan":{"tasks":[]}}`),
		PreviewMarkdown: "- Apply the authoritative Company Plan diff",
		Summary:         &summary,
	}, nil
}

func (s *planActionParticipantStub) ExecuteCompanyPlanApply(
	_ context.Context,
	_ *generated.Queries,
	_ *[]events.Event,
	actor CompanyPlanActor,
	_ json.RawMessage,
) (CompanyPlanActionResult, error) {
	s.executeCalls++
	s.executed = actor
	if s.executeErr != nil {
		return CompanyPlanActionResult{}, s.executeErr
	}
	reason := "the Company Plan base changed after the preview"
	return CompanyPlanActionResult{
		Result:        json.RawMessage(`{"created":0,"updated":0,"deleted":0}`),
		SkippedReason: &reason,
	}, nil
}

func TestApprovalRaiseReplayAndResolutionAreTransactional(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'owner')`, fixture.CompanyA, fixture.UserB)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	participant := &planActionParticipantStub{}
	requested := recordTopic[Approval](t, bus, Topic)
	notifications := recordTopic[inbox.ToastHint](t, bus, inbox.TopicNotification)
	service, err := NewApprovalService(
		store,
		bus,
		participant,
		inbox.NewRealtimePresenter(bus),
	)
	if err != nil {
		t.Fatalf("NewApprovalService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	requestKey := uuid.Must(uuid.NewV7())
	payload := json.RawMessage(`{"artifactVersion":1,"desired":{"tasks":[]}}`)

	first, firstCreated, err := service.RequestCompanyPlanApply(ctx, principal, origin, requestKey, payload)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	if !firstCreated || first.ID == uuid.Nil || first.Status != ApprovalStatusPending {
		t.Fatalf("first request = %+v created=%t", first, firstCreated)
	}
	var compactPresentation bytes.Buffer
	if err := json.Compact(&compactPresentation, first.Presentation); err != nil {
		t.Fatalf("compact first Presentation: %v", err)
	}
	if compactPresentation.String() != `{"kind":"company_plan","plan":{"tasks":[]}}` {
		t.Fatalf("first Presentation = %s", first.Presentation)
	}
	requestedEvents := requested.named(EventApprovalRequested)
	if len(requestedEvents) != 1 || requestedEvents[0].Payload.ID != first.ID {
		t.Fatalf("Approval requested events = %+v", requestedEvents)
	}
	// The frame carries no audience: a direct Approval routes to its origin
	// user, and the bridge reads that off these two payload fields.
	requestedSnapshot := requestedEvents[0].Payload
	if requestedSnapshot.TaskID != nil || requestedSnapshot.CompanyID != fixture.CompanyA ||
		requestedSnapshot.OriginUserID != fixture.UserA {
		t.Fatalf("Approval requested routing = %+v", requestedSnapshot)
	}
	if requestedEvents[0].Actor != events.AgentActor(origin.AgentID.String()) {
		t.Fatalf("Approval requested actor = %#v, want the raising agent", requestedEvents[0].Actor)
	}
	issued := notifications.named(inbox.EventNotificationIssued)
	if len(issued) != 1 || issued[0].Payload.Type != inbox.TypeApprovalRequest ||
		issued[0].Payload.NotificationID == uuid.Nil || issued[0].Payload.DeepLink == nil ||
		*issued[0].Payload.DeepLink != "/app?agentId="+origin.AgentID.String()+"&conversationId="+origin.ConversationID.String()+"&conversationKind=direct&approvalId="+first.ID.String() {
		t.Fatalf("Approval notification hints = %+v", issued)
	}
	otherOwner := auth.UserPrincipal{
		UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	otherPage, err := service.List(ctx, otherOwner, ApprovalListOptions{})
	if err != nil {
		t.Fatalf("List(other owner) error = %v", err)
	}
	if len(otherPage.Items) != 0 {
		t.Fatalf("direct Approval visible to other owner = %d, want 0", len(otherPage.Items))
	}
	if _, err := service.Get(ctx, otherOwner, first.ID); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("Get(other owner) error = %v, want ErrApprovalNotFound", err)
	}
	if _, err := service.Approve(ctx, otherOwner, first.ID, ApprovalDecisionInput{
		ExpectedRevision: first.Revision,
	}); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("Approve(other owner) error = %v, want ErrApprovalNotFound", err)
	}
	replay, replayCreated, err := service.RequestCompanyPlanApply(ctx, principal, origin, requestKey, payload)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply(replay) error = %v", err)
	}
	if replayCreated || replay.ID != first.ID || participant.prepareCalls != 1 {
		t.Fatalf("replay = %+v; prepare calls = %d", replay, participant.prepareCalls)
	}
	if issued = notifications.named(inbox.EventNotificationIssued); len(issued) != 1 {
		t.Fatalf("Approval notification hints after replay = %+v, want one", issued)
	}
	if _, _, err := service.RequestCompanyPlanApply(
		ctx, principal, origin, requestKey, json.RawMessage(`{"artifactVersion":2}`),
	); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("different replay error = %v, want ErrApprovalConflict", err)
	}

	resolved, err := service.Approve(ctx, principal, first.ID, ApprovalDecisionInput{
		ExpectedRevision: first.Revision,
	})
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	// The decision opens a continuation receipt; it does not name a Run. The
	// id appears only when the reconciler inserts one.
	if resolved.Status != ApprovalStatusApproved || resolved.Execution == nil ||
		resolved.Execution.Status != ApprovalExecutionSkipped || resolved.ContinuationRunID != nil ||
		participant.executeCalls != 1 {
		t.Fatalf("resolved Approval = %+v; execute calls = %d", resolved, participant.executeCalls)
	}
	var openInbox int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_items
		WHERE company_id=$1 AND entity_type='approval' AND entity_id=$2
		  AND archived_at IS NULL`, fixture.CompanyA, first.ID.String()).Scan(&openInbox); err != nil {
		t.Fatalf("count open Approval Inbox items: %v", err)
	}
	if openInbox != 0 {
		t.Fatalf("open Approval Inbox items = %d, want 0", openInbox)
	}

	secondKey := uuid.Must(uuid.NewV7())
	second, _, err := service.RequestCompanyPlanApply(ctx, principal, origin, secondKey, payload)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply(second) error = %v", err)
	}
	participant.executeErr = errors.New("storage unavailable")
	if _, err := service.Approve(ctx, principal, second.ID, ApprovalDecisionInput{
		ExpectedRevision: second.Revision,
	}); err == nil || errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("Approve(infrastructure failure) error = %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM approvals WHERE id=$1`, second.ID).
		Scan(&status); err != nil {
		t.Fatalf("read rolled-back Approval: %v", err)
	}
	if status != "pending" {
		t.Fatalf("rolled-back Approval status = %q, want pending", status)
	}
}

func TestProactiveCompanyPlanApprovalIsResolvedAtomically(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE companies
		SET proactive_mode_enabled = true,
		    proactive_mode_enabled_by_user_id = $2,
		    proactive_mode_conversation_id = $3
		WHERE id = $1`, fixture.CompanyA, fixture.UserA, fixture.DirectConversationA)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	participant := &planActionParticipantStub{}
	changes := recordTopic[Approval](t, bus, Topic)
	service, err := NewApprovalService(store, bus, participant, nil)
	if err != nil {
		t.Fatalf("NewApprovalService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	requestKey := uuid.Must(uuid.NewV7())
	payload := json.RawMessage(`{"artifactVersion":1,"desired":{"tasks":[]}}`)

	first, created, err := service.RequestCompanyPlanApply(
		ctx,
		principal,
		origin,
		requestKey,
		payload,
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	if !created || first.Status != ApprovalStatusApproved || first.Execution == nil ||
		first.Execution.Status != ApprovalExecutionSkipped || first.ContinuationRunID != nil {
		t.Fatalf("automatic Approval = %+v, created = %t", first, created)
	}
	if participant.prepareCalls != 1 || participant.executeCalls != 1 {
		t.Fatalf(
			"participant calls = prepare %d, execute %d; want 1 each",
			participant.prepareCalls,
			participant.executeCalls,
		)
	}
	// The Run resolves the plan it raised, for its member: nobody else
	// decided it.
	resolvedEvents := changes.named(EventApprovalResolved)
	if len(resolvedEvents) != 1 || resolvedEvents[0].Payload.ID != first.ID ||
		resolvedEvents[0].Actor != origin.Actor() {
		t.Fatalf("automatic Approval events = %+v", resolvedEvents)
	}
	if requestedEvents := changes.named(EventApprovalRequested); len(requestedEvents) != 0 {
		t.Fatalf("automatic Approval requested events = %+v, want none", requestedEvents)
	}

	var continuationStatus string
	var openInbox int
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status,
		       (SELECT count(*) FROM inbox_items
		        WHERE company_id = approvals.company_id
		          AND entity_type = 'approval'
		          AND entity_id = approvals.id::text
		          AND archived_at IS NULL)
		FROM approvals
		WHERE id = $1`, first.ID).Scan(&continuationStatus, &openInbox); err != nil {
		t.Fatalf("read automatic Approval receipt: %v", err)
	}
	if continuationStatus != "not_required" || openInbox != 0 {
		t.Fatalf(
			"automatic Approval receipt = continuation %q, open Inbox %d",
			continuationStatus,
			openInbox,
		)
	}

	replay, replayCreated, err := service.RequestCompanyPlanApply(
		ctx,
		principal,
		origin,
		requestKey,
		payload,
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply(replay) error = %v", err)
	}
	if replayCreated || replay.ID != first.ID || replay.Status != ApprovalStatusApproved ||
		participant.prepareCalls != 1 || participant.executeCalls != 1 {
		t.Fatalf(
			"automatic replay = %+v, created = %t, calls = prepare %d execute %d",
			replay,
			replayCreated,
			participant.prepareCalls,
			participant.executeCalls,
		)
	}

	participant.executeErr = errors.New("storage unavailable")
	failedKey := uuid.Must(uuid.NewV7())
	if _, _, err := service.RequestCompanyPlanApply(
		ctx,
		principal,
		origin,
		failedKey,
		payload,
	); err == nil {
		t.Fatal("RequestCompanyPlanApply(execution failure) error = nil")
	}
	var failedReceiptCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM approvals
		WHERE origin_run_id = $1 AND request_key = $2`, origin.RunID, failedKey).
		Scan(&failedReceiptCount); err != nil {
		t.Fatalf("count rolled-back proactive Approval: %v", err)
	}
	if failedReceiptCount != 0 {
		t.Fatalf("rolled-back proactive Approval count = %d, want 0", failedReceiptCount)
	}
}

func TestProactiveCompanyPlanApprovalFailsClosedWhenAuthorityDrifts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	otherConversationID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'owner')`, fixture.CompanyA, fixture.UserB)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'direct', $3, $4, 'Other owner direct')`,
		otherConversationID, fixture.CompanyA, fixture.AgentA, fixture.UserB)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE companies
		SET proactive_mode_enabled = true,
		    proactive_mode_enabled_by_user_id = $2,
		    proactive_mode_conversation_id = $3
		WHERE id = $1`, fixture.CompanyA, fixture.UserB, otherConversationID)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	participant := &planActionParticipantStub{}
	service, err := NewApprovalService(store, bus, participant, nil)
	if err != nil {
		t.Fatalf("NewApprovalService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)

	inexact, created, err := service.RequestCompanyPlanApply(
		ctx,
		principal,
		origin,
		uuid.Must(uuid.NewV7()),
		json.RawMessage(`{"artifactVersion":1}`),
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	if !created || inexact.Status != ApprovalStatusPending || participant.executeCalls != 0 {
		t.Fatalf(
			"inexact-conversation Approval = %+v, created = %t, execute calls = %d",
			inexact,
			created,
			participant.executeCalls,
		)
	}

	dbtest.MustExec(t, ctx, pool, `
		UPDATE companies
		SET proactive_mode_enabled_by_user_id = $2,
		    proactive_mode_conversation_id = $3
		WHERE id = $1`, fixture.CompanyA, fixture.UserA, fixture.DirectConversationA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET archived_at = statement_timestamp() WHERE id = $1`, fixture.AgentA)
	archived, created, err := service.RequestCompanyPlanApply(
		ctx,
		principal,
		origin,
		uuid.Must(uuid.NewV7()),
		json.RawMessage(`{"artifactVersion":1}`),
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply(archived root) error = %v", err)
	}
	if !created || archived.Status != ApprovalStatusPending || participant.executeCalls != 0 {
		t.Fatalf(
			"archived-root Approval = %+v, created = %t, execute calls = %d",
			archived,
			created,
			participant.executeCalls,
		)
	}
	var openInbox int
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM inbox_items
		WHERE company_id = $1 AND entity_type = 'approval'
		  AND entity_id = ANY($2::text[]) AND archived_at IS NULL`,
		fixture.CompanyA,
		[]string{inexact.ID.String(), archived.ID.String()},
	).Scan(&openInbox); err != nil {
		t.Fatalf("count fail-closed Approval Inbox items: %v", err)
	}
	if openInbox != 2 {
		t.Fatalf("fail-closed Approval Inbox items = %d, want 2", openInbox)
	}
}

func TestQuestionRaiseReplayAnswerAndDirectVisibility(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	notifications := recordTopic[inbox.ToastHint](t, bus, inbox.TopicNotification)
	service, err := NewQuestionService(store, bus, inbox.NewRealtimePresenter(bus))
	if err != nil {
		t.Fatalf("NewQuestionService() error = %v", err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	asked := recordTopic[Question](t, bus, TopicQuestion)
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	requestKey := uuid.Must(uuid.NewV7())
	input := QuestionAskInput{Title: "Choose a target", Prompts: []QuestionPrompt{{
		ID: "target", Prompt: "Which target?", SelectionMode: QuestionSelectionSingle,
		Required: true, Options: []QuestionOption{{ID: "a", Label: "Target A"}, {ID: "b", Label: "Target B"}},
	}}}

	first, _, err := service.Ask(ctx, principal, origin, requestKey, input)
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	askedEvents := asked.named(EventQuestionAsked)
	if len(askedEvents) != 1 || askedEvents[0].Payload.ID != first.ID {
		t.Fatalf("Question asked events = %+v", askedEvents)
	}
	// A direct Question routes to its origin user, off the payload.
	askedSnapshot := askedEvents[0].Payload
	if askedSnapshot.TaskID != nil || askedSnapshot.CompanyID != fixture.CompanyA ||
		askedSnapshot.OriginUserID != fixture.UserA {
		t.Fatalf("Question asked routing = %+v", askedSnapshot)
	}
	issued := notifications.named(inbox.EventNotificationIssued)
	if len(issued) != 1 || issued[0].Payload.Type != inbox.TypeQuestionRequest ||
		issued[0].Payload.NotificationID == uuid.Nil || issued[0].Payload.DeepLink == nil ||
		*issued[0].Payload.DeepLink != "/app?agentId="+origin.AgentID.String()+"&conversationId="+origin.ConversationID.String()+"&conversationKind=direct&questionId="+first.ID.String() {
		t.Fatalf("Question notification hints = %+v", issued)
	}
	replay, replayCreated, err := service.Ask(ctx, principal, origin, requestKey, input)
	if err != nil || replayCreated || replay.ID != first.ID {
		t.Fatalf("Ask(replay) = %+v, %v", replay, err)
	}
	if issued = notifications.named(inbox.EventNotificationIssued); len(issued) != 1 {
		t.Fatalf("Question notification hints after replay = %+v, want one", issued)
	}
	other := auth.UserPrincipal{UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	page, err := service.List(ctx, other, QuestionListOptions{Limit: 20})
	if err != nil {
		t.Fatalf("List(other member) error = %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("direct Questions visible to other member = %d, want 0", len(page.Items))
	}
	// Get follows the same visibility rule as List: the origin user reads
	// their private Question, another member gets not-found, and so does an
	// id nobody owns.
	got, err := service.Get(ctx, principal, first.ID)
	if err != nil || got.ID != first.ID || got.Title != "Choose a target" || got.Revision != first.Revision ||
		got.AskedByAgent.ID != origin.AgentID || len(got.Prompts) != 1 {
		t.Fatalf("Get(origin user) = %+v, %v", got, err)
	}
	if _, err := service.Get(ctx, other, first.ID); !errors.Is(err, ErrQuestionNotFound) {
		t.Fatalf("Get(other member) error = %v, want ErrQuestionNotFound", err)
	}
	if _, err := service.Get(ctx, principal, uuid.Must(uuid.NewV7())); !errors.Is(err, ErrQuestionNotFound) {
		t.Fatalf("Get(unknown id) error = %v, want ErrQuestionNotFound", err)
	}
	if _, err := service.Answer(ctx, other, first.ID, QuestionAnswerInput{
		ExpectedRevision: first.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"a"}}},
	}); !errors.Is(err, ErrQuestionNotFound) {
		t.Fatalf("Answer(other member) error = %v, want ErrQuestionNotFound", err)
	}
	answered, err := service.Answer(ctx, principal, first.ID, QuestionAnswerInput{
		ExpectedRevision: first.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"a"}}},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if answered.Status != QuestionStatusAnswered || answered.ContinuationRunID != nil ||
		len(answered.Answers) != 1 {
		t.Fatalf("answered Question = %+v", answered)
	}
}

func seedDirectRunningOrigin(
	t *testing.T,
	ctx context.Context,
	pool interface {
		Begin(context.Context) (pgx.Tx, error)
	},
	fixture dbtest.Fixture,
) execution.RunOrigin {
	t.Helper()
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin direct Run seed: %v", err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	// The id comes back from runs_id_seq rather than being minted: runs.id is a
	// bigserial, and seeding one by hand would leave the sequence behind it —
	// the next INSERT, continuation included, would then collide.
	var runID uuid.UUID
	if err := transaction.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status
		, run_type) VALUES ($1, $2, $3, $4, $5, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA, fixture.RuntimeA, fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("insert direct running Run: %v", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatalf("commit direct running Run: %v", err)
	}
	return execution.RunOrigin{
		RunID: runID, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		ConversationID: fixture.DirectConversationA, ConversationKind: "direct",
		OriginUserID: &fixture.UserA,
	}
}

func seedTaskRunningOrigin(
	t *testing.T,
	ctx context.Context,
	pool interface {
		Begin(context.Context) (pgx.Tx, error)
	},
	fixture dbtest.Fixture,
) execution.RunOrigin {
	t.Helper()
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin task Run seed: %v", err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	// The Run's lane comes from the conversation it names: ConversationA is
	// kind 'task', which is what makes this a task-lane origin.
	var runID uuid.UUID
	if err := transaction.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id, execution_authorized_by_user_id, status
		, run_type) VALUES ($1, $2, $3, $4, $5, $6, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA,
		fixture.AgentA,
		fixture.TaskA,
		fixture.ConversationA,
		fixture.RuntimeA,
		fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("insert task running Run: %v", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatalf("commit task running Run: %v", err)
	}
	return execution.RunOrigin{
		RunID: runID, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA, ConversationKind: "task",
	}
}

// recorded is one event as a subscriber saw it, with its payload already
// re-typed to what the topic carries.
type recorded[P any] struct {
	Name    string
	Actor   events.Actor
	Payload P
}

// topicLog collects what a subscriber receives. A event event is opaque by
// design, so the only place to read one is where everything else reads it: at
// a subscriber, after publication.
type topicLog[P any] struct {
	mu     sync.Mutex
	events []recorded[P]
}

func recordTopic[P any](t *testing.T, bus *events.Bus, topic string) *topicLog[P] {
	t.Helper()
	log := &topicLog[P]{}
	if err := bus.Subscribe(topic,
		func(_ context.Context, event events.Event) error {
			payload, err := events.PayloadOf[P](event)
			if err != nil {
				return err
			}
			log.mu.Lock()
			defer log.mu.Unlock()
			log.events = append(log.events, recorded[P]{
				Name: event.Name, Actor: event.Actor, Payload: payload,
			})
			return nil
		}); err != nil {
		t.Fatalf("subscribe to %s: %v", topic, err)
	}
	return log
}

// named is the events of one member of the family, in arrival order: a
// subscriber receives the whole family, so a test asserting on one event
// filters on Event.Name the same way.
func (l *topicLog[P]) named(name string) []recorded[P] {
	l.mu.Lock()
	defer l.mu.Unlock()
	var matched []recorded[P]
	for _, event := range l.events {
		if event.Name == name {
			matched = append(matched, event)
		}
	}
	return matched
}
