// Package task owns Boardly's durable product Task contract. Product Tasks are
// deliberately separate from queued agent Runs.
package task

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// TaskService owns the product Task lifecycle — creation, listing, mutation
// and execution hand-off — together with the dependency views over it.
//
// The name keeps the Task prefix on purpose: AGENTS.md pins every service to
// the trio XService / DefaultXService / NewXService and forbids a bare
// Service, so revive's stutter advice is the one thing we cannot follow here.
//
//nolint:revive // AGENTS.md service-naming trio; bare Service is forbidden.
type TaskService interface {
	FinalizeResults(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		FinalizeResultsInput,
	) (Response, error)
	ResultSelection(context.Context, auth.UserPrincipal, uuid.UUID) (ResultSelection, error)
	SubmitHuddleProposal(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		SubmitHuddleProposalInput,
	) (conversation.HuddleProposal, error)
	Create(context.Context, auth.UserPrincipal, CreateInput) (MutationResult, error)
	List(context.Context, auth.UserPrincipal, ListOptions, *RunScope) (Page, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID, *RunScope) (Response, error)
	ResolveReference(context.Context, auth.UserPrincipal, string) (uuid.UUID, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, UpdateInput) (MutationResult, error)
	StartExecution(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		StartExecutionInput,
	) (MutationResult, error)
	MarkFailed(context.Context, auth.UserPrincipal, execution.RunOrigin, FailureInput) (Response, error)
	ListDependencies(context.Context, auth.UserPrincipal, uuid.UUID, *RunScope) (DependencySet, error)
	ListDependencyGraph(context.Context, auth.UserPrincipal, DependencyGraphOptions) (DependencyGraph, error)
	Workspace(context.Context, auth.UserPrincipal, uuid.UUID, WorkspaceOptions, *RunScope) (Workspace, error)
}

var _ TaskService = (*DefaultTaskService)(nil)

type DefaultTaskService struct {
	store          *db.Store
	bus            *events.Bus
	inboxPresenter inbox.Presenter
}

// NewTaskService accepts a nil inboxPresenter: Inbox presentation is a
// best-effort post-commit hint and inbox.PresentCreated is nil-safe.
func NewTaskService(
	store *db.Store,
	bus *events.Bus,
	inboxPresenter inbox.Presenter,
) (*DefaultTaskService, error) {
	if store == nil {
		return nil, errors.New("Task database store is required")
	}
	if bus == nil {
		return nil, errors.New("Task event bus is required")
	}
	return &DefaultTaskService{
		store: store,
		bus:   bus, inboxPresenter: inboxPresenter,
	}, nil
}

// Create owns one complete authorization/transaction boundary: Task and
// Conversation creation and the assignment Run are never split across
// independent commits. The assignee is the caller unless the create names
// another member, which needs tasks.assign (ADR 0014); the Run starts only
// when the caller is the assignee and an Agent does the work, because it
// executes on the caller's own binding of that Agent. Assigning someone
// else starts nothing: they are told to start it on their computer. A Run's
// create files work only for its member and only where the delegate rule
// lets it read (RunScope.admitCreate) and write (RunScope.admitCreateProject).
func (s *DefaultTaskService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input CreateInput,
) (MutationResult, error) {
	normalized, err := normalizeCreateInput(input, principal.UserID)
	if err != nil {
		return MutationResult{}, err
	}
	var effects taskWriteEffects
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(tx *generated.Queries) error {
			var writeErr error
			effects, writeErr = s.createTaskInTx(ctx, tx, principal, normalized)
			return writeErr
		},
	)
	if err != nil {
		return MutationResult{}, fmt.Errorf("create Task: %w", err)
	}
	s.publishTaskWrite(ctx, effects)
	return effects.result, nil
}

type taskWriteEffects struct {
	result       MutationResult
	pending      []events.Event
	enqueued     *generated.Run
	inboxResults []inbox.CreateResult
}

func (s *DefaultTaskService) publishTaskWrite(ctx context.Context, effects taskWriteEffects) {
	for _, group := range events.GroupByTopic(effects.pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	s.wakeRuntime(ctx, effects.enqueued)
	for _, item := range effects.inboxResults {
		inbox.PresentCreated(s.inboxPresenter, item)
	}
}

func (s *DefaultTaskService) createTaskInTx(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	normalized CreateInput,
) (taskWriteEffects, error) {
	return s.createTaskWithExecutionInTx(ctx, tx, principal, normalized, true)
}

// Proposal confirmation saves work for a separate, explicit execution action.
func (s *DefaultTaskService) createTaskWithExecutionInTx(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	normalized CreateInput,
	startExecution bool,
) (taskWriteEffects, error) {
	var effects taskWriteEffects
	role, err := authorizeTaskMutation(ctx, tx, principal)
	if err != nil {
		return effects, err
	}
	if err := normalized.Run.admitCreate(ctx, tx, principal.CompanyID, normalized); err != nil {
		return effects, err
	}
	if normalized.Assignee.ID != principal.UserID {
		if !role.Can(auth.PermissionTasksAssign) {
			return effects, ErrAssignPermissionRequired
		}
		if err := lockAssigneeMembership(ctx, tx, principal.CompanyID, normalized.Assignee.ID); err != nil {
			return effects, err
		}
	}
	// Creating a Task changes the vertex set that Company Plan apply
	// snapshots before locking every Task Conversation. Participate in the
	// same company-scoped graph lock so that Conversation -> Task ordering
	// remains complete even when creation races an apply. An apply takes
	// it before its Project locks, so a create takes it before its own.
	if err := tx.LockTaskDependencyGraph(ctx, principal.CompanyID); err != nil {
		return effects, fmt.Errorf("lock Task dependency graph for create: %w", err)
	}
	project, err := tx.GetProjectForUpdate(
		ctx,
		generated.GetProjectForUpdateParams{CompanyID: principal.CompanyID, ProjectID: normalized.ProjectID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return effects, auth.ErrProjectNotVisible
	}
	if err != nil {
		return effects, fmt.Errorf("lock Task Project: %w", err)
	}
	// Creating a Task in a Project needs project.tasks.write there
	// (ADR 0017), decided under the Project lock just taken. A Project the
	// caller cannot read answers like a missing one: 404 project_not_found,
	// as GET of the Project does, since the Project is what the create
	// names. The assignee must read it too: they are responsible for work
	// they must be able to see.
	access, err := auth.ResolveProjectAccess(ctx, tx, principal, project.ID)
	if err != nil && !errors.Is(err, auth.ErrProjectNotVisible) {
		return effects, err
	}
	if access.Via == "" {
		return effects, auth.ErrProjectNotVisible
	}
	if err := requireProjectPermission(access, auth.ProjectPermissionTasksWrite); err != nil {
		return effects, err
	}
	// A Run's create also passes the delegate rule for project.tasks.write
	// (IA-FR10), decided under the Project lock: only where its Authorizer
	// may, and a Task Run only in its Task chat's Project.
	if err := normalized.Run.admitCreateProject(ctx, tx, principal.CompanyID, project.ID); err != nil {
		return effects, err
	}
	if normalized.Assignee.ID != principal.UserID {
		if err := checkAssigneeReadsProject(
			ctx, tx, principal.CompanyID, project.ID, normalized.Assignee.ID,
		); err != nil {
			return effects, err
		}
	}
	if project.ArchivedAt.Valid {
		return effects, ErrProjectArchived
	}
	allocated, err := allocateTaskKey(ctx, tx, principal.CompanyID, normalized.Key)
	if err != nil {
		return effects, err
	}
	normalized.Key = allocated.key
	// A create the caller assigns to themself with an Agent starts the
	// assignment Run: it does the Task's work, so the Task is committed
	// in progress from the start, exactly as a Task message moves it
	// when its Run materializes. A Task created already closed gets no
	// Run, as it admits no message; a Task assigned to someone else
	// waits for them (ADR 0014).
	enqueueAssignment, transition := false, false
	if startExecution && normalized.Schedule == nil && normalized.ExecutedBy.Kind == ActorAgent &&
		normalized.Assignee.ID == principal.UserID {
		enqueueAssignment, transition = taskMessageRunStatusPolicy(normalized.Status)
	}
	if transition {
		normalized.Status = StatusInProgress
	}
	// The assignment Run is the caller's own Task Run, so it needs their
	// project.tasks.execute; the refusal is checked before the INSERT so
	// nothing is created half-started.
	if enqueueAssignment {
		if err := requireExecutePermission(
			ctx,
			tx,
			access,
			principal.UserID,
			normalized.ExecutedBy.ID,
		); err != nil {
			return effects, err
		}
	}
	// Files travel on a message the assignment Run owns, so they need
	// the Run a self-assigned create with an executing Agent starts
	// (ADR 0014); a create that starts none has nothing to carry them.
	if len(normalized.AttachmentIDs) > 0 && !enqueueAssignment {
		return effects, ErrInvalidInput
	}
	value, createdConversation, err := insertTaskWithConversation(
		ctx,
		tx,
		principal,
		normalized,
		allocated.number,
		&effects.pending,
	)
	if err != nil {
		return effects, err
	}

	var calendarBefore *activity.Snapshot
	if normalized.Schedule != nil || normalized.Assignee.ID != principal.UserID {
		calendarBefore, err = activity.ObserveInTx(ctx, tx, Topic, value.ID)
		if err != nil {
			return effects, err
		}
	}
	if normalized.Assignee.ID != principal.UserID {
		row, err := tx.SetTaskAssignment(
			ctx,
			generated.SetTaskAssignmentParams{
				CompanyID:        principal.CompanyID,
				TaskID:           value.ID,
				AssignmentStatus: "pending",
			},
		)
		if err != nil {
			return effects, err
		}
		value = taskFromRow(row)
	}
	if normalized.Schedule != nil {
		value, err = createTaskCalendarInTx(ctx, tx, value, *normalized.Schedule)
		if err != nil {
			return effects, err
		}
	}

	if calendarBefore != nil {
		if err := activity.RecordInTx(
			ctx,
			tx,
			&effects.pending,
			normalized.Run.actor(principal.UserID),
			EventUpdated,
			calendarBefore,
			Topic,
			value.ID,
		); err != nil {
			return effects, err
		}
	}

	// The Conversation is announced before anything is written in it.
	if policy := normalized.ResultPolicy; policy != nil {
		if err := tx.SetTaskResultPolicy(
			ctx,
			generated.SetTaskResultPolicyParams{
				CompanyID:            principal.CompanyID,
				ID:                   value.ID,
				ResultProposalOnly:   policy.ProposalOnly,
				ResultReviewRequired: policy.ReviewRequired,
			},
		); err != nil {
			return effects, err
		}
	}
	if err := stageTaskConversationCreated(
		ctx,
		tx,
		&effects.pending,
		principal.UserID,
		value.ID,
		createdConversation,
	); err != nil {
		return effects, err
	}
	// The assignment Run starts inside this same transaction, on the
	// caller's binding of the Agent, the way an explicit start does. It
	// freezes the create's files, which the files message below links.
	if enqueueAssignment {
		run, err := startTaskRunInTx(
			ctx, tx, &effects.pending, principal, value, execution.RunSourceAssign,
			taskRunOptions{attachmentIDs: normalized.AttachmentIDs, run: normalized.Run},
		)
		if err != nil {
			return effects, err
		}
		effects.enqueued = &run
	}
	value, err = taskProjectionForActiveMember(ctx, tx, principal, value.ID)
	if err != nil {
		return effects, err
	}
	if len(normalized.AttachmentIDs) > 0 {
		files, err := insertTaskFilesMessage(
			ctx, tx, principal, value, effects.enqueued.ID, normalized.AttachmentIDs,
		)
		if err != nil {
			return effects, err
		}
		if err := stageTaskEvent(ctx, tx, &effects.pending, newTaskMessageCreatedEvent(
			normalized.Run.actor(principal.UserID), principal.CompanyID, files,
		)); err != nil {
			return effects, err
		}
		if err := stageBumpedTaskConversation(
			ctx, tx, &effects.pending, principal.UserID, normalized.Run.actor(principal.UserID), value,
		); err != nil {
			return effects, err
		}
	}
	if err := stageTaskEvent(
		ctx,
		tx,
		&effects.pending,
		newTaskEvent(principal.UserID, EventCreated, value),
	); err != nil {
		return effects, err
	}
	effects.inboxResults, err = createTaskInboxItems(
		ctx,
		tx,
		&effects.pending,
		nil,
		value,
		principal.UserID,
		handoffOutcome{},
	)
	if err != nil {
		return effects, err
	}
	effects.result = MutationResult{Task: value}
	if effects.enqueued != nil {
		runID := effects.enqueued.ID
		effects.result.RunID = &runID
	}
	return effects, nil
}

// wakeRuntime says, after commit, that the runtime may have claimable work,
// so a Run a Task write enqueued is claimed now rather than on the daemon's
// next poll — exactly what a Task message does. Whoever wakes daemons
// subscribes to that; this domain holds no handle to them.
func (s *DefaultTaskService) wakeRuntime(ctx context.Context, run *generated.Run) {
	if run != nil {
		execution.PublishRuntimeWork(ctx, s.bus, *run)
	}
}

// List reads the Company's Tasks. Inside a Run (a non-nil scope) it reads
// only the Run's own Tasks, whatever assignee or Agent filter was asked for,
// and only in Projects the delegate rule lets it read. The planner
// (RunScope.Planner) lists every Task its member reads, with the filters
// asked for.
func (s *DefaultTaskService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ListOptions,
	run *RunScope,
) (Page, error) {
	normalized, err := normalizeListOptions(options)
	if err != nil {
		return Page{}, err
	}
	queries := s.store.Queries()
	if normalized.ProjectID != nil {
		if err := requireProjectReadable(ctx, queries, principal, *normalized.ProjectID); err != nil {
			return Page{}, fmt.Errorf("list Tasks: %w", err)
		}
	}
	page, err := listTasks(ctx, queries, principal, run.listOptions(normalized))
	if err != nil {
		return Page{}, fmt.Errorf("list Tasks: %w", err)
	}
	// A Run lists only Tasks whose Project the delegate rule lets it read.
	page.Tasks, err = run.filterProjects(ctx, queries, principal.CompanyID, page.Tasks)
	if err != nil {
		return Page{}, fmt.Errorf("list Tasks: %w", err)
	}
	if page.Tasks == nil {
		page.Tasks = []Response{}
	}
	return page, nil
}

// Get reads one Task. Inside a Run (a non-nil scope) a Task that is not the
// Run's own is refused with ErrOutsideRunScope; a Task that does not exist
// is ErrNotFound either way.
func (s *DefaultTaskService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	run *RunScope,
) (Response, error) {
	value, err := getTask(ctx, s.store.Queries(), principal, taskID)
	if err == nil {
		err = run.admitRead(ctx, s.store.Queries(), value)
	}
	if err != nil {
		return Response{}, fmt.Errorf("get Task: %w", err)
	}
	return value, nil
}

// Update applies a target-state mutation under the same lock order used by
// message sends: Company/member, Conversation, then Task. This keeps agent
// assignment serializable at READ COMMITTED without widening the isolation
// level, and the whole mutation owns one complete authorization/transaction
// boundary. Update resolves Agent assignment against the locked current row;
// a new Agent assignment gets a database-generated Run identity.
// lockTaskMutationTarget takes the write path's locks in the one order every
// task mutation uses — dependency graph, then the Task's Project (FOR SHARE,
// ADR 0017), then conversation, then the task row — and returns the row it
// locked with the caller's standing on the Project, refusing a caller who
// cannot read the Project (not found) or whose expected revision has already
// moved.
//
// The order is a deadlock rule rather than a preference: two mutations racing
// on the same task have to queue at the same first lock. Naming it once is
// what stops a new mutation taking them in a different order.
//
// lockGraph is false for edits that cannot change the dependency graph. The
// graph lock is company-wide, so taking it to rename a task would serialize
// every title edit in the company against every other.
func lockTaskMutationTarget(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	expectedRevision int64,
	lockGraph bool,
	purpose string,
) (Response, auth.CompanyRole, auth.ProjectAccess, error) {
	role, err := authorizeTaskMutation(ctx, tx, principal)
	if err != nil {
		return Response{}, "", auth.ProjectAccess{}, err
	}
	if lockGraph {
		if err := tx.LockTaskDependencyGraph(ctx, principal.CompanyID); err != nil {
			return Response{}, "", auth.ProjectAccess{}, fmt.Errorf(
				"lock Task dependency graph for %s: %w",
				purpose,
				err,
			)
		}
	}
	// Resolve the Task through the same visibility query Get uses: a Task
	// whose Project the caller cannot read is not found (ADR 0017).
	visible, err := tx.GetTaskForActiveMember(ctx, generated.GetTaskForActiveMemberParams{
		ActorUserID: principal.UserID, TaskID: taskID, CompanyID: principal.CompanyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, "", auth.ProjectAccess{}, ErrNotFound
	}
	if err != nil {
		return Response{}, "", auth.ProjectAccess{}, fmt.Errorf(
			"resolve Task Conversation for %s: %w",
			purpose,
			err,
		)
	}
	// The Project comes next in the lock order (company -> member ->
	// project -> conversation -> run); the graph lock, an advisory lock a
	// Company Plan apply also takes before its Project locks, stays ahead of
	// it. Each caller decides which Project permission its write needs from
	// the standing resolved under this lock.
	access, err := lockTaskProjectAccess(ctx, tx, principal, visible.ProjectID)
	if err != nil {
		return Response{}, "", auth.ProjectAccess{}, err
	}
	if _, err := tx.LockConversationForActiveUserMutation(
		ctx,
		generated.LockConversationForActiveUserMutationParams{
			ActorUserID:    principal.UserID,
			ConversationID: visible.ConversationID,
			CompanyID:      principal.CompanyID,
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return Response{}, "", auth.ProjectAccess{}, ErrNotFound
	} else if err != nil {
		return Response{}, "", auth.ProjectAccess{}, fmt.Errorf(
			"lock Task Conversation for %s: %w",
			purpose,
			err,
		)
	}
	current, err := lockTaskForUpdate(ctx, tx, principal.CompanyID, taskID)
	if err != nil {
		return Response{}, "", auth.ProjectAccess{}, err
	}
	if current.Revision != expectedRevision {
		return Response{}, "", auth.ProjectAccess{}, ErrConflict
	}
	return current, role, access, nil
}

func (s *DefaultTaskService) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	input UpdateInput,
) (MutationResult, error) {
	if !util.ValidUUID(taskID) {
		return MutationResult{}, ErrNotFound
	}
	normalized, err := normalizeUpdateInput(input)
	if err != nil {
		return MutationResult{}, err
	}
	var result MutationResult
	var inboxResults []inbox.CreateResult
	var pending []events.Event
	var enqueued *generated.Run
	err = s.store.WithinTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite,
	}, func(tx *generated.Queries) error {
		pending = pending[:0]
		enqueued = nil
		if replay, err := replayTaskResult(ctx, tx, principal, taskID, normalized); err != nil {
			return err
		} else if replay != nil {
			result = MutationResult{Task: *replay, AssigneeOutsideProject: assigneeOutsideProject(*replay)}
			return nil
		}
		assigning := normalized.Assignee != nil || normalized.ExecutedBy != nil
		current, role, access, err := lockTaskMutationTarget(
			ctx, tx, principal, taskID, normalized.ExpectedRevision, assigning, "update",
		)
		if err != nil {
			return err
		}
		// Changing a Task inside a Project needs project.tasks.write there
		// (ADR 0017); the Company role is not consulted, except for a
		// handoff below.
		if err := requireProjectPermission(access, auth.ProjectPermissionTasksWrite); err != nil {
			return err
		}

		target := applyTaskUpdateTarget(current, normalized)
		if normalized.ResultSelection != nil || normalized.ResultPolicy != nil {
			canManageResults := role.Can(auth.PermissionTasksAssign) ||
				current.Assignee.ID == principal.UserID
			if normalized.ResultPolicy != nil && !canManageResults {
				return ErrReviewPermissionRequired
			}
			if normalized.ResultSelection != nil && !canManageResults {
				return ErrReviewPermissionRequired
			}
			if normalized.Assignee != nil || normalized.ExecutedBy != nil {
				return ErrInvalidInput
			}
			if err := changeTaskResultsInTx(
				ctx,
				tx,
				principal,
				taskResultMutation{
					Current: current,
					Target:  &target,
					Input:   normalized,
					Pending: &pending,
				},
			); err != nil {
				return err
			}
		}
		if !validTaskDates(target.StartDate, target.DueDate) {
			return ErrInvalidInput
		}
		// The assignee does the work themself only under their own id: a
		// user executedBy that names anyone else is invalid, since a Run
		// executes on the assignee's computer and nobody else's.
		if target.ExecutedBy.Kind == ActorUser && target.ExecutedBy.ID != target.Assignee.ID {
			return ErrInvalidInput
		}
		// Review remains with the assignee or a tasks.assign holder.
		if current.Status == StatusInReview && target.Status != StatusInReview &&
			!role.Can(auth.PermissionTasksAssign) && current.Assignee.ID != principal.UserID {
			return ErrReviewPermissionRequired
		}
		// Changing the assignee or the executing Agent is a handoff
		// (ADR 0014): a tasks.assign holder or the current assignee may;
		// once the Task has run it needs confirmation, because it cancels
		// the live work and moves the Task to another computer. Re-selecting
		// the current values is not a handoff.
		handoff := !sameActor(current.Assignee, target.Assignee) ||
			!sameActor(current.ExecutedBy, target.ExecutedBy)
		if handoff {
			if !role.Can(auth.PermissionTasksAssign) && current.Assignee.ID != principal.UserID {
				return ErrHandoffPermissionRequired
			}
			if current.ExecutionGeneration > 0 && !normalized.ConfirmHandoff {
				// The refusal says whether confirming cancels live work: the
				// same Runs the confirmed handoff below would cancel.
				live, err := tx.ListActiveRunsForTaskHandoff(
					ctx,
					generated.ListActiveRunsForTaskHandoffParams{
						CompanyID: principal.CompanyID, TaskID: current.ID,
					},
				)
				if err != nil {
					return fmt.Errorf("list live Task Runs for handoff: %w", err)
				}
				return &HandoffConfirmationError{RunLive: len(live) > 0}
			}
			// The Company lock serializes every membership mutation, so the
			// new assignee's row is safe to lock after the Task's.
			if target.Assignee.ID != current.Assignee.ID {
				if err := lockAssigneeMembership(
					ctx,
					tx,
					principal.CompanyID,
					target.Assignee.ID,
				); err != nil {
					return err
				}
				// The new assignee must read the Project, under the Project
				// lock held since lockTaskMutationTarget (ADR 0017).
				if err := checkAssigneeReadsProject(
					ctx, tx, principal.CompanyID, current.ProjectID, target.Assignee.ID,
				); err != nil {
					return err
				}
			}
		}
		// A confirmed handoff of a Task that has run advances its
		// generation, so none of its earlier Runs is live or can be
		// reviewed any more. A Task left in progress or in review goes back
		// to todo: the new holder starts it fresh, and nobody is asked to
		// review work that can no longer be merged.
		advanceGeneration := handoff && current.ExecutionGeneration > 0
		if advanceGeneration && (target.Status == StatusInProgress || target.Status == StatusInReview) {
			target.Status = StatusTodo
		}
		// A handoff that lands on the caller with an Agent starts the
		// assignment Run: it does the Task's work, so the Task moves into
		// progress under the same policy as a Task message. A closed Task
		// admits none, and a handoff to someone else starts nothing — they
		// start it on their own computer. The transition is settled here so
		// a failed Task reopened by a handoff meets the same
		// active-generation check as any reopen.
		newAgentAssignment := handoff && target.ExecutedBy.Kind == ActorAgent &&
			target.Assignee.ID == principal.UserID && current.ScheduleID == nil
		if newAgentAssignment {
			var transition bool
			newAgentAssignment, transition = taskMessageRunStatusPolicy(target.Status)
			if transition {
				target.Status = StatusInProgress
			}
		}
		// The assignment Run is the caller's own Task Run: it needs their
		// project.tasks.execute (ADR 0017).
		if newAgentAssignment {
			if err := requireExecutePermission(
				ctx,
				tx,
				access,
				principal.UserID,
				target.ExecutedBy.ID,
			); err != nil {
				return err
			}
		}
		if current.Status == StatusFailed && target.Status != StatusFailed &&
			target.Status != StatusDone && target.Status != StatusCancelled {
			active, err := tx.TaskHasNonterminalRunForCurrentExecutionGeneration(
				ctx,
				generated.TaskHasNonterminalRunForCurrentExecutionGenerationParams{
					TaskID: current.ID, CompanyID: principal.CompanyID,
				},
			)
			if err != nil {
				return fmt.Errorf("check active Task generation before reopen: %w", err)
			}
			if active {
				return ErrConflict
			}
		}
		changed := changedTaskFields(current, target, normalized)
		if normalized.ResultSelection != nil || normalized.ResultPolicy != nil ||
			normalized.Status != nil && *normalized.Status == StatusDone {
			changed = append(changed, "results")
		}
		if len(changed) == 0 {
			current, err = taskProjectionForActiveMember(ctx, tx, principal, current.ID)
			if err != nil {
				return err
			}
			result = MutationResult{Task: current, AssigneeOutsideProject: assigneeOutsideProject(current)}
			return nil
		}
		if err := execution.LockTaskRunMutationFenceInTx(
			ctx,
			tx,
			principal.CompanyID,
			current.ID,
		); errors.Is(err, execution.ErrTaskMergeInProgress) {
			return ErrConflict
		} else if err != nil {
			return err
		}
		if newAgentAssignment {
			if err := ensureTaskExecutionEligible(ctx, tx, principal.CompanyID, taskID); err != nil {
				return err
			}
		}
		// A confirmed handoff of a Task that has run cancels every live Run
		// first (run.cancelled, task_reassigned), then advances the
		// execution and workspace generations in the Task write, so the
		// previous computer's Runs, merges and workspace lose their hold.
		// Queued Messages stay: the next Run reads them.
		outcome := handoffOutcome{advanced: advanceGeneration}
		var calendarReset bool
		if handoff && target.ScheduleID != nil {
			target, calendarReset, err = cancelPendingCalendarRunInTx(
				ctx,
				tx,
				&pending,
				target,
				execution.CancellationScheduleEdited,
			)
			if err != nil {
				return err
			}
			outcome.runStopped = calendarReset
		}
		if advanceGeneration {
			cancelled, err := execution.CancelTaskRunsForHandoffInTx(
				ctx,
				tx,
				&pending,
				execution.TaskHandoff{
					CompanyID: principal.CompanyID, TaskID: current.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("cancel Task Runs for handoff: %w", err)
			}
			outcome.runStopped = outcome.runStopped || len(cancelled) > 0
		}

		if normalized.Status != nil && *normalized.Status == StatusDone {
			state, err := tx.GetTaskResultState(ctx, generated.GetTaskResultStateParams{
				CompanyID: target.CompanyID, ID: target.ID,
			})
			if err != nil {
				return err
			}
			if state.ResultSetID != uuid.Nil &&
				!role.Can(auth.PermissionTasksAssign) && current.Assignee.ID != principal.UserID {
				return ErrReviewPermissionRequired
			}
			if err := approveTaskResultsInTx(ctx, tx, target, principal.UserID); err != nil {
				return err
			}
		}

		updated, err := updateTask(
			ctx,
			tx,
			target,
			current.Revision,
			&pending,
			events.UserActor(principal.UserID.String()),
			advanceGeneration,
			personWriteFence(principal.UserID, auth.ProjectPermissionTasksWrite),
		)
		if err != nil {
			return err
		}
		if handoff ||
			((updated.Title != current.Title || updated.Description != current.Description) && updated.Assignee.ID != principal.UserID && updated.ScheduleID != nil) {
			state := "accepted"
			if updated.Assignee.ID != principal.UserID {
				state = "pending"
			}
			updated.AssignmentStatus = state
			if calendarReset {
				updated.ScheduleFiredAt = target.ScheduleFiredAt
				updated.ScheduleDisposition = target.ScheduleDisposition
			}
			updated, err = updateTaskCalendarInTx(
				ctx,
				tx,
				updated,
				&pending,
				events.UserActor(principal.UserID.String()),
			)
			if err != nil {
				return err
			}
		}

		if handoff || updated.Title != current.Title || updated.Description != current.Description {
			if err := reviseTaskCalendarInTx(
				ctx,
				tx,
				&pending,
				updated,
				principal.UserID,
				handoff,
			); err != nil {
				return err
			}
		}

		// Each nudge about a colleague's queued Message went to the assignee
		// of the moment. After a handoff that member can no longer start the
		// Run that carries it, so the nudges close.
		if updated.Assignee.ID != current.Assignee.ID {
			if err := conversation.CloseQueuedTaskMessageNudgesInTx(
				ctx, tx, &pending, principal.CompanyID, updated.ConversationID,
			); err != nil {
				return fmt.Errorf("close queued Task Message nudges for handoff: %w", err)
			}
		}

		// A fresh Agent assignment starts the Run the way an explicit start
		// does and stages the bumped conversation.updated event so list
		// summaries pick up the new active run.
		var assignmentRunID *uuid.UUID
		if newAgentAssignment {
			run, err := startTaskRunInTx(
				ctx,
				tx,
				&pending,
				principal,
				updated,
				execution.RunSourceAssign,
				taskRunOptions{},
			)
			if err != nil {
				return err
			}
			assignmentRunID = &run.ID
			enqueued = &run
		}
		if newAgentAssignment || advanceGeneration {
			if err := stageBumpedTaskConversation(
				ctx,
				tx,
				&pending,
				principal.UserID,
				events.UserActor(principal.UserID.String()),
				updated,
			); err != nil {
				return err
			}
		}
		updated, err = taskProjectionForActiveMember(ctx, tx, principal, updated.ID)
		if err != nil {
			return err
		}
		if err := stageTaskEvent(
			ctx,
			tx,
			&pending,
			newTaskEvent(principal.UserID, EventUpdated, updated),
		); err != nil {
			return err
		}
		if err := stageDependentProjectionEventsInTx(
			ctx,
			tx,
			&pending,
			events.UserActor(principal.UserID.String()),
			principal.CompanyID,
			updated.ID,
			current.Status,
			updated.Status,
		); err != nil {
			return err
		}
		if err := reconcileTaskReportEvent(
			ctx,
			tx,
			&pending,
			principal.UserID,
			current,
			updated,
		); err != nil {
			return err
		}
		inboxResults, err = createTaskInboxItems(
			ctx,
			tx,
			&pending,
			&current,
			updated,
			principal.UserID,
			outcome,
		)
		if err != nil {
			return err
		}
		result = MutationResult{
			Task: updated, RunID: assignmentRunID, AssigneeOutsideProject: assigneeOutsideProject(updated),
		}
		return nil
	})
	if err != nil {
		return MutationResult{}, fmt.Errorf("update Task: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	s.wakeRuntime(ctx, enqueued)
	for _, inboxResult := range inboxResults {
		inbox.PresentCreated(s.inboxPresenter, inboxResult)
	}
	return result, nil
}

// StartExecution atomically moves one Task into progress and
// inserts its execution prompt Message. Only the assignee may: the Run
// executes on their own binding of the Agent (ADR 0014). The Conversation
// lock serializes this path with assignment updates and message-triggered
// Runs.
func (s *DefaultTaskService) StartExecution(
	ctx context.Context,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	input StartExecutionInput,
) (MutationResult, error) {
	if !util.ValidUUID(taskID) {
		return MutationResult{}, ErrNotFound
	}
	var effects taskWriteEffects
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(tx *generated.Queries) error {
			var writeErr error
			effects, writeErr = s.startTaskExecutionInTx(
				ctx,
				tx,
				principal,
				taskExecutionRequest{taskID: taskID, revision: input.ExpectedRevision},
			)
			return writeErr
		},
	)
	if err != nil {
		return MutationResult{}, fmt.Errorf("start Task execution: %w", err)
	}
	s.publishTaskWrite(ctx, effects)
	return effects.result, nil
}

type taskExecutionRequest struct {
	taskID   uuid.UUID
	revision int64
	agentID  uuid.UUID
	prompt   string
}

func (s *DefaultTaskService) startTaskExecutionInTx(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	request taskExecutionRequest,
) (taskWriteEffects, error) {
	var effects taskWriteEffects
	taskID, expectedAgent, prompt := request.taskID, request.agentID, request.prompt
	input := StartExecutionInput{ExpectedRevision: request.revision}
	current, _, access, err := lockTaskMutationTarget(
		ctx, tx, principal, taskID, input.ExpectedRevision, true, "execution",
	)
	if err != nil {
		return effects, err
	}
	if current.Assignee.ID != principal.UserID {
		return effects, ErrAssigneeRequired
	}
	if expectedAgent != uuid.Nil && current.ExecutedBy.ID != expectedAgent {
		return effects, ErrConflict
	}
	if !runnableTaskStatus(current.Status) {
		return effects, ErrConflict
	}
	// Starting enqueues the assignee's Task Run, which needs their
	// project.tasks.execute (ADR 0017). The caller is the assignee. A
	// human Task starts its assisting Agent's Run, so the refusal names
	// the Run's Agent, never the member doing the work.
	if !access.Can(auth.ProjectPermissionTasksExecute) {
		return effects, taskRunRefusal(ctx, tx, current.CompanyID, current.ID, principal.UserID)
	}
	if status, exists, err := latestTaskRunStatus(ctx, tx, principal, current); err != nil {
		return effects, err
	} else if exists && !terminalRunStatus(status) {
		return effects, ErrConflict
	}
	if err := ensureTaskExecutionEligible(ctx, tx, principal.CompanyID, taskID); err != nil {
		return effects, err
	}

	// An explicit start supersedes a cancelled, skipped or missed occurrence
	// too. Its later chat and continuation Runs must see that work has begun.
	startingOccurrence := current.ScheduleDisposition != "" && current.ScheduleDisposition != "started"
	if current.AssignmentStatus != "accepted" || startingOccurrence {
		current.AssignmentStatus = "accepted"
		if startingOccurrence {
			current.ScheduleDisposition = "started"
			if current.ScheduleFiredAt == nil {
				now := time.Now().UTC()
				current.ScheduleFiredAt = &now
			}
		}
		current, err = updateTaskCalendarInTx(
			ctx,
			tx,
			current,
			&effects.pending,
			events.UserActor(principal.UserID.String()),
		)
		if err != nil {
			return effects, err
		}
	}

	target := current
	target.Status = StatusInProgress
	updated, err := updateTask(
		ctx,
		tx,
		target,
		current.Revision,
		&effects.pending,
		events.UserActor(principal.UserID.String()),
		false,
		// Starting needs the assignee's project.tasks.execute; the
		// statement checks it again.
		personWriteFence(principal.UserID, auth.ProjectPermissionTasksExecute),
	)
	if err != nil {
		return effects, err
	}
	run, err := startTaskRunInTx(
		ctx,
		tx,
		&effects.pending,
		principal,
		updated,
		execution.RunSourceExecution,
		taskRunOptions{prompt: prompt},
	)
	if err != nil {
		return effects, err
	}
	effects.enqueued = &run
	// The conversation projection bump keeps list summaries in step with
	// the new active run.
	if err := stageBumpedTaskConversation(
		ctx, tx, &effects.pending, principal.UserID, events.UserActor(principal.UserID.String()), updated,
	); err != nil {
		return effects, err
	}
	updated, err = taskProjectionForActiveMember(ctx, tx, principal, updated.ID)
	if err != nil {
		return effects, err
	}
	if err := stageTaskEvent(
		ctx,
		tx,
		&effects.pending,
		newTaskEvent(principal.UserID, EventUpdated, updated),
	); err != nil {
		return effects, err
	}
	runID := run.ID
	effects.result = MutationResult{Task: updated, RunID: &runID}
	return effects, nil
}

type taskRunOptions struct {
	attachmentIDs []uuid.UUID
	prompt        string
	// run is the scope of the Run whose proof started this Run, when a Run
	// creates a Task for its member with an Agent: the new Run, its prompt
	// and its Activity then name that Run's Agent, for the member (D31).
	run *RunScope
}

// startTaskRunInTx is the one way a member starts their Task's Run: an
// explicit start, a create for themself, or a handoff to themself. Each
// posts the Task's title and description as the member's message, so the
// chat shows what the Agent was asked and the Run freezes the same words
// and their author. A reviewed Huddle proposal can supply its own prompt;
// ordinary creates, starts and handoffs use the Task title and description.
// source preserves the Run's triggering operation.
//
// The Run goes in first and the prompt item after it: runs.id is a
// database-generated UUID that needs nothing from the Conversation stream,
// and the item is what carries run_id. TriggerPrompt is the same text the
// item is about to hold — there is no row to read it back from yet.
// attachmentIDs are a create's files, frozen into the Run with the prompt;
// the caller links them on a files message the Run owns (D25).
func startTaskRunInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	value Response,
	source execution.RunEnqueueSource,
	options taskRunOptions,
) (generated.Run, error) {
	agent, err := tx.GetTaskRunAgent(
		ctx,
		generated.GetTaskRunAgentParams{CompanyID: value.CompanyID, TaskID: value.ID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.Run{}, mapRunEnqueueError(execution.ErrRunAgentArchived)
	}
	if err != nil {
		return generated.Run{}, fmt.Errorf("resolve Task Run Agent: %w", err)
	}
	prompt := taskExecutionPromptContent(value)
	if options.prompt != "" {
		prompt = options.prompt
	}
	run, err := execution.EnqueueRunInTx(ctx, tx, pending, execution.RunTrigger{
		CompanyID:            principal.CompanyID,
		ConversationID:       value.ConversationID,
		AgentID:              agent.ID,
		TaskID:               uuid.NullUUID{UUID: value.ID, Valid: true},
		TriggerPrompt:        prompt,
		TriggerAttachmentIDs: options.attachmentIDs,
		Priority:             execution.PriorityToInt(string(value.Priority)),
		Source:               source,
		ActorUserID:          principal.UserID,
		AuthorizedByUserID:   principal.UserID,
		Actor:                runTriggerActor(options.run, principal.UserID),
	})
	if err != nil {
		return generated.Run{}, mapRunEnqueueError(err)
	}
	// The colleagues' saved Messages ride ahead of the prompt, exactly as
	// the kernel froze them into the Run's snapshot.
	if _, err := conversation.MaterializeQueuedTaskMessagesInTx(ctx, tx, pending, run); err != nil {
		return generated.Run{}, fmt.Errorf("carry queued Task Messages: %w", err)
	}
	promptMessage, err := insertTaskExecutionPromptMessage(ctx, tx, principal, value, run.ID, prompt)
	if err != nil {
		return generated.Run{}, err
	}
	promptView, err := conversation.MessageViewFromRow(ctx, tx, promptMessage)
	if err != nil {
		return generated.Run{}, fmt.Errorf("project Task execution prompt Message: %w", err)
	}
	if err := stageTaskEvent(ctx, tx, pending, newTaskMessageCreatedEvent(
		options.run.actor(principal.UserID), principal.CompanyID, promptView,
	)); err != nil {
		return generated.Run{}, err
	}
	return run, nil
}

// runTriggerActor is who a started Run's run.queued names: the Agent of the
// Run that started it, for the member (D31), or the zero Actor, which keeps
// the member.
func runTriggerActor(run *RunScope, userID uuid.UUID) events.Actor {
	if run == nil {
		return events.Actor{}
	}
	return run.actor(userID)
}

type allocatedTaskKey struct {
	number int64
	key    string
}

// allocateTaskKey hands out the Task's number and settles its key. A create
// without a key gets the one every surface displays, {taskPrefix}-{number},
// minted from the same allocation so the key a person quotes and the number
// on the board are the same Task; a label already held by an older row
// makes the mint take the next number rather than fail on the unique fence.
// A chosen key is kept, unless it is shaped like a label: that namespace is
// the server's. A Company Plan chooses its own stable slugs and never comes
// this way.
func allocateTaskKey(
	ctx context.Context,
	tx *generated.Queries,
	companyID uuid.UUID,
	chosen string,
) (allocatedTaskKey, error) {
	const mintAttempts = 64
	for attempt := 0; attempt < mintAttempts; attempt++ {
		allocated, err := tx.AllocateTaskNumber(ctx, companyID)
		if err != nil {
			return allocatedTaskKey{}, mapTaskMutationError("allocate Task number", err)
		}
		if chosen != "" {
			if KeyInMintedNamespace(allocated.TaskPrefix, chosen) {
				return allocatedTaskKey{}, ErrInvalidInput
			}
			return allocatedTaskKey{number: allocated.Number, key: chosen}, nil
		}
		key := MintKey(allocated.TaskPrefix, allocated.Number)
		taken, err := tx.TaskKeyTaken(ctx, generated.TaskKeyTakenParams{CompanyID: companyID, Key: key})
		if err != nil {
			return allocatedTaskKey{}, fmt.Errorf("check minted Task key: %w", err)
		}
		if !taken {
			return allocatedTaskKey{number: allocated.Number, key: key}, nil
		}
	}
	return allocatedTaskKey{}, ErrConflict
}

func runnableTaskStatus(status Status) bool {
	return status == StatusBacklog || status == StatusTodo || status == StatusBlocked || status == StatusError
}

func terminalRunStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

// mapRunEnqueueError maps the kernel's productizable refusals onto the Task
// domain's conflict sentinel; a missing binding keeps its own identity, so
// the handler can name the setup step rather than a stale state, and
// infrastructure failures pass through wrapped.
func mapRunEnqueueError(err error) error {
	if errors.Is(err, execution.ErrRunAgentArchived) ||
		errors.Is(err, execution.ErrDuplicatePendingRun) ||
		errors.Is(err, execution.ErrTaskMergeInProgress) {
		return ErrConflict
	}
	if errors.Is(err, execution.ErrRunAgentNotBound) || errors.Is(err, execution.ErrRunTaskAssigneeRequired) {
		return err
	}
	return fmt.Errorf("enqueue Task Run: %w", err)
}

// stageBumpedTaskConversation bumps the Task Conversation projection and
// stages the conversation.updated event carrying the recomputed list summary,
// so clients following the summary see the freshly-enqueued run. actorID is
// the member whose summary the frame carries; actor is who the Activity row
// names: the member, or a Run's Agent acting for them (D31).
func stageBumpedTaskConversation(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actorID uuid.UUID,
	actor events.Actor,
	value Response,
) error {
	before, err := activity.ObserveInTx(ctx, q, conversation.Topic, value.ConversationID)
	if err != nil {
		return err
	}
	row, err := q.BumpConversationProjection(ctx, generated.BumpConversationProjectionParams{
		ConversationID: value.ConversationID,
		CompanyID:      value.CompanyID,
	})
	if err != nil {
		return fmt.Errorf("bump Task Conversation projection: %w", err)
	}
	summary, err := taskConversationSummary(ctx, q, actorID, row)
	if err != nil {
		return err
	}
	if err := stageTaskConversationUpdated(
		ctx, q, pending,
		actorID,
		value.ID,
		bumpedConversation{row: row, summary: summary},
	); err != nil {
		return err
	}
	return activity.RecordInTx(
		ctx,
		q,
		pending,
		actor,
		conversation.EventConversationUpdated,
		before,
		conversation.Topic,
		row.ID,
	)
}

func applyTaskUpdateTarget(current Response, input UpdateInput) Response {
	target := current
	if input.MilestoneType.Present {
		target.MilestoneType = input.MilestoneType.Value
	}
	if input.Title != nil {
		target.Title = *input.Title
	}
	if input.Description != nil {
		target.Description = *input.Description
	}
	if input.Status != nil {
		target.Status = *input.Status
	}
	if input.Priority != nil {
		target.Priority = *input.Priority
	}
	if input.Assignee != nil {
		target.Assignee = *input.Assignee
		// A moved assignee takes the work with them unless the update names
		// an Agent: the executedBy user is always the assignee.
		if target.ExecutedBy.Kind == ActorUser {
			target.ExecutedBy = *input.Assignee
		}
	}
	if input.ExecutedBy != nil {
		target.ExecutedBy = *input.ExecutedBy
	}
	if input.Labels != nil {
		target.Labels = input.Labels
	}
	if input.StartDate.Present {
		target.StartDate = input.StartDate.Value
	}
	if input.DueDate.Present {
		target.DueDate = input.DueDate.Value
	}
	return target
}

func changedTaskFields(before, after Response, input UpdateInput) []Field {
	fields := make([]Field, 0, 8)
	if input.MilestoneType.Present && before.MilestoneType != after.MilestoneType {
		fields = append(fields, FieldMilestoneType)
	}
	if input.Title != nil && before.Title != after.Title {
		fields = append(fields, FieldTitle)
	}
	if input.Description != nil && before.Description != after.Description {
		fields = append(fields, FieldDescription)
	}
	if input.Status != nil && before.Status != after.Status {
		fields = append(fields, FieldStatus)
	}
	if input.Priority != nil && before.Priority != after.Priority {
		fields = append(fields, FieldPriority)
	}
	if input.Assignee != nil && !sameActor(before.Assignee, after.Assignee) {
		fields = append(fields, FieldAssignee)
	}
	if !sameActor(before.ExecutedBy, after.ExecutedBy) {
		fields = append(fields, FieldExecutedBy)
	}
	if input.Labels != nil && !slices.Equal(before.Labels, after.Labels) {
		fields = append(fields, FieldLabels)
	}
	if input.StartDate.Present && !sameDate(before.StartDate, after.StartDate) {
		fields = append(fields, FieldStartDate)
	}
	if input.DueDate.Present && !sameDate(before.DueDate, after.DueDate) {
		fields = append(fields, FieldDueDate)
	}
	return fields
}
