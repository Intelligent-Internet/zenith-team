package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// RunEnqueueSource identifies which kind of write started a run.
type RunEnqueueSource string

const (
	// RunSourceMessage covers a user message sent into a direct conversation
	// or an agent-assigned task conversation.
	RunSourceMessage RunEnqueueSource = "message"
	// RunSourceAssign covers task creation with an agent assignee and a
	// reassignment onto an agent — the task is being handed to an agent.
	RunSourceAssign RunEnqueueSource = "assign"
	// RunSourceExecution covers the explicit start-execution action on an
	// agent-assigned task.
	RunSourceExecution RunEnqueueSource = "execution"
	// RunSourcePromotion covers a run-queue head materializing after the
	// previous run reached a terminal state.
	RunSourcePromotion RunEnqueueSource = "promotion"
	// RunSourceHumanInput covers a durable continuation after an Approval or
	// Question reaches a terminal state. It is a fresh root Run: parent_run_id
	// remains NULL because this is not an automatic retry.
	RunSourceHumanInput RunEnqueueSource = "human_input"
	// RunSourceSchedule covers a recurring or manually fired Schedule.
	RunSourceSchedule RunEnqueueSource = "schedule"
	// RunSourceTaskReport covers a user-confirmed summary batch sent to an Agent.
	RunSourceTaskReport      RunEnqueueSource = "task_report"
	RunSourceCompanyResearch RunEnqueueSource = "company_research"
	RunSourceCompanyWelcome  RunEnqueueSource = "company_welcome"
)

// ErrRunAgentArchived signals that the enqueue refused to queue work because
// the destination agent has been archived. Productizable — surface as "this
// agent has been archived" rather than retrying.
var ErrRunAgentArchived = errors.New("run enqueue: agent archived")

// ErrRunTriggerMessageOutOfScope and ErrDuplicateTriggerRun are gone with
// runs.trigger_message_id. The scope check they guarded verified that a
// caller-named message belonged to the Run's own Conversation; there is no
// caller-named message any more — the Run row is written first and the user's
// item is written after, carrying this Run's run_id, so the Conversation is
// the Run's by construction. Enqueue idempotency moved to the item's
// request_key unique index, which fences the send the user actually retried
// instead of a second copy of the edge; the caller probes that BEFORE
// enqueuing.

// ErrDuplicatePendingRun means a fresh enqueue lost the race to a concurrent
// one: a queued/dispatched run for the same (task, agent) already exists, so
// the pending-run unique index rejected the insert. Benign — a sibling run
// already covers this target. Returned BARE so no upper layer can leak the
// constraint name.
var ErrDuplicatePendingRun = errors.New("a pending run for this task and agent already exists")

// ErrTaskMergeInProgress means a daemon has claimed a merge for the Task. A
// fresh Run or Task mutation must wait for a terminal result, even after lease
// expiry: the old process may still be unwinding local Git work while a
// recovery claimant takes over the same job.
var ErrTaskMergeInProgress = errors.New("a Task merge is still in progress")

// ErrRunContextTooLarge means the context a caller composed, together with
// the kernel's own additions, exceeds the 1 MiB a Run may carry. The
// Conversation snapshot is bounded on its own, so this names an oversize
// caller document rather than a long chat.
var ErrRunContextTooLarge = errors.New("run context exceeds 1 MiB")

// ExecutePermissionError refuses a Task Run whose authorizer — always the
// Task's assignee (ADR 0014) — lacks project.tasks.execute on the Project of
// the Task chat (ADR 0017). It carries the executing Agent's name so the
// boundary can say which Agent did not start; it never names the Project.
// errors.Is(err, auth.ErrProjectPermissionRequired) holds, so the plain 403
// mapping still applies wherever the detail is not composed.
type ExecutePermissionError struct {
	// AssigneeID is the person refused: the Task's assignee, who authorizes
	// every Task Run. The boundary compares it with the caller to word the
	// refusal for them or for a colleague; it is never served.
	AssigneeID uuid.UUID
	// AgentName is the executing Agent's name, or "" when the Agent could
	// not be read.
	AgentName string
}

func (e *ExecutePermissionError) Error() string {
	return auth.ErrProjectPermissionRequired.Error() + ": the assignee cannot run Tasks in this Project"
}

func (e *ExecutePermissionError) Unwrap() error { return auth.ErrProjectPermissionRequired }

// ExecutePermissionRefusal builds the ExecutePermissionError for a Task's
// Agent, reading its name. An Agent that cannot be read still refuses,
// without it.
func ExecutePermissionRefusal(ctx context.Context, q *db.Queries, companyID, assigneeID, agentID uuid.UUID) error {
	refusal := &ExecutePermissionError{AssigneeID: assigneeID}
	agent, err := q.GetAgentForCompany(ctx, db.GetAgentForCompanyParams{AgentID: agentID, CompanyID: companyID})
	switch {
	case err == nil:
		refusal.AgentName = agent.Name
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("read the Task's Agent for the execute refusal: %w", err)
	}
	return refusal
}

// requireRunExecutePermissionInTx is the kernel's Project access fence
// (ADR 0017): a Run in a Task chat needs the delegate rule's
// project.tasks.execute on the chat's Project (delegate_permission_held,
// D31), the rule the claim fence applies through run_permission_held. The
// Run does not exist yet, so its Authorizer is bound here. The
// Task service and the Task chat already refuse before they write; this is
// the backstop every Run passes, so a path that forgot the check still cannot
// queue work the claim would never admit. It reads without the Project lock,
// since callers may already hold the Conversation lock that must follow it.
func requireRunExecutePermissionInTx(ctx context.Context, q *db.Queries, in RunTrigger, agent db.Agent) error {
	permitted, err := q.RunConversationExecutePermitted(ctx, db.RunConversationExecutePermittedParams{
		CompanyID:                 in.CompanyID,
		ConversationID:            in.ConversationID,
		AuthorizerUserID:          in.AuthorizedByUserID,
		ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	})
	if err != nil {
		return fmt.Errorf("check the Run authorizer's Project access: %w", err)
	}
	if permitted {
		return nil
	}
	return &ExecutePermissionError{
		AssigneeID: in.AuthorizedByUserID,
		AgentName:  agent.Name,
	}
}

// claimCandidateParams binds the claim's Project access arm (ADR 0017), the
// delegate rule asked for project.tasks.execute (run_permission_held, D31),
// for the empty-claim cache's candidate listing; ClaimRun passes the same
// values to SelectClaimableRun, so the two never disagree.
func claimCandidateParams(runtimeIDs ...uuid.UUID) db.ListQueuedClaimCandidatesByRuntimesParams {
	return db.ListQueuedClaimCandidatesByRuntimesParams{
		RuntimeIds:                runtimeIDs,
		ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	}
}

// isDuplicatePendingRunErr reports whether err is the pending-run
// unique-index violation (a concurrent enqueue won the race).
func isDuplicatePendingRunErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "runs_one_pending_per_task_agent_idx"
}

// RunTrigger describes one prospective run in its enqueue shape. PostgreSQL
// allocates the run identity when the kernel inserts it.
type RunTrigger struct {
	// DocumentWriting freezes text-only generation separately from comment commands.
	DocumentWriting bool
	// Research settings apply only to the platform initialization Run.
	ResearchModel         string
	ResearchThinkingLevel string
	InvocationID          uuid.NullUUID
	CompanyID             uuid.UUID
	ConversationID        uuid.UUID
	AgentID               uuid.UUID
	// TaskID is set for task-conversation runs and empty for direct chats;
	// it arms the one-pending-per-(task, agent) serialization slot.
	TaskID uuid.NullUUID
	// TriggerPrompt is the user's words this Run is being started on, frozen
	// into the Run's context under `prompt` (ADR 0003).
	//
	// It arrives as text rather than as a message id because the order flipped:
	// runs.id is a database-generated UUID that needs nothing from the conversation stream,
	// so the Run row is INSERTed first and the caller writes the 'user_message'
	// item after, carrying the run_id this call returns. At this point the item
	// does not exist yet, so there is nothing to read it back from — the caller
	// holds the same string it is about to commit. Empty for a Run no one's
	// words start, such as a composed continuation.
	TriggerPrompt string
	// TriggerAttachmentIDs are the files the caller is about to link to the
	// message it writes after this call, for the same reason TriggerPrompt is
	// text: that message does not exist yet. A Task Run freezes them beside
	// the files already on the Task's Conversation.
	TriggerAttachmentIDs []uuid.UUID
	Priority             int32
	Context              []byte
	Source               RunEnqueueSource
	// ForceFreshSession prevents a recurring or manually fired Schedule from
	// inheriting provider session state from an earlier occurrence.
	ForceFreshSession bool
	// ActorUserID attributes the run.queued event to the member whose write
	// triggered it; empty publishes as system. With a TriggerPrompt it is also
	// frozen as context.trigger.author, the person the agent is answering.
	ActorUserID uuid.UUID
	// Actor, when set, is who the run.queued Activity row and frame name
	// instead of ActorUserID's member: an Agent that started this Run from
	// its own Run, for that Run's authorizer (D31). The zero value keeps the
	// member or the system.
	Actor events.Actor
	// AuthorizedByUserID is the member whose permitted action asks for this
	// execution — for a Task Run always the Task's assignee (the member who
	// starts it, whose message it answers, or whose Approval or Question
	// decision continues it); for a direct chat the conversation's creator;
	// for a Schedule its creator. The Run executes on this member's own
	// binding of the Agent (ADR 0014): the kernel refuses the enqueue unless
	// that binding names one of their live runtimes, stores the fact on the
	// Run, and the claim rechecks it. Never a system principal and never
	// inferred from the Company owner: an unknown authorizer fails closed.
	AuthorizedByUserID uuid.UUID
}

// PriorityToInt maps boardly's task priority vocabulary onto the runs
// priority column.
func PriorityToInt(p string) int32 {
	switch p {
	case "urgent":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// EnqueueRunInTx is the enqueue kernel: it validates the agent, checks that
// the authorizer may run work in a Task chat's Project (ADR 0017), resolves
// the authorizer's binding of the Agent into the Run's runtime, and inserts
// the queued run row inside the CALLER'S transaction,
// appending the run.queued event to the caller's pending slice for
// post-commit publication. The caller owns the conversation lock, so the run
// becomes visible atomically with the message/task write that triggered it.
//
// The Run row is written FIRST and the user's 'user_message' item after it:
// runs.id is a database-generated UUID that needs nothing from the conversation stream, and
// the item is what carries run_id. A caller writing a prompt therefore calls
// this, then inserts the item with the returned run's id — and probes the
// send's request_key for an exact replay BEFORE either write, because that
// unique index is the whole of enqueue idempotency now.
//
// The post-commit daemon wakeup (EmptyClaim.Bump + NotifyRunAvailable) is NOT
// performed here — the caller hands the returned run to
// RunService.NotifyRunEnqueued (or an equivalent notifier) once its
// transaction commits.
func EnqueueRunInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	in RunTrigger,
) (db.Run, error) {
	agent, err := q.GetAgentForCompany(ctx, db.GetAgentForCompanyParams{
		AgentID:   in.AgentID,
		CompanyID: in.CompanyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// GetAgentForCompany filters archived rows, so a miss is either a
		// foreign agent or an archived one; both refuse the enqueue.
		return db.Run{}, ErrRunAgentArchived
	}
	if err != nil {
		return db.Run{}, fmt.Errorf("load agent: %w", err)
	}

	if in.InvocationID.Valid {
		invocation, err := q.GetChannelInvocation(
			ctx,
			db.GetChannelInvocationParams{InvocationID: in.InvocationID.UUID, CompanyID: in.CompanyID},
		)
		if err != nil {
			return db.Run{}, err
		}
		in.ActorUserID = invocation.CallerUserID
		if in.Source == RunSourceHumanInput {
			in.TriggerPrompt = invocation.TriggerContent + "\n\nHuman decision for this request:\n" + in.TriggerPrompt
		}
		if err := AuthorizeChannelInvocationInTx(ctx, q, in); err != nil {
			return db.Run{}, err
		}
	}
	// A Task chat's Run needs its authorizer's project.tasks.execute
	// (ADR 0017), decided before any write like the binding below.
	if err := requireRunExecutePermissionInTx(ctx, q, in, agent); err != nil {
		return db.Run{}, err
	}
	// The run executes on the authorizer's own binding of the agent
	// (ADR 0014). An offline runtime is fine: the run waits queued and the
	// daemon claims it on next online. No binding is not: that refusal comes
	// before any write, so the caller's transaction has nothing to undo.
	binding, err := resolveRunBinding(ctx, q, in.CompanyID, agent.ID, in.AuthorizedByUserID)
	if err != nil {
		return db.Run{}, err
	}
	runtimeID := binding.RuntimeID
	if in.TaskID.Valid {
		if err := LockTaskRunMutationFenceInTx(
			ctx,
			q,
			in.CompanyID,
			in.TaskID.UUID,
		); err != nil {
			return db.Run{}, err
		}
	}

	// Company resources and the frozen prompt ride on the run, so a daemon
	// holding a local directory executes in place instead of in a fresh
	// worktree, and the provider hears the user's words without fetching them
	// back through the user plane at claim time. Attached here because this is
	// the one path every run takes.
	if err := q.InitializeConversationModelSettings(
		ctx,
		db.InitializeConversationModelSettingsParams{ConversationID: in.ConversationID, AgentID: in.AgentID},
	); err != nil {
		return db.Run{}, fmt.Errorf("initialize chat model settings: %w", err)
	}
	runContext, err := buildRunContext(ctx, q, in, runtimeID)
	if err != nil {
		return db.Run{}, err
	}

	run, err := q.CreateRun(ctx, db.CreateRunParams{
		InvocationID: in.InvocationID,
		CompanyID:    in.CompanyID, AgentID: in.AgentID,
		TaskID: in.TaskID, ConversationID: in.ConversationID, RuntimeID: runtimeID,
		ExecutionAuthorizedByUserID: in.AuthorizedByUserID,
		Status:                      "queued", Priority: in.Priority,
		Context: runContext, Attempt: 1, ForceFreshSession: in.ForceFreshSession,
		StudioEnabled: frozenStudioEnabled(runContext),
	})
	if err != nil {
		if isDuplicatePendingRunErr(err) {
			slog.Debug("run enqueue coalesced: pending run already exists",
				"task_id", util.NullUUIDToString(in.TaskID),
				"agent_id", in.AgentID.String())
			return db.Run{}, ErrDuplicatePendingRun
		}
		return db.Run{}, fmt.Errorf("create run: %w", err)
	}

	if err := freezeRunSkills(ctx, q, run); err != nil {
		return db.Run{}, err
	}
	overviewEvent, err := canvas.EnsureTaskOverviewInTx(ctx, q, run)
	if err != nil {
		return db.Run{}, err
	}
	slog.Info(
		"run enqueued",
		"run_id", run.ID,
		"conversation_id", run.ConversationID.String(),
		"agent_id", run.AgentID.String(),
		"runtime_id", run.RuntimeID.String(),
		"source", string(in.Source),
	)
	actor := events.SystemActor()
	if in.ActorUserID != uuid.Nil && in.Source != RunSourceSchedule {
		actor = events.UserActor(in.ActorUserID.String())
	}
	if in.Actor.Type != "" {
		actor = in.Actor
	}
	if pending != nil {
		event, eventErr := runQueuedEvent(ctx, q, run, in.ActorUserID)
		if eventErr != nil {
			return db.Run{}, eventErr
		}
		if in.Actor.Type != "" {
			event.Actor = in.Actor
		}
		*pending = append(*pending, event)
	}
	if in.InvocationID.Valid && in.Source == RunSourceHumanInput {
		if _, err := q.SetChannelInvocationState(
			ctx,
			db.SetChannelInvocationStateParams{
				InvocationID:      in.InvocationID.UUID,
				Status:            "active",
				PublicationStatus: "pending",
			},
		); err != nil {
			return db.Run{}, fmt.Errorf("resume channel invocation: %w", err)
		}
	}
	if err := activity.RecordInTx(ctx, q, pending, actor, EventRunQueued, nil, Topic, run.ID); err != nil {
		return db.Run{}, err
	}
	if overviewEvent != nil && pending != nil {
		*pending = append(*pending, *overviewEvent)
	}
	return run, nil
}

// LockTaskRunMutationFenceInTx serializes a Task mutation or fresh Run with a
// daemon Task merge. The caller's transaction owns the returned Task lock.
// An unclaimed pending job is safe to supersede. An executing job is not: the
// daemon may already be mutating Git, so fresh work remains fenced until a
// claimant or recovery attempt records a terminal result.
func LockTaskRunMutationFenceInTx(
	ctx context.Context,
	q *db.Queries,
	companyID uuid.UUID,
	taskID uuid.UUID,
) error {
	if _, err := q.GetTaskForCompanyByIDForUpdate(
		ctx,
		db.GetTaskForCompanyByIDForUpdateParams{
			CompanyID: companyID,
			TaskID:    taskID,
		},
	); err != nil {
		return fmt.Errorf("lock Task Run mutation fence: %w", err)
	}
	if _, err := q.SupersedePendingTaskMergeJobsForTask(ctx, taskID); err != nil {
		return fmt.Errorf("supersede pending Task merge: %w", err)
	}
	live, err := q.HasExecutingTaskMergeJob(ctx, taskID)
	if err != nil {
		return fmt.Errorf("check live Task merge: %w", err)
	}
	if live {
		return ErrTaskMergeInProgress
	}
	return nil
}

// ProbeRunEnqueue reports whether an enqueue for (company, agent) on behalf
// of authorizedByUserID would be accepted right now — the agent is active
// and the authorizer has it bound to one of their live runtimes. Promotion
// uses it BEFORE writing anything so an unavailable or no-longer-bound
// target invalidates the queue head instead of orphaning a Run with no
// words. Returns ErrRunAgentArchived, ErrRunAgentNotBound or nil.
func ProbeRunEnqueue(
	ctx context.Context,
	q *db.Queries,
	companyID, agentID, authorizedByUserID uuid.UUID,
) error {
	agent, err := q.GetAgentForCompany(ctx, db.GetAgentForCompanyParams{
		AgentID:   agentID,
		CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunAgentArchived
	}
	if err != nil {
		return fmt.Errorf("load agent: %w", err)
	}
	_, err = resolveRunBinding(ctx, q, companyID, agent.ID, authorizedByUserID)
	return err
}

// runQueuedEvent is the ∅ → queued wire event staged inside the enqueue
// transaction and published post-commit by the caller.
func runQueuedEvent(
	ctx context.Context,
	q *db.Queries,
	run db.Run,
	actorUserID uuid.UUID,
) (events.Event, error) {
	view, err := loadRunView(ctx, q, run)
	if err != nil {
		return events.Event{}, fmt.Errorf("load queued run event projection: %w", err)
	}
	actor := events.SystemActor()
	if actorUserID != uuid.Nil {
		actor = events.UserActor(actorUserID.String())
	}
	return events.Event{
		EntityType:     Topic,
		Name:           EventRunQueued,
		CompanyID:      view.CompanyID,
		CreatedAt:      time.Now().UTC(),
		RunID:          view.ID,
		TaskID:         util.UUIDToValue(view.TaskID),
		ConversationID: view.ConversationID,
		Actor:          actor,
		Payload:        view.RunSnapshot,
	}, nil
}

// MarkTaskRunScheduledInTx preserves Calendar admission checks for the first
// attempt while its computer is offline. Retries use the ordinary Task lifecycle.
// The caller owns the occurrence receipt in this same transaction.
func MarkTaskRunScheduledInTx(ctx context.Context, q *db.Queries, run db.Run) error {
	if err := q.MarkTaskRunScheduled(ctx, db.MarkTaskRunScheduledParams{
		RunID: run.ID, TaskID: run.TaskID, CompanyID: run.CompanyID,
	}); err != nil {
		return fmt.Errorf("mark scheduled Task Run: %w", err)
	}
	return nil
}
