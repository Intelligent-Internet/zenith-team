//go:build integration

package conversation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestHuddleWorkNoticeAuthorityAndRecovery(t *testing.T) {
	f := newDiscussionFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":false,"participation":"addressed"}') WHERE id=$1`,
		f.seed.AgentA,
	)
	budget, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000},
	); err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := NewHuddleAIService(HuddleAIServiceOptions{
		Store:                 f.store,
		Enabled:               true,
		TicksPerSecond:        100,
		ReasoningReserveTicks: 1000,
		ReadContext: func(context.Context, *generated.Queries, uuid.UUID, voice.ContextRequest) (voice.ContextResult, error) {
			return voice.ContextResult{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := huddles.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := huddles.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = huddles.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.SetEnabled(
		f.ctx,
		f.a,
		started.Huddle.ID,
		SetHuddleAIInput{
			Enabled:             true,
			AllowCompanyContext: true,
			AgentID:             f.seed.AgentA,
			ExpectedRevision:    1,
			RequestKey:          uuid.New(),
		},
	); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_states SET retry_at=now()-interval '1 second' WHERE huddle_id=$1`,
		started.Huddle.ID,
	)
	claim := func() (uuid.UUID, HuddleAILease) {
		t.Helper()
		if err := ai.Reconcile(f.ctx); err != nil {
			t.Fatal(err)
		}
		status, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
		if err != nil || status.AttemptID == nil {
			t.Fatalf("status = %+v, %v", status, err)
		}
		lease, err := ai.Claim(f.ctx, *status.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		return *status.AttemptID, lease
	}
	attempt, lease := claim()
	draft, err := ai.ProposeWork(
		f.ctx,
		attempt,
		lease.Capability,
		HuddleProposalInput{
			RequestKey: "notice-draft",
			ProjectID:  f.seed.ProjectA,
			Title:      "Work notice",
			Brief:      "Reviewed shared work",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_work_proposals SET state='submitted',submitted_task_id=$2,submitted_task_key='TASK-1',submitted_run_id=$3 WHERE id=$1`,
		draft.ID,
		f.seed.TaskA,
		f.seed.RunA,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE runs SET status='completed',task_execution_generation=0 WHERE id=$1`,
		f.seed.RunA,
	)
	heartbeat := func(input HuddleAIHeartbeat) HuddleAILease {
		t.Helper()
		result, err := ai.Heartbeat(f.ctx, attempt, lease.Capability, input)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := heartbeat(HuddleAIHeartbeat{PollWork: true}); len(got.WorkNotices) != 0 {
		t.Fatalf("unproven result = %+v", got.WorkNotices)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,item_key,content) VALUES($1,$2,$3,2,'agent_text','agent',$4,'notice-final','Do not expose this result in proactive notices')`,
		f.a.CompanyID,
		f.seed.ConversationA,
		f.seed.RunA,
		f.seed.AgentA,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE runs SET final_answer_item_key='notice-final' WHERE id=$1`,
		f.seed.RunA,
	)
	offer := heartbeat(HuddleAIHeartbeat{PollWork: true})
	if len(offer.WorkNotices) != 1 || offer.WorkNotices[0].State != "completed" {
		t.Fatalf("offer = %+v", offer.WorkNotices)
	}
	key := offer.WorkNotices[0].ID
	// Notices are spoken with the access of the person who shared Company
	// context: while A cannot read ProjectA, its work is not offered.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`DELETE FROM project_grants WHERE company_id=$1 AND project_id=$2 AND user_id=$3`,
		f.a.CompanyID,
		f.seed.ProjectA,
		f.a.UserID,
	)
	if got := heartbeat(HuddleAIHeartbeat{PollWork: true}); len(got.WorkNotices) != 0 {
		t.Fatalf("notice for a sharer without the Project = %+v", got.WorkNotices)
	}
	dbtest.GrantProject(t, f.ctx, f.pool, f.a.CompanyID, f.seed.ProjectA, f.a.UserID)
	if got := heartbeat(HuddleAIHeartbeat{ClaimWorkNotice: "foreign-or-stale"}); got.ClaimedWorkNotice != nil {
		t.Fatalf("foreign claim = %+v", got)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_states SET allow_company_context=false WHERE huddle_id=$1`,
		started.Huddle.ID,
	)
	if got := heartbeat(
		HuddleAIHeartbeat{PollWork: true, ClaimWorkNotice: key},
	); len(got.WorkNotices) != 0 ||
		got.ClaimedWorkNotice != nil {
		t.Fatal("context-disabled notice leaked")
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_states SET allow_company_context=true WHERE huddle_id=$1`,
		started.Huddle.ID,
	)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='running' WHERE id=$1`, f.seed.RunA)
	if got := heartbeat(HuddleAIHeartbeat{ClaimWorkNotice: key}); got.ClaimedWorkNotice != nil {
		t.Fatal("stale completion claimed")
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='completed' WHERE id=$1`, f.seed.RunA)
	var results [2]HuddleAILease
	var claimErrors [2]error
	var done sync.WaitGroup
	for i := range results {
		done.Go(func() {
			results[i], claimErrors[i] = ai.Heartbeat(
				f.ctx,
				attempt,
				lease.Capability,
				HuddleAIHeartbeat{ClaimWorkNotice: key},
			)
		})
	}
	done.Wait()
	claims := 0
	for i, result := range results {
		if claimErrors[i] != nil {
			t.Fatal(claimErrors[i])
		}
		if result.ClaimedWorkNotice != nil {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("claims = %d, want 1", claims)
	}
	row, err := f.store.Queries().
		GetHuddleProposal(f.ctx, generated.GetHuddleProposalParams{CompanyID: f.a.CompanyID, HuddleID: started.Huddle.ID, ID: draft.ID})
	if err != nil || row.Revision != draft.Revision {
		t.Fatalf("bookkeeping changed review revision = %+v, %v", row, err)
	}
	// A fresh attempt in the same call cannot replay a claimed speech notification.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET state='finished',usage_final=true WHERE id=$1`,
		attempt,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_states SET retry_at=now()-interval '1 second' WHERE huddle_id=$1`,
		started.Huddle.ID,
	)
	attempt, lease = claim()
	if got := heartbeat(
		HuddleAIHeartbeat{PollWork: true, ClaimWorkNotice: key},
	); len(got.WorkNotices) != 0 ||
		got.ClaimedWorkNotice != nil {
		t.Fatal("recovery replayed notice")
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO questions(company_id,title,asked_by_agent_id,origin_run_id,request_key,task_id,conversation_id,prompts,request_sha256,expires_at) VALUES($1,'Private question',$2,$3,gen_random_uuid(),$4,$5,'[]','test',now()+interval '1 hour')`,
		f.a.CompanyID,
		f.seed.AgentA,
		f.seed.RunA,
		f.seed.TaskA,
		f.seed.ConversationA,
	)
	inputOffer := heartbeat(HuddleAIHeartbeat{PollWork: true})
	if len(inputOffer.WorkNotices) != 1 || inputOffer.WorkNotices[0].State != "waiting_for_input" {
		t.Fatalf("input notice = %+v", inputOffer.WorkNotices)
	}
	if _, err = ai.Heartbeat(
		f.ctx,
		attempt,
		"wrong",
		HuddleAIHeartbeat{PollWork: true},
	); !errors.Is(
		err,
		ErrHuddleForbidden,
	) {
		t.Fatalf("wrong capability = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_work_proposals SET voice_notice_keys=array_fill('prior'::text,ARRAY[128]) WHERE id=$1`,
		draft.ID,
	)
	if got := heartbeat(HuddleAIHeartbeat{PollWork: true}); len(got.WorkNotices) != 0 {
		t.Fatal("notice cap ignored")
	}
	count, err := f.store.Queries().
		ClaimHuddleWorkNotice(f.ctx, generated.ClaimHuddleWorkNoticeParams{CompanyID: f.a.CompanyID, HuddleID: started.Huddle.ID, ProposalID: draft.ID, NoticeKey: "over-cap"})
	if err != nil || count != 0 {
		t.Fatalf("capped write = %d, %v", count, err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_work_proposals SET voice_notice_keys='{}' WHERE id=$1`,
		draft.ID,
	)
	if _, err = huddles.Leave(f.ctx, f.a, started.Huddle.ID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	// The person who shared Company context left the call, so the
	// context-bearing lease ends at once: human grace no longer
	// keeps it renewable, and its pending notice is never consumed.
	if _, err = ai.Heartbeat(f.ctx, attempt, lease.Capability, HuddleAIHeartbeat{
		PollWork: true, ClaimWorkNotice: inputOffer.WorkNotices[0].ID,
	}); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("heartbeat after the context sharer left = %v, want %v", err, ErrHuddleUnavailable)
	}
	var consumed bool
	if err = f.pool.QueryRow(
		f.ctx, `SELECT $2=ANY(voice_notice_keys) FROM huddle_work_proposals WHERE id=$1`,
		draft.ID, inputOffer.WorkNotices[0].ID,
	).Scan(&consumed); err != nil || consumed {
		t.Fatalf("empty-room notice consumed = %v, %v", consumed, err)
	}
	if _, err = ai.Leave(f.ctx, attempt, lease.Capability); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.Heartbeat(
		f.ctx,
		attempt,
		lease.Capability,
		HuddleAIHeartbeat{PollWork: true},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("removed invitation = %v", err)
	}
}

func TestHuddleWorkReceiptLineageAndResults(t *testing.T) {
	f := newDiscussionFixture(t)
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := huddles.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	q := f.store.Queries()
	draft, err := q.CreateHuddleProposal(f.ctx, generated.CreateHuddleProposalParams{
		CompanyID:          f.a.CompanyID,
		HuddleID:           started.Huddle.ID,
		InvitationRevision: 1,
		CreatedByAgentID:   f.seed.AgentA,
		AgentName:          "Eli",
		ProjectID:          uuid.NullUUID{UUID: f.seed.ProjectA, Valid: true},
		ProjectTitle:       "Project",
		RequestKey:         "work",
		RequestHash:        []byte("work"),
		Title:              "Shared work",
		Brief:              "Reviewed work",
		ExpiresAt:          pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_work_proposals SET state='submitted',submitted_task_id=$2,submitted_task_key='task_a',submitted_run_id=$3 WHERE id=$1`,
		draft.ID,
		f.seed.TaskA,
		f.seed.RunA,
	)
	// Work progress follows its reader's own access, with no chat
	// audience: no reader, or one who cannot read ProjectA (B), gets the
	// receipt without its Task's progress; A reads ProjectA before anyone
	// has joined the call.
	workAs := func(reader uuid.UUID) generated.ReadHuddleWorkParams {
		return generated.ReadHuddleWorkParams{
			CompanyID: f.a.CompanyID, HuddleID: started.Huddle.ID, ReaderUserID: reader,
			ReadPermission:            string(auth.ProjectPermissionRead),
			CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
		}
	}
	for _, reader := range []uuid.UUID{uuid.Nil, f.b.UserID} {
		withheld, err := readHuddleWork(f.ctx, q, workAs(reader))
		if err != nil || len(withheld.Items) != 1 || withheld.Items[0].Work == nil ||
			withheld.Items[0].Work.TaskStatus != "" {
			t.Fatalf("work for reader %s = %+v, %v", reader, withheld, err)
		}
	}
	params := workAs(f.a.UserID)
	read := func() *voice.WorkProgress {
		t.Helper()
		result, err := readHuddleWork(f.ctx, q, params)
		if err != nil || len(result.Items) != 1 {
			t.Fatalf("work = %+v, %v", result, err)
		}
		return result.Items[0].Work
	}
	if got := read(); got.TaskStatus == "" {
		t.Fatalf("work for a reader of the Project = %+v", got)
	}
	foreignCompany, foreignHuddle, foreignProposal := params, params, params
	foreignCompany.CompanyID = f.seed.CompanyB
	foreignHuddle.HuddleID = uuid.New()
	foreignProposal.ProposalID = uuid.New()
	for _, scope := range []generated.ReadHuddleWorkParams{foreignCompany, foreignHuddle, foreignProposal} {
		result, err := readHuddleWork(f.ctx, q, scope)
		if err != nil || len(result.Items) != 0 {
			t.Fatalf("foreign scope = %+v, %v", result, err)
		}
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE runs SET status='queued',task_execution_generation=0 WHERE id=$1`,
		f.seed.RunA,
	)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runtimes SET status='offline' WHERE id=$1`, f.seed.RuntimeA)
	if got := read(); got.State != "waiting_for_runtime" {
		t.Fatalf("offline = %+v", got)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runtimes SET status='online' WHERE id=$1`, f.seed.RuntimeA)
	if got := read(); got.State != "queued" {
		t.Fatalf("online = %+v", got)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='failed' WHERE id=$1`, f.seed.RunA)
	newRun := func(parent *uuid.UUID) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := f.pool.QueryRow(f.ctx, `INSERT INTO runs(company_id,agent_id,task_id,conversation_id,runtime_id,status,run_type,revision,parent_run_id,task_execution_generation)
		VALUES($1,$2,$3,$4,$5,'completed','task',1,$6,0) RETURNING id`, f.a.CompanyID, f.seed.AgentA, f.seed.TaskA, f.seed.ConversationA, f.seed.RuntimeA, parent).
			Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	retry := newRun(&f.seed.RunA)
	_ = newRun(nil) // Newer unrelated work must not replace the receipt's result.
	if got := read(); got.RunID == nil || *got.RunID != retry || got.State != "completed" {
		t.Fatalf("retry = %+v", got)
	}
	var questionID uuid.UUID
	err = f.pool.QueryRow(f.ctx, `INSERT INTO questions(company_id,title,asked_by_agent_id,origin_run_id,request_key,task_id,conversation_id,prompts,request_sha256,expires_at)
	VALUES($1,'Clarify outcome',$2,$3,gen_random_uuid(),$4,$5,'[]','test',now()+interval '1 hour') RETURNING id`, f.a.CompanyID, f.seed.AgentA, retry, f.seed.TaskA, f.seed.ConversationA).
		Scan(&questionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); got.State != "waiting_for_input" {
		t.Fatalf("question = %+v", got)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE questions SET status='answered',continuation_status='pending' WHERE id=$1`,
		questionID,
	)
	if got := read(); got.State != "resuming" {
		t.Fatalf("resuming = %+v", got)
	}
	continued := newRun(nil)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE questions SET continuation_status='delivered',continuation_run_id=$2 WHERE id=$1`,
		questionID,
		continued,
	)
	if got := read(); got.RunID == nil || *got.RunID != continued {
		t.Fatalf("continuation = %+v", got)
	}
	var approvalID uuid.UUID
	err = f.pool.QueryRow(f.ctx, `INSERT INTO approvals(company_id,rule_key,title,preview_markdown,requested_by_agent_id,origin_run_id,request_key,task_id,conversation_id,canonical_action,request_sha256,action_sha256,expires_at)
	VALUES($1,'plan','Review plan','Review',$2,$3,gen_random_uuid(),$4,$5,'{}','test','test',now()+interval '1 hour') RETURNING id`, f.a.CompanyID, f.seed.AgentA, continued, f.seed.TaskA, f.seed.ConversationA).
		Scan(&approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); got.State != "waiting_for_input" {
		t.Fatalf("approval = %+v", got)
	}
	approvedRun := newRun(nil)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE approvals SET status='approved',continuation_status='delivered',continuation_run_id=$2 WHERE id=$1`,
		approvalID,
		approvedRun,
	)
	continued = approvedRun
	if got := read(); got.RunID == nil || *got.RunID != continued {
		t.Fatalf("approval continuation = %+v", got)
	}
	// An origin can ask several questions. An unanswered sibling remains visible
	// after another answer has already spawned a deeper continuation/retry chain.
	err = f.pool.QueryRow(f.ctx, `INSERT INTO questions(company_id,title,asked_by_agent_id,origin_run_id,request_key,task_id,conversation_id,prompts,request_sha256,expires_at)
	VALUES($1,'Second question',$2,$3,gen_random_uuid(),$4,$5,'[]','test',now()+interval '1 hour') RETURNING id`, f.a.CompanyID, f.seed.AgentA, retry, f.seed.TaskA, f.seed.ConversationA).
		Scan(&questionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); got.State != "waiting_for_input" {
		t.Fatalf("ancestor pending input = %+v", got)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE questions SET status='answered',continuation_status='pending' WHERE id=$1`,
		questionID,
	)
	if got := read(); got.State != "resuming" {
		t.Fatalf("ancestor pending continuation = %+v", got)
	}
	sibling := newRun(nil)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE questions SET continuation_status='delivered',continuation_run_id=$2 WHERE id=$1`,
		questionID,
		sibling,
	)
	continued = sibling
	if got := read(); got.RunID == nil || *got.RunID != sibling {
		t.Fatalf("newer shallow continuation = %+v", got)
	}
	// A completed sibling must not hide still-active work in the same receipt.
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='running' WHERE id=$1`, approvedRun)
	if got := read(); got.RunID == nil || *got.RunID != approvedRun || got.State != "running" {
		t.Fatalf("active sibling = %+v", got)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='completed' WHERE id=$1`, approvedRun)
	// Only the proven final answer is shared, not later commentary or reasoning.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,item_key,content)
	VALUES($1,$2,$3,2,'agent_text','agent',$4,'final',$5),($1,$2,$3,3,'agent_text','agent',$4,'commentary','PRIVATE COMMENTARY')`,
		f.a.CompanyID,
		f.seed.ConversationA,
		continued,
		f.seed.AgentA,
		strings.Repeat("界", 2001),
	)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET final_answer_item_key='final' WHERE id=$1`, continued)
	if got := read(); !got.HasResult || got.ResultText != "" {
		t.Fatalf("status leaked result = %+v", got)
	}
	params.IncludeResult = true
	if got := read(); len([]rune(got.ResultText)) != 2000 || !got.ResultTruncated ||
		strings.Contains(got.ResultText, "PRIVATE") {
		t.Fatalf("result = %+v", got)
	}
	if got := read(); got.ResultMessageID == nil || got.ResultConversationID == nil || got.ResultMessageSeq != 2 {
		t.Fatalf("result navigation = %+v", got)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE tasks SET execution_generation=1 WHERE id=$1`, f.seed.TaskA)
	if got := read(); !got.NewerTaskExecution || *got.RunID != continued {
		t.Fatalf("new work = %+v", got)
	}
	// Agent departure/call end does not cancel submitted work or erase its receipt.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddles SET state='ended',ended_at=now() WHERE id=$1`,
		started.Huddle.ID,
	)
	ai, err := NewHuddleAIService(HuddleAIServiceOptions{Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	status, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || len(status.Proposals) != 1 || status.Proposals[0].Work == nil ||
		status.Proposals[0].Work.State != "completed" {
		t.Fatalf("ended call = %+v, %v", status, err)
	}
	// The status a person reads binds that person: B, in the chat but
	// without ProjectA, sees the receipt and no progress.
	status, err = ai.Get(f.ctx, f.b, started.Huddle.ID)
	if err != nil || len(status.Proposals) != 1 ||
		(status.Proposals[0].Work != nil && (status.Proposals[0].Work.TaskStatus != "" || status.Proposals[0].Work.RunID != nil)) {
		t.Fatalf("ended call for a reader without the Project = %+v, %v", status, err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM tasks WHERE id=$1`, f.seed.TaskA)
	if got := read(); got.State != "unavailable" || got.HasResult || got.ResultText != "" {
		t.Fatalf("deleted target = %+v", got)
	}
}
